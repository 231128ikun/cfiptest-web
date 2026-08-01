// Package engine 提供 Cloudflare IP 延迟测试与下载测速的核心能力。
//
// 核心原理：对候选 IP:Port 发起 TCP 连接测握手延迟，然后劫持 HTTP 传输层
// 复用同一连接请求 https://speed.cloudflare.com/cdn-cgi/trace，
// 若响应为合法 trace 文本（含 uag 回显），则该 IP 是真实 Cloudflare 边缘节点。
package engine

import (
	"net"
	"strconv"
)

// Target 表示一个待测试的 IP:Port 目标。
type Target struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// String 返回 ip:port 形式；IPv6 自动加方括号，与前端 store.js 的
// targetToLine 保持一致，两端来回传的文本行才不会走样。
func (t Target) String() string {
	if t.Port == 0 {
		return t.IP
	}
	return net.JoinHostPort(t.IP, strconv.Itoa(t.Port))
}

// ResolveDefaultPorts 返回一份可执行目标：用户未指定端口（Port=0）时，
// TLS 使用 443，非 TLS 使用 80；显式端口始终原样保留。
func ResolveDefaultPorts(targets []Target, enableTLS bool) []Target {
	defaultPort := 80
	if enableTLS {
		defaultPort = 443
	}
	out := make([]Target, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.Port == 0 {
			target.Port = defaultPort
		}
		key := target.IP + "|" + strconv.Itoa(target.Port)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

// Result 表示一个 IP 的完整测试结果，通过 SSE 以 JSON 推送给前端。
type Result struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`

	TCPLatencyMs int64 `json:"tcpLatencyMs"` // TCP 握手延迟（毫秒）

	// Cloudflare Trace 提取
	DataCenter string `json:"dataCenter"` // colo= IATA 三字码
	LocCode    string `json:"locCode"`    // loc= 国家二字码

	// 地理位置（由 locations.json 按 IATA 查表）
	Region   string `json:"region"`
	City     string `json:"city"`
	RegionZh string `json:"regionZh"`
	Country  string `json:"country"`     // 边缘节点国家中文名（locations 表按 colo 查）
	CountryCode string `json:"countryCode"` // 边缘节点 ISO 二字母码（locations.cca2；非 trace loc）
	CityZh   string `json:"cityZh"`
	Emoji    string `json:"emoji"`

	// 出站信息
	OutboundIP string `json:"outboundIP"`
	IPType     string `json:"ipType"` // "IPv4" / "IPv6" / "未知"

	// Trace 元数据
	VisitScheme string `json:"visitScheme"`
	TLSVersion  string `json:"tlsVersion"`
	SNI         string `json:"sni"`
	HTTPVersion string `json:"httpVersion"`
	WARP        string `json:"warp"`
	Gateway     string `json:"gateway"`
	RBI         string `json:"rbi"`
	KEX         string `json:"kex"`
	Timestamp   string `json:"timestamp"`

	// ASN
	ASN    uint   `json:"asn"`
	ASNOrg string `json:"asnOrg"`

	// IPS 类型（可选，未启用时为 "N/A"）
	IPSType string `json:"ipsType"`

	// 测速结果（测速阶段回填，0 表示未测速）
	DownloadSpeedKBs float64 `json:"downloadSpeedKBs"`
}

// LatencyOptions 控制延迟测试阶段的参数。
type LatencyOptions struct {
	MaxConcurrency int  `json:"maxConcurrency"` // 并发数，默认 100
	TimeoutMs      int  `json:"timeoutMs"`      // 单连接超时（毫秒），默认 1000
	MaxLatencyMs   int  `json:"maxLatencyMs"`   // 延迟过滤阈值，0=不过滤
	MaxResults     int  `json:"maxResults"`     // 达到该数量的合格结果即停止，0=不限制（全部测完）
	EnableTLS      bool `json:"enableTLS"`      // 是否 HTTPS，默认 true
	EnableIPAPI    bool `json:"enableIPAPI"`    // 是否调用 ipapi.is 检测 IPS 类型，默认 false
}

// DefaultLatencyOptions 返回默认延迟测试配置。
func DefaultLatencyOptions() LatencyOptions {
	return LatencyOptions{
		MaxConcurrency: 100,
		TimeoutMs:      1000,
		MaxLatencyMs:   0,
		MaxResults:     0,
		EnableTLS:      true,
		EnableIPAPI:    false,
	}
}

// SpeedOptions 控制测速阶段的参数。
type SpeedOptions struct {
	MaxConcurrency int     `json:"maxConcurrency"` // 测速并发数，默认 5
	DurationSec    int     `json:"durationSec"`    // 单 IP 测速时限（秒），默认 5
	MinSpeedKBs    float64 `json:"minSpeedKBs"`    // 速度过滤阈值，0=不过滤
	MaxResults     int     `json:"maxResults"`     // 达到该数量的达标结果即停止，0=不限制
	DownloadURL    string  `json:"downloadURL"`    // 测速文件地址（不含协议头）
	EnableTLS      bool    `json:"enableTLS"`
}

// DefaultSpeedOptions 返回默认测速配置。
func DefaultSpeedOptions() SpeedOptions {
	return SpeedOptions{
		MaxConcurrency: 5,
		DurationSec:    5,
		MinSpeedKBs:    0,
		MaxResults:     0,
		DownloadURL:    "speed.cloudflare.com/__down?bytes=500000000",
		EnableTLS:      true,
	}
}

// EventType 标识流式事件类型。
type EventType string

const (
	EventResult   EventType = "result"   // 单条有效或最终结果
	EventProgress EventType = "progress" // 进度更新
	EventSpeed    EventType = "speed"    // 测速阶段单条速度更新
	EventDone     EventType = "done"     // 阶段完成
	EventError    EventType = "error"    // 错误/取消
	EventAuto     EventType = "auto"     // 自动化编排进度/日志（Message 为 JSON 文本）
)

// DoneReason 说明一个阶段为何结束。
//
// 存在的必要：达到最大结果数是通过 context 取消实现的，而用户点「停止」
// 也是取消 context——两者的 ctx.Err() 都是 context.Canceled，无法区分。
// 若不显式区分，「凑够 N 个正常收工」会被上层当成错误报给用户。
type DoneReason string

const (
	DoneCompleted DoneReason = "completed" // 全部目标测试完毕
	DoneStopped   DoneReason = "stopped"   // 用户主动停止
	DoneLimit     DoneReason = "limit"     // 达到最大结果数，提前收工
)

// Event 是流式回调的统一事件载体，直接序列化为 SSE data。
type Event struct {
	Type     EventType  `json:"type"`
	Result   *Result    `json:"result,omitempty"`
	Progress *Progress  `json:"progress,omitempty"`
	Message  string     `json:"message,omitempty"`
	Reason   DoneReason `json:"reason,omitempty"` // 仅 EventDone 携带
}

// Progress 表示一次进度快照。
type Progress struct {
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	ValidIPs  int    `json:"validIPs"`
	Phase     string `json:"phase,omitempty"` // latency | speed
}

// EventCallback 由 server 层传入，每产出一个事件调用一次。
type EventCallback func(Event)
