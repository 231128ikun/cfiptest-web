package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// TaskInput 描述任务的初始化来源（可选）：official/file/remote 在每次维护运行前导入并清洗进 IP 库；
type TaskInput struct {
	Mode       string `json:"mode"` // file | remote | official | none；none = 仅维护对象库
	File       string `json:"file,omitempty"`
	URL        string `json:"url,omitempty"`
	Family     string `json:"family,omitempty"`
	SampleMode string `json:"sampleMode,omitempty"`
	SampleN    int    `json:"sampleN,omitempty"`
	Port       int    `json:"port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

// TaskOutput 描述任务的输出。
type TaskOutput struct {
	Path     string `json:"path,omitempty"`     // 相对 data 目录；空 = 回写输入文件
	Format   string `json:"format,omitempty"`   // txt | csv
	Template string `json:"template,omitempty"` // 占位符模板
}

// TaskSchedule 描述程序运行期间的自动维护计划。Cron 使用标准 5 段格式：分 时 日 月 周。
type TaskSchedule struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron,omitempty"`
}

// Task 是维护任务（替代旧「订阅器」概念）。
type Task struct {
	ID                 string       `json:"id,omitempty"`
	Name               string       `json:"name"`
	Enabled            bool         `json:"enabled"`   // 单独维护开关；false = 一键维护跳过
	LibraryID          string       `json:"libraryId"` // 绑定的 IP 库 ID；空 = 默认库
	Input              TaskInput    `json:"input"`
	Output             TaskOutput   `json:"output"`
	Limit              int          `json:"limit"`                        // 总数限制（合并去重后取前 N）；0=不限
	SpeedEnabled       bool         `json:"speedEnabled"`                 // 顶部测速总开关；关 = 规则速度字段无效
	LatencyConcurrency int          `json:"latencyConcurrency,omitempty"` // 延迟并发；0 = 用设置页全局默认
	SpeedConcurrency   int          `json:"speedConcurrency,omitempty"`   // 测速并发；0 = 用设置页全局默认
	LatencyTimeoutMs   int          `json:"latencyTimeoutMs,omitempty"`   // 延迟超时；0 = 用设置页全局默认
	LatencyProbes      int          `json:"latencyProbes,omitempty"`      // TCP 探测次数；0 = 用设置页全局默认
	LatencyHTTPProbes  int          `json:"latencyHTTPProbes,omitempty"`  // HTTP(trace) 校验次数；0 = 用设置页全局默认
	SpeedDurationSec   int          `json:"speedDurationSec,omitempty"`   // 测速时长；0 = 用设置页全局默认
	Schedule           TaskSchedule `json:"schedule,omitempty"`
	Rules              []TaskRule   `json:"rules"`
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
	t.Input.Mode = strings.ToLower(strings.TrimSpace(t.Input.Mode))
	if t.Input.Mode == "" {
		t.Input.Mode = "file"
	}
	t.Input.File = strings.TrimSpace(t.Input.File)
	t.Input.URL = strings.TrimSpace(t.Input.URL)
	t.Input.Protocol = strings.ToLower(strings.TrimSpace(t.Input.Protocol))
	if t.Input.Protocol == "" {
		t.Input.Protocol = "https"
	}
	if t.Input.Protocol != "http" && t.Input.Protocol != "https" {
		return fmt.Errorf("任务 %q 检测协议仅支持 http/https", t.Name)
	}
	if t.Input.Port < 0 || t.Input.Port > 65535 {
		return fmt.Errorf("任务 %q 端口必须在 1-65535 之间，0 表示按协议默认", t.Name)
	}
	switch t.Input.Mode {
	case "none":
		if t.Input.File != "" || t.Input.URL != "" {
			return fmt.Errorf("任务 %q 初始化来源为「无」时不能同时填写文件或 URL", t.Name)
		}
	case "file":
		if t.Input.File != "" {
			cleaned := filepath.Clean(t.Input.File)
			if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return fmt.Errorf("任务 %q 输入文件必须位于 data 目录内", t.Name)
			}
			t.Input.File = cleaned
		}
	case "remote":
		if t.Input.URL == "" {
			return fmt.Errorf("任务 %q 远程来源必须填写 URL", t.Name)
		}
	case "official":
		t.Input.Family = strings.ToLower(strings.TrimSpace(t.Input.Family))
		if t.Input.Family == "" {
			t.Input.Family = "ipv4"
		}
		if t.Input.Family != "ipv4" && t.Input.Family != "ipv6" {
			return fmt.Errorf("任务 %q 官方来源地址类型仅支持 ipv4/ipv6", t.Name)
		}
		t.Input.SampleMode = strings.ToLower(strings.TrimSpace(t.Input.SampleMode))
		if t.Input.SampleMode == "" {
			t.Input.SampleMode = "one"
		}
		if t.Input.SampleMode != "one" && t.Input.SampleMode != "n" && t.Input.SampleMode != "all" {
			return fmt.Errorf("任务 %q 官方来源抽样方式仅支持 one/n/all", t.Name)
		}
		if t.Input.SampleMode == "n" && (t.Input.SampleN < 1 || t.Input.SampleN > 256) {
			return fmt.Errorf("任务 %q 每段抽样数量必须在 1-256 之间", t.Name)
		}
		if t.Input.SampleMode != "n" && t.Input.SampleN < 0 {
			return fmt.Errorf("任务 %q 抽样数量不能为负", t.Name)
		}
	default:
		return fmt.Errorf("任务 %q 初始化来源仅支持 file/remote/official/none", t.Name)
	}

	if t.LatencyConcurrency < 0 || t.LatencyConcurrency > 1000 {
		return fmt.Errorf("任务 %q 延迟并发必须在 1-1000 之间，0 表示全局默认", t.Name)
	}
	if t.SpeedConcurrency < 0 || t.SpeedConcurrency > 100 {
		return fmt.Errorf("任务 %q 测速并发必须在 1-100 之间，0 表示全局默认", t.Name)
	}
	if t.LatencyTimeoutMs < 0 || t.LatencyTimeoutMs > 60000 {
		return fmt.Errorf("任务 %q 延迟超时必须在 1-60000ms 之间，0 表示全局默认", t.Name)
	}
	if t.LatencyProbes < 0 || t.LatencyProbes > 10 {
		return fmt.Errorf("任务 %q TCP 探测次数必须在 1-10 之间，0 表示全局默认", t.Name)
	}
	if t.LatencyHTTPProbes < 0 || t.LatencyHTTPProbes > 10 {
		return fmt.Errorf("任务 %q HTTP 校验次数必须在 1-10 之间，0 表示全局默认", t.Name)
	}
	if t.SpeedDurationSec < 0 || t.SpeedDurationSec > 120 {
		return fmt.Errorf("任务 %q 测速时长必须在 1-120 秒之间，0 表示全局默认", t.Name)
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
	t.Schedule.Cron = strings.TrimSpace(t.Schedule.Cron)
	if t.Schedule.Enabled {
		if t.Schedule.Cron == "" {
			t.Schedule.Cron = "0 3 * * *"
		}
		if err := ValidateCron(t.Schedule.Cron); err != nil {
			return fmt.Errorf("任务 %q 定时表达式无效: %w", t.Name, err)
		}
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

var cronNumberPattern = regexp.MustCompile(`^\d+$`)

// ValidateCron 校验标准 5 段 Cron 表达式。支持 *、列表、范围和步长。
func ValidateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("需要 5 段：分 时 日 月 周")
	}
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if err := validateCronField(field, limits[i][0], limits[i][1]); err != nil {
			return fmt.Errorf("第 %d 段错误: %w", i+1, err)
		}
	}
	return nil
}

