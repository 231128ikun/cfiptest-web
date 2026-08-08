// Package subscription 实现自动维护任务的规则展开、候选检测与结果输出。
package subscription

import (
	"os"
	"path/filepath"
)

// Group 是编排器内部的一个分组约束：满足该约束的 IP 保留 Count 条。
// 由任务规则（TaskRule）展开得到；字段与前端规则编辑器一一对应。
type Group struct {
	Name         string   `json:"name"`                   // 分组名（如 "美国"）
	CountryCode  string   `json:"countryCode,omitempty"`  // ISO 3166-1 alpha-2；空 = 不限国家
	Country      string   `json:"country,omitempty"`      // 展示用中文名
	Cities       []string `json:"cities,omitempty"`       // 城市（中文名），空 = 不限
	DataCenters  []string `json:"dataCenters,omitempty"`  // 数据中心 IATA（colo），空 = 不限
	ASNs         []uint   `json:"asns,omitempty"`         // ASN，空 = 不限
	Regions      []string `json:"regions,omitempty"`      // 区域（中文名），空 = 不限
	Ports        []int    `json:"ports,omitempty"`        // 空 = 不限端口
	LatencyMinMs int64    `json:"latencyMinMs,omitempty"` // 0 = 不限
	MaxLatencyMs int64    `json:"maxLatencyMs,omitempty"` // 0 = 不限
	MinSpeedKBs  float64  `json:"minSpeedKBs,omitempty"`  // 0 = 不限
	MaxSpeedKBs  float64  `json:"maxSpeedKBs,omitempty"`  // 0 = 不限
	RequireSpeed bool     `json:"requireSpeed,omitempty"` // 需要有效测速结果
	Count        int      `json:"count"`                  // 配额
}

// Output 描述订阅文件的输出方式。
type Output struct {
	Path     string `json:"path"`               // 相对 dataDir 的文件路径（如 out/sub.txt）
	Format   string `json:"format,omitempty"`   // txt | csv，默认 txt
	Template string `json:"template,omitempty"` // 占位符模板，默认 {ip}:{port}#{country}
	Sort     string `json:"sort,omitempty"`     // 输出排序，默认 latencyAsc（见 OutputSort* 常量）
}

// 输出排序方式（Output.Sort / TaskOutput.Sort）。
const (
	OutputSortLatencyAsc  = "latencyAsc"  // 延迟升序（默认）
	OutputSortLatencyDesc = "latencyDesc" // 延迟降序
	OutputSortSpeedDesc   = "speedDesc"   // 速度降序；未测速条目排在最后
	OutputSortSpeedAsc    = "speedAsc"    // 速度升序；未测速条目排在最后
	OutputSortIPAsc       = "ipAsc"       // IP 地址升序（先 IPv4 后 IPv6）
	OutputSortCountryAsc  = "countryAsc"  // 国家/地区升序（优先 ISO 二字码，其次国家名）
)

// DefaultTemplate 是默认 TXT 输出模板。
const DefaultTemplate = "{ip}:{port}#{country}"

// SubscriptionsFile 是旧版订阅器定义文件名，仅用于一次性迁移到 tasks.json。
const SubscriptionsFile = "subscriptions.json"

// writeFileAtomic 先写同目录临时文件再改名，避免异常退出留下半截 JSON。
func writeFileAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".iptest-*.tmp")
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
