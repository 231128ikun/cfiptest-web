package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/oschwald/geoip2-golang"
)

// Runner 是测试任务编排器，持有地理位置与 ASN 数据库引用。
// 通过 NewRunner 创建，使用完毕后应调用 Close。
type Runner struct {
	locations  map[string]Location
	asnDB      *geoip2.Reader
	traceURL   string
	ipsTypeURL string

	// ownASN 用于测速源 auto 模式的运营商探测；默认 lookupOwnASN
	// （本地 ASN 库 + trace 出口 IP），测试可替换为固定值。
	ownASN func(ctx context.Context) (uint, string)

	// 测速源 auto 模式的解析结果缓存（单进程首次探测后复用）。
	autoOnce  sync.Once
	autoURL   string
	autoLabel string
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
	IPSTypeURL      string   // IPS类型检测接口，空则用 DefaultIPSTypeURL
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
	ipsTypeURL := rc.IPSTypeURL
	if ipsTypeURL == "" {
		ipsTypeURL = DefaultIPSTypeURL
	}

	locations, err := loadLocations(rc.DataDir, locSources)
	if err != nil {
		return nil, err
	}
	RegisterCountryNames(locations)

	asnDB, err := loadASN(rc.DataDir, asnSources)
	if err != nil {
		fmt.Printf("警告: %v（ASN 信息将不可用）\n", err)
		asnDB = nil
	}

	runner := &Runner{locations: locations, asnDB: asnDB, traceURL: traceURL, ipsTypeURL: ipsTypeURL}
	runner.ownASN = runner.lookupOwnASN
	return runner, nil
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

func (r *Runner) lookupLocation(colocode string) (Location, bool) {
	loc, ok := r.locations[colocode]
	return loc, ok
}

func (r *Runner) lookupASN(ip string) (uint, string) {
	if r.asnDB == nil {
		return 0, ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 0, ""
	}
	record, err := r.asnDB.ASN(parsed)
	if err != nil {
		return 0, ""
	}
	return uint(record.AutonomousSystemNumber), record.AutonomousSystemOrganization
}

// ErrResultLimitReached 表示测试因达到 MaxResults 而提前结束。
// 这是正常收工而非故障，上层不应作为错误展示给用户。
var ErrResultLimitReached = errors.New("已达到最大结果数")

// countFailure 按失败阶段累计计数，返回对应的分类名（空串表示无需累计）。
func countFailure(stage ProbeFailureStage, tcp, lat, trace, colo *atomic.Int64) string {
	switch stage {
	case FailureTCPConnect:
		tcp.Add(1)
		return "tcp"
	case FailureLatency:
		lat.Add(1)
		return "lat"
	case FailureTraceRequest:
		trace.Add(1)
		return "trace"
	case FailureTraceColo:
		colo.Add(1)
		return "colo"
	}
	return ""
}

// failureSummary 把各失败阶段计数拼成一句诊断摘要，供完成消息展示；无失败时返回空串。
func failureSummary(tcp, lat, trace, colo int64) string {
	parts := make([]string, 0, 4)
	if tcp > 0 {
		parts = append(parts, fmt.Sprintf("TCP连接失败 %d", tcp))
	}
	if lat > 0 {
		parts = append(parts, fmt.Sprintf("延迟超标 %d", lat))
	}
	if trace > 0 {
		parts = append(parts, fmt.Sprintf("trace请求失败 %d", trace))
	}
	if colo > 0 {
		parts = append(parts, fmt.Sprintf("缺colo %d", colo))
	}
	return strings.Join(parts, "、")
}

// RunLatencyTest 并发执行延迟测试 + CF 节点验证，流式回调事件。
//
// 并发模型：每阶段独立的信号量 channel + WaitGroup，进度用 atomic 计数，
// 结果经带缓冲 channel 收集——避免了旧版共享信号量导致的死锁与数据竞争。
// ctx 取消时进行中的连接会中断，函数尽早返回已收集的部分结果。
//
// opts.MaxResults > 0 时，凑够该数量的有效结果就取消剩余任务并返回
// ErrResultLimitReached；为 0 则测完全部目标（默认行为）。
func (r *Runner) RunLatencyTest(ctx context.Context, targets []Target, opts LatencyOptions, cb EventCallback) ([]Result, error) {
	if opts.MaxConcurrency < 1 {
		opts.MaxConcurrency = 1
	}

	total := len(targets)
	sem := make(chan struct{}, opts.MaxConcurrency)
	resultCh := make(chan Result, total)

	// 用于「凑够就停」：派生一个可取消的子 ctx，达到上限时取消它。
	// 注意它与用户停止共用 context.Canceled，故另设 hitLimit 标志区分。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var hitLimit atomic.Bool

	var completed int64
	var validCount int64
	var failTCP, failLatency, failTrace, failColo atomic.Int64
	var wg sync.WaitGroup

loop:
	for _, t := range targets {
		select {
		case <-runCtx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(target Target) {
			completedNormally := false
			defer func() {
				<-sem
				wg.Done()
				if !completedNormally {
					return
				}
				n := atomic.AddInt64(&completed, 1)
				cb(Event{Type: EventProgress, Progress: &Progress{
					Completed: int(n),
					Total:     total,
					ValidIPs:  int(atomic.LoadInt64(&validCount)),
				}})
			}()

			res, failure := r.testSingleIP(runCtx, target, opts)
			// failure 同时表示「无效」和「被取消」。只有上下文仍正常时，
			// 才能确认该候选已经完整检查，可以从前端候选漏斗中移除。
			if runCtx.Err() != nil {
				return
			}
			if res == nil {
				completedNormally = true
				if failure != nil {
					countFailure(failure.Stage, &failTCP, &failLatency, &failTrace, &failColo)
				}
				cb(Event{Type: EventTargetDone, Target: &target, Failure: failure})
				return
			}
			var n int64
			for {
				current := atomic.LoadInt64(&validCount)
				if opts.MaxResults > 0 && int(current) >= opts.MaxResults {
					completedNormally = true
					cb(Event{Type: EventTargetDone, Target: &target})
					return
				}
				if atomic.CompareAndSwapInt64(&validCount, current, current+1) {
					n = current + 1
					break
				}
			}
			resultCh <- *res
			cb(Event{Type: EventResult, Result: res})
			completedNormally = true
			cb(Event{Type: EventTargetDone, Target: &target})
			if opts.MaxResults > 0 && int(n) >= opts.MaxResults {
				hitLimit.Store(true)
				cancel()
			}
		}(t)
	}

	wg.Wait()
	close(resultCh)

	var results []Result
	for res := range resultCh {
		results = append(results, res)
	}

	summary := failureSummary(failTCP.Load(), failLatency.Load(), failTrace.Load(), failColo.Load())
	switch {
	case hitLimit.Load():
		msg := fmt.Sprintf("已达到最大结果数，找到 %d 个有效 IP（剩余目标未测试）", len(results))
		if summary != "" {
			msg += "；" + summary
		}
		cb(Event{Type: EventDone, Reason: DoneLimit, Message: msg})
		return results, ErrResultLimitReached
	case ctx.Err() != nil:
		cb(Event{Type: EventDone, Reason: DoneStopped,
			Message: fmt.Sprintf("已停止，保留 %d 个有效结果", len(results))})
		return results, ctx.Err()
	}
	msg := fmt.Sprintf("延迟测试完成，共 %d 个有效 IP", len(results))
	if summary != "" {
		msg += "；" + summary
	}
	cb(Event{Type: EventDone, Reason: DoneCompleted, Message: msg})
	return results, nil
}

// RunSpeedTest 对给定目标子集并发测速，逐条回调速度结果。
// 使用独立的信号量池，与延迟测试互不干扰。
//
// opts.MaxResults 语义同 RunLatencyTest：0=测完全部，>0=凑够达标结果即停。
func (r *Runner) RunSpeedTest(ctx context.Context, targets []Target, opts SpeedOptions, cb EventCallback) error {
	if opts.MaxConcurrency < 1 {
		opts.MaxConcurrency = 1
	}
	// 解析测速源：auto 按本机 ISP 自动选源；自定义 URL 补全协议。记录实际使用源。
	resolvedURL, sourceLabel := r.ResolveSpeedURL(ctx, opts.DownloadURL, opts.EnableTLS)
	opts.DownloadURL = resolvedURL
	cb(speedSourceEvent(sourceLabel))

	total := len(targets)
	sem := make(chan struct{}, opts.MaxConcurrency)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var hitLimit atomic.Bool

	var completed int64
	var validCount int64
	var wg sync.WaitGroup

loop:
	for _, t := range targets {
		select {
		case <-runCtx.Done():
			break loop
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(target Target) {
			completedNormally := false
			defer func() {
				<-sem
				wg.Done()
				if !completedNormally {
					return
				}
				n := atomic.AddInt64(&completed, 1)
				cb(Event{Type: EventProgress, Progress: &Progress{
					Completed: int(n),
					Total:     total,
					ValidIPs:  int(atomic.LoadInt64(&validCount)),
				}})
			}()

			speed := testSingleSpeed(runCtx, target, opts)
			if speed <= 0 {
				// 达到结果上限后的主动取消不是测速失败，不应再推送额外失败事件。
				if runCtx.Err() != nil {
					return
				}
				cb(Event{Type: EventSpeed, Result: &Result{
					IP:               target.IP,
					Port:             target.Port,
					DownloadSpeedKBs: -1,
				}})
				completedNormally = true
				cb(Event{Type: EventTargetDone, Target: &target})
				return
			}
			if opts.MinSpeedKBs > 0 && speed < opts.MinSpeedKBs {
				completedNormally = true
				cb(Event{Type: EventTargetDone, Target: &target})
				return
			}
			var n int64
			for {
				current := atomic.LoadInt64(&validCount)
				if opts.MaxResults > 0 && int(current) >= opts.MaxResults {
					completedNormally = true
					cb(Event{Type: EventTargetDone, Target: &target})
					return
				}
				if atomic.CompareAndSwapInt64(&validCount, current, current+1) {
					n = current + 1
					break
				}
			}
			cb(Event{Type: EventSpeed, Result: &Result{
				IP:               target.IP,
				Port:             target.Port,
				DownloadSpeedKBs: speed,
			}})
			completedNormally = true
			cb(Event{Type: EventTargetDone, Target: &target})
			if opts.MaxResults > 0 && int(n) >= opts.MaxResults {
				hitLimit.Store(true)
				cancel()
			}
		}(t)
	}

	wg.Wait()

	valid := atomic.LoadInt64(&validCount)
	switch {
	case hitLimit.Load():
		cb(Event{Type: EventDone, Reason: DoneLimit,
			Message: fmt.Sprintf("已达到最大结果数，%d 个 IP 达标（剩余未测速）", opts.MaxResults)})
		return ErrResultLimitReached
	case ctx.Err() != nil:
		cb(Event{Type: EventDone, Reason: DoneStopped, Message: "测速已停止"})
		return ctx.Err()
	}
	cb(Event{Type: EventDone, Reason: DoneCompleted,
		Message: fmt.Sprintf("测速完成，%d/%d 个达标（测速源：%s）", valid, total, sourceLabel)})
	return nil
}

// LatencyOne 检测单个目标，返回有效结果；ctx 取消或检测无效时返回 false。
func (r *Runner) LatencyOne(ctx context.Context, target Target, opts LatencyOptions) (Result, bool) {
	res, _ := r.testSingleIP(ctx, target, opts)
	if res == nil || ctx.Err() != nil {
		return Result{}, false
	}
	return *res, true
}

// SpeedOne 对单个目标测速，返回 kB/s；失败或取消返回 0。
func (r *Runner) SpeedOne(ctx context.Context, target Target, opts SpeedOptions) float64 {
	return testSingleSpeed(ctx, target, opts)
}

// RunCombinedTest 执行「延迟 → 测速」流水线：每个 IP 先测延迟，延迟合格再测速。
// 并发模型：取 min(延迟并发, 测速并发) 作为总并发，每个 worker 串行完成 延迟 → 测速 → 判定，
// 而不是先并发测完全部延迟再并发测速——这样整体约束（如最终保留前 N 个）才能生效。
// 事件：无数量上限（MaxResults==0）时，延迟合格先发 EventResult，测速完成再发 EventSpeed（速度回填）；
// 有数量上限（MaxResults>0）时不再提前发 EventResult，只在「延迟+测速」双达标并入结果后
// 一次性推送 EventResult+EventSpeed，保证表格/导出数量不会超过约束。
// 最终只有测速达标的 IP 进入返回结果。MaxResults 作用于最终达标结果。
func (r *Runner) RunCombinedTest(ctx context.Context, targets []Target, latency LatencyOptions, speed SpeedOptions, cb EventCallback) ([]Result, error) {
	if latency.MaxConcurrency < 1 {
		latency.MaxConcurrency = 1
	}
	if speed.MaxConcurrency < 1 {
		speed.MaxConcurrency = 1
	}
	speed.EnableTLS = latency.EnableTLS
	// 解析测速源：auto 按本机 ISP 自动选源；自定义 URL 补全协议。记录实际使用源。
	resolvedURL, sourceLabel := r.ResolveSpeedURL(ctx, speed.DownloadURL, speed.EnableTLS)
	speed.DownloadURL = resolvedURL
	cb(speedSourceEvent(sourceLabel))

	total := len(targets)
	concurrency := latency.MaxConcurrency
	if speed.MaxConcurrency < concurrency {
		concurrency = speed.MaxConcurrency
	}
	sem := make(chan struct{}, concurrency)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var hitLimit atomic.Bool
	var mu sync.Mutex
	var results []Result
	var completedCount int64
	var validCount int64
	var failTCP, failLatency, failTrace, failColo atomic.Int64
	var wg sync.WaitGroup

	emitPipelineProgress := func() {
		cb(Event{Type: EventProgress, Progress: &Progress{
			Completed: int(atomic.LoadInt64(&completedCount)),
			Total:     total,
			ValidIPs:  int(atomic.LoadInt64(&validCount)),
			Phase:     "pipeline",
		}})
	}

	worker := func(target Target) {
		completedNormally := false
		defer func() {
			<-sem
			wg.Done()
			if !completedNormally {
				return
			}
			atomic.AddInt64(&completedCount, 1)
			emitPipelineProgress()
		}()

		res, failure := r.testSingleIP(runCtx, target, latency)
		if runCtx.Err() != nil {
			return
		}
		if res == nil {
			completedNormally = true
			if failure != nil {
				countFailure(failure.Stage, &failTCP, &failLatency, &failTrace, &failColo)
			}
			cb(Event{Type: EventTargetDone, Target: &target, Failure: failure})
			return
		}
		// 无数量上限时保持渐进式展示：延迟合格立即推送 result，速度再回填；
		// 有上限时延迟结果必须等测速双达标后才能推送，否则表格/导出会超过约束。
		streamLatencyResult := speed.MaxResults == 0
		if streamLatencyResult {
			cb(Event{Type: EventResult, Result: res})
		}

		speedVal := testSingleSpeed(runCtx, target, speed)
		if speedVal <= 0 {
			if runCtx.Err() != nil {
				return
			}
			res.DownloadSpeedKBs = -1
			if streamLatencyResult {
				cb(Event{Type: EventSpeed, Result: res})
			}
			completedNormally = true
			cb(Event{Type: EventTargetDone, Target: &target})
			return
		}
		if speed.MinSpeedKBs > 0 && speedVal < speed.MinSpeedKBs {
			completedNormally = true
			cb(Event{Type: EventTargetDone, Target: &target})
			return
		}
		res.DownloadSpeedKBs = speedVal

		mu.Lock()
		if speed.MaxResults > 0 && len(results) >= speed.MaxResults {
			mu.Unlock()
			completedNormally = true
			cb(Event{Type: EventTargetDone, Target: &target})
			return
		}
		results = append(results, *res)
		n := len(results)
		mu.Unlock()
		atomic.AddInt64(&validCount, 1)
		if !streamLatencyResult {
			// 约束模式：此前没有推送过该 IP 的 result，达标后补发 result+speed
			cb(Event{Type: EventResult, Result: res})
		}
		cb(Event{Type: EventSpeed, Result: res})
		completedNormally = true
		cb(Event{Type: EventTargetDone, Target: &target})
		if speed.MaxResults > 0 && n >= speed.MaxResults {
			hitLimit.Store(true)
			cancel()
		}
	}

loop:
	for _, t := range targets {
		if hitLimit.Load() {
			break loop
		}
		select {
		case <-runCtx.Done():
			break loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go worker(t)
	}

	wg.Wait()
	cancel()

	switch {
	case hitLimit.Load():
		cb(Event{Type: EventDone, Reason: DoneLimit,
			Message: fmt.Sprintf("已达到最大结果数，延迟+测速均达标 %d 个 IP（测速源：%s）", len(results), sourceLabel)})
		return results, ErrResultLimitReached
	case ctx.Err() != nil:
		cb(Event{Type: EventDone, Reason: DoneStopped,
			Message: fmt.Sprintf("已停止，保留 %d 个有效结果", len(results))})
		return results, ctx.Err()
	}
	summary := failureSummary(failTCP.Load(), failLatency.Load(), failTrace.Load(), failColo.Load())
	msg := fmt.Sprintf("延迟+测速完成，%d/%d 个 IP 达标（测速源：%s）", len(results), total, sourceLabel)
	if summary != "" {
		msg += "；" + summary
	}
	cb(Event{Type: EventDone, Reason: DoneCompleted, Message: msg})
	return results, nil
}
