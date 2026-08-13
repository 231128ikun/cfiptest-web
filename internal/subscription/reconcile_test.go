package subscription

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
)

// fakeTester 用脚本数据模拟检测，不联网。
type fakeTester struct {
	mu                    sync.Mutex
	latencyOK             map[string]bool    // key -> 延迟是否通过
	countries             map[string]string  // key -> 国家码
	speeds                map[string]float64 // key -> 速度（缺省或<=0 表示测速失败）
	latencyCalled         []string
	speedCalled           []string
	latencyMaxConcurrency int
	speedMaxConcurrency   int
}

func newFake() *fakeTester {
	return &fakeTester{
		latencyOK: map[string]bool{},
		countries: map[string]string{},
		speeds:    map[string]float64{},
	}
}

func (f *fakeTester) add(ip string, port int, country string, latencyOK bool, speed float64) {
	key := library.Key(ip, port)
	f.latencyOK[key] = latencyOK
	f.countries[key] = country
	if speed > 0 {
		f.speeds[key] = speed
	}
}

func (f *fakeTester) LatencyOne(_ context.Context, target engine.Target, opts engine.LatencyOptions) (engine.Result, bool) {
	key := library.Key(target.IP, target.Port)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latencyMaxConcurrency = opts.MaxConcurrency
	f.latencyCalled = append(f.latencyCalled, key)
	if !f.latencyOK[key] {
		return engine.Result{}, false
	}
	return engine.Result{IP: target.IP, Port: target.Port, TCPLatencyMs: latencyOf(target.IP), CountryCode: f.countries[key], Country: countryName(f.countries[key])}, true
}

func (f *fakeTester) SpeedOne(_ context.Context, target engine.Target, opts engine.SpeedOptions) float64 {
	key := library.Key(target.IP, target.Port)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.speedMaxConcurrency = opts.MaxConcurrency
	f.speedCalled = append(f.speedCalled, key)
	return f.speeds[key]
}

func (f *fakeTester) ResolveSpeedURL(_ context.Context, raw string, enableTLS bool) (string, string) {
	return fakeResolveSpeedURL(raw, enableTLS)
}

// fakeResolveSpeedURL 模拟引擎的测速源解析：auto 解析为 Cloudflare 官方源，其余仅补全协议。
func fakeResolveSpeedURL(raw string, enableTLS bool) (string, string) {
	scheme := "https://"
	if !enableTLS {
		scheme = "http://"
	}
	v := strings.TrimSpace(raw)
	if v == "" || strings.EqualFold(v, "auto") || v == "自动选择" {
		return scheme + engine.CloudflareSpeedURL, "Cloudflare（自动选择）"
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return scheme + v, "自定义"
	}
	return v, "自定义"
}

// countryName 返回国家码的中文名（模拟真实 locations 查表）。
func countryName(code string) string {
	switch code {
	case "US":
		return "美国"
	case "JP":
		return "日本"
	default:
		return code
	}
}

// latencyOf 按 IP 末段生成稳定延迟，方便断言排序。
func latencyOf(ip string) int64 {
	parts := strings.Split(ip, ".")
	last := parts[len(parts)-1]
	var n int64
	for _, ch := range last {
		n = n*10 + int64(ch-'0')
	}
	return n
}

