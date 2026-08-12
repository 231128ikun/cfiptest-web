package subscription

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
)

// simTester mimics a remote list with unknown geo until tested.
type simTester struct {
	countries     map[string]string
	pass          map[string]bool
	latencyCalled []string
}

func (f *simTester) RunLatencyTest(_ context.Context, targets []engine.Target, opts engine.LatencyOptions, cb engine.EventCallback) ([]engine.Result, error) {
	var out []engine.Result
	for _, t := range targets {
		key := library.Key(t.IP, t.Port)
		f.latencyCalled = append(f.latencyCalled, key)
		if !f.pass[key] {
			continue
		}
		out = append(out, engine.Result{IP: t.IP, Port: t.Port, TCPLatencyMs: 100, CountryCode: f.countries[key], Country: f.countries[key]})
		cb(engine.Event{Type: engine.EventResult, Result: &out[len(out)-1]})
	}
	return out, nil
}

func (f *simTester) RunSpeedTest(_ context.Context, targets []engine.Target, opts engine.SpeedOptions, cb engine.EventCallback) error {
	return nil
}

// buildRemoteSim 构造 200 条“未知地区”候选（模拟远程 URL 库，库内无国家元数据）：
// cluster=false 时五国交错（i%5），true 时按国家聚类（每 40 条一个地区）；
// 每条 ~60% 概率延迟通过，通过后回填对应国家码。漏斗按缺口取候选、达标即停，
// 因此随机通过率下检测量不确定，只断言“填满后提前停止”，不断言固定条数。
func buildRemoteSim(t *testing.T, cluster bool) (*simTester, *library.Store) {
	sim := &simTester{countries: map[string]string{}, pass: map[string]bool{}}
	rng := rand.New(rand.NewSource(1))
	codes := []string{"HK", "JP", "KR", "SG", "US"}
	entries := make([]library.Entry, 0, 200)
	for i := 0; i < 200; i++ {
		ip := fmt.Sprintf("1.0.0.%d", i+1)
		entries = append(entries, library.Entry{IP: ip, Port: 0, Status: library.StatusNew})
		key := library.Key(ip, 443)
		ci := i % 5
		if cluster {
			ci = i / 40
		}
		sim.countries[key] = codes[ci]
		sim.pass[key] = rng.Intn(10) < 6
	}
	lib := library.NewInMemory(t.TempDir(), entries)
	return sim, lib
}

