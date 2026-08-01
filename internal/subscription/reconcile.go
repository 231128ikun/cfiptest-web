package subscription

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Stage       string `json:"stage"` // gather | latency | speed | fill | output
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
	LatencyTimeoutMs int    // 单连接延迟超时（ms），默认 1000
	SpeedDurationSec int    // 单 IP 测速时长（秒），默认 5
	SpeedConcurrency int    // 测速并发，默认 5
	DownloadURL      string // 测速文件地址（不含协议头），默认 engine 默认值
	SlackFactor      int    // 每组候选倍数：count*SlackFactor + SlackExtra，默认 3
	SlackExtra       int    // 默认 10
	MaxPerGroup      int    // 每组候选硬上限，默认 200
}

func (o RunOptions) withDefaults() RunOptions {
	if o.LatencyTimeoutMs <= 0 {
		o.LatencyTimeoutMs = 1000
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
	if o.MaxPerGroup <= 0 {
		o.MaxPerGroup = 200
	}
	return o
}

func (o RunOptions) candidateCap(count int) int {
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
	Failed      int    `json:"failed"`      // 延迟失败（已从库移除）
	SpeedTested int    `json:"speedTested"` // 测速数
	SpeedFailed int    `json:"speedFailed"` // 测速失败（保留条目）
	Updated     int    `json:"updated"`     // 结果与库不一致回写数
	New         int    `json:"new"`         // 新入库条目数
}

// Report 是一次运行的汇总。
type Report struct {
	Subscription string        `json:"subscription"`
	StartedAt    time.Time     `json:"startedAt"`
	FinishedAt   time.Time     `json:"finishedAt"`
	DurationMs   int64         `json:"durationMs"`
	Groups       []GroupReport `json:"groups"`
	OutputPath   string        `json:"outputPath"`
	TotalLines   int           `json:"totalLines"`
	Shortages    []string      `json:"shortages"`
	RemovedDead  int           `json:"removedDead"`
}

// Run 执行一次订阅维护：
//  1. 从库中收集候选（按分组，含国家未知条目；未指定端口的补 443）；
//  2. 批量延迟检测：失败者从库移除，通过者回写元数据/延迟/状态；
//  3. 需要测速的分组对短名单批量测速：失败不判死，只标记 SpeedValid=false；
//  4. 按分组配额取最新结果，去重后渲染并原子写出订阅文件。
//
// ctx 取消时保存已完成的库更新并返回部分报告（带 context 错误）。
func Run(ctx context.Context, t Tester, lib *library.Store, sub Subscription, opts RunOptions, prog ProgressFunc) (*Report, error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	now := time.Now()
	report := &Report{Subscription: sub.Name, StartedAt: now, Groups: make([]GroupReport, len(sub.Groups))}
	if prog == nil {
		prog = func(Progress) error { return nil }
	}
	for gi, g := range sub.Groups {
		report.Groups[gi] = GroupReport{Name: g.Name, Target: g.Count}
	}

	// ---- 1. 收集候选（多分组去重） ----
	type cand struct {
		entry  library.Entry
		groups map[int]Group
	}
	candidates := make(map[string]*cand)
	var order []string
	for gi, g := range sub.Groups {
		capN := opts.candidateCap(g.Count)
		matched := make([]library.Entry, 0, capN)
		for _, e := range lib.All() {
			if g.CandidatePriority(e) == 0 {
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
	_ = prog(Progress{Stage: "gather", Tested: len(order), Log: fmt.Sprintf("收集候选 %d 条", len(order))})

	// 解析未指定端口（TLS 默认 443），并让候选键跟随端口修正，
	// 否则按旧键（端口 0）找不到检测结果会被误判为失效。
	resolvedOrder := make([]string, 0, len(order))
	for _, key := range order {
		c := candidates[key]
		e := c.entry
		if e.Port == 0 {
			e.Port = 443
			c.entry = e
			newKey := e.Key()
			if newKey != key {
				delete(candidates, key)
				candidates[newKey] = c
				lib.RemoveKey(key) // 旧键（端口 0）作废
			}
			key = newKey
		}
		resolvedOrder = append(resolvedOrder, key)
	}
	order = resolvedOrder

	// ---- 2. 批量延迟检测 ----
	targets := make([]engine.Target, 0, len(order))
	for _, key := range order {
		e := candidates[key].entry
		targets = append(targets, engine.Target{IP: e.IP, Port: e.Port})
	}
	latOpts := engine.LatencyOptions{
		MaxConcurrency: 100,
		TimeoutMs:      opts.LatencyTimeoutMs,
		EnableTLS:      true,
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

	fresh := make(map[string]library.Entry, len(order))
	for _, key := range order {
		c := candidates[key]
		res, ok := resultByKey[key]
		if !ok {
			lib.RemoveKey(key) // 延迟失败：失效即移除
			report.RemovedDead++
			for gi := range c.groups {
				report.Groups[gi].Tested++
				report.Groups[gi].Failed++
			}
			continue
		}
		updated, changed := applyResult(c.entry, res, now)
		isNew := lib.Upsert(updated)
		fresh[key] = updated
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
	_ = prog(Progress{Stage: "latency", Tested: len(order), Passed: len(fresh), Failed: report.RemovedDead,
		Log: fmt.Sprintf("延迟检测完成：通过 %d，失败 %d（已从库移除）", len(fresh), report.RemovedDead)})
	if ctx.Err() != nil {
		_ = lib.Save()
		return report, ctx.Err()
	}

	// ---- 3. 测速短名单（仅需要测速的分组） ----
	speedPool := make(map[int][]library.Entry)   // 分组 -> 短名单
	speedQueue := make(map[string]library.Entry) // 跨分组去重
	for gi, g := range sub.Groups {
		if !g.RequiresSpeed(sub) {
			continue
		}
		pool := freshForGroup(fresh, g)
		capN := opts.candidateCap(g.Count)
		if len(pool) > capN {
			pool = pool[:capN]
		}
		speedPool[gi] = pool
		for _, e := range pool {
			speedQueue[e.Key()] = e
		}
	}
	if len(speedQueue) > 0 {
		speedTargets := make([]engine.Target, 0, len(speedQueue))
		for _, e := range speedQueue {
			speedTargets = append(speedTargets, engine.Target{IP: e.IP, Port: e.Port})
		}
		speedMap := make(map[string]float64, len(speedQueue))
		speedOpts := engine.SpeedOptions{
			MaxConcurrency: opts.SpeedConcurrency,
			DurationSec:    opts.SpeedDurationSec,
			MinSpeedKBs:    0, // 不过滤，阈值由分组判断，失败原因才能区分
			EnableTLS:      true,
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
		// 回写测速结果（失败不判死，仅标记无效）
		speedFailed := 0
		for key, e := range speedQueue {
			spd, ok := speedMap[key]
			updated := e
			if ok && spd > 0 {
				updated.SpeedKBs = spd
				updated.SpeedValid = true
			} else {
				updated.SpeedValid = false
			}
			lib.Upsert(updated)
			speedQueue[key] = updated
			if !updated.SpeedValid {
				speedFailed++
			}
		}
		for gi := range speedPool {
			gr := &report.Groups[gi]
			for i, e := range speedPool[gi] {
				u := speedQueue[e.Key()]
				speedPool[gi][i] = u
				gr.SpeedTested++
				if !u.SpeedValid {
					gr.SpeedFailed++
				}
			}
		}
		_ = prog(Progress{Stage: "speed", Tested: len(speedQueue), Failed: speedFailed,
			Log: fmt.Sprintf("测速完成：有效 %d，失败 %d（保留待下次验证）", len(speedQueue)-speedFailed, speedFailed)})
		if ctx.Err() != nil {
			_ = lib.Save()
			return report, ctx.Err()
		}
	}

	// ---- 4. 按分组配额选结果 ----
	var output []library.Entry
	seenOut := make(map[string]bool)
	for gi, g := range sub.Groups {
		gr := &report.Groups[gi]
		if g.RequiresSpeed(sub) {
			filled := 0
			for _, e := range speedPool[gi] {
				if filled >= g.Count {
					break
				}
				if !g.SpeedOK(e.SpeedKBs, e.SpeedValid) {
					continue
				}
				if !seenOut[e.Key()] {
					seenOut[e.Key()] = true
					output = append(output, e)
				}
				filled++
			}
			gr.Filled = filled
			gr.Shortage = g.Count - filled
			continue
		}
		pool := freshForGroup(fresh, g)
		filled := 0
		for _, e := range pool {
			if filled >= g.Count {
				break
			}
			if !seenOut[e.Key()] {
				seenOut[e.Key()] = true
				output = append(output, e)
			}
			filled++
		}
		gr.Filled = filled
		gr.Shortage = g.Count - filled
	}
	for gi := range report.Groups {
		if report.Groups[gi].Shortage > 0 {
			report.Shortages = append(report.Shortages, fmt.Sprintf("分组 %q 缺 %d 条（候选不足，请导入更多该地区 IP）",
				report.Groups[gi].Name, report.Groups[gi].Shortage))
		}
	}

	// ---- 5. 写出订阅文件 ----
	path, err := WriteOutput(lib.Dir(), sub, output)
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

// freshForGroup 从本次延迟通过的条目中，按分组实测约束筛选并排序。
func freshForGroup(fresh map[string]library.Entry, g Group) []library.Entry {
	pool := make([]library.Entry, 0)
	for _, e := range fresh {
		if !g.CountryMatches(e.CountryCode) || !g.LatencyOK(e.TCPLatencyMs) {
			continue
		}
		if len(g.Ports) > 0 && !containsInt(g.Ports, e.Port) {
			continue
		}
		pool = append(pool, e)
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
	changed := false
	mark := func(a, b string) string {
		if a != "" && a != b {
			changed = true
			return a
		}
		return b
	}
	e.CountryCode = mark(res.LocCode, e.CountryCode)
	e.Country = mark(res.Country, e.Country)
	e.CityZh = mark(res.CityZh, e.CityZh)
	e.Emoji = mark(res.Emoji, e.Emoji)
	e.DataCenter = mark(res.DataCenter, e.DataCenter)
	e.RegionZh = mark(res.RegionZh, e.RegionZh)
	if res.ASN != 0 && res.ASN != e.ASN {
		e.ASN = res.ASN
		changed = true
	}
	if res.ASNOrg != "" && res.ASNOrg != e.ASNOrg {
		e.ASNOrg = res.ASNOrg
		changed = true
	}
	if res.TCPLatencyMs > 0 && res.TCPLatencyMs != e.TCPLatencyMs {
		e.TCPLatencyMs = res.TCPLatencyMs
		changed = true
	}
	e.Status = library.StatusActive
	e.LastCheckedAt = now
	e.Checks++
	if e.Source == "" {
		e.Source = library.SourceTopup
	}
	return e, changed
}

// WriteOutput 将订阅结果按格式渲染并原子写入 dataDir 下的输出文件。
func WriteOutput(dataDir string, sub Subscription, entries []library.Entry) (string, error) {
	if err := sub.Validate(); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, filepath.FromSlash(sub.Output.Path))
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}
	var lines []string
	if sub.Output.Format == "csv" {
		lines = RenderCSV(entries)
	} else {
		lines = RenderTXT(sub.Output.Template, entries)
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
