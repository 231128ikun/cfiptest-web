// Package library 提供 IP 库的持久化存储：data/ipdb.jsonl。
//
// 每条记录以 ip|port 为唯一键，保存最近一次检测得到的元数据与测量值。
// 库本身不做过期淘汰：是否重新检测由上层（subscription 编排器）在“用到时现测”决定。
package library

import (
	"strconv"
	"time"

	"iptest-web/internal/engine"
)

// 条目状态。
const (
	StatusNew    = "new"    // 已导入、尚未通过延迟检测
	StatusActive = "active" // 最近一次延迟检测通过
)

// 条目来源。
const (
	SourceManual   = "manual"   // 手动输入
	SourceImport   = "import"   // 导入（粘贴 / 远程 TXT / CSV）
	SourceOfficial = "official" // 官方 IP 段
	SourceTopup    = "topup"    // 自动补足时从订阅器回写
)

// Entry 是 IP 库中的一条记录。
//
// 设计要点：
//   - country 字段来自检测时的 CF trace + locations 数据，只要测过一次就不需要重测即可按国家过滤；
//   - 检测结果与库不一致时（如国家变化）以最新检测为准整体覆盖（见 subscription 编排器）；
//   - 延迟失败视为失效并从库中删除；测速失败只标记 SpeedValid=false（测速链路本身不稳定，不判死）。
type Entry struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`

	// 上次检测得到的元数据。
	CountryCode string `json:"countryCode"` // ISO 3166-1 alpha-2（trace loc / locations cca2）
	Country     string `json:"country"`     // 中文名
	CityZh      string `json:"cityZh"`
	Emoji       string `json:"emoji"`
	DataCenter  string `json:"dataCenter"`
	RegionZh    string `json:"regionZh"`
	ASN         uint   `json:"asn"`
	ASNOrg      string `json:"asnOrg"`

	// 测量值。
	TCPLatencyMs int64   `json:"tcpLatencyMs"` // 0 = 尚未通过延迟检测
	SpeedKBs     float64 `json:"speedKBs"`     // 最近一次有效测速值
	SpeedValid   bool    `json:"speedValid"`   // 最近一次测速是否有效（false=链路不稳/未测）

	Source        string    `json:"source"` // manual | import | official | topup
	Status        string    `json:"status"` // new | active
	FirstSeenAt   time.Time `json:"firstSeenAt"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
	Checks        int       `json:"checks"` // 累计检测次数
}

// Key 返回 ip|port 形式的稳定键。
func Key(ip string, port int) string {
	return ip + "|" + strconv.Itoa(port)
}

// Key 返回本条目的键。
func (e *Entry) Key() string { return Key(e.IP, e.Port) }

// IsActive 判断条目当前是否视为有效。
func (e *Entry) IsActive() bool { return e.Status == StatusActive }

// EntryFromResult 从一次检测结果构造库条目（状态 active，来源 import）。
// 用于把「手动三步检测」通过的结果一键导入 IP 库。
func EntryFromResult(res engine.Result, now time.Time) Entry {
	return Entry{
		IP:           res.IP,
		Port:         res.Port,
		CountryCode:  res.LocCode,
		Country:      res.Country,
		CityZh:       res.CityZh,
		Emoji:        res.Emoji,
		DataCenter:   res.DataCenter,
		RegionZh:     res.RegionZh,
		ASN:          res.ASN,
		ASNOrg:       res.ASNOrg,
		TCPLatencyMs: res.TCPLatencyMs,
		SpeedKBs:     res.DownloadSpeedKBs,
		SpeedValid:   res.DownloadSpeedKBs > 0,
		Source:       SourceImport,
		Status:       StatusActive,
		FirstSeenAt:  now,
		LastCheckedAt: now,
		Checks:       1,
	}
}
