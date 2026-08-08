package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iptest-web/internal/library"
)

func TestValidateCron(t *testing.T) {
	valid := []string{"0 3 * * *", "*/15 * * * *", "0 */6 * * *", "5 4 * * 1", "0 0 1 * *", "10-20 * * * *", "0,30 * * * *", "0 0 1 1 *", "*/5 9-18 * * 1-5"}
	for _, expr := range valid {
		if err := ValidateCron(expr); err != nil {
			t.Fatalf("表达式 %q 应合法: %v", expr, err)
		}
	}
	invalid := []string{"", "* * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "a * * * *", "5-1 * * * *"}
	for _, expr := range invalid {
		if err := ValidateCron(expr); err == nil {
			t.Fatalf("表达式 %q 应非法", expr)
		}
	}
}

func TestCronMatches(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 8, 2, h, m, 0, 0, time.Local) }
	if !CronMatches("0 3 * * *", at(3, 0)) {
		t.Fatal("每天 03:00 应命中")
	}
	if CronMatches("0 3 * * *", at(3, 1)) {
		t.Fatal("非指定分钟不应命中")
	}
	if !CronMatches("*/15 * * * *", at(10, 30)) {
		t.Fatal("每 15 分钟应命中")
	}
	if CronMatches("*/15 * * * *", at(10, 31)) {
		t.Fatal("非 15 分钟倍数不应命中")
	}
	if !CronMatches("0 */6 * * *", at(6, 0)) {
		t.Fatal("每 6 小时应命中")
	}
	day := time.Date(2026, 8, 2, 3, 0, 0, 0, time.Local)
	sunday0 := fmt.Sprintf("0 3 * * %d", int(day.Weekday()))
	if !CronMatches(sunday0, day) {
		t.Fatalf("星期 %d 应命中 %q", int(day.Weekday()), sunday0)
	}
	if day.Weekday() == time.Sunday && !CronMatches("0 3 * * 7", day) {
		t.Fatal("周日 7 别名应命中")
	}
	if !CronMatches("0 3 * * 0", day) && day.Weekday() != time.Sunday {
		t.Fatalf("非周日不应命中 0")
	}
}

func TestTaskValidateSchedule(t *testing.T) {
	task := Task{Name: "定时任务", Rules: []TaskRule{{Name: "r", Limit: 1}}, Schedule: TaskSchedule{Enabled: true, Cron: "0 3 * * *"}}
	if err := task.Validate(); err != nil {
		t.Fatalf("合法定时应通过: %v", err)
	}
	task.Schedule.Cron = "bad cron"
	if err := task.Validate(); err == nil {
		t.Fatal("非法 Cron 应校验失败")
	}
}

func TestTaskValidateDefaults(t *testing.T) {
	task := Task{Name: "日本专线", Rules: []TaskRule{{Name: "r1", Limit: 5, Conditions: []Condition{{Field: "country", Values: []string{"jp"}}}}}}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	if task.LibraryID != library.DefaultID {
		t.Fatalf("默认库应为 default: %q", task.LibraryID)
	}
	if task.Output.Path != filepath.FromSlash("out/日本专线.txt") || task.Output.Format != "txt" || task.Output.Template != DefaultTemplate {
		t.Fatalf("输出默认值错误: %+v", task.Output)
	}
}

func TestTaskValidateErrors(t *testing.T) {
	cases := []Task{
		{Name: "", Rules: []TaskRule{{Name: "r", Limit: 1}}},
		{Name: "x", Rules: nil},
		{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1, Conditions: []Condition{{Field: "bogus", Values: []string{"a"}}}}}},
		{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1, LatencyMin: 500, LatencyMax: 100}}},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d 应校验失败", i)
		}
	}
}

