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

	"iptest-web/internal/engine"
	"iptest-web/internal/fsutil"
	"iptest-web/internal/library"
)

// TasksFile 是维护任务定义文件名。
const TasksFile = "tasks.json"

// 维护来源（Task.LibrarySource）。
const (
	LibrarySourceLocal    = "local"    // 本地 IP 库：维护时检测失效的条目从库中删除
	LibrarySourceOfficial = "official" // 官方 IP 段：优先本地缓存/内置兜底，每次维护按抽样展开为内存候选，失效不删除
	LibrarySourceRemote   = "remote"   // 远程 URL 库：远程库，运行时拉取，失效不删除
)

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

// TaskInput 描述任务的「初始化」（可选）：维护开始前把文件/远程 URL 解析出的基础 IP 导入本地 IP 库。
// family/sampleMode/sampleN/port/protocol 是旧版官方「初始化来源」字段，加载时迁移到
// Task.Library*（维护来源）并从本结构中清除，仅保留用于读取旧 tasks.json。
type TaskInput struct {
	Mode       string `json:"mode"` // none | file | remote；none = 不初始化
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
	Path     string `json:"path,omitempty"`     // 相对 data 目录；空 = out/<任务名>.<格式>
	Format   string `json:"format,omitempty"`   // txt | csv
	Template string `json:"template,omitempty"` // 占位符模板
	Sort     string `json:"sort,omitempty"`     // 输出排序：latencyAsc（默认）| latencyDesc | speedDesc | speedAsc | ipAsc
	Cloud    string `json:"cloud,omitempty"`    // 云端同步配置 ID（设置页「云端存储配置」）；空 = 不同步
	CloudKey string `json:"cloudKey,omitempty"` // 云端路径（如 iptest/final.txt）；空 = 使用输出文件名
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
	Enabled            bool         `json:"enabled"`                     // 单独维护开关；false = 一键维护跳过
	LibraryID          string       `json:"libraryId"`                   // 本地库 ID（librarySource=local 时使用）；空 = 默认库
	LibrarySource      string       `json:"librarySource,omitempty"`     // 维护来源：local | official | remote；空 = local（旧任务）
	LibraryURL         string       `json:"libraryUrl,omitempty"`        // librarySource=remote 时的远程库 URL
	LibraryFamily      string       `json:"libraryFamily,omitempty"`     // librarySource=official：ipv4 | ipv6
	LibrarySampleMode  string       `json:"librarySampleMode,omitempty"` // librarySource=official：one | n | all
	LibrarySampleN     int          `json:"librarySampleN,omitempty"`    // librarySource=official：每段抽样数
	LibraryProtocol    string       `json:"libraryProtocol,omitempty"`   // librarySource=official：http | https
	LibraryPort        int          `json:"libraryPort,omitempty"`       // librarySource=official：0 = 按协议默认
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
	// ---- 维护来源：local（本地库）/ official（官方 IP 段）/ remote（远程 URL 库）----
	t.Input.Mode = strings.ToLower(strings.TrimSpace(t.Input.Mode))
	if t.Input.Mode == "" {
		t.Input.Mode = "none"
	}
	// 旧任务 mode=file 但未填路径：视为不初始化（旧版允许空文件路径）。
	if t.Input.Mode == "file" && strings.TrimSpace(t.Input.File) == "" {
		t.Input.Mode = "none"
	}
	legacyOfficial := t.Input.Mode == "official"
	if legacyOfficial {
		t.Input.Mode = "none"
	}
	t.LibrarySource = strings.ToLower(strings.TrimSpace(t.LibrarySource))
	if t.LibrarySource == "" {
		// 旧任务迁移：官方「初始化来源」→ 官方维护来源；其余默认本地库。
		if legacyOfficial {
			t.LibrarySource = LibrarySourceOfficial
		} else {
			t.LibrarySource = LibrarySourceLocal
		}
	}
	switch t.LibrarySource {
	case LibrarySourceLocal:
		if t.LibraryID == "" {
			t.LibraryID = library.DefaultID
		}
	case LibrarySourceOfficial:
		t.LibraryFamily = strings.ToLower(strings.TrimSpace(t.LibraryFamily))
		t.LibrarySampleMode = strings.ToLower(strings.TrimSpace(t.LibrarySampleMode))
		t.LibraryProtocol = strings.ToLower(strings.TrimSpace(t.LibraryProtocol))
		// 旧版官方初始化参数迁移到维护来源。
		if t.LibraryFamily == "" {
			t.LibraryFamily = strings.ToLower(strings.TrimSpace(t.Input.Family))
		}
		if t.LibrarySampleMode == "" {
			t.LibrarySampleMode = strings.ToLower(strings.TrimSpace(t.Input.SampleMode))
		}
		if t.LibrarySampleN == 0 {
			t.LibrarySampleN = t.Input.SampleN
		}
		if t.LibraryProtocol == "" {
			t.LibraryProtocol = strings.ToLower(strings.TrimSpace(t.Input.Protocol))
		}
		if t.LibraryPort == 0 {
			t.LibraryPort = t.Input.Port
		}
		if t.LibraryFamily == "" {
			t.LibraryFamily = "ipv4"
		}
		if t.LibraryFamily != "ipv4" && t.LibraryFamily != "ipv6" {
			return fmt.Errorf("任务 %q 官方 IP 段地址类型仅支持 ipv4/ipv6", t.Name)
		}
		if t.LibrarySampleMode == "" {
			t.LibrarySampleMode = "one"
		}
		if t.LibrarySampleMode != "one" && t.LibrarySampleMode != "n" && t.LibrarySampleMode != "all" {
			return fmt.Errorf("任务 %q 官方 IP 段抽样方式仅支持 one/n/all", t.Name)
		}
		if t.LibrarySampleMode == "n" && (t.LibrarySampleN < 1 || t.LibrarySampleN > 256) {
			return fmt.Errorf("任务 %q 官方 IP 段每段抽样数量必须在 1-256 之间", t.Name)
		}
		if t.LibrarySampleMode != "n" && t.LibrarySampleN < 0 {
			return fmt.Errorf("任务 %q 官方 IP 段抽样数量不能为负", t.Name)
		}
		if t.LibraryProtocol == "" {
			t.LibraryProtocol = "https"
		}
		if t.LibraryProtocol != "http" && t.LibraryProtocol != "https" {
			return fmt.Errorf("任务 %q 官方 IP 段协议仅支持 http/https", t.Name)
		}
		if t.LibraryPort < 0 || t.LibraryPort > 65535 {
			return fmt.Errorf("任务 %q 官方 IP 段端口必须在 1-65535 之间，0 表示按协议默认", t.Name)
		}
	case LibrarySourceRemote:
		t.LibraryURL = strings.TrimSpace(t.LibraryURL)
		if t.LibraryURL == "" {
			return fmt.Errorf("任务 %q 远程维护库必须填写 URL", t.Name)
		}
	default:
		return fmt.Errorf("任务 %q 维护来源仅支持 local/official/remote", t.Name)
	}

	// ---- 初始化（可选）：none | file | remote ----
	t.Input.File = strings.TrimSpace(t.Input.File)
	t.Input.URL = strings.TrimSpace(t.Input.URL)
	switch t.Input.Mode {
	case "none":
		if t.Input.File != "" || t.Input.URL != "" {
			return fmt.Errorf("任务 %q 初始化来源为「无」时不能同时填写文件或 URL", t.Name)
		}
	case "file":
		if t.Input.File == "" {
			return fmt.Errorf("任务 %q 初始化文件必须填写路径", t.Name)
		}
		cleaned := filepath.Clean(t.Input.File)
		if filepath.IsAbs(cleaned) {
			// 服务器绝对路径：本工具仅监听 127.0.0.1，允许直接读取运行主机上的文本文件。
			t.Input.File = cleaned
			break
		}
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("任务 %q 输入文件必须位于 data 目录内或使用服务器绝对路径", t.Name)
		}
		t.Input.File = cleaned
	case "remote":
		if t.Input.URL == "" {
			return fmt.Errorf("任务 %q 初始化远程来源必须填写 URL", t.Name)
		}
	default:
		return fmt.Errorf("任务 %q 初始化来源仅支持 file/remote/none", t.Name)
	}
	// 旧版官方字段已迁移到 Library*，从初始化结构中清除，避免新任务误用。
	t.Input.Family = ""
	t.Input.SampleMode = ""
	t.Input.SampleN = 0
	t.Input.Port = 0
	t.Input.Protocol = ""

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
			switch strings.ToLower(strings.TrimSpace(c.Field)) {
			case "country", "city", "port", "asn", "region":
				c.Field = strings.ToLower(strings.TrimSpace(c.Field))
			case "datacenter":
				c.Field = "dataCenter"
			default:
				return fmt.Errorf("任务 %q 规则 %s 含未知条件字段 %q", t.Name, r.Name, c.Field)
			}
			if c.Field == "country" {
				c.Values = normalizeCountryValues(c.Values)
				if len(c.Values) == 0 {
					c.Values = nil
				}
				continue
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
	t.Output.Format = strings.ToLower(strings.TrimSpace(t.Output.Format))
	if t.Output.Format == "" {
		t.Output.Format = "txt"
	}
	if t.Output.Format != "txt" && t.Output.Format != "csv" {
		return fmt.Errorf("任务 %q 输出格式仅支持 txt/csv", t.Name)
	}
	ext := "." + t.Output.Format
	p := strings.TrimSpace(t.Output.Path)
	if p == "" {
		p = filepath.Join("out", t.Name+ext)
	} else {
		// 只填文件名时固定输出到 data/out；填写相对目录时输出到 data/<目录>；
		// 服务器绝对路径直接按运行主机路径处理（本工具仅监听 127.0.0.1）。
		if !filepath.IsAbs(p) && !strings.Contains(p, "/") && !strings.Contains(p, "\\") {
			p = filepath.Join("out", p)
		}
		lower := strings.ToLower(p)
		switch {
		case strings.HasSuffix(lower, ".txt"):
			p = p[:len(p)-len(".txt")] + ext
		case strings.HasSuffix(lower, ".csv"):
			p = p[:len(p)-len(".csv")] + ext
		default:
			p += ext
		}
	}
	p = filepath.Clean(p)
	normalizedPath := strings.ReplaceAll(p, "\\", "/")
	if !filepath.IsAbs(p) && (strings.HasPrefix(normalizedPath, "/") || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator))) {
		return fmt.Errorf("任务 %q 输出位置必须位于 data 目录内或使用服务器绝对路径", t.Name)
	}
	t.Output.Path = p
	if t.Output.Template == "" {
		t.Output.Template = DefaultTemplate
	}
	switch strings.ToLower(strings.TrimSpace(t.Output.Sort)) {
	case "latencyasc":
		t.Output.Sort = OutputSortLatencyAsc
	case "latencydesc":
		t.Output.Sort = OutputSortLatencyDesc
	case "speeddesc":
		t.Output.Sort = OutputSortSpeedDesc
	case "speedasc":
		t.Output.Sort = OutputSortSpeedAsc
	case "ipasc":
		t.Output.Sort = OutputSortIPAsc
	case "countryasc":
		t.Output.Sort = OutputSortCountryAsc
	case "":
		t.Output.Sort = OutputSortLatencyAsc
	default:
		return fmt.Errorf("任务 %q 输出排序仅支持 latencyAsc/latencyDesc/speedDesc/speedAsc/ipAsc/countryAsc", t.Name)
	}
	return nil
}

