package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"iptest-web/internal/library"
)

// TasksFile 是维护任务定义文件名。
const TasksFile = "tasks.json"

// Condition 是规则里的一个条件字段（多值 = 或）。
type Condition struct {
	Field  string   `json:"field"`            // country | city | port
	Values []string `json:"values,omitempty"` // 空 = 不限
}

// TaskRule 是任务内的一条规则卡片。
// 多条件 = 且（笛卡尔积组合）；每个组合取前 Limit 条。
type TaskRule struct {
	Name       string      `json:"name,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Limit      int         `json:"limit"` // 每个组合取前 N；0=不限
	LatencyMin int64       `json:"latencyMin,omitempty"`
	LatencyMax int64       `json:"latencyMax,omitempty"` // 0=不限
	SpeedMin   float64     `json:"speedMin,omitempty"`
	SpeedMax   float64     `json:"speedMax,omitempty"`
}

// TaskInput 描述任务的数据来源。
type TaskInput struct {
	Mode string `json:"mode"` // file | remote（remote 预留）
	File string `json:"file,omitempty"`
	URL  string `json:"url,omitempty"`
}

// TaskOutput 描述任务的输出。
type TaskOutput struct {
	Path     string `json:"path,omitempty"`     // 相对 data 目录；空 = 回写输入文件
	Format   string `json:"format,omitempty"`   // txt | csv
	Template string `json:"template,omitempty"` // 占位符模板
}

// Task 是维护任务（替代旧「订阅器」概念）。
type Task struct {
	ID           string     `json:"id,omitempty"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`      // 单独维护开关；false = 一键维护跳过
	LibraryID    string     `json:"libraryId"`    // 绑定的 IP 库 ID；空 = 默认库
	Input        TaskInput  `json:"input"`
	Output       TaskOutput `json:"output"`
	Limit        int        `json:"limit"`        // 总数限制（合并去重后取前 N）；0=不限
	SpeedEnabled bool       `json:"speedEnabled"` // 顶部测速总开关；关 = 规则速度字段无效
	Rules        []TaskRule `json:"rules"`
}

// Validate 校验并规范化任务。
func (t *Task) Validate() error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("任务名称不能为空")
	}
	if len(t.Rules) == 0 {
		return fmt.Errorf("任务 %q 至少需要一条规则", t.Name)
	}
	if t.LibraryID == "" {
		t.LibraryID = library.DefaultID
	}
	if t.Input.Mode == "" {
		t.Input.Mode = "file"
	}
	seenNames := map[string]bool{}
	for i := range t.Rules {
		r := &t.Rules[i]
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			r.Name = fmt.Sprintf("规则%d", i+1)
		}
		if seenNames[r.Name] {
			return fmt.Errorf("任务 %q 规则名重复: %s", t.Name, r.Name)
		}
		seenNames[r.Name] = true
		if r.Limit < 0 {
			return fmt.Errorf("任务 %q 规则 %s 的数量不能为负", t.Name, r.Name)
		}
		if r.LatencyMin < 0 || r.LatencyMax < 0 || (r.LatencyMin > 0 && r.LatencyMax > 0 && r.LatencyMin > r.LatencyMax) {
			return fmt.Errorf("任务 %q 规则 %s 延迟范围非法", t.Name, r.Name)
		}
		if r.SpeedMin < 0 || r.SpeedMax < 0 || (r.SpeedMin > 0 && r.SpeedMax > 0 && r.SpeedMin > r.SpeedMax) {
			return fmt.Errorf("任务 %q 规则 %s 速度范围非法", t.Name, r.Name)
		}
		for j := range r.Conditions {
			c := &r.Conditions[j]
			c.Field = strings.ToLower(strings.TrimSpace(c.Field))
			switch c.Field {
			case "country", "city", "port", "dataCenter", "asn", "region":
			default:
				return fmt.Errorf("任务 %q 规则 %s 含未知条件字段 %q", t.Name, r.Name, c.Field)
			}
			cleaned := make([]string, 0, len(c.Values))
			for _, v := range c.Values {
				if v = strings.TrimSpace(v); v != "" {
					cleaned = append(cleaned, v)
				}
			}
			c.Values = cleaned
			if len(c.Values) == 0 {
				c.Values = nil
			}
		}
	}
	if t.Limit < 0 {
		return fmt.Errorf("任务 %q 总数限制不能为负", t.Name)
	}
	if t.Output.Path == "" {
		if strings.TrimSpace(t.Input.File) != "" {
			t.Output.Path = filepath.Clean(strings.TrimSpace(t.Input.File))
		} else {
			t.Output.Path = filepath.Join("out", t.Name+".txt")
		}
	}
	if t.Output.Format == "" {
		t.Output.Format = "txt"
	}
	t.Output.Format = strings.ToLower(t.Output.Format)
	if t.Output.Format != "txt" && t.Output.Format != "csv" {
		return fmt.Errorf("任务 %q 输出格式仅支持 txt/csv", t.Name)
	}
	if t.Output.Template == "" {
		t.Output.Template = DefaultTemplate
	}
	return nil
}

// LoadTasks 读取 data/tasks.json；不存在时尝试迁移旧 subscriptions.json。
func LoadTasks(dataDir string) ([]Task, error) {
	body, err := os.ReadFile(filepath.Join(dataDir, TasksFile))
	if err != nil {
		if os.IsNotExist(err) {
			return migrateSubscriptions(dataDir)
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", TasksFile, err)
	}
	var tasks []Task
	if err := json.Unmarshal(body, &tasks); err != nil {
		return nil, fmt.Errorf("%s 格式错误: %w", TasksFile, err)
	}
	for i := range tasks {
		if err := tasks[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s 第 %d 项无效: %w", TasksFile, i+1, err)
		}
	}
	return tasks, nil
}

// SaveTasks 原子写回 data/tasks.json。
func SaveTasks(dataDir string, tasks []Task) error {
	for i := range tasks {
		if err := tasks[i].Validate(); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dataDir, TasksFile), append(body, '\n'))
}

// migrateSubscriptions 把旧版 subscriptions.json 转成任务（仅当 tasks.json 不存在）。
func migrateSubscriptions(dataDir string) ([]Task, error) {
	body, err := os.ReadFile(filepath.Join(dataDir, SubscriptionsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", SubscriptionsFile, err)
	}
	var subs []Subscription
	if err := json.Unmarshal(body, &subs); err != nil {
		return nil, fmt.Errorf("%s 格式错误: %w", SubscriptionsFile, err)
	}
	tasks := make([]Task, 0, len(subs))
	for i, sub := range subs {
		if err := sub.Validate(); err != nil {
			return nil, fmt.Errorf("%s 第 %d 项无效: %w", SubscriptionsFile, i+1, err)
		}
		task := Task{
			ID:           fmt.Sprintf("t-%d", i+1),
			Name:         sub.Name,
			Enabled:      true,
			LibraryID:    library.DefaultID,
			Input:        TaskInput{Mode: "file", File: sub.InputPath},
			Output:       TaskOutput{Path: sub.Output.Path, Format: sub.Output.Format, Template: sub.Output.Template},
			SpeedEnabled: sub.EnableSpeed,
		}
		for gi, g := range sub.Groups {
			rule := TaskRule{
				Name:       g.Name,
				Limit:      g.Count,
				LatencyMax: g.MaxLatencyMs,
				SpeedMin:   g.MinSpeedKBs,
			}
			if g.CountryCode != "" {
				rule.Conditions = append(rule.Conditions, Condition{Field: "country", Values: []string{g.CountryCode}})
			}
			if len(g.Ports) > 0 {
				vs := make([]string, 0, len(g.Ports))
				for _, p := range g.Ports {
					vs = append(vs, fmt.Sprintf("%d", p))
				}
				rule.Conditions = append(rule.Conditions, Condition{Field: "port", Values: vs})
			}
			_ = gi
			task.Rules = append(task.Rules, rule)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}
