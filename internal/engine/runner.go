package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/oschwald/geoip2-golang"
)

// Runner 是测试任务编排器，持有地理位置与 ASN 数据库引用。
// 通过 NewRunner 创建，使用完毕后应调用 Close。
type Runner struct {
	locations map[string]Location
	asnDB     *geoip2.Reader
	traceURL  string
}

// RunnerConfig 是创建 Runner 所需的外部资源配置。
//
// 这里用朴素的结构体而非直接引用 config.Config：config 包依赖 engine 取默认值，
// engine 若反向依赖 config 就成了循环导入。由 main 负责把两者接起来。
type RunnerConfig struct {
	DataDir         string   // 数据文件目录（exe 同目录）
	LocationSources []string // locations.json 下载源，依次尝试
	ASNSources      []string // GeoLite2-ASN.mmdb 下载源，依次尝试
	TraceURL        string   // CF 节点验证接口（不含协议头），空则用 DefaultTraceURL
}

// NewRunner 加载 dataDir 下的 locations.json 与 GeoLite2-ASN.mmdb（缺失时自动下载）。
// locations 加载失败是致命错误；ASN 加载失败仅降级（ASN 信息留空）。
func NewRunner(rc RunnerConfig) (*Runner, error) {
	locSources := rc.LocationSources
	if len(locSources) == 0 {
		locSources = DefaultLocationSources
	}
	asnSources := rc.ASNSources
	if len(asnSources) == 0 {
		asnSources = DefaultASNSources
	}
	traceURL := rc.TraceURL
	if traceURL == "" {
		traceURL = DefaultTraceURL
	}

	locations, err := loadLocations(rc.DataDir, locSources)
	if err != nil {
		return nil, err
	}

	asnDB, err := loadASN(rc.DataDir, asnSources)
	if err != nil {
		fmt.Printf("警告: %v（ASN 信息将不可用）\n", err)
		asnDB = nil
	}

	return &Runner{locations: locations, asnDB: asnDB, traceURL: traceURL}, nil
}

// Close 释放 ASN 数据库等资源。
func (r *Runner) Close() {
	if r.asnDB != nil {
		r.asnDB.Close()
	}
}

// ASNLoaded 报告 ASN 数据库是否可用（供 /api/config 展示）。
func (r *Runner) ASNLoaded() bool { return r.asnDB != nil }

// LocationCount 返回已加载的地理位置条目数。
func (r *Runner) LocationCount() int { return len(r.locations) }

// RunLatencyTest 并发执行延迟测试 + CF 节点验证，流式回调事件。
//
// 并发模型：每阶段独立的信号量 channel + WaitGroup，进度用 atomic 计数，
// 结果经带缓冲 channel 收集——避免了旧版共享信号量导致的死锁与数据竞争。
// ctx 取消时进行中的连接会中断，函数尽早返回已收集的部分结果。
func (r *Runner) RunLatencyTest(ctx context.Context, targets []Target, opts LatencyOptions, cb EventCallback) ([]Result, error) {
	if opts.MaxConcurrency < 1 {
		opts.MaxConcurrency = 1
	}

	total := len(targets)
	sem := make(chan struct{}, opts.MaxConcurrency)
	resultCh := make(chan Result, total)

	var completed int64
	var validCount int64
	var wg sync.WaitGroup

loop:
	for _, t := range targets {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(target Target) {
			defer func() {
				<-sem
				wg.Done()
				n := atomic.AddInt64(&completed, 1)
				cb(Event{Type: EventProgress, Progress: &Progress{
					Completed: int(n),
					Total:     total,
					ValidIPs:  int(atomic.LoadInt64(&validCount)),
				}})
			}()

			res := r.testSingleIP(ctx, target, opts)
			if res != nil {
				atomic.AddInt64(&validCount, 1)
				resultCh <- *res
				cb(Event{Type: EventResult, Result: res})
			}
		}(t)
	}

	wg.Wait()
	close(resultCh)

	var results []Result
	for res := range resultCh {
		results = append(results, res)
	}

	if ctx.Err() != nil {
		cb(Event{Type: EventDone, Message: fmt.Sprintf("已停止，保留 %d 个有效结果", len(results))})
		return results, ctx.Err()
	}
	cb(Event{Type: EventDone, Message: fmt.Sprintf("延迟测试完成，共 %d 个有效 IP", len(results))})
	return results, nil
}

// RunSpeedTest 对给定目标子集并发测速，逐条回调速度结果。
// 使用独立的信号量池，与延迟测试互不干扰。
func (r *Runner) RunSpeedTest(ctx context.Context, targets []Target, opts SpeedOptions, cb EventCallback) error {
	if opts.MaxConcurrency < 1 {
		opts.MaxConcurrency = 1
	}

	total := len(targets)
	sem := make(chan struct{}, opts.MaxConcurrency)

	var completed int64
	var validCount int64
	var wg sync.WaitGroup

loop:
	for _, t := range targets {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(target Target) {
			defer func() {
				<-sem
				wg.Done()
				n := atomic.AddInt64(&completed, 1)
				cb(Event{Type: EventProgress, Progress: &Progress{
					Completed: int(n),
					Total:     total,
					ValidIPs:  int(atomic.LoadInt64(&validCount)),
				}})
			}()

			speed := testSingleSpeed(ctx, target, opts)
			if speed <= 0 {
				return
			}
			if opts.MinSpeedKBs > 0 && speed < opts.MinSpeedKBs {
				return
			}
			atomic.AddInt64(&validCount, 1)
			cb(Event{Type: EventSpeed, Result: &Result{
				IP:               target.IP,
				Port:             target.Port,
				DownloadSpeedKBs: speed,
			}})
		}(t)
	}

	wg.Wait()

	if ctx.Err() != nil {
		cb(Event{Type: EventDone, Message: "测速已停止"})
		return ctx.Err()
	}
	cb(Event{Type: EventDone, Message: fmt.Sprintf("测速完成，%d/%d 个达标", atomic.LoadInt64(&validCount), total)})
	return nil
}
