package subscription

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/fsutil"
	"iptest-web/internal/library"
)

// Tester 抽象检测能力，便于测试替换；*engine.Runner 满足该接口。
type Tester interface {
	LatencyOne(ctx context.Context, target engine.Target, opts engine.LatencyOptions) (engine.Result, bool)
	SpeedOne(ctx context.Context, target engine.Target, opts engine.SpeedOptions) float64
	// ResolveSpeedURL 把测速源原始配置（可能为 auto）解析为实际下载 URL 与来源标签。
	ResolveSpeedURL(ctx context.Context, raw string, enableTLS bool) (string, string)
}

var _ Tester = (*engine.Runner)(nil)

// Progress 是一次自动化运行的阶段进度消息（序列化进 engine.EventAuto.Message）。
type Progress struct {
	Group       string `json:"group,omitempty"`
	Stage       string `json:"stage"`           // gather | latency | speed | fill | output
	Round       int    `json:"round,omitempty"` // 编排轮次（第 N 轮）
	Tested      int    `json:"tested"`
	Passed      int    `json:"passed"`
	Failed      int    `json:"failed"`      // 延迟失败（已从库移除）
	SpeedFailed int    `json:"speedFailed"` // 测速失败（保留条目）
	Filled      int    `json:"filled"`
	Target      int    `json:"target"`
	Log         string `json:"log,omitempty"`
}

// ProgressFunc 是进度回调；返回 error 可中止运行。
type ProgressFunc func(p Progress) error

// RunOptions 控制编排运行的行为；零值使用默认值。
type RunOptions struct {
	LatencyTimeoutMs     int             // 单连接延迟超时（ms），默认 3000
	LatencyProbes        int             // TCP 探测次数，默认 4；成功探测取平均
	LatencyHTTPProbes    int             // HTTP(trace) 校验次数，默认 1
	LatencyHTTPTimeoutMs int             // HTTP(trace) 校验单请求超时（ms），默认 3000；0 = 用 LatencyTimeoutMs
	RemoveAfterFailures  int             // 延迟失败保留阈值：连续失败达该次数才从库移除，默认 3
	LatencyConcurrency   int             // 延迟检测并发，默认 50；维护场景过高并发易漏测/误判
	SpeedDurationSec     int             // 单 IP 测速时长（秒），默认 5
	SpeedConcurrency     int             // 测速并发，默认 5
	DownloadURL          string          // 测速文件地址；auto=按运营商自动选择，省略协议时自动补全，默认 engine 默认值
	InputTargets         []engine.Target `json:"-"`                  // 服务端已解析的初始化来源目标（远程 URL），不参与 JSON
	InputSource          string          `json:"-"`                  // 初始化来源标签，用于进度日志
	RemoteLibrary        bool            `json:"-"`                  // 维护来源为远程库（官方/URL）：失效不删本地库、结果不落盘
	Protocol             string          `json:"protocol,omitempty"` // https（默认）| http
}

func (o RunOptions) withDefaults() RunOptions {
	if o.LatencyTimeoutMs <= 0 {
		o.LatencyTimeoutMs = 3000
	}
	if o.LatencyProbes <= 0 {
		o.LatencyProbes = 4
	}
	if o.LatencyHTTPProbes <= 0 {
		o.LatencyHTTPProbes = 1
	}
	if o.LatencyHTTPTimeoutMs <= 0 {
		o.LatencyHTTPTimeoutMs = 3000
	}
	if o.RemoveAfterFailures <= 0 {
		o.RemoveAfterFailures = 3
	}
	if o.LatencyConcurrency <= 0 {
		o.LatencyConcurrency = 50
	}
	if o.SpeedDurationSec <= 0 {
		o.SpeedDurationSec = 5
	}
	if o.SpeedConcurrency <= 0 {
		o.SpeedConcurrency = 5
	}
	if o.DownloadURL == "" {
		o.DownloadURL = engine.DefaultSpeedOptions().DownloadURL
	}
	if o.Protocol != "http" {
		o.Protocol = "https"
	}
	return o
}

// GroupReport 是单个分组的运行结果。
type GroupReport struct {
	Name        string `json:"name"`
	Target      int    `json:"target"`
	Filled      int    `json:"filled"`
	Shortage    int    `json:"shortage"`
	Tested      int    `json:"tested"`      // 延迟测试数
	Failed      int    `json:"failed"`      // 本轮延迟失败数（含移除与失败留库）
	SpeedTested int    `json:"speedTested"` // 测速数
	SpeedFailed int    `json:"speedFailed"` // 测速失败（保留条目）
}