func TestExpandTaskRules(t *testing.T) {
	task := Task{
		Name: "x",
		Rules: []TaskRule{
			{Name: "美日", Limit: 5, Conditions: []Condition{
				{Field: "country", Values: []string{"US", "JP"}},
				{Field: "port", Values: []string{"443"}},
			}},
			{Name: "东京", Limit: 3, Conditions: []Condition{
				{Field: "city", Values: []string{"东京"}},
			}},
			{Name: "重复", Limit: 5, Conditions: []Condition{
				{Field: "country", Values: []string{"US"}},
				{Field: "port", Values: []string{"443"}},
			}},
		},
	}
	groups, err := expandTaskRules(task)
	if err != nil {
		t.Fatal(err)
	}
	// US+443、JP+443、东京 = 3 个分组；重复项被去重
	if len(groups) != 3 {
		t.Fatalf("应展开 3 个分组（重复去重），实际 %d: %+v", len(groups), groups)
	}
	byName := map[string]Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	us := byName["美日-1"]
	if us.CountryCode != "US" || len(us.Ports) != 1 || us.Ports[0] != 443 || us.Count != 5 {
		t.Fatalf("US 分组错误: %+v", us)
	}
	jp := byName["美日-2"]
	if jp.CountryCode != "JP" || jp.Count != 5 {
		t.Fatalf("JP 分组错误: %+v", jp)
	}
	tokyo := byName["东京"]
	if len(tokyo.Cities) != 1 || tokyo.Cities[0] != "东京" || tokyo.Count != 3 {
		t.Fatalf("东京分组错误: %+v", tokyo)
	}
	// 不限条件：空条件规则 -> 一个全匹配分组
	task2 := Task{Name: "y", Rules: []TaskRule{{Name: "全部", Limit: 0}}}
	groups2, _ := expandTaskRules(task2)
	if len(groups2) != 1 || groups2[0].Count != 0 || groups2[0].CountryCode != "" {
		t.Fatalf("空条件规则展开错误: %+v", groups2)
	}
}

func TestRunTaskWithRules(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	// 库：美国 2 条、日本 1 条、未知 1 条
	lib.Upsert(library.Entry{IP: "1.0.0.11", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 100})
	lib.Upsert(library.Entry{IP: "1.0.0.12", Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: 120})
	lib.Upsert(library.Entry{IP: "1.0.0.13", Port: 443, CountryCode: "JP", Status: library.StatusActive, TCPLatencyMs: 90})
	lib.Upsert(library.Entry{IP: "1.0.0.14", Port: 443, Status: library.StatusNew})
	fake.add("1.0.0.11", 443, "US", true, 0)
	fake.add("1.0.0.12", 443, "US", true, 0)
	fake.add("1.0.0.13", 443, "JP", true, 0)
	fake.add("1.0.0.14", 443, "SG", true, 0)

	task := Task{
		Name:    "综合",
		Enabled: true,
		Rules: []TaskRule{
			{Name: "美国", Limit: 1, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}},
			{Name: "日本", Limit: 1, Conditions: []Condition{{Field: "country", Values: []string{"JP"}}}},
		},
		Output: TaskOutput{Path: "out/t.txt", Template: "{ip}:{port}#{country}"},
	}
	report, err := RunTask(context.Background(), fake, lib, task, RunOptions{}, nil)
	if err != nil {
		t.Fatalf("RunTask 失败: %v", err)
	}
	if report.TotalLines != 2 || report.Groups[0].Filled != 1 || report.Groups[1].Filled != 1 {
		t.Fatalf("填充错误: %+v", report)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "out", "t.txt"))
	if !strings.Contains(string(body), "#美国") || !strings.Contains(string(body), "#日本") {
		t.Fatalf("输出错误: %s", body)
	}
	if report.TaskID != "" {
		t.Fatal("未设 ID 时 TaskID 应为空")
	}
}

func TestRunTaskTotalLimit(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	for i := 0; i < 5; i++ {
		ip := "1.0.0." + strings.Repeat("1", 1) // placeholder
		_ = ip
	}
	// 5 条美国 IP，延迟递增
	ips := []string{"1.0.0.11", "1.0.0.12", "1.0.0.13", "1.0.0.14", "1.0.0.15"}
	for i, ip := range ips {
		lib.Upsert(library.Entry{IP: ip, Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: int64(100 + i*10)})
		fake.add(ip, 443, "US", true, 0)
	}
	task := Task{
		Name:   "限3条",
		Limit:  3,
		Rules:  []TaskRule{{Name: "美国", Limit: 0, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}}},
		Output: TaskOutput{Path: "out/t.txt", Template: "{ip}:{port}"},
	}
	report, err := RunTask(context.Background(), fake, lib, task, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalLines != 3 {
		t.Fatalf("总数限制应截断为 3 条，实际 %d", report.TotalLines)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "out", "t.txt"))
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 || lines[0] != "1.0.0.11:443" {
		t.Fatalf("应按延迟升序取前 3: %v", lines)
	}
}