// runRemoteSim 以远程库模式（不回写、不删除）执行单轮编排，返回报告。
func runRemoteSim(t *testing.T, sim *simTester, lib *library.Store, count int) *Report {
	t.Helper()
	codes := []string{"HK", "JP", "KR", "SG", "US"}
	var groups []Group
	for _, c := range codes {
		groups = append(groups, Group{Name: c, CountryCode: c, Country: c, Count: count})
	}
	prog := func(p Progress) error {
		if p.Log != "" {
			t.Log(p.Log)
		}
		return nil
	}
	report, err := runCore(context.Background(), sim, lib, groups, "", Output{Path: "out/sim.txt", Template: "{ip}:{port}"}, false, 72, RunOptions{RemoteLibrary: true}, prog)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestSimulateRemoteRounds(t *testing.T) {
	// 漏斗语义：未知国家候选按分段交错取队列，按当前缺口组成小批次检测；
	// 某个规则填满后立即停止为它取候选，全部填满即提前结束，不检测完整候选池。
	checkFilled := func(t *testing.T, report *Report, wantPer int, wantTotal int) {
		t.Helper()
		if report.TotalLines != wantTotal {
			t.Fatalf("应共输出 %d 条，实际 %d", wantTotal, report.TotalLines)
		}
		for _, gr := range report.Groups {
			if gr.Filled != wantPer || gr.Shortage != 0 {
				t.Fatalf("group %s 应填满 %d 条且无缺口，实际 %+v", gr.Name, wantPer, gr)
			}
		}
	}

	t.Run("interleaved_fills_then_stops", func(t *testing.T) {
		// 五国交错 + 配额 10：分批检测直至五国全部填满，随后立即停止；
		// 检测量应远小于 200（不能把整个候选池都测完再挑结果）。
		sim, lib := buildRemoteSim(t, false)
		report := runRemoteSim(t, sim, lib, 10)
		checkFilled(t, report, 10, 50)
		if n := len(sim.latencyCalled); n >= 200 || n < 50 {
			t.Fatalf("漏斗应在填满后提前停止（检测 %d/%d），而不是全量检测", n, report.Candidates)
		}
	})

	t.Run("clustered_reaches_tail_then_stops", func(t *testing.T) {
		// 按国家聚类（前 40 条全是 HK，最后 40 条是 US）+ 配额 5：
		// 分段交错必须让 US/SG 等尾部聚类也被检测到并填满，且全部填满后提前停止。
		sim, lib := buildRemoteSim(t, true)
		report := runRemoteSim(t, sim, lib, 5)
		checkFilled(t, report, 5, 25)
		if n := len(sim.latencyCalled); n >= 200 || n < 25 {
			t.Fatalf("聚类场景应覆盖尾部国家并提前停止（检测 %d/%d）", n, report.Candidates)
		}
	})

	t.Run("interleaved_small_quota_stops_early", func(t *testing.T) {
		// 五国交错 + 配额 5：填满 25 条后停止，检测量应明显小于 200。
		sim, lib := buildRemoteSim(t, false)
		report := runRemoteSim(t, sim, lib, 5)
		checkFilled(t, report, 5, 25)
		if n := len(sim.latencyCalled); n >= 200 || n < 25 {
			t.Fatalf("小配额应提前停止（检测 %d/%d），而不是按固定预算检测", n, report.Candidates)
		}
	})
}

// TestSimulateRemoteCountryPrefilter 验证“先加载远程库 → 按国家预筛 → 再检测”：
// 远程列表自带国家标记（模拟 all.txt 的 IP:端口#国家），库条目直接带 CountryCode；
// 只检测目标国家的候选（非目标国家一条都不测），并按分组配额填满。
func TestSimulateRemoteCountryPrefilter(t *testing.T) {
	target := []string{"HK", "JP", "KR", "SG", "US"}
	sim := &simTester{countries: map[string]string{}, pass: map[string]bool{}}
	entries := make([]library.Entry, 0, 300)
	// 200 条目标国家（每国 40）+ 100 条非目标国家（NL/DE/FR/IN/BR 各 20）。
	for i := 0; i < 200; i++ {
		ip := fmt.Sprintf("2.0.0.%d", i+1)
		c := target[i%5]
		entries = append(entries, library.Entry{IP: ip, Port: 0, CountryCode: c, Status: library.StatusNew})
		key := library.Key(ip, 443)
		sim.countries[key] = c
		sim.pass[key] = true
	}
	others := []string{"NL", "DE", "FR", "IN", "BR"}
	for i := 0; i < 100; i++ {
		ip := fmt.Sprintf("2.0.1.%d", i+1)
		c := others[i%5]
		entries = append(entries, library.Entry{IP: ip, Port: 0, CountryCode: c, Status: library.StatusNew})
		key := library.Key(ip, 443)
		sim.countries[key] = c
		sim.pass[key] = true
	}
	lib := library.NewInMemory(t.TempDir(), entries)

	var groups []Group
	for _, c := range target {
		groups = append(groups, Group{Name: c, CountryCode: c, Country: c, Count: 10})
	}
	report, err := runCore(context.Background(), sim, lib, groups, "",
		Output{Path: "out/sim.txt", Template: "{ip}:{port}"}, false, 72,
		RunOptions{RemoteLibrary: true}, func(p Progress) error {
			if p.Log != "" {
				t.Log(p.Log)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// 漏斗：每国 10 条、全部候选已知且通过，首批缺口 50 条检测后立即全部达标停止；
	// 非目标国家候选一条都不检测。
	if len(sim.latencyCalled) != 50 {
		t.Fatalf("已知候选全部通过时应只检测缺口 50 条（5 国 × 10），实际 %d", len(sim.latencyCalled))
	}
	for _, key := range sim.latencyCalled {
		c := sim.countries[key]
		if !foldContains(target, c) {
			t.Fatalf("预筛失效：非目标国家 %s 的候选被检测: %s", c, key)
		}
	}
	if report.TotalLines != 50 {
		t.Fatalf("应每国 10 条共 50 条，实际 %d", report.TotalLines)
	}
	for _, gr := range report.Groups {
		if gr.Filled != 10 || gr.Shortage != 0 {
			t.Fatalf("group %s 应填满 10 条，实际 %+v", gr.Name, gr)
		}
	}

}

func foldContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// TestSimulateUserScenarioFullChain 复现用户实际配置的完整链路：
// tasks.json 里 country 值是一整串 "香港；日本；韩国；新加坡；美国"（分号分隔），
// 远程库行格式为 "IP:端口#国家"（all.txt 格式）。
// 链路：Validate 拆分并归一化 → expandTaskRules 得到 5 个分组 →
// 远程文本 ParseTargets 带国家码 → 候选预筛只测目标国 → 每国 10 条共 50 条输出。
func TestSimulateUserScenarioFullChain(t *testing.T) {
	var sb strings.Builder
	target := []string{"HK", "JP", "KR", "SG", "US"}
	others := []string{"NL", "DE"}
	n := 0
	for _, c := range target {
		for i := 0; i < 12; i++ {
			n++
			fmt.Fprintf(&sb, "10.%d.%d.%d:443#%s\n", n/25000, (n/250)%250, n%250+1, c)
		}
	}
	for _, c := range others {
		for i := 0; i < 10; i++ {
			n++
			fmt.Fprintf(&sb, "10.%d.%d.%d:443#%s\n", n/25000, (n/250)%250, n%250+1, c)
		}
	}
	targets := engine.ParseTargets(sb.String())
	if len(targets) != 12*5+10*2 {
		t.Fatalf("解析远程库条目数错误: %d", len(targets))
	}
	entries := make([]library.Entry, 0, len(targets))
	for _, tt := range targets {
		entries = append(entries, library.Entry{IP: tt.IP, Port: tt.Port, CountryCode: tt.CountryCode, Status: library.StatusNew})
	}
	lib := library.NewInMemory(t.TempDir(), entries)

	sim := &simTester{countries: map[string]string{}, pass: map[string]bool{}}
	for _, tt := range targets {
		key := library.Key(tt.IP, tt.Port)
		sim.countries[key] = tt.CountryCode
		sim.pass[key] = true
	}

	// 用户实际保存的任务：country 值是一整串中文，分号分隔
	task := Task{
		Name:          "test",
		Enabled:       true,
		LibrarySource: LibrarySourceRemote,
		LibraryURL:    "https://zip.cm.edu.kg/all.txt",
		Output:        TaskOutput{Path: "out/test.txt", Template: "{ip}:{port}#{country}"},
		Rules: []TaskRule{{Name: "\u89c4\u5219 1", Limit: 10, Conditions: []Condition{
			{Field: "country", Values: []string{"\u9999\u6e2f\uff1b\u65e5\u672c\uff1b\u97e9\u56fd\uff1b\u65b0\u52a0\u5761\uff1b\u7f8e\u56fd"}},
		}}},
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	groups, err := expandTaskRules(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 5 {
		t.Fatalf("应展开 5 个分组, 得到 %d", len(groups))
	}
	report, err := runCore(context.Background(), sim, lib, groups, "",
		Output{Path: "out/sim.txt", Template: "{ip}:{port}#{country}"}, false, 72,
		RunOptions{RemoteLibrary: true}, func(p Progress) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// 漏斗：目标国每国 12 条候选且全部通过，缺口 50 条首批检测后全部达标，第 51 条起不检测；
	// 非目标国候选（NL/DE）完全不被检测。
	if len(sim.latencyCalled) != 50 {
		t.Fatalf("应只按缺口检测 50 条目标国候选并提前停止, 实际 %d", len(sim.latencyCalled))
	}
	if report.TotalLines != 50 {
		t.Fatalf("应每国 10 条共 50 条, 实际 %d", report.TotalLines)
	}
	for _, gr := range report.Groups {
		if gr.Filled != 10 || gr.Shortage != 0 {
			t.Fatalf("group %s 应填满 10 条, 实际 %+v", gr.Name, gr)
		}
	}

	// 输出应按规则国家顺序分组：香港→日本→韩国→新加坡→美国，组内延迟升序。
	body, err := os.ReadFile(report.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	wantSeq := []string{"HK", "JP", "KR", "SG", "US"}
	prev := 0
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		idx := strings.LastIndex(line, "#")
		if idx < 0 {
			t.Fatalf("输出行缺少国家标记: %q", line)
		}
		count++
		cc := line[idx+1:]
		gi := slices.Index(wantSeq, cc)
		if gi < 0 {
			t.Fatalf("输出包含非目标国家 %q: %q", cc, line)
		}
		if gi < prev {
			t.Fatalf("输出未按规则顺序分组：%s 出现在 %s 之后", wantSeq[gi], wantSeq[prev])
		}
		prev = gi
	}
	if count != 50 {
		t.Fatalf("输出行数应为 50, 实际 %d", count)
	}
}
