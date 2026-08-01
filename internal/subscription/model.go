// Package subscription 定义订阅器（约束 + 输出）模型，并实现自动化编排：
// 从 IP 库取候选现测，延迟失败移除、测速失败保留，检测结果与库不一致时回写更新，
// 直到每个分组配额满足或候选耗尽，最后按输出模板生成订阅文件。
package subscription

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Group 是编排器内部的一个分组约束：满足该约束的 IP 保留 Count 条。
// 由任务规则（TaskRule）展开得到；字段与前端规则编辑器一一对应。
type Group struct {
	Name          string   `json:"name"`                   // 分组名（如 "美国"）
	CountryCode   string   `json:"countryCode,omitempty"`  // ISO 3166-1 alpha-2；空 = 不限国家
	Country       string   `json:"country,omitempty"`      // 展示用中文名
	Cities        []string `json:"cities,omitempty"`       // 城市（中文名），空 = 不限
	DataCenters   []string `json:"dataCenters,omitempty"`  // 数据中心 IATA（colo），空 = 不限
	ASNs          []uint   `json:"asns,omitempty"`         // ASN，空 = 不限
	Regions       []string `json:"regions,omitempty"`      // 区域（中文名），空 = 不限
	Ports         []int    `json:"ports,omitempty"`        // 空 = 不限端口
	LatencyMinMs  int64    `json:"latencyMinMs,omitempty"` // 0 = 不限
	MaxLatencyMs  int64    `json:"maxLatencyMs,omitempty"` // 0 = 不限
	MinSpeedKBs   float64  `json:"minSpeedKBs,omitempty"`  // 0 = 不限
	MaxSpeedKBs   float64  `json:"maxSpeedKBs,omitempty"`  // 0 = 不限
	RequireSpeed  bool     `json:"requireSpeed,omitempty"` // 需要有效测速结果（配合 EnableSpeed）
	Count         int      `json:"count"`                  // 配额
}

// Output 描述订阅文件的输出方式。
type Output struct {
	Path     string `json:"path"`               // 相对 dataDir 的文件路径（如 out/sub.txt）
	Format   string `json:"format,omitempty"`   // txt | csv，默认 txt
	Template string `json:"template,omitempty"` // 占位符模板，默认 {ip}:{port}#{country}
}

// Subscription 是一个订阅器定义。
type Subscription struct {
	Name        string  `json:"name"`
	InputPath   string  `json:"inputPath,omitempty"` // 原订阅文件（相对 data 目录，如 out/原订阅.txt）；维护时先解析导入到 IP 库
	EnableSpeed bool    `json:"enableSpeed"`         // 补足时是否执行测速
	Groups      []Group `json:"groups"`
	Output      Output  `json:"output"`
}

// DefaultTemplate 是默认 TXT 输出模板。
const DefaultTemplate = "{ip}:{port}#{country}"

// Validate 校验订阅器定义并规范化（大写国家码、填充默认值）。
func (s *Subscription) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("订阅器名称不能为空")
	}
	if len(s.Groups) == 0 {
		return fmt.Errorf("订阅器 %q 至少需要一个分组", s.Name)
	}
	seen := make(map[string]bool, len(s.Groups))
	for i := range s.Groups {
		g := &s.Groups[i]
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" {
			g.Name = fmt.Sprintf("分组%d", i+1)
		}
		if seen[g.Name] {
			return fmt.Errorf("分组名重复: %s", g.Name)
		}
		seen[g.Name] = true
		g.CountryCode = strings.ToUpper(strings.TrimSpace(g.CountryCode))
		if g.Count < 1 {
			return fmt.Errorf("分组 %q 的配额 count 必须 >= 1", g.Name)
		}
		for j := range g.Ports {
			if g.Ports[j] < 1 || g.Ports[j] > 65535 {
				return fmt.Errorf("分组 %q 包含非法端口 %d", g.Name, g.Ports[j])
			}
		}
		if g.RequireSpeed {
			s.EnableSpeed = true
		}
	}
	if s.Output.Path == "" {
		if strings.TrimSpace(s.InputPath) != "" {
			// 未指定输出文件时，直接更新原订阅文件
			s.Output.Path = filepath.Clean(strings.TrimSpace(s.InputPath))
		} else {
			s.Output.Path = filepath.Join("out", s.Name+".txt")
		}
	}
	if s.Output.Format == "" {
		s.Output.Format = "txt"
	}
	s.Output.Format = strings.ToLower(s.Output.Format)
	if s.Output.Format != "txt" && s.Output.Format != "csv" {
		return fmt.Errorf("订阅器 %q 输出格式仅支持 txt/csv", s.Name)
	}
	if s.Output.Template == "" {
		s.Output.Template = DefaultTemplate
	}
	return nil
}

// SubscriptionsFile 是订阅器定义文件名。
const SubscriptionsFile = "subscriptions.json"

// LoadSubscriptions 读取 dataDir/subscriptions.json；文件不存在时返回空列表。
func LoadSubscriptions(dataDir string) ([]Subscription, error) {
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
	for i := range subs {
		if err := subs[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s 第 %d 项无效: %w", SubscriptionsFile, i+1, err)
		}
	}
	return subs, nil
}

// SaveSubscriptions 原子写回 dataDir/subscriptions.json。
func SaveSubscriptions(dataDir string, subs []Subscription) error {
	for i := range subs {
		if err := subs[i].Validate(); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dataDir, SubscriptionsFile), append(body, '\n'))
}

// writeFileAtomic 先写同目录临时文件再改名，避免异常退出留下半截 JSON。
func writeFileAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".subs-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