func mkLib(t *testing.T, entries ...library.Entry) *library.Store {
	t.Helper()
	s, err := library.Open(filepath.Join(t.TempDir(), library.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s.Upsert(e)
	}
	return s
}

func usGroup(count int) Group {
	return Group{Name: "美国", CountryCode: "US", Country: "美国", Count: count}
}

type testRun struct {
	Name        string
	InputPath   string
	EnableSpeed bool
	Groups      []Group
	Output      Output
}

func runTest(ctx context.Context, tester Tester, lib *library.Store, spec testRun, opts RunOptions, prog ProgressFunc) (*Report, error) {
	return runCore(ctx, tester, lib, spec.Groups, spec.InputPath, spec.Output, spec.EnableSpeed, 0, opts, prog)
}

func TestRunFillsAndWritesOutput(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		library.Entry{IP: "1.0.0.11", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 150, Checks: 2},
		library.Entry{IP: "1.0.0.12", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 160},
		library.Entry{IP: "1.0.0.13", Port: 443, Status: library.StatusNew}, // 未测，国家未知
	)
	fake.add("1.0.0.11", 443, "US", true, 0)
	fake.add("1.0.0.12", 443, "US", true, 0)
	fake.add("1.0.0.13", 443, "US", true, 0)

	sub := testRun{
		Name:   "测试订阅",
		Groups: []Group{usGroup(2)},
		Output: Output{Path: "out/test.txt", Template: "{ip}:{port}#{country}"},
	}
	// 让输出文件落在临时目录
	libPath := lib.Path()
	dataDir := filepath.Dir(libPath)

	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if report.Groups[0].Filled != 2 || report.Groups[0].Shortage != 0 {
		t.Fatalf("配额未满足: %+v", report.Groups[0])
	}
	if report.TotalLines != 2 {
		t.Fatalf("期望输出 2 行，实际 %d", report.TotalLines)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "out", "test.txt"))
	if err != nil {
		t.Fatalf("输出文件未生成: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "#美国") {
		t.Fatalf("输出内容错误: %q", lines)
	}
	// 流水线并发检测：候选可能在途被多测（数据有效），但不会超配额选中输出。
	if got := len(fake.latencyCalled); got < 2 || got > 3 {
		t.Fatalf("应只检测 2~3 条候选: called=%v report=%+v", fake.latencyCalled, report)
	}
	if report.Tested != len(fake.latencyCalled) || report.Candidates != 3 {
		t.Fatalf("Tested 应与实际检测一致: called=%v report=%+v", fake.latencyCalled, report)
	}
	if report.LibraryUpdated != len(fake.latencyCalled) {
		t.Fatalf("库更新应按唯一实际检测条目计数: called=%v report=%+v", fake.latencyCalled, report)
	}
}