func validateCronField(field string, min, max int) error {
	for _, part := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		if hasStep {
			step, err := strconv.Atoi(stepText)
			if err != nil || step < 1 || strings.Contains(stepText, "/") {
				return fmt.Errorf("步长必须是正整数")
			}
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			pair := strings.Split(base, "-")
			if len(pair) != 2 {
				return fmt.Errorf("范围格式错误")
			}
			start, err1 := cronValue(pair[0], min, max)
			end, err2 := cronValue(pair[1], min, max)
			if err1 != nil || err2 != nil || start > end {
				return fmt.Errorf("范围超出 %d-%d 或起止颠倒", min, max)
			}
			continue
		}
		if hasStep {
			return fmt.Errorf("步长只能用于 * 或范围")
		}
		if _, err := cronValue(base, min, max); err != nil {
			return err
		}
	}
	return nil
}

func cronValue(raw string, min, max int) (int, error) {
	if !cronNumberPattern.MatchString(raw) {
		return 0, fmt.Errorf("只支持数字和 Cron 符号")
	}
	value, _ := strconv.Atoi(raw)
	if value < min || value > max {
		return 0, fmt.Errorf("数值 %d 超出 %d-%d", value, min, max)
	}
	return value, nil
}

// CronMatches 判断本地时间是否命中表达式。
func CronMatches(expr string, now time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	values := []int{now.Minute(), now.Hour(), now.Day(), int(now.Month()), int(now.Weekday())}
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i := range fields {
		if !cronFieldMatches(fields[i], values[i], limits[i][0], limits[i][1], i == 4) {
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, value, min, max int, sundayAlias bool) bool {
	for _, part := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			step, _ = strconv.Atoi(stepText)
		}
		start, end := min, max
		if base != "*" {
			if strings.Contains(base, "-") {
				pair := strings.SplitN(base, "-", 2)
				start, _ = strconv.Atoi(pair[0])
				end, _ = strconv.Atoi(pair[1])
			} else {
				start, _ = strconv.Atoi(base)
				end = start
			}
		}
		check := value
		if sundayAlias && check == 0 && start == 7 && end == 7 {
			check = 7
		}
		if check >= start && check <= end && (check-start)%step == 0 {
			return true
		}
	}
	return false
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
