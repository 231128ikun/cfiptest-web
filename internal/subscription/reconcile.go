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
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
)

// Tester 抽象检测能力，便于测试替换；*engine.Runner 满足该接口。
type Tester interface {
	RunLatencyTest(ctx context.Context, targets []engine.Target, opts engine.LatencyOptions, cb engine.EventCallback) ([]engine.Result, error)
	RunSpeedTest(ctx context.Context, targets []engine.Target, opts engine.SpeedOptions, cb engine.EventCallback) error
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
	DownloadURL          string          // 测速文件地址（不含协议头），默认 engine 默认值
	SlackFactor          int             // 每组候选倍数：count*SlackFactor + SlackExtra，默认 3
	SlackExtra           int             // 默认 10
	MaxPerGroup          int             // 内部每组候选批次预算，默认 200；不限制最终检测数量
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
	if o.SlackFactor <= 0 {
		o.SlackFactor = 3
	}
	if o.SlackExtra <= 0 {
		o.SlackExtra = 10
	}
	if o.Protocol != "http" {
		o.Protocol = "https"
	}
	if o.MaxPerGroup <= 0 {
		o.MaxPerGroup = 200
	}
	return o
}

func (o RunOptions) candidateCap(count int) int {
	if count <= 0 {
		return o.MaxPerGroup
	}
	capN := count*o.SlackFactor + o.SlackExtra
	if capN < count {
		capN = count
	}
	if capN > o.MaxPerGroup {
		capN = o.MaxPerGroup
	}
	return capN
}

// GroupReport 是单个分组的运行结果。
type GroupReport struct {
	Name        string `json:"name"`
	Target      int    `json:"target"`
	Filled      int    `json:"filled"`
	Shortage    int    `json:"shortage"`
	Tested      int    `json:"tested"`      // 延迟测试数
	Failed      int    `json:"failed"`      // 本轮延迟失败数（含移除与标记保留）
	SpeedTested int    `json:"speedTested"` // 测速数
	SpeedFailed int    `json:"speedFailed"` // 测速失败（保留条目）
	Updated     int    `json:"updated"`     // 结果与库不一致回写数
	New         int    `json:"new"`         // 新入库条目数
}