func TestRunRemovesLatencyFailures(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		library.Entry{IP: "1.0.0.21", Port: 443, CountryCode: "US", Status: library.StatusActive},
		library.Entry{IP: "1.0.0.22", Port: 443, CountryCode: "US", Status: library.StatusActive},
		library.Entry{IP: "1.0.0.23", Port: 443, CountryCode: "US", Status: library.StatusActive},
	)
	fake.add("1.0.0.21", 443, "US", true, 0)
	fake.add("1.0.0.22", 443, "US", false, 0) // 延迟失败
	fake.add("1.0.0.23", 443, "US", true, 0)

	sub := testRun{Name: "x", Groups: []Group{usGroup(2)},
		Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{RemoveAfterFailures: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedDead != 1 || report.Groups[0].Failed != 1 {
		t.Fatalf("失效移除统计错误: %+v", report)
	}
	if _, ok := lib.Get("1.0.0.22", 443); ok {
		t.Fatal("延迟失败的条目应从库中移除")
	}
	if report.Groups[0].Shortage != 0 {
		t.Fatalf("不应有缺口: %+v", report.Groups[0])
	}
}

func TestRunUpdatesChangedCountry(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		// 库中记录为美国，实测为日本 → 应更新国家，且不再填美国组
		library.Entry{IP: "1.0.0.31", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 100},
		// 真正的美国 IP
		library.Entry{IP: "1.0.0.32", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 120},
		library.Entry{IP: "1.0.0.33", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 130},
	)
	fake.add("1.0.0.31", 443, "JP", true, 0)
	fake.add("1.0.0.32", 443, "US", true, 0)
	fake.add("1.0.0.33", 443, "US", true, 0)

	sub := testRun{Name: "x", Groups: []Group{
		usGroup(2),
		{Name: "日本", CountryCode: "JP", Count: 1},
	}, Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := lib.Get("1.0.0.31", 443)
	if got.CountryCode != "JP" {
		t.Fatalf("国家应更新为 JP: %+v", got)
	}
	if report.LibraryUpdated == 0 {
		t.Fatalf("应记录唯一条目级库更新: %+v", report)
	}
	if report.Groups[1].Filled != 1 {
		t.Fatalf("日本组应被更新的条目填满: %+v", report.Groups[1])
	}
	if report.TotalLines != 3 {
		t.Fatalf("输出应为 3 行（US2+JP1），实际 %d", report.TotalLines)
	}
}

func TestRunSpeedFailureKeepsEntry(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		library.Entry{IP: "1.0.0.41", Port: 443, CountryCode: "US", Status: library.StatusActive},
		library.Entry{IP: "1.0.0.42", Port: 443, CountryCode: "US", Status: library.StatusActive},
	)
	// 41 测速失败后漏斗继续，42 测速通过后立即达标停止。
	fake.add("1.0.0.41", 443, "US", true, -1)
	fake.add("1.0.0.42", 443, "US", true, 5000)

	sub := testRun{Name: "x", EnableSpeed: true, Groups: []Group{{
		Name: "美国", CountryCode: "US", Count: 1, MinSpeedKBs: 1000, RequireSpeed: true,
	}}, Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 测速失败的条目保留在库中，仅 SpeedValid=false
	got, ok := lib.Get("1.0.0.41", 443)
	if !ok {
		t.Fatal("测速失败的条目不应从库移除")
	}
	if got.SpeedValid {
		t.Fatal("测速失败应标记 SpeedValid=false")
	}
	if report.Groups[0].SpeedFailed != 1 || report.Groups[0].SpeedTested != 2 {
		t.Fatalf("测速统计错误: %+v", report.Groups[0])
	}
	if report.Groups[0].Filled != 1 || report.Groups[0].Shortage != 0 {
		t.Fatalf("应恰好填满 1 条: %+v", report.Groups[0])
	}
	if got.Status != library.StatusActive {
		t.Fatal("延迟通过的状态应保持 active")
	}
}

func TestRunShortageReported(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		library.Entry{IP: "1.0.0.51", Port: 443, CountryCode: "US", Status: library.StatusActive},
	)
	fake.add("1.0.0.51", 443, "US", true, 0)

	sub := testRun{Name: "x", Groups: []Group{usGroup(3)},
		Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Groups[0].Shortage != 2 {
		t.Fatalf("应报告缺口 2: %+v", report.Groups[0])
	}
	if len(report.Shortages) != 1 {
		t.Fatalf("应有缺口提示: %+v", report.Shortages)
	}
	if report.TotalLines != 1 {
		t.Fatalf("输出应为 1 行: %d", report.TotalLines)
	}
}

func TestRunDedupesAcrossGroups(t *testing.T) {
	fake := newFake()
	lib := mkLib(t,
		library.Entry{IP: "1.0.0.61", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 90},
		library.Entry{IP: "1.0.0.62", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 100},
	)
	fake.add("1.0.0.61", 443, "US", true, 0)
	fake.add("1.0.0.62", 443, "US", true, 0)

	// 不限国家分组 + 美国组：同一批 IP 会同时命中两个组，输出应去重
	sub := testRun{Name: "x", Groups: []Group{
		{Name: "全部", Count: 2},
		usGroup(2),
	}, Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalLines != 2 {
		t.Fatalf("跨分组应去重为 2 行，实际 %d", report.TotalLines)
	}
}

func TestRunProgressCalled(t *testing.T) {
	fake := newFake()
	lib := mkLib(t, library.Entry{IP: "1.0.0.71", Port: 443, CountryCode: "US", Status: library.StatusActive})
	fake.add("1.0.0.71", 443, "US", true, 0)

	var stages []string
	var logs []string
	sub := testRun{Name: "x", Groups: []Group{usGroup(1)},
		Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	_, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, func(p Progress) error {
		stages = append(stages, p.Stage)
		logs = append(logs, p.Log)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"开始漏斗检测", "延迟+测速进行中", "累计 1/1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("维护进度缺少 %q：\n%s", want, joined)
		}
	}
	sort.Strings(stages)
	if got := strings.Join(stages, ","); got != "gather,latency,latency,latency,output" {
		t.Fatalf("进度阶段不完整，实际 %s", got)
	}
}

func TestRunSpeedProgressCalled(t *testing.T) {
	fake := newFake()
	lib := mkLib(t, library.Entry{IP: "1.0.0.72", Port: 443, CountryCode: "US", Status: library.StatusActive})
	fake.add("1.0.0.72", 443, "US", true, 2048)
	sub := testRun{Name: "x", EnableSpeed: true, Groups: []Group{{
		Name: "美国", CountryCode: "US", Count: 1, MinSpeedKBs: 1, RequireSpeed: true,
	}}, Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	var logs []string
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, func(p Progress) error {
		logs = append(logs, p.Log)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SpeedTested != 1 || report.SpeedPassed != 1 {
		t.Fatalf("应执行 1 条测速且有效: %+v", report)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"开始漏斗检测：候选 1 条，并发 5", "按需测速结束：测试 1", "测速源："} {
		if !strings.Contains(joined, want) {
			t.Fatalf("维护测速进度缺少 %q：\n%s", want, joined)
		}
	}
}

func TestRunSpeedSourceLabelLogged(t *testing.T) {
	fake := newFake()
	lib := mkLib(t, library.Entry{IP: "1.0.0.73", Port: 443, CountryCode: "US", Status: library.StatusActive})
	fake.add("1.0.0.73", 443, "US", true, 1024)
	sub := testRun{Name: "x", EnableSpeed: true, Groups: []Group{{
		Name: "美国", CountryCode: "US", Count: 1, MinSpeedKBs: 1, RequireSpeed: true,
	}}, Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	var logs []string
	_, err := runTest(context.Background(), fake, lib, sub, RunOptions{DownloadURL: "auto"}, func(p Progress) error {
		logs = append(logs, p.Log)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "测速源：Cloudflare（自动选择）") {
		t.Fatalf("开始日志应标注自动选择的测速源：\n%s", joined)
	}
}
func TestRunResolvesDefaultPort(t *testing.T) {
	fake := newFake()
	lib := mkLib(t, library.Entry{IP: "1.0.0.81", Port: 0, CountryCode: "US", Status: library.StatusNew})
	fake.add("1.0.0.81", 443, "US", true, 0) // 按 TLS 默认 443 测试

	sub := testRun{Name: "x", Groups: []Group{usGroup(1)},
		Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := runTest(context.Background(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := lib.Get("1.0.0.81", 443)
	if got.Port != 443 || report.Groups[0].Filled != 1 {
		t.Fatalf("端口 0 应解析为 443: %+v", report)
	}
}

func TestWriteOutputCSV(t *testing.T) {
	dir := t.TempDir()
	sub := testRun{Name: "x", Groups: []Group{usGroup(1)},
		Output: Output{Path: "out/t.csv", Format: "csv"}}
	path, err := WriteOutput(dir, sub.Output, []library.Entry{{IP: "1.2.3.4", Port: 443, CountryCode: "US", Country: "美国", TCPLatencyMs: 99}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "ip,port,country") || !strings.Contains(string(body), "1.2.3.4,443,美国,US") {
		t.Fatalf("CSV 输出错误: %s", body)
	}
}

func TestRunCancelSavesPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	fake := newFake()
	lib := mkLib(t, library.Entry{IP: "1.0.0.91", Port: 443, CountryCode: "US", Status: library.StatusNew})
	sub := testRun{Name: "x", Groups: []Group{usGroup(1)},
		Output: Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	_, err := runTest(ctx, fake, lib, sub, RunOptions{}, nil)
	if err == nil {
		t.Fatal("取消时应返回错误")
	}
	_ = fmt.Sprint(err)
}

// TestRunTaskConcurrencyOptions 验证并发配置透传到检测引擎（默认 50/5，显式覆盖生效）。
func TestRunTaskConcurrencyOptions(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	lib.Upsert(library.Entry{IP: "1.0.0.1", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 50})
	fake.add("1.0.0.1", 443, "US", true, 100)

	task := Task{
		Name:         "x",
		Enabled:      true,
		SpeedEnabled: true,
		Rules: []TaskRule{
			{Name: "美国", Limit: 1, SpeedMin: 1, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}},
		},
		Output: TaskOutput{Path: "out/t.txt", Template: "{ip}:{port}"},
	}

	// 默认：延迟 50、测速 5
	if _, err := RunTask(context.Background(), fake, lib, task, RunOptions{}, nil); err != nil {
		t.Fatalf("RunTask 失败: %v", err)
	}
	if fake.latencyMaxConcurrency != 50 || fake.speedMaxConcurrency != 5 {
		t.Fatalf("默认并发应 50/5，实际 %d/%d", fake.latencyMaxConcurrency, fake.speedMaxConcurrency)
	}

	// 显式覆盖
	fake2 := newFake()
	fake2.add("1.0.0.1", 443, "US", true, 100)
	if _, err := RunTask(context.Background(), fake2, lib, task, RunOptions{LatencyConcurrency: 12, SpeedConcurrency: 3}, nil); err != nil {
		t.Fatalf("RunTask 失败: %v", err)
	}
	if fake2.latencyMaxConcurrency != 12 || fake2.speedMaxConcurrency != 3 {
		t.Fatalf("覆盖并发应 12/3，实际 %d/%d", fake2.latencyMaxConcurrency, fake2.speedMaxConcurrency)
	}
}

// TestImportInputTargetsKeepsExistingMetadata 验证重复导入初始化来源时，
// 已有条目保留全部检测元数据，只有新条目以 StatusNew 入库。
func TestSortOutputCountryAsc(t *testing.T) {
	entries := []library.Entry{
		{IP: "1.1.1.3", Port: 443, CountryCode: "US", Country: "美国"},
		{IP: "1.1.1.1", Port: 443, CountryCode: "JP", Country: "日本"},
		{IP: "1.1.1.2", Port: 443, Country: "巴西"},
		{IP: "1.1.1.4", Port: 443, CountryCode: "CN", Country: "中国"},
	}
	sortOutput(entries, OutputSortCountryAsc)
	want := []string{"1.1.1.4", "1.1.1.1", "1.1.1.3", "1.1.1.2"}
	for i, e := range entries {
		if e.IP != want[i] {
			t.Fatalf("第 %d 条=%s，期望 %s", i, e.IP, want[i])
		}
	}
}

// TestSortOutputGrouped 验证输出按规则分组顺序排列（先写的国家排前面），
// 组内按配置排序键（默认延迟升序）排序。
func TestSortOutputGrouped(t *testing.T) {
	entries := []library.Entry{
		{IP: "9.9.9.9", Port: 443, CountryCode: "US", TCPLatencyMs: 300},
		{IP: "1.1.1.1", Port: 443, CountryCode: "JP", TCPLatencyMs: 50},
		{IP: "2.2.2.2", Port: 443, CountryCode: "JP", TCPLatencyMs: 10},
		{IP: "8.8.8.8", Port: 443, CountryCode: "HK", TCPLatencyMs: 200},
	}
	groupOf := map[string]int{
		library.Key("8.8.8.8", 443): 0, // 规则顺序：香港
		library.Key("2.2.2.2", 443): 1, // 日本
		library.Key("1.1.1.1", 443): 1,
		library.Key("9.9.9.9", 443): 2, // 美国
	}
	sortOutputGrouped(entries, OutputSortLatencyAsc, groupOf)
	want := []string{"8.8.8.8", "2.2.2.2", "1.1.1.1", "9.9.9.9"}
	for i, e := range entries {
		if e.IP != want[i] {
			t.Fatalf("第 %d 条=%s，期望 %s（按 香港→日本→美国 分组、组内延迟升序）", i, e.IP, want[i])
		}
	}
}

func TestImportInputTargetsKeepsExistingMetadata(t *testing.T) {
	lib := mkLib(t, library.Entry{
		IP: "1.0.0.1", Port: 443, Source: library.SourceOfficial,
		CountryCode: "US", Country: "美国", TCPLatencyMs: 120,
		Status: library.StatusActive, Checks: 5, SpeedValid: true, DownloadSpeedKBs: 1000,
	})
	added, updated := importInputTargets(lib, []engine.Target{
		{IP: "1.0.0.1", Port: 443, CountryCode: "SG"}, // 已存在且已有国家：保留原元数据，不覆盖为 SG
		{IP: "1.0.0.2", Port: 443, CountryCode: "JP"}, // 新条目：国家标记应入库
	}, time.Now(), library.SourceOfficial)
	if added != 1 || updated != 1 {
		t.Fatalf("期望新增 1 / 更新 1，实际 %d/%d", added, updated)
	}
	got, ok := lib.Get("1.0.0.1", 443)
	if !ok || got.Status != library.StatusActive || got.CountryCode != "US" ||
		got.TCPLatencyMs != 120 || got.Checks != 5 || !got.SpeedValid || got.DownloadSpeedKBs != 1000 {
		t.Fatalf("已有条目元数据被重置: %+v", got)
	}
	entry, ok := lib.Get("1.0.0.2", 443)
	if !ok || entry.Status != library.StatusNew || entry.Source != library.SourceOfficial ||
		entry.CountryCode != "JP" || entry.FirstSeenAt.IsZero() {
		t.Fatalf("新条目应以 StatusNew 并携带国家标记入库: %+v", entry)
	}
}

// TestRunTaskInitSourceFeedsLibraryOnly 验证任务带初始化来源时：
// 来源目标先导入库，候选只从库收集；库中已有条目重复导入不重置元数据。
func TestRunTaskInitSourceFeedsLibraryOnly(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, err := library.Open(filepath.Join(dir, library.FileName))
	if err != nil {
		t.Fatal(err)
	}
	lib.Upsert(library.Entry{IP: "1.0.0.1", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 100, Checks: 3})
	fake.add("1.0.0.1", 443, "US", true, 0)
	fake.add("1.0.0.2", 443, "US", true, 0)

	task := Task{
		Name: "初始化+维护",
		Rules: []TaskRule{
			{Name: "美国", Limit: 2, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}},
		},
		Output: TaskOutput{Path: "out/t.txt", Template: "{ip}:{port}"},
	}
	report, err := RunTask(context.Background(), fake, lib, task, RunOptions{
		InputTargets: []engine.Target{
			{IP: "1.0.0.1", Port: 443}, // 已存在：不应重置
			{IP: "1.0.0.2", Port: 443}, // 新：先入库再检测
		},
		InputSource: library.SourceOfficial,
	}, nil)
	if err != nil {
		t.Fatalf("RunTask 失败: %v", err)
	}
	if report.InputAdded != 1 || report.InputUpdated != 1 {
		t.Fatalf("初始化导入统计错误: added=%d updated=%d", report.InputAdded, report.InputUpdated)
	}
	got, _ := lib.Get("1.0.0.1", 443)
	if got.Status != library.StatusActive || got.Checks != 4 || got.CountryCode != "US" {
		t.Fatalf("已有条目被重置: %+v", got)
	}
	entry, ok := lib.Get("1.0.0.2", 443)
	if !ok || entry.Status != library.StatusActive || entry.CountryCode != "US" {
		t.Fatalf("新来源条目应入库并检测回写: %+v", entry)
	}
	if report.TotalLines != 2 || report.Groups[0].Filled != 2 {
		t.Fatalf("应输出 2 条: %+v", report)
	}
}

func TestRunTaskRemoteLibraryDoesNotWriteBack(t *testing.T) {
	fake := newFake()
	original := library.Entry{
		IP: "1.0.0.90", Port: 0, CountryCode: "", Status: library.StatusNew,
		TCPLatencyMs: 999, ConsecutiveFailures: 2,
	}
	lib := library.NewInMemory(t.TempDir(), []library.Entry{original})
	fake.add(original.IP, 443, "SG", true, 0)
	task := Task{
		Name:   "远程只读",
		Rules:  []TaskRule{{Name: "新加坡", Limit: 1, Conditions: []Condition{{Field: "country", Values: []string{"SG"}}}}},
		Output: TaskOutput{Path: "out/remote.txt", Template: "{ip}:{port}"},
	}
	report, err := RunTask(context.Background(), fake, lib, task, RunOptions{RemoteLibrary: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalLines != 1 {
		t.Fatalf("应输出检测结果，实际 %d 条", report.TotalLines)
	}
	got, ok := lib.Get(original.IP, original.Port)
	_, createdResolvedKey := lib.Get(original.IP, 443)
	if !ok || createdResolvedKey || got.CountryCode != original.CountryCode || got.TCPLatencyMs != original.TCPLatencyMs || got.ConsecutiveFailures != original.ConsecutiveFailures {
		t.Fatalf("远程候选库不应写回检测结果：before=%+v after=%+v", original, got)
	}
}

func TestWriteOutputAbsolutePath(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "exported", "sub", "t.txt")
	path, err := WriteOutput(dataDir, Output{Path: target, Format: "txt", Template: "{ip}:{port}"}, []library.Entry{{IP: "1.2.3.4", Port: 443}})
	if err != nil {
		t.Fatalf("绝对路径输出失败: %v", err)
	}
	if path != target {
		t.Fatalf("返回路径=%q，期望 %q", path, target)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取绝对输出失败: %v", err)
	}
	if string(body) != "1.2.3.4:443\n" {
		t.Fatalf("绝对输出内容错误: %q", body)
	}
}
