// Package library 提供 IP 库的持久化存储：data/ipdb.jsonl。
//
// 每条记录以 ip|port 为唯一键，保存最近一次检测得到的元数据与测量值。
// 库本身不做过期淘汰：是否重新检测由上层（subscription 编排器）在“用到时现测”决定。
package library

import (
	"encoding/json"
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

	// engine.Result 的全量检测字段。IP 库持久化完整结果，避免后续筛选、导出或排障时信息丢失。
	TCPLatencyMs int64  `json:"tcpLatencyMs"`
	DataCenter   string `json:"dataCenter"`
	LocCode      string `json:"locCode"`
	Region       string `json:"region"`
	City         string `json:"city"`
	RegionZh     string `json:"regionZh"`
	Country      string `json:"country"`
	CountryCode  string `json:"countryCode"`
	CityZh       string `json:"cityZh"`
	Emoji        string `json:"emoji"`
	OutboundIP   string `json:"outboundIP"`
	IPType       string `json:"ipType"`
	VisitScheme  string `json:"visitScheme"`
	TLSVersion   string `json:"tlsVersion"`
	SNI          string `json:"sni"`
	HTTPVersion  string `json:"httpVersion"`
	WARP         string `json:"warp"`
	Gateway      string `json:"gateway"`
	RBI          string `json:"rbi"`
	KEX          string `json:"kex"`
	Timestamp    string `json:"timestamp"`
	ASN          uint   `json:"asn"`
	ASNOrg       string `json:"asnOrg"`
	IPSType      string `json:"ipsType"`

	DownloadSpeedKBs float64 `json:"downloadSpeedKBs"`
	// SpeedKBs 保留为源码兼容别名，不参与新库文件序列化。
	SpeedKBs   float64 `json:"-"`
	SpeedValid bool    `json:"speedValid"`

	Source        string    `json:"source"`
	Status        string    `json:"status"`
	FirstSeenAt   time.Time `json:"firstSeenAt"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
	Checks        int       `json:"checks"`
	// ConsecutiveFailures 连续延迟失败次数；达到上层阈值才从库移除，成功一次即清零。
	ConsecutiveFailures int `json:"consecutiveFailures,omitempty"`
}

// UnmarshalJSON 兼容旧版库中的 speedKBs 字段；保存时统一写为 downloadSpeedKBs。
func (e *Entry) UnmarshalJSON(data []byte) error {
	type entryAlias Entry
	var raw struct {
		entryAlias
		LegacySpeedKBs *float64 `json:"speedKBs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = Entry(raw.entryAlias)
	if e.DownloadSpeedKBs == 0 && raw.LegacySpeedKBs != nil {
		e.DownloadSpeedKBs = *raw.LegacySpeedKBs
	}
	e.SpeedKBs = e.DownloadSpeedKBs
	return nil
}

// Key 返回 ip|port 形式的稳定键。
func Key(ip string, port int) string {
	return ip + "|" + strconv.Itoa(port)
}

// Key 返回本条目的键。
func (e *Entry) Key() string { return Key(e.IP, e.Port) }

// EntryFromResult 从一次检测结果构造库条目（状态 active，来源 import）。
// 用于把「手动三步检测」通过的结果一键导入 IP 库。
func EntryFromResult(res engine.Result, now time.Time) Entry {
	return Entry{
		IP: res.IP, Port: res.Port,
		TCPLatencyMs: res.TCPLatencyMs, DataCenter: res.DataCenter, LocCode: res.LocCode,
		Region: res.Region, City: res.City, RegionZh: res.RegionZh,
		Country: res.Country, CountryCode: res.CountryCode, CityZh: res.CityZh, Emoji: res.Emoji,
		OutboundIP: res.OutboundIP, IPType: res.IPType,
		VisitScheme: res.VisitScheme, TLSVersion: res.TLSVersion, SNI: res.SNI,
		HTTPVersion: res.HTTPVersion, WARP: res.WARP, Gateway: res.Gateway, RBI: res.RBI,
		KEX: res.KEX, Timestamp: res.Timestamp, ASN: res.ASN, ASNOrg: res.ASNOrg, IPSType: res.IPSType,
		DownloadSpeedKBs: res.DownloadSpeedKBs, SpeedKBs: res.DownloadSpeedKBs, SpeedValid: res.DownloadSpeedKBs > 0,
		Source: SourceImport, Status: StatusActive, FirstSeenAt: now, LastCheckedAt: now, Checks: 1,
	}
}