// Report 是一次运行的汇总。
type Report struct {
	TaskID         string        `json:"taskId,omitempty"`
	Subscription   string        `json:"subscription"`
	StartedAt      time.Time     `json:"startedAt"`
	FinishedAt     time.Time     `json:"finishedAt"`
	DurationMs     int64         `json:"durationMs"`
	Groups         []GroupReport `json:"groups"`
	OutputPath     string        `json:"outputPath"`
	TotalLines     int           `json:"totalLines"`
	Shortages      []string      `json:"shortages"`
	ShortageTotal  int           `json:"shortageTotal"`
	Candidates     int           `json:"candidates"`     // 本轮可用候选总数
	Tested         int           `json:"tested"`         // 实际延迟检测的唯一候选数
	Passed         int           `json:"passed"`         // 延迟通过数
	Failed         int           `json:"failed"`         // 延迟失败数
	SpeedTested    int           `json:"speedTested"`    // 实际测速的唯一候选数
	SpeedPassed    int           `json:"speedPassed"`    // 测速返回有效速度数（不代表满足规则阈值）
	SpeedFailed    int           `json:"speedFailed"`    // 测速无有效结果数
	LibraryUpdated int           `json:"libraryUpdated"` // 本地库实际更新的唯一条目数；远程只读库恒为 0
	RemovedDead    int           `json:"removedDead"`
	RetainedFailed int           `json:"retainedFailed"` // 延迟失败但未达移除阈值，留库待复检
	InputAdded     int           `json:"inputAdded"`     // 从初始化来源新导入的 IP 数
	InputUpdated   int           `json:"inputUpdated"`   // 初始化来源与库重叠更新的条数
}

// RunTask 执行一次维护任务：
//
//	1.（可选）按任务「初始化来源」把官方/URL/文件解析出的 IP 导入绑定的 IP 库（已有条目保留元数据）；
//	2. 把任务规则展开成分组（多条件笛卡尔积，每个组合取前 N）；
//	3. 只从 IP 库收集候选，检测 / 清理 / 回写 / 补足；
//	4. 合并去重后按任务总数限制截断，渲染并原子写出输出文件。
func RunTask(ctx context.Context, t Tester, lib *library.Store, task Task, opts RunOptions, prog ProgressFunc) (*Report, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}
	groups, err := expandTaskRules(task)
	if err != nil {
		return nil, err
	}
	inputPath := ""
	if task.Input.Mode == "file" {
		inputPath = task.Input.File
	}
	if opts.Protocol == "" {
		opts.Protocol = task.LibraryProtocol
		if opts.Protocol == "" {
			opts.Protocol = "https"
		}
	}
	output := Output{Path: task.Output.Path, Format: task.Output.Format, Template: task.Output.Template, Sort: task.Output.Sort}
	report, err := runCore(ctx, t, lib, groups, inputPath, output, task.SpeedEnabled, task.Limit, opts, prog)
	if err != nil {
		return report, err
	}
	report.TaskID = task.ID
	report.Subscription = task.Name
	return report, nil
}

// runCore 执行核心编排流程：
//
//	0.（可选）初始化来源先导入 IP 库（官方/远程库为内存临时库）；候选只从库内收集。
//	1. 为每个规则建立候选队列：已知匹配优先，未知字段候选分段交错；
//	2. 按当前剩余缺口组成自适应小批次，执行延迟检测并立即回写本地库；
//	3. 只有延迟通过且规则要求速度的候选进入测速漏斗；测速失败只留库待复检；
//	4. 每批结束立即填充规则，规则达标后不再为其取候选；全部达标或候选耗尽即停止；
//	5. 合并去重、排序并原子写出；缺口按“目标/达标/缺少条数”报告。

