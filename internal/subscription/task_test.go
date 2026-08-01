package subscription

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iptest-web/internal/library"
)

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
	// 输入文件存在时输出默认回写输入文件
	t2 := Task{Name: "x", Input: TaskInput{Mode: "file", File: "out/原.txt"}, Rules: []TaskRule{{Name: "r", Limit: 1}}}
	if err := t2.Validate(); err != nil {
		t.Fatal(err)
	}
	if t2.Output.Path != filepath.FromSlash("out/原.txt") {
		t.Fatalf("输出应回写输入文件: %q", t2.Output.Path)
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
		Name:  "限3条",
		Limit: 3,
		Rules: []TaskRule{{Name: "美国", Limit: 0, Conditions: []Condition{{Field: "country", Values: []string{"US"}}}}},
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
	subs := []Subscription{{
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