var cronNumberPattern = regexp.MustCompile(`^\d+$`)

// normalizeCountryValues 把 country 条件取值拆分为大写二字码（ISO 3166-1 alpha-2）：
// 兼容旧任务把 “香港；日本；韩国；新加坡；美国” 一整串写在一个 value 的情况，
// 也兼容 US / us / USA / 美国 / United States 等常见写法。
func normalizeCountryValues(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		for _, part := range strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '，' || r == ';' || r == '；'
		}) {
			code := engine.NormalizeCountry(part)
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

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
	return fsutil.WriteFileAtomic(filepath.Join(dataDir, TasksFile), append(body, '\n'), 0o644)
}

// legacySubscription 仅用于把 subscriptions.json 一次性迁移为任务；运行时不再使用旧订阅器模型。
type legacySubscription struct {
	Name        string  `json:"name"`
	InputPath   string  `json:"inputPath,omitempty"`
	EnableSpeed bool    `json:"enableSpeed"`
	Groups      []Group `json:"groups"`
	Output      Output  `json:"output"`
}

func (s legacySubscription) toTask(index int) (Task, error) {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return Task{}, fmt.Errorf("订阅器名称不能为空")
	}
	if len(s.Groups) == 0 {
		return Task{}, fmt.Errorf("订阅器 %q 至少需要一个分组", name)
	}
	task := Task{
		ID:           fmt.Sprintf("t-%d", index+1),
		Name:         name,
		Enabled:      true,
		LibraryID:    library.DefaultID,
		Input:        TaskInput{Mode: "none"},
		Output:       TaskOutput{Path: s.Output.Path, Format: s.Output.Format, Template: s.Output.Template, Sort: s.Output.Sort},
		SpeedEnabled: s.EnableSpeed,
	}
	if inputPath := strings.TrimSpace(s.InputPath); inputPath != "" {
		task.Input = TaskInput{Mode: "file", File: inputPath}
	}
	seen := make(map[string]bool, len(s.Groups))
	for i, group := range s.Groups {
		group.Name = strings.TrimSpace(group.Name)
		if group.Name == "" {
			group.Name = fmt.Sprintf("分组%d", i+1)
		}
		if seen[group.Name] {
			return Task{}, fmt.Errorf("分组名重复: %s", group.Name)
		}
		seen[group.Name] = true
		if group.Count < 1 {
			return Task{}, fmt.Errorf("分组 %q 的配额 count 必须 >= 1", group.Name)
		}
		for _, port := range group.Ports {
			if port < 1 || port > 65535 {
				return Task{}, fmt.Errorf("分组 %q 包含非法端口 %d", group.Name, port)
			}
		}
		rule := TaskRule{
			Name:       group.Name,
			Limit:      group.Count,
			LatencyMin: group.LatencyMinMs,
			LatencyMax: group.MaxLatencyMs,
			SpeedMin:   group.MinSpeedKBs,
			SpeedMax:   group.MaxSpeedKBs,
		}
		addCondition := func(field string, values []string) {
			if len(values) > 0 {
				rule.Conditions = append(rule.Conditions, Condition{Field: field, Values: values})
			}
		}
		if countryCode := strings.ToUpper(strings.TrimSpace(group.CountryCode)); countryCode != "" {
			addCondition("country", []string{countryCode})
		}
		addCondition("city", group.Cities)
		addCondition("dataCenter", group.DataCenters)
		addCondition("region", group.Regions)
		if len(group.ASNs) > 0 {
			asns := make([]string, len(group.ASNs))
			for j, asn := range group.ASNs {
				asns[j] = strconv.FormatUint(uint64(asn), 10)
			}
			addCondition("asn", asns)
		}
		if len(group.Ports) > 0 {
			ports := make([]string, len(group.Ports))
			for j, port := range group.Ports {
				ports[j] = strconv.Itoa(port)
			}
			addCondition("port", ports)
		}
		if group.RequireSpeed {
			task.SpeedEnabled = true
		}
		task.Rules = append(task.Rules, rule)
	}
	if err := task.Validate(); err != nil {
		return Task{}, err
	}
	return task, nil
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
	var subscriptions []legacySubscription
	if err := json.Unmarshal(body, &subscriptions); err != nil {
		return nil, fmt.Errorf("%s 格式错误: %w", SubscriptionsFile, err)
	}
	tasks := make([]Task, 0, len(subscriptions))
	for i, subscription := range subscriptions {
		task, err := subscription.toTask(i)
		if err != nil {
			return nil, fmt.Errorf("%s 第 %d 项无效: %w", SubscriptionsFile, i+1, err)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}