func TestRunTaskChecksAllCandidatesUntilQuota(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	ips := []string{"1.0.0.11", "1.0.0.12", "1.0.0.13", "1.0.0.14", "1.0.0.15"}
	for i, ip := range ips {
		lib.Upsert(library.Entry{IP: ip, Port: 443, CountryCode: "US", Status: library.StatusActive, TCPLatencyMs: int64(100 + i*10)})
		fake.add(ip, 443, "US", i >= 3, 0)
	}
	task := Task{Name: "检测全部候选", Limit: 2, Rules: []TaskRule{{Name: "美国", Limit: 2, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}}}, Output: TaskOutput{Path: "out/t.txt", Template: "{ip}:{port}"}}
	report, err := RunTask(context.Background(), fake, lib, task, RunOptions{MaxPerGroup: 1, RemoveAfterFailures: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalLines != 2 {
		t.Fatalf("应凑够 2 条，实际 %d", report.TotalLines)
	}
	if len(fake.latencyCalled) != 5 {
		t.Fatalf("应持续检测至凑够结果或候选耗尽，实际 %d: %v", len(fake.latencyCalled), fake.latencyCalled)
	}
}

func TestRunTaskSpeedGate(t *testing.T) {
	dir := t.TempDir()
	fake := newFake()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	lib.Upsert(library.Entry{IP: "1.0.0.21", Port: 443, CountryCode: "US", Status: library.StatusActive})
	lib.Upsert(library.Entry{IP: "1.0.0.22", Port: 443, CountryCode: "US", Status: library.StatusActive})
	fake.add("1.0.0.21", 443, "US", true, 5000)
	fake.add("1.0.0.22", 443, "US", true, 300) // 低于下限

	// 测速关闭：速度字段被忽略，两条都入订阅
	taskOff := Task{Name: "off", Rules: []TaskRule{{Name: "美国", Limit: 2, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}, SpeedMin: 1000}}}
	r1, err := RunTask(context.Background(), fake, lib, taskOff, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.TotalLines != 2 {
		t.Fatalf("测速关闭时应忽略速度范围: %d", r1.TotalLines)
	}
	// 测速开启：速度下限 1000 生效，仅 21 入订阅
	taskOn := Task{Name: "on", SpeedEnabled: true, Rules: []TaskRule{{Name: "美国", Limit: 2, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}, SpeedMin: 1000}}}
	r2, err := RunTask(context.Background(), fake, lib, taskOn, RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r2.TotalLines != 1 || r2.Groups[0].SpeedTested != 2 || r2.Groups[0].SpeedFailed != 0 {
		t.Fatalf("测速开启时速度范围应生效（低速条被阈值过滤而非判死）: %+v", r2.Groups[0])
	}
	if r2.Groups[0].Filled != 1 || r2.Groups[0].Shortage != 1 {
		t.Fatalf("低速条目应被阈值过滤: %+v", r2.Groups[0])
	}
}

func TestLoadTasksMigratesSubscriptions(t *testing.T) {
	dir := t.TempDir()
	subs := []legacySubscription{{
		Name: "旧订阅", EnableSpeed: true,
		Groups: []Group{{Name: "美国", CountryCode: "US", Count: 10, MaxLatencyMs: 300, MinSpeedKBs: 1000, RequireSpeed: true}},
		Output: Output{Path: "out/sub.txt", Template: "{ip}:{port}#{country}"},
	}}
	body, _ := json.Marshal(subs)
	if err := os.WriteFile(filepath.Join(dir, SubscriptionsFile), body, 0644); err != nil {
		t.Fatal(err)
	}
	tasks, err := LoadTasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Name != "旧订阅" || !tasks[0].SpeedEnabled {
		t.Fatalf("迁移失败: %+v", tasks)
	}
	if len(tasks[0].Rules) != 1 || len(tasks[0].Rules[0].Conditions) != 1 || tasks[0].Rules[0].Conditions[0].Field != "country" {
		t.Fatalf("分组未转规则: %+v", tasks[0].Rules)
	}
	if tasks[0].Rules[0].SpeedMin != 1000 || tasks[0].Rules[0].Limit != 10 {
		t.Fatalf("规则字段转换错误: %+v", tasks[0].Rules[0])
	}
	// 保存后再读，不应再触发迁移
	if err := SaveTasks(dir, tasks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, TasksFile)); err != nil {
		t.Fatal("tasks.json 未写出")
	}
	tasks2, err := LoadTasks(dir)
	if err != nil || len(tasks2) != 1 {
		t.Fatalf("重读失败: %v %+v", err, tasks2)
	}
}