// 官方/远程库（opts.RemoteLibrary=true）只检测与输出：延迟/测速结果、失败移除均不写回库。
// ctx 取消时保存已完成的库更新并返回部分报告（带 context 错误）。
func runCore(ctx context.Context, t Tester, lib *library.Store, groups []Group, inputPath string, out Output, enableSpeed bool, totalLimit int, opts RunOptions, prog ProgressFunc) (*Report, error) {
	opts = opts.withDefaults()
	now := time.Now()
	report := &Report{StartedAt: now, Groups: make([]GroupReport, len(groups))}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if prog == nil {
		prog = func(Progress) error { return nil }
	}
	for gi, g := range groups {
		report.Groups[gi] = GroupReport{Name: g.Name, Target: g.Count}
	}

	// 0.（可选）初始化来源：先导入维护库，再从库中按需取候选。
	if len(opts.InputTargets) > 0 {
		added, updated := importInputTargets(lib, opts.InputTargets, now, sourceForInput(opts.InputSource))
		report.InputAdded, report.InputUpdated = added, updated
		label := opts.InputSource
		if label == "" {
			label = "初始化来源"
		}
		_ = prog(Progress{Stage: "input", Tested: added + updated, Log: fmt.Sprintf("初始化导入%s：新增 %d / 更新 %d", label, added, updated)})
	} else if strings.TrimSpace(inputPath) != "" {
		added, updated, err := importInputFile(lib, inputPath, now)
		if err != nil {
			return nil, fmt.Errorf("读取初始化文件失败: %w", err)
		}
		report.InputAdded, report.InputUpdated = added, updated
		_ = prog(Progress{Stage: "input", Tested: added + updated,
			Log: fmt.Sprintf("初始化导入文件 %s：新增 %d / 更新 %d", inputPath, added, updated)})
	}

	// 1. 构建漏斗候选队列。已知符合规则的条目优先；字段未知的条目分段交错，
	// 避免远程列表按国家聚类时只检测列表前部。同一候选全局只检测一次。
	type funnelCandidate struct {
		entry       library.Entry
		originalKey string
	}
	defaultPort := 443
	if opts.Protocol == "http" {
		defaultPort = 80
	}
	candidateByKey := make(map[string]funnelCandidate)
	for _, raw := range lib.All() {
		e := raw
		originalKey := raw.Key()
		if e.Port == 0 {
			e.Port = defaultPort
		}
		if candidateMaxPriority(groups, e) == 0 {
			continue
		}
		key := e.Key()
		if prev, exists := candidateByKey[key]; exists {
			// 同时存在“端口 0”和实际默认端口时，保留实际端口记录。
			if raw.Port != 0 || prev.originalKey == key {
				candidateByKey[key] = funnelCandidate{entry: e, originalKey: originalKey}
			}
			continue
		}
		candidateByKey[key] = funnelCandidate{entry: e, originalKey: originalKey}
	}
	candidateKeys := make([]string, 0, len(candidateByKey))
	for key := range candidateByKey {
		candidateKeys = append(candidateKeys, key)
	}
	sort.Strings(candidateKeys)
	report.Candidates = len(candidateKeys)

	queues := make([][]string, len(groups))
	knownCounts := make([]int, len(groups))
	for gi, g := range groups {
		var known, unknown []string
		for _, key := range candidateKeys {
			p := g.CandidatePriority(candidateByKey[key].entry)
			switch p {
			case 2:
				known = append(known, key)
			case 1:
				unknown = append(unknown, key)
			}
		}
		sort.SliceStable(known, func(i, j int) bool {
			a, b := candidateByKey[known[i]].entry, candidateByKey[known[j]].entry
			aReady := groupMatches(g, a) && (!groupNeedsSpeed(g, enableSpeed) || g.SpeedOK(effectiveSpeed(a), a.SpeedValid))
			bReady := groupMatches(g, b) && (!groupNeedsSpeed(g, enableSpeed) || g.SpeedOK(effectiveSpeed(b), b.SpeedValid))
			if aReady != bReady {
				return aReady
			}
			if a.ConsecutiveFailures != b.ConsecutiveFailures {
				return a.ConsecutiveFailures < b.ConsecutiveFailures
			}
			if (a.TCPLatencyMs > 0) != (b.TCPLatencyMs > 0) {
				return a.TCPLatencyMs > 0
			}
			if a.TCPLatencyMs > 0 && b.TCPLatencyMs > 0 && a.TCPLatencyMs != b.TCPLatencyMs {
				return a.TCPLatencyMs < b.TCPLatencyMs
			}
			return known[i] < known[j]
		})
		knownCounts[gi] = len(known)
		queues[gi] = append(known, spreadCandidateKeys(unknown, len(groups))...)
	}

	if report.Candidates == 0 {
		_ = prog(Progress{Stage: "gather", Log: "没有与规则可能匹配的候选"})
	} else {
		parts := make([]string, 0, len(groups))
		for gi, g := range groups {
			parts = append(parts, fmt.Sprintf("%s 已知 %d/可探测 %d", g.Name, knownCounts[gi], len(queues[gi])))
		}
		_ = prog(Progress{Stage: "gather", Tested: report.Candidates,
			Log: fmt.Sprintf("漏斗候选 %d 条：%s（按需检测，规则达标即停止）", report.Candidates, strings.Join(parts, "、"))})
	}

	selected := make([]library.Entry, 0)
	selectedKeys := make(map[string]bool)
	outGroup := make(map[string]int)
	testedKeys := make(map[string]bool)
	updatedKeys := make(map[string]bool)
	queuePos := make([]int, len(groups))
	nextGroup := 0

	needsMore := func(gi int) bool {
		g := groups[gi]
		return g.Count <= 0 || report.Groups[gi].Filled < g.Count
	}
	allLimitedGroupsFilled := func() bool {
		for gi, g := range groups {
			if g.Count <= 0 || report.Groups[gi].Filled < g.Count {
				return false
			}
		}
		return len(groups) > 0
	}
	limitReached := func() bool {
		return totalLimit > 0 && len(selected) >= totalLimit
	}

	enableTLS := opts.Protocol != "http"
	latOpts := engine.LatencyOptions{
		MaxConcurrency: opts.LatencyConcurrency, TimeoutMs: opts.LatencyTimeoutMs,
		ProbeCount: opts.LatencyProbes, HTTPProbeCount: opts.LatencyHTTPProbes,
		HTTPTimeoutMs: opts.LatencyHTTPTimeoutMs, EnableTLS: enableTLS,
	}
	// 测速源：auto 等原始配置统一解析为实际下载 URL，并在开始日志标注实际来源，
	// 便于排查「所有 IP 测速都卡在同一上限」时确认瓶颈在源端还是链路上。
	// 仅当确有规则需要测速时才解析，避免无谓的运营商探测。
	speedSourceLabel := ""
	if enableSpeed {
		for _, g := range groups {
			if groupNeedsSpeed(g, enableSpeed) {
				if resolvedURL, label := t.ResolveSpeedURL(ctx, opts.DownloadURL, enableTLS); resolvedURL != "" {
					opts.DownloadURL = resolvedURL
					speedSourceLabel = label
				}
				break
			}
		}
	}
	speedOpts := engine.SpeedOptions{
		MaxConcurrency: opts.SpeedConcurrency, DurationSec: opts.SpeedDurationSec,
		MinSpeedKBs: 0, EnableTLS: enableTLS, DownloadURL: opts.DownloadURL,
	}
	stoppedByQuota := false
	var lastLogged int64
	var progressLogMu sync.Mutex

	// 并发数：需要同时测速时按测速工作台规则取 min(延迟并发, 测速并发)，
	// 每个候选走“延迟 → 测速 → 判定”流水线，够数即停。
	concurrency := opts.LatencyConcurrency
	if enableSpeed {
		for _, g := range groups {
			if groupNeedsSpeed(g, true) {
				if opts.SpeedConcurrency < concurrency {
					concurrency = opts.SpeedConcurrency
				}
				break
			}
		}
	}
	if concurrency < 1 {
		concurrency = 1
	}

	var (
		mu          sync.Mutex
		completed   int64
		passed      int64
		maxSpeedKBs float64
	)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pickNext := func() (string, []int, bool) {
		if len(groups) == 0 {
			return "", nil, false
		}
		for attempt := 0; attempt < len(groups); attempt++ {
			gi := (nextGroup + attempt) % len(groups)
			if !needsMore(gi) {
				continue
			}
			for queuePos[gi] < len(queues[gi]) && testedKeys[queues[gi][queuePos[gi]]] {
				queuePos[gi]++
			}
			if queuePos[gi] >= len(queues[gi]) {
				continue
			}
			key := queues[gi][queuePos[gi]]
			queuePos[gi]++
			nextGroup = (gi + 1) % len(groups)
			testedKeys[key] = true
			c := candidateByKey[key]
			potential := make([]int, 0, 4)
			for pgi, g := range groups {
				if needsMore(pgi) && g.CandidatePriority(c.entry) > 0 {
					potential = append(potential, pgi)
					report.Groups[pgi].Tested++
				}
			}
			report.Tested++
			return key, potential, true
		}
		return "", nil, false
	}

	emitPipelineProgress := func(force bool) {
		c := atomic.LoadInt64(&completed)
		progressLogMu.Lock()
		if !force && c-lastLogged < int64(concurrency) {
			progressLogMu.Unlock()
			return
		}
		lastLogged = c
		progressLogMu.Unlock()

		mu.Lock()
		spd := report.SpeedTested
		spdOK := report.SpeedPassed
		filled := len(selected)
		p := int(atomic.LoadInt64(&passed))
		maxKBs := maxSpeedKBs
		mu.Unlock()

		maxMbps := maxKBs * 8192 / 1e6
		_ = prog(Progress{Stage: "latency", Tested: int(c), Passed: p,
			Log: fmt.Sprintf("延迟+测速进行中：累计 %d/%d，延迟通过 %d，测速 %d（有效 %d），已选 %d，最高 %.2f Mbps",
				c, report.Candidates, p, spd, spdOK, filled, maxMbps)})
	}

	startLog := fmt.Sprintf("开始漏斗检测：候选 %d 条，并发 %d（延迟并发 %d，测速并发 %d），逐条「延迟→测速→判定」",
		report.Candidates, concurrency, opts.LatencyConcurrency, opts.SpeedConcurrency)
	if speedSourceLabel != "" {
		startLog += "；测速源：" + speedSourceLabel
	}
	_ = prog(Progress{Stage: "latency", Log: startLog})

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	stopRequested := false

	worker := func() {
		defer wg.Done()
		defer func() { <-sem }()
		for {
			mu.Lock()
			if stopRequested || runCtx.Err() != nil {
				mu.Unlock()
				return
			}
			key, potential, ok := pickNext()
			if !ok {
				mu.Unlock()
				return
			}
			mu.Unlock()

			c := candidateByKey[key]
			target := engine.Target{IP: c.entry.IP, Port: c.entry.Port}

			res, valid := t.LatencyOne(runCtx, target, latOpts)
			if runCtx.Err() != nil {
				return
			}
			atomic.AddInt64(&completed, 1)

			if !valid {
				mu.Lock()
				report.Failed++
				for _, gi := range potential {
					report.Groups[gi].Failed++
				}
				if !opts.RemoteLibrary {
					e := c.entry
					e.ConsecutiveFailures++
					e.LastCheckedAt = time.Now()
					if c.originalKey != key {
						lib.RemoveKey(c.originalKey)
					}
					if e.ConsecutiveFailures >= opts.RemoveAfterFailures {
						lib.RemoveKey(key)
						report.RemovedDead++
					} else {
						lib.Upsert(e)
						updatedKeys[key] = true
						report.RetainedFailed++
					}
				}
				mu.Unlock()
				emitPipelineProgress(false)
				continue
			}

			atomic.AddInt64(&passed, 1)
			updated, _ := applyResult(c.entry, res, time.Now())

			mu.Lock()
			report.Passed++
			if !opts.RemoteLibrary {
				if c.originalKey != key {
					lib.RemoveKey(c.originalKey)
				}
				lib.Upsert(updated)
				updatedKeys[key] = true
			}
			matching := make([]int, 0, 4)
			needSpeed := false
			for gi, g := range groups {
				if !needsMore(gi) || !groupMatches(g, updated) {
					continue
				}
				matching = append(matching, gi)
				if groupNeedsSpeed(g, enableSpeed) {
					needSpeed = true
				}
			}
			mu.Unlock()

			if needSpeed {
				spd := t.SpeedOne(runCtx, target, speedOpts)
				if runCtx.Err() != nil {
					return
				}
				mu.Lock()
				if spd > maxSpeedKBs {
					maxSpeedKBs = spd
				}
				report.SpeedTested++
				for _, gi := range matching {
					if groupNeedsSpeed(groups[gi], enableSpeed) {
						report.Groups[gi].SpeedTested++
					}
				}
				if spd > 0 {
					updated.DownloadSpeedKBs = spd
					updated.SpeedKBs = spd
					updated.SpeedValid = true
					report.SpeedPassed++
				} else {
					updated.SpeedValid = false
					report.SpeedFailed++
					for _, gi := range matching {
						if groupNeedsSpeed(groups[gi], enableSpeed) {
							report.Groups[gi].SpeedFailed++
						}
					}
				}
				if !opts.RemoteLibrary {
					lib.Upsert(updated)
					updatedKeys[key] = true
				}
				mu.Unlock()
			}

			mu.Lock()
			for _, gi := range matching {
				g := groups[gi]
				if !needsMore(gi) {
					continue
				}
				if groupNeedsSpeed(g, enableSpeed) && !g.SpeedOK(effectiveSpeed(updated), updated.SpeedValid) {
					continue
				}
				report.Groups[gi].Filled++
				if !selectedKeys[key] {
					selectedKeys[key] = true
					outGroup[key] = gi
					selected = append(selected, updated)
				}
			}
			stopNow := allLimitedGroupsFilled() || limitReached()
			if stopNow {
				stopRequested = true
				stoppedByQuota = true
			}
			mu.Unlock()

			emitPipelineProgress(stopNow)
			if stopNow {
				return
			}
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go worker()
	}
	wg.Wait()
	cancel()

	if ctx.Err() != nil {
		report.LibraryUpdated = len(updatedKeys)
		_ = lib.Save()
		return report, ctx.Err()
	}

	report.LibraryUpdated = len(updatedKeys)
	stopReason := "候选已耗尽"
	if stoppedByQuota || allLimitedGroupsFilled() {
		stopReason = "所有规则已达标"
	} else if limitReached() {
		stopReason = "已达到任务总数上限"
	}
	latencyNote := fmt.Sprintf("漏斗检测结束：%s；实际检测 %d/%d，通过 %d，失败 %d", stopReason, report.Tested, report.Candidates, report.Passed, report.Failed)
	if opts.RemoteLibrary {
		latencyNote += "（远程只读，不改来源库）"
	} else {
		latencyNote += fmt.Sprintf("（库更新 %d，移除 %d，失败留库 %d）", report.LibraryUpdated, report.RemovedDead, report.RetainedFailed)
	}
	_ = prog(Progress{Stage: "latency", Round: 1, Tested: report.Tested, Passed: report.Passed, Failed: report.Failed, Log: latencyNote})
	if report.SpeedTested > 0 {
		_ = prog(Progress{Stage: "speed", Round: 1, Tested: report.SpeedTested, Failed: report.SpeedFailed,
			Log: fmt.Sprintf("按需测速结束：测试 %d，有效 %d，失败 %d（失败条目保留待复检）", report.SpeedTested, report.SpeedPassed, report.SpeedFailed)})
	}

	shortHint := "候选已耗尽：请增加该条件候选，或放宽规则条件"
	if opts.RemoteLibrary {
		shortHint = "远程候选已耗尽：请增加来源候选，或放宽规则条件"
	}
	for gi := range report.Groups {
		g := groups[gi]
		if g.Count > 0 && report.Groups[gi].Filled < g.Count {
			report.Groups[gi].Shortage = g.Count - report.Groups[gi].Filled
			report.ShortageTotal += report.Groups[gi].Shortage
			report.Shortages = append(report.Shortages, fmt.Sprintf("分组 %q 目标 %d，达标 %d，缺 %d（%s）",
				report.Groups[gi].Name, g.Count, report.Groups[gi].Filled, report.Groups[gi].Shortage, shortHint))
		}
	}

	// 输出按规则顺序分组，组内按配置排序；漏斗中未额外检测“只为比较速度/延迟”的候选。
	sortOutputGrouped(selected, out.Sort, outGroup)
	if totalLimit > 0 && len(selected) > totalLimit {
		selected = selected[:totalLimit]
	}
	path, err := WriteOutput(lib.BaseDir(), out, selected)
	if err != nil {
		_ = lib.Save()
		return report, fmt.Errorf("写出订阅文件失败: %w", err)
	}
	report.OutputPath = path
	report.TotalLines = len(selected)
	report.FinishedAt = time.Now()
	report.DurationMs = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	_ = prog(Progress{Stage: "output", Filled: len(selected), Log: fmt.Sprintf("已写出 %d 行 → %s", len(selected), path)})
	if err := lib.Save(); err != nil {
		return report, fmt.Errorf("保存 IP 库失败: %w", err)
	}
	return report, nil
}

// spreadCandidateKeys 将未知候选按分段交错顺序排列，使前几个候选也能覆盖列表首尾与中间区域。
func spreadCandidateKeys(keys []string, buckets int) []string {
	if len(keys) < 2 {
		return append([]string(nil), keys...)
	}
	if buckets < 2 {
		buckets = 2
	}
	if buckets > len(keys) {
		buckets = len(keys)
	}
	out := make([]string, 0, len(keys))
	for offset := 0; len(out) < len(keys); offset++ {
		for bucket := 0; bucket < buckets; bucket++ {
			start := bucket * len(keys) / buckets
			end := (bucket + 1) * len(keys) / buckets
			idx := start + offset
			if idx < end {
				out = append(out, keys[idx])
			}
		}
	}
	return out
}

// groupNeedsSpeed 判断分组是否需要在本次运行中测速：任务开启测速且该分组设了速度范围。
func groupNeedsSpeed(g Group, enableSpeed bool) bool {
	return enableSpeed && (g.RequireSpeed || g.MinSpeedKBs > 0 || g.MaxSpeedKBs > 0)
}

// expandTaskRules 把任务规则展开成编排分组：规则内多条件取笛卡尔积，
// 每个组合 = 一个分组（国家/城市/端口 交集），取前 Limit 条。
func expandTaskRules(task Task) ([]Group, error) {
	var groups []Group
	seen := make(map[string]bool)
	for _, rule := range task.Rules {
		combos := expandCombinations(rule.Conditions)
		if len(combos) == 0 {
			combos = []map[string]string{{}}
		}
		for _, combo := range combos {
			g := Group{
				Name:         rule.Name,
				CountryCode:  engine.NormalizeCountry(combo["country"]),
				LatencyMinMs: rule.LatencyMin,
				MaxLatencyMs: rule.LatencyMax,
				MinSpeedKBs:  rule.SpeedMin,
				MaxSpeedKBs:  rule.SpeedMax,
				RequireSpeed: task.SpeedEnabled && (rule.SpeedMin > 0 || rule.SpeedMax > 0),
				Count:        rule.Limit,
			}
			if city := strings.TrimSpace(combo["city"]); city != "" {
				g.Cities = []string{city}
			}
			if dc := strings.TrimSpace(combo["dataCenter"]); dc != "" {
				g.DataCenters = []string{strings.ToUpper(dc)}
			}
			if region := strings.TrimSpace(combo["region"]); region != "" {
				g.Regions = []string{region}
			}
			if asn := strings.TrimSpace(combo["asn"]); asn != "" {
				n, aerr := strconv.ParseUint(asn, 10, 32)
				if aerr != nil || n == 0 {
					return nil, fmt.Errorf("任务 %q 规则 %s ASN 非法: %s", task.Name, rule.Name, asn)
				}
				g.ASNs = []uint{uint(n)}
			}
			if port := strings.TrimSpace(combo["port"]); port != "" {
				p, perr := strconv.Atoi(port)
				if perr != nil || p < 1 || p > 65535 {
					return nil, fmt.Errorf("任务 %q 规则 %s 端口非法: %s", task.Name, rule.Name, port)
				}
				g.Ports = []int{p}
			}
			if len(combos) > 1 {
				g.Name = fmt.Sprintf("%s-%d", rule.Name, len(groups)+1)
			}
			key := fmt.Sprintf("%s|%v|%v|%v|%v|%v|%d|%d|%v|%v|%d", g.CountryCode, g.Cities, g.DataCenters, g.Regions, g.ASNs, g.Ports, g.LatencyMinMs, g.MaxLatencyMs, g.MinSpeedKBs, g.MaxSpeedKBs, g.Count)
			if seen[key] {
				continue
			}
			seen[key] = true
			groups = append(groups, g)
		}
	}
	return groups, nil
}

// expandCombinations 计算条件字段取值集合的笛卡尔积；空值条件 = 不限，不参与组合。
func expandCombinations(conds []Condition) []map[string]string {
	combos := []map[string]string{{}}
	for _, c := range conds {
		if len(c.Values) == 0 {
			continue
		}
		var next []map[string]string
		for _, combo := range combos {
			for _, v := range c.Values {
				cp := make(map[string]string, len(combo)+1)
				for k, vv := range combo {
					cp[k] = vv
				}
				cp[c.Field] = v
				next = append(next, cp)
			}
		}
		combos = next
	}
	return combos
}

// groupMatches 判断条目是否符合分组的筛选约束（国家、延迟、城市、DC、区域、ASN、端口）。
func groupMatches(g Group, e library.Entry) bool {
	if (g.CountryCode != "" && !strings.EqualFold(e.CountryCode, g.CountryCode)) || !g.LatencyOK(e.TCPLatencyMs) {
		return false
	}
	if len(g.Cities) > 0 && !containsFold(g.Cities, e.CityZh) {
		return false
	}
	if len(g.DataCenters) > 0 && !containsFold(g.DataCenters, e.DataCenter) {
		return false
	}
	if len(g.Regions) > 0 && !containsFold(g.Regions, e.RegionZh) {
		return false
	}
	if len(g.ASNs) > 0 && !containsUint(g.ASNs, e.ASN) {
		return false
	}
	if len(g.Ports) > 0 && !containsInt(g.Ports, e.Port) {
		return false
	}
	return true
}

// applyResult 把一次延迟检测结果回写到条目，返回（更新后的条目，是否有数据变化）。
// 国家/城市/ASN/延迟等以最新检测为准整体覆盖。
func applyResult(e library.Entry, res engine.Result, now time.Time) (library.Entry, bool) {
	before := e
	// 延迟阶段返回的是完整 Result；非空/非零字段以最新检测结果为准，保留库元数据。
	if res.TCPLatencyMs > 0 {
		e.TCPLatencyMs = res.TCPLatencyMs
	}
	if res.DataCenter != "" {
		e.DataCenter = res.DataCenter
	}
	if res.LocCode != "" {
		e.LocCode = res.LocCode
	}
	if res.Region != "" {
		e.Region = res.Region
	}
	if res.City != "" {
		e.City = res.City
	}
	if res.RegionZh != "" {
		e.RegionZh = res.RegionZh
	}
	if res.Country != "" {
		e.Country = res.Country
	}
	if res.CountryCode != "" {
		e.CountryCode = res.CountryCode
	}
	if res.CityZh != "" {
		e.CityZh = res.CityZh
	}
	if res.Emoji != "" {
		e.Emoji = res.Emoji
	}
	if res.OutboundIP != "" {
		e.OutboundIP = res.OutboundIP
	}
	if res.IPType != "" {
		e.IPType = res.IPType
	}
	if res.VisitScheme != "" {
		e.VisitScheme = res.VisitScheme
	}
	if res.TLSVersion != "" {
		e.TLSVersion = res.TLSVersion
	}
	if res.SNI != "" {
		e.SNI = res.SNI
	}
	if res.HTTPVersion != "" {
		e.HTTPVersion = res.HTTPVersion
	}
	if res.WARP != "" {
		e.WARP = res.WARP
	}
	if res.Gateway != "" {
		e.Gateway = res.Gateway
	}
	if res.RBI != "" {
		e.RBI = res.RBI
	}
	if res.KEX != "" {
		e.KEX = res.KEX
	}
	if res.Timestamp != "" {
		e.Timestamp = res.Timestamp
	}
	if res.ASN != 0 {
		e.ASN = res.ASN
	}
	if res.ASNOrg != "" {
		e.ASNOrg = res.ASNOrg
	}
	if res.IPSType != "" {
		e.IPSType = res.IPSType
	}
	if res.DownloadSpeedKBs != 0 {
		e.DownloadSpeedKBs = res.DownloadSpeedKBs
		e.SpeedKBs = res.DownloadSpeedKBs
		e.SpeedValid = res.DownloadSpeedKBs > 0
	}
	e.Status = library.StatusActive
	e.LastCheckedAt = now
	e.Checks++
	e.ConsecutiveFailures = 0 // 检测通过，重置连续失败计数
	if e.Source == "" {
		e.Source = library.SourceTopup
	}
	return e, e != before
}

func effectiveSpeed(e library.Entry) float64 {
	if e.DownloadSpeedKBs != 0 {
		return e.DownloadSpeedKBs
	}
	return e.SpeedKBs
}

// sortOutputGrouped 对最终结果按规则分组顺序排序：同一条规则/分组的条目聚在一起
// （规则里先写的国家/分组排前面），组内再按配置的排序键（默认延迟升序）排序。
// 单分组时等价于全局排序。
func sortOutputGrouped(entries []library.Entry, sortKey string, groupOf map[string]int) {
	sort.SliceStable(entries, func(i, j int) bool {
		gi, gj := groupOf[entries[i].Key()], groupOf[entries[j].Key()]
		if gi != gj {
			return gi < gj
		}
		return lessBySortKey(entries[i], entries[j], sortKey)
	})
}

// lessBySortKey 按输出排序键比较两条目（仅组内排序键；键相同时按条目 Key 稳定兜底）。
func lessBySortKey(a, b library.Entry, sortKey string) bool {
	speedDesc := sortKey == OutputSortSpeedDesc
	switch sortKey {
	case OutputSortLatencyDesc:
		if a.TCPLatencyMs != b.TCPLatencyMs {
			return a.TCPLatencyMs > b.TCPLatencyMs
		}
	case OutputSortSpeedDesc, OutputSortSpeedAsc:
		if a.SpeedValid != b.SpeedValid {
			return a.SpeedValid // 有效测速结果排前面
		}
		if a.SpeedValid {
			sa, sb := effectiveSpeed(a), effectiveSpeed(b)
			if sa != sb {
				if speedDesc {
					return sa > sb
				}
				return sa < sb
			}
		}
		if a.TCPLatencyMs != b.TCPLatencyMs {
			return a.TCPLatencyMs < b.TCPLatencyMs
		}
	case OutputSortIPAsc:
		if cmp := compareIP(a.IP, b.IP); cmp != 0 {
			return cmp < 0
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
	case OutputSortCountryAsc:
		if cmp := strings.Compare(countrySortKey(a), countrySortKey(b)); cmp != 0 {
			return cmp < 0
		}
	default: // latencyAsc（含旧任务空值）
		if a.TCPLatencyMs != b.TCPLatencyMs {
			return a.TCPLatencyMs < b.TCPLatencyMs
		}
	}
	return a.Key() < b.Key()
}

// sortOutput 按输出排序方式对最终结果做全局排序（仅测试与独立调用使用；
// 任务编排统一走 sortOutputGrouped，保证规则顺序优先）。
func sortOutput(entries []library.Entry, sortKey string) {
	sort.SliceStable(entries, func(i, j int) bool {
		return lessBySortKey(entries[i], entries[j], sortKey)
	})
}

// countrySortKey 返回稳定的国家排序键：优先 ISO 二字母码，其次国家名，
// 两者皆空（无国家信息）的条目排最后。
func countrySortKey(e library.Entry) string {
	switch {
	case e.CountryCode != "":
		return "0" + e.CountryCode
	case e.Country != "":
		return "1" + e.Country
	default:
		return "2"
	}
}

// compareIP 按 IP 地址数值比较；无法解析的字符串排后面。
func compareIP(x, y string) int {
	ax, aerr := netip.ParseAddr(x)
	ay, yerr := netip.ParseAddr(y)
	switch {
	case aerr == nil && yerr == nil:
		return ax.Compare(ay)
	case aerr == nil:
		return -1
	case yerr == nil:
		return 1
	default:
		return strings.Compare(x, y)
	}
}

func sourceForInput(source string) string {
	if source == library.SourceOfficial {
		return library.SourceOfficial
	}
	return library.SourceImport
}

// importInputTargets 把初始化来源（官方/URL/文件）解析出的目标导入 IP 库。
// 已有条目保留全部检测元数据（国家/延迟/状态/次数等），仅补齐来源标记；
// 只有真正的新条目以 StatusNew 入库。避免每次维护重复导入把已验证条目重置回“未测”。
func importInputTargets(lib *library.Store, targets []engine.Target, now time.Time, source string) (int, int) {
	added, updated := 0, 0
	for _, t := range targets {
		if strings.TrimSpace(t.IP) == "" {
			continue
		}
		if existing, ok := lib.Get(t.IP, t.Port); ok {
			if existing.Source == "" {
				existing.Source = source
			}
			if existing.CountryCode == "" && t.CountryCode != "" {
				existing.CountryCode = t.CountryCode // 远程库标记补国家，便于本次按国家预筛
			}
			lib.Upsert(existing)
			updated++
			continue
		}
		lib.Upsert(library.Entry{IP: t.IP, Port: t.Port, CountryCode: t.CountryCode, Source: source, Status: library.StatusNew, FirstSeenAt: now})
		added++
	}
	return added, updated
}

// importInputFile 读取初始化来源文件（相对库目录或服务器绝对路径），解析出 IP 并导入 IP 库（新条目状态 new）。
// 解析复用 engine.ParseTargetsWithCIDR：支持 ip:port、ip:port#备注、CIDR 展开等现有输入逻辑。
func importInputFile(lib *library.Store, rel string, now time.Time) (int, int, error) {
	var abs string
	if filepath.IsAbs(rel) {
		abs = filepath.Clean(rel)
	} else {
		var err error
		abs, err = resolveInDir(lib.BaseDir(), rel)
		if err != nil {
			return 0, 0, err
		}
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return 0, 0, err
	}
	targets := engine.ParseTargetsWithCIDR(string(body), engine.SampleOnePerSubnet, 1)
	added, updated := importInputTargets(lib, targets, now, library.SourceImport)
	return added, updated, nil
}

// resolveInDir 把相对路径安全地解析到 dir 内，拒绝目录穿越与绝对路径逃逸。
func resolveInDir(dir, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("路径为空")
	}
	base := filepath.Clean(dir)
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(rel)))
	relTo, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(relTo, "..") || filepath.IsAbs(relTo) {
		return "", fmt.Errorf("路径必须在 %s 目录内: %s", dir, rel)
	}
	return target, nil
}

// WriteOutput 将订阅结果按格式渲染并原子写入输出文件（相对路径位于 dataDir 下，绝对路径直接写入运行主机位置）。
func WriteOutput(dataDir string, out Output, entries []library.Entry) (string, error) {
	if strings.TrimSpace(out.Path) == "" {
		return "", fmt.Errorf("输出路径为空")
	}
	if out.Format == "" {
		out.Format = "txt"
	}
	var path string
	if filepath.IsAbs(out.Path) {
		path = filepath.Clean(filepath.FromSlash(out.Path))
	} else {
		path = filepath.Join(dataDir, filepath.FromSlash(out.Path))
	}
	var lines []string
	if out.Format == "csv" {
		lines = RenderCSV(entries)
	} else {
		lines = RenderTXT(out.Template, entries)
	}
	body := []byte(strings.Join(lines, "\n"))
	if len(body) > 0 {
		body = append(body, '\n')
	}
	if err := fsutil.WriteFileAtomic(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