// Report 是一次运行的汇总。
type Report struct {
	TaskID       string        `json:"taskId,omitempty"`
	Subscription string        `json:"subscription"`
	StartedAt    time.Time     `json:"startedAt"`
	FinishedAt   time.Time     `json:"finishedAt"`
	DurationMs   int64         `json:"durationMs"`
	Groups       []GroupReport `json:"groups"`
	OutputPath   string        `json:"outputPath"`
	TotalLines   int           `json:"totalLines"`
	Shortages    []string      `json:"shortages"`
	RemovedDead  int           `json:"removedDead"`
	MarkedFailed int           `json:"markedFailed"` // 延迟失败但未达阈值，保留并累计连续失败
	InputAdded   int           `json:"inputAdded"`   // 从初始化来源新导入的 IP 数
	InputUpdated int           `json:"inputUpdated"` // 初始化来源与库重叠更新的条数
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
//	1. 分批收集未检测候选：有配额缺口的分组优先，跨批次跳过已测条目；
//	2. 批量延迟检测：失败者软失败累计，达到阈值才移除；通过者回写元数据/延迟/状态；
//	3. 需要测速的分组对短名单批量测速：失败不判死，只标记 SpeedValid=false；
//	4. 每轮结束检查分组配额，全部满足或候选耗尽即停止；
//	5. 按分组配额取累计结果，合并去重，按输出排序方式排序；若设了总数限制则截断；
//	6. 渲染并原子写出输出文件。
//
// 官方/远程库（opts.RemoteLibrary=true）只检测与输出：延迟/测速结果、失败移除均不写回库。
// ctx 取消时保存已完成的库更新并返回部分报告（带 context 错误）。
func runCore(ctx context.Context, t Tester, lib *library.Store, groups []Group, inputPath string, out Output, enableSpeed bool, totalLimit int, opts RunOptions, prog ProgressFunc) (*Report, error) {
	opts = opts.withDefaults()
	now := time.Now()
	report := &Report{StartedAt: now, Groups: make([]GroupReport, len(groups))}
	if prog == nil {
		prog = func(Progress) error { return nil }
	}
	for gi, g := range groups {
		report.Groups[gi] = GroupReport{Name: g.Name, Target: g.Count}
	}

	// 0.（可选）初始化来源：官方/URL/文件先导入 IP 库（已有条目保留元数据）；候选只从库内收集。
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
		report.InputAdded = added
		report.InputUpdated = updated
		_ = prog(Progress{Stage: "input", Tested: added + updated,
			Log: fmt.Sprintf("初始化导入文件 %s：新增 %d / 更新 %d", inputPath, added, updated)})
	}

	// ---- 编排循环：分多轮收集 / 检测 / 补足 ----
	// 每批收集一组未检测候选（按分组候选预算控制内存和并发规模，跨批次跳过已测条目），
	// 直到所有有限配额满足、无限配额的候选耗尽或候选耗尽为止；
	// 官方/远程库（RemoteLibrary=true）只检测与输出，不做任何库回写。
	type cand struct {
		entry  library.Entry
		groups map[int]Group
	}
	tested := make(map[string]bool)            // 已做延迟检测的 key（跨轮累计，避免重复检测）
	freshAll := make(map[string]library.Entry) // 已通过延迟检测的条目（跨轮累计）
	speedPool := make(map[int][]library.Entry) // 需要测速的分组短名单（跨轮累计，已测速）
	speedTested := make(map[string]bool)       // 已做测速的 key（跨轮累计，避免重复测速）

	// groupQualified 统计该分组当前实测可用候选数（不做跨组去重，仅用于判断是否还需补充）。
	groupQualified := func(gi int, g Group) int {
		n := 0
		if groupNeedsSpeed(g, enableSpeed) {
			for _, e := range speedPool[gi] {
				if g.SpeedOK(effectiveSpeed(e), e.SpeedValid) {
					n++
				}
			}
			return n
		}
		for _, e := range freshAll {
			if groupMatches(g, e) {
				n++
			}
		}
		return n
	}

	// 内部按批次调度候选，直到配额满足或候选耗尽；不暴露为用户配置。
	for round := 1; ; round++ {
		// ---- 1. 收集本轮候选（跨分组去重；跳过已检测条目） ----
		// 有配额缺口的分组优先收集（缺口大的先取），已满足的分组不再为其追加候选。
		type needGroup struct {
			gi, need int
		}
		var shortGroups []needGroup
		var unlimitedGroups []int
		for gi, g := range groups {
			if g.Count > 0 {
				if need := g.Count - groupQualified(gi, g); need > 0 {
					shortGroups = append(shortGroups, needGroup{gi, need})
				}
			} else {
				unlimitedGroups = append(unlimitedGroups, gi) // 不限数量分组始终参与，但排在最后
			}
		}
		sort.SliceStable(shortGroups, func(a, b int) bool { return shortGroups[a].need > shortGroups[b].need })
		gatherOrder := make([]int, 0, len(groups))
		for _, sg := range shortGroups {
			gatherOrder = append(gatherOrder, sg.gi)
		}
		gatherOrder = append(gatherOrder, unlimitedGroups...)

		candidates := make(map[string]*cand)
		var order []string
		for _, gi := range gatherOrder {
			g := groups[gi]
			capN := opts.candidateCap(g.Count)
			matched := make([]library.Entry, 0, capN)
			for _, e := range lib.All() {
				if tested[e.Key()] || g.CandidatePriority(e) == 0 {
					continue
				}
				matched = append(matched, e)
			}
			sort.Slice(matched, func(i, j int) bool {
				pi, pj := g.CandidatePriority(matched[i]), g.CandidatePriority(matched[j])
				if pi != pj {
					return pi > pj
				}
				if matched[i].TCPLatencyMs != matched[j].TCPLatencyMs {
					return matched[i].TCPLatencyMs < matched[j].TCPLatencyMs
				}
				return matched[i].Key() < matched[j].Key()
			})
			if len(matched) > capN {
				matched = matched[:capN]
			}
			for _, e := range matched {
				key := e.Key()
				c, ok := candidates[key]
				if !ok {
					c = &cand{entry: e, groups: make(map[int]Group)}
					candidates[key] = c
					order = append(order, key)
				}
				c.groups[gi] = g
			}
		}
		if len(order) == 0 {
			// 候选耗尽（或所有分组已满足）：不再有可检测的新条目，输出当前累计结果。
			_ = prog(Progress{Stage: "gather", Round: round, Tested: 0,
				Log: fmt.Sprintf("第 %d 轮无可测新候选，本轮结束", round)})
			break
		}
		_ = prog(Progress{Stage: "gather", Round: round, Tested: len(order),
			Log: fmt.Sprintf("第 %d 轮收集候选 %d 条（内部批次，持续检测至满足配额或候选耗尽）", round, len(order))})

		// 解析未指定端口（HTTPS 默认 443，HTTP 默认 80），并让候选键跟随端口修正，
		// 否则按旧键（端口 0）找不到检测结果会被误判为失效。
		defaultPort := 443
		if opts.Protocol == "http" {
			defaultPort = 80
		}
		resolvedOrder := make([]string, 0, len(order))
		for _, originalKey := range order {
			key := originalKey
			c := candidates[key]
			e := c.entry
			if e.Port == 0 {
				e.Port = defaultPort
				c.entry = e
				newKey := e.Key()
				if newKey != key {
					delete(candidates, key)
					candidates[newKey] = c
					// 本地库需要把旧的 0 端口键迁移到实际端口；官方/远程候选库保持只读。
					if !opts.RemoteLibrary {
						lib.RemoveKey(key)
					}
				}
				key = newKey
			}
			// 同时记录来源键与修正后的检测键，避免只读候选中的 0 端口条目被下一轮重复检测。
			tested[originalKey] = true
			tested[key] = true
			resolvedOrder = append(resolvedOrder, key)
		}
		order = resolvedOrder

		// ---- 2. 批量延迟检测 ----
		targets := make([]engine.Target, 0, len(order))
		for _, key := range order {
			e := candidates[key].entry
			targets = append(targets, engine.Target{IP: e.IP, Port: e.Port})
		}
		enableTLS := opts.Protocol != "http"
		latOpts := engine.LatencyOptions{
			MaxConcurrency: opts.LatencyConcurrency,
			TimeoutMs:      opts.LatencyTimeoutMs,
			ProbeCount:     opts.LatencyProbes,
			HTTPProbeCount: opts.LatencyHTTPProbes,
			HTTPTimeoutMs:  opts.LatencyHTTPTimeoutMs,
			EnableTLS:      enableTLS,
		}
		results, err := t.RunLatencyTest(ctx, targets, latOpts, func(engine.Event) {})
		if err != nil && ctx.Err() != nil {
			_ = lib.Save()
			return report, ctx.Err()
		}
		resultByKey := make(map[string]engine.Result, len(results))
		for _, res := range results {
			resultByKey[library.Key(res.IP, res.Port)] = res
		}

		roundFresh := make(map[string]library.Entry, len(order))
		roundFailed := 0
		for _, key := range order {
			c := candidates[key]
			res, ok := resultByKey[key]
			if !ok {
				// 软失败：保留条目并累计连续失败，达到阈值才移除，避免单次抖动误删刚验活的 IP。
				e := c.entry
				e.ConsecutiveFailures++
				e.LastCheckedAt = now
				if e.ConsecutiveFailures >= opts.RemoveAfterFailures {
					if !opts.RemoteLibrary {
						lib.RemoveKey(key) // 连续失败达阈值才移除；官方/远程库不回写
					}
					report.RemovedDead++
				} else {
					if !opts.RemoteLibrary {
						lib.Upsert(e)
					}
					report.MarkedFailed++
				}
				roundFailed++
				for gi := range c.groups {
					report.Groups[gi].Tested++
					report.Groups[gi].Failed++
				}
				continue
			}
			updated, changed := applyResult(c.entry, res, now)
			isNew := false
			if !opts.RemoteLibrary {
				isNew = lib.Upsert(updated) // 官方/远程库不回写
			}
			freshAll[key] = updated
			roundFresh[key] = updated
			for gi := range c.groups {
				report.Groups[gi].Tested++
				if changed {
					report.Groups[gi].Updated++
				}
				if isNew {
					report.Groups[gi].New++
				}
			}
		}
		deadNote := fmt.Sprintf("（连续失败 %d 次后移除；已移除 %d、标记保留 %d）", opts.RemoveAfterFailures, report.RemovedDead, report.MarkedFailed)
		if opts.RemoteLibrary {
			deadNote = "（官方/远程库不删除、不回写）"
		}
		_ = prog(Progress{Stage: "latency", Round: round, Tested: len(order), Passed: len(roundFresh), Failed: roundFailed,
			Log: fmt.Sprintf("第 %d 轮延迟检测完成：通过 %d，失败 %d%s", round, len(roundFresh), roundFailed, deadNote)})
		if ctx.Err() != nil {
			_ = lib.Save()
			return report, ctx.Err()
		}

		// ---- 3. 测速短名单（仅需要测速的分组；跨轮去重） ----
		roundSpeedQueue := make(map[string]library.Entry)
		for _, g := range groups {
			if !groupNeedsSpeed(g, enableSpeed) {
				continue
			}
			pool := freshForGroup(roundFresh, g)
			capN := opts.candidateCap(g.Count)
			if len(pool) > capN {
				pool = pool[:capN]
			}
			for _, e := range pool {
				if !speedTested[e.Key()] {
					roundSpeedQueue[e.Key()] = e
				}
			}
		}
		if len(roundSpeedQueue) > 0 {
			speedTargets := make([]engine.Target, 0, len(roundSpeedQueue))
			for _, e := range roundSpeedQueue {
				speedTargets = append(speedTargets, engine.Target{IP: e.IP, Port: e.Port})
			}
			speedMap := make(map[string]float64, len(roundSpeedQueue))
			speedOpts := engine.SpeedOptions{
				MaxConcurrency: opts.SpeedConcurrency,
				DurationSec:    opts.SpeedDurationSec,
				MinSpeedKBs:    0, // 不过滤，阈值由分组判断，失败原因才能区分
				EnableTLS:      enableTLS,
				DownloadURL:    opts.DownloadURL,
			}
			sErr := t.RunSpeedTest(ctx, speedTargets, speedOpts, func(ev engine.Event) {
				if ev.Type != engine.EventSpeed || ev.Result == nil {
					return
				}
				speedMap[library.Key(ev.Result.IP, ev.Result.Port)] = ev.Result.DownloadSpeedKBs
			})
			if sErr != nil && ctx.Err() != nil {
				_ = lib.Save()
				return report, ctx.Err()
			}
			// 更新测速结果（失败不判死，仅标记无效；官方/远程库不回写）
			speedFailed := 0
			for key, e := range roundSpeedQueue {
				spd, ok := speedMap[key]
				updated := e
				if ok && spd > 0 {
					updated.DownloadSpeedKBs = spd
					updated.SpeedKBs = spd
					updated.SpeedValid = true
				} else {
					updated.SpeedValid = false
				}
				if !opts.RemoteLibrary {
					lib.Upsert(updated)
				}
				speedTested[key] = true
				if !updated.SpeedValid {
					speedFailed++
				}
				for gi, g := range groups {
					if !groupNeedsSpeed(g, enableSpeed) || !groupMatches(g, updated) {
						continue
					}
					speedPool[gi] = append(speedPool[gi], updated)
					gr := &report.Groups[gi]
					gr.SpeedTested++
					if !updated.SpeedValid {
						gr.SpeedFailed++
					}
				}
			}
			_ = prog(Progress{Stage: "speed", Round: round, Tested: len(roundSpeedQueue), Failed: speedFailed,
				Log: fmt.Sprintf("第 %d 轮测速完成：有效 %d，失败 %d（保留待下次验证）", round, len(roundSpeedQueue)-speedFailed, speedFailed)})
			if ctx.Err() != nil {
				_ = lib.Save()
				return report, ctx.Err()
			}
		}

		// ---- 4. 检查配额是否全部满足 ----
		allFilled := true
		for gi, g := range groups {
			// Count=0 表示不限数量：必须继续检测，直到该分组没有候选，
			// 不能把“没有数量目标”误判为“已经满足”。
			if g.Count <= 0 || groupQualified(gi, g) < g.Count {
				allFilled = false
				break
			}
		}
		if allFilled {
			break
		}
	}

	// ---- 5. 按分组配额选结果（多轮累计口径） ----
	var output []library.Entry
	seenOut := make(map[string]bool)
	for gi, g := range groups {
		gr := &report.Groups[gi]
		if groupNeedsSpeed(g, enableSpeed) {
			filled := 0
			pool := speedPool[gi]
			sort.Slice(pool, func(i, j int) bool {
				if pool[i].TCPLatencyMs != pool[j].TCPLatencyMs {
					return pool[i].TCPLatencyMs < pool[j].TCPLatencyMs
				}
				return pool[i].Key() < pool[j].Key()
			})
			for _, e := range pool {
				if g.Count > 0 && filled >= g.Count {
					break
				}
				if !g.SpeedOK(effectiveSpeed(e), e.SpeedValid) {
					continue
				}
				if !seenOut[e.Key()] {
					seenOut[e.Key()] = true
					output = append(output, e)
				}
				filled++
			}
			gr.Filled = filled
			if g.Count > 0 {
				gr.Shortage = g.Count - filled
			}
			continue
		}
		pool := freshForGroup(freshAll, g)
		filled := 0
		for _, e := range pool {
			if g.Count > 0 && filled >= g.Count {
				break
			}
			if !seenOut[e.Key()] {
				seenOut[e.Key()] = true
				output = append(output, e)
			}
			filled++
		}
		gr.Filled = filled
		if g.Count > 0 {
			gr.Shortage = g.Count - filled
		}
	}
	shortHint := "IP 库候选已耗尽：请用初始化来源导入更多该地区 IP，或放宽规则条件"
	if opts.RemoteLibrary {
		shortHint = "官方/远程候选已耗尽：请扩大抽样范围或放宽规则条件"
	}
	for gi := range report.Groups {
		if report.Groups[gi].Shortage > 0 {
			report.Shortages = append(report.Shortages, fmt.Sprintf("分组 %q 缺 %d 条（%s）",
				report.Groups[gi].Name, report.Groups[gi].Shortage, shortHint))
		}
	}

	// 输出排序（默认延迟升序；速度排序时未测速条目排最后），排序后再应用总数限制截断
	sortOutput(output, out.Sort)
	if totalLimit > 0 && len(output) > totalLimit {
		output = output[:totalLimit]
	}

	// ---- 5. 写出订阅文件 ----
	path, err := WriteOutput(lib.BaseDir(), out, output)
	if err != nil {
		_ = lib.Save()
		return report, fmt.Errorf("写出订阅文件失败: %w", err)
	}
	report.OutputPath = path
	report.TotalLines = len(output)
	report.FinishedAt = time.Now()
	report.DurationMs = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	_ = prog(Progress{Stage: "output", Filled: len(output), Log: fmt.Sprintf("已写出 %d 行 → %s", len(output), path)})

	if err := lib.Save(); err != nil {
		return report, fmt.Errorf("保存 IP 库失败: %w", err)
	}
	return report, nil
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
				CountryCode:  strings.ToUpper(strings.TrimSpace(combo["country"])),
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

// freshForGroup 从本次延迟通过的条目中，按分组实测约束筛选并排序。
func freshForGroup(fresh map[string]library.Entry, g Group) []library.Entry {
	pool := make([]library.Entry, 0)
	for _, e := range fresh {
		if groupMatches(g, e) {
			pool = append(pool, e)
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].TCPLatencyMs != pool[j].TCPLatencyMs {
			return pool[i].TCPLatencyMs < pool[j].TCPLatencyMs
		}
		return pool[i].Key() < pool[j].Key()
	})
	return pool
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

// sortOutput 按输出排序方式对最终结果排序。
// 延迟/IP 排序：TCPLatencyMs 或 IP 地址；速度排序时无有效测速值的条目一律排最后。
func sortOutput(entries []library.Entry, sortKey string) {
	speedDesc := sortKey == OutputSortSpeedDesc
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
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
			lib.Upsert(existing)
			updated++
			continue
		}
		lib.Upsert(library.Entry{IP: t.IP, Port: t.Port, Source: source, Status: library.StatusNew, FirstSeenAt: now})
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
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".out-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}