func TestLegacyMigrationKeepsInputAndOutputSeparate(t *testing.T) {
	dir := t.TempDir()
	subscriptions := []legacySubscription{{
		Name:      "旧订阅",
		InputPath: "out/source.txt",
		Groups:    []Group{{Name: "新加坡", CountryCode: "sg", Count: 1}},
	}}
	body, err := json.Marshal(subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SubscriptionsFile), body, 0644); err != nil {
		t.Fatal(err)
	}
	tasks, err := LoadTasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("迁移任务数量错误: %+v", tasks)
	}
	if tasks[0].Input.File != filepath.FromSlash("out/source.txt") {
		t.Fatalf("初始化文件未保留: %+v", tasks[0].Input)
	}
	if tasks[0].Output.Path != filepath.FromSlash("out/旧订阅.txt") {
		t.Fatalf("未设置输出时应使用独立默认路径，不得覆盖初始化文件: %q", tasks[0].Output.Path)
	}
	if tasks[0].Rules[0].Conditions[0].Values[0] != "SG" {
		t.Fatalf("国家码未规范化: %+v", tasks[0].Rules[0].Conditions)
	}
}
func TestTaskConcurrencyJSON(t *testing.T) {
	task := Task{Name: "x", LatencyConcurrency: 20, SpeedConcurrency: 6, Rules: []TaskRule{{Name: "r", Limit: 1}}}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var back Task
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.LatencyConcurrency != 20 || back.SpeedConcurrency != 6 {
		t.Fatalf("并发配置未往返保留: %+v", back)
	}
	// 0 应被 omitempty 省略
	zero, _ := json.Marshal(Task{Name: "y", Rules: []TaskRule{{Name: "r", Limit: 1}}})
	if strings.Contains(string(zero), "latencyConcurrency") || strings.Contains(string(zero), "speedConcurrency") {
		t.Fatalf("0 并发不应序列化: %s", zero)
	}
}

func TestTaskValidateConcurrency(t *testing.T) {
	base := Task{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1}}}
	negLat := base
	negLat.LatencyConcurrency = -1
	if err := negLat.Validate(); err == nil {
		t.Fatal("负延迟并发应校验失败")
	}
	negSpd := base
	negSpd.SpeedConcurrency = -1
	if err := negSpd.Validate(); err == nil {
		t.Fatal("负测速并发应校验失败")
	}
	ok := base
	ok.LatencyConcurrency = 30
	ok.SpeedConcurrency = 4
	if err := ok.Validate(); err != nil {
		t.Fatalf("正并发应通过: %v", err)
	}
}

func TestTaskValidateInputSources(t *testing.T) {
	base := Task{Name: "source", Rules: []TaskRule{{Name: "r", Limit: 1}}}
	cases := []struct {
		name  string
		input TaskInput
		valid bool
	}{
		{"none valid", TaskInput{Mode: "none"}, true},
		{"none rejects file", TaskInput{Mode: "none", File: "a.txt"}, false},
		{"none rejects url", TaskInput{Mode: "none", URL: "https://example.com/x"}, false},
		{"file without path downgrades to none", TaskInput{Mode: "file"}, true},
		{"file path traversal", TaskInput{Mode: "file", File: "../secret.txt"}, false},
		{"file server absolute", TaskInput{Mode: "file", File: filepath.Join(string(filepath.Separator), "tmp", "seed.txt")}, true},
		{"file absolute with .. cleans", TaskInput{Mode: "file", File: filepath.Join(string(filepath.Separator), "tmp", "..", "seed.txt")}, true},
		{"file valid", TaskInput{Mode: "file", File: "init/seed.txt"}, true},
		{"legacy empty-file downgrades to none", TaskInput{Mode: "file", File: ""}, true},
		{"remote requires url", TaskInput{Mode: "remote"}, false},
		{"remote valid", TaskInput{Mode: "remote", URL: "https://example.com/list.csv"}, true},
		{"legacy official migrates to official library", TaskInput{Mode: "official", Family: "ipv6", SampleMode: "n", SampleN: 2, Protocol: "http", Port: 8080}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := base
			task.Input = tc.input
			err := task.Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate() error=%v, valid=%v", err, tc.valid)
			}
			if err == nil && tc.input.Mode == "official" {
				if task.LibrarySource != LibrarySourceOfficial || task.LibraryFamily != "ipv6" || task.LibrarySampleN != 2 || task.LibraryProtocol != "http" || task.LibraryPort != 8080 {
					t.Fatalf("旧官方初始化应迁移到维护来源: %+v", task)
				}
			}
			if err == nil && tc.input.Mode == "file" && tc.input.File == "" {
				if task.Input.Mode != "none" {
					t.Fatalf("空路径 file 应降级为 none，实际 mode=%q", task.Input.Mode)
				}
			}
		})
	}
}

func TestTaskValidateLibrarySources(t *testing.T) {
	base := Task{Name: "lib", Rules: []TaskRule{{Name: "r", Limit: 1}}}
	cases := []struct {
		name   string
		mutate func(*Task)
		valid  bool
		check  func(*Task) bool
	}{
		{"default local", func(t *Task) {}, true, func(t *Task) bool { return t.LibrarySource == LibrarySourceLocal && t.LibraryID == library.DefaultID }},
		{"local valid", func(t *Task) { t.LibrarySource = LibrarySourceLocal; t.LibraryID = "l2" }, true, func(t *Task) bool { return t.LibrarySource == LibrarySourceLocal && t.LibraryID == "l2" }},
		{"official defaults", func(t *Task) { t.LibrarySource = LibrarySourceOfficial }, true, func(t *Task) bool {
			return t.LibraryFamily == "ipv4" && t.LibrarySampleMode == "one" && t.LibraryProtocol == "https"
		}},
		{"official invalid family", func(t *Task) { t.LibrarySource = LibrarySourceOfficial; t.LibraryFamily = "ipv5" }, false, nil},
		{"official invalid sample mode", func(t *Task) { t.LibrarySource = LibrarySourceOfficial; t.LibrarySampleMode = "two" }, false, nil},
		{"official n requires sampleN", func(t *Task) { t.LibrarySource = LibrarySourceOfficial; t.LibrarySampleMode = "n" }, false, nil},
		{"official n valid", func(t *Task) {
			t.LibrarySource = LibrarySourceOfficial
			t.LibrarySampleMode = "n"
			t.LibrarySampleN = 8
		}, true, nil},
		{"official invalid port", func(t *Task) { t.LibrarySource = LibrarySourceOfficial; t.LibraryPort = 70000 }, false, nil},
		{"official custom port", func(t *Task) {
			t.LibrarySource = LibrarySourceOfficial
			t.LibraryProtocol = "http"
			t.LibraryPort = 8080
		}, true, func(t *Task) bool { return t.LibraryPort == 8080 }},
		{"remote requires url", func(t *Task) { t.LibrarySource = LibrarySourceRemote }, false, nil},
		{"remote valid", func(t *Task) { t.LibrarySource = LibrarySourceRemote; t.LibraryURL = "https://example.com/ips.txt" }, true, nil},
		{"bad source", func(t *Task) { t.LibrarySource = "other" }, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := base
			tc.mutate(&task)
			err := task.Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate() error=%v, valid=%v", err, tc.valid)
			}
			if err == nil && tc.check != nil && !tc.check(&task) {
				t.Fatalf("规范化结果不符: %+v", task)
			}
		})
	}
}

func TestTaskValidateOutputPaths(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		format string
		want   string
	}{
		{name: "文件名", path: "abc", format: "txt", want: "out/abc.txt"},
		{name: "切换扩展名", path: "abc.txt", format: "csv", want: "out/abc.csv"},
		{name: "相对目录", path: "custom/abc", format: "csv", want: "custom/abc.csv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1}}, Output: TaskOutput{Path: tc.path, Format: tc.format}}
			if err := task.Validate(); err != nil {
				t.Fatal(err)
			}
			if task.Output.Path != filepath.FromSlash(tc.want) {
				t.Fatalf("输出路径=%q，期望 %q", task.Output.Path, filepath.FromSlash(tc.want))
			}
		})
	}
	for _, invalid := range []string{"../escape", "C:/escape", "/escape"} {
		task := Task{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1}}, Output: TaskOutput{Path: invalid}}
		if err := task.Validate(); err == nil {
			t.Fatalf("越界路径 %q 应被拒绝", invalid)
		}
	}

	// 国家/地区排序可被识别并规范化为常量。
	sortTask := Task{Name: "x", Rules: []TaskRule{{Name: "r", Limit: 1}}, Output: TaskOutput{Sort: "countryAsc"}}
	if err := sortTask.Validate(); err != nil {
		t.Fatalf("countryAsc 应通过校验: %v", err)
	}
	if sortTask.Output.Sort != OutputSortCountryAsc {
		t.Fatalf("排序=%q，期望 %q", sortTask.Output.Sort, OutputSortCountryAsc)
	}
}
