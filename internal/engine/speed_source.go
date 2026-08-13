package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"iptest-web/internal/netutil"
)

// 测速源预设：移植自 CFData-WEB 的「预设 + ISP 自动识别」模型。
// 预设值即测速 URL（通常不含协议头），URL 省略协议时由引擎按 EnableTLS 自动补全；
// auto 模式按本机公网 ISP/ASN（本地 GeoLite2-ASN.mmdb）自动选择，识别失败回退 Cloudflare。
const (
	AutoSpeedURLValue   = "auto"   // 配置中保存的自动选择取值
	AutoSpeedURLLabel   = "自动选择"   // 前端展示文案
	CustomSpeedURLValue = "custom" // 前端「手动输入」预设取值（引擎不直接使用）

	// CloudflareSpeedURL 是默认/兜底测速源（Cloudflare 官方）。
	CloudflareSpeedURL = "speed.cloudflare.com/__down?bytes=500000000"
	// CMSpeedURL 与 MobileSpeedURL 是社区维护的移动测速源（CFData-WEB 同款），
	// 仅在中国移动网络下自动选用；其他网络保持 Cloudflare 官方源。
	CMSpeedURL     = "cf.090227.xyz/__down?bytes=99999999"
	MobileSpeedURL = "speed.okl.abrdns.com"
)

// isAutoSpeedURL 判断用户输入是否为「自动选择」：空、auto 或中文文案都算。
func isAutoSpeedURL(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, AutoSpeedURLValue) || value == AutoSpeedURLLabel
}

// schemeFor 返回 enableTLS 对应的协议名（不含 ://）。
func schemeFor(enableTLS bool) string {
	if enableTLS {
		return "https"
	}
	return "http"
}

// normalizeDownloadURL 为测速 URL 补全协议：
//   - 空串原样返回；
//   - "//host/path" → "scheme://host/path"（协议相对）；
//   - 无 http/https 前缀 → "scheme://"+raw；
//   - 已带协议 → 原样保留（不因 EnableTLS 改变用户显式指定的协议）。
func normalizeDownloadURL(raw, scheme string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	scheme = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(scheme)), "://")
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(value, "//") {
		return scheme + ":" + value
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return scheme + "://" + value
	}
	return value
}

// ResolveSpeedURL 把测速地址解析为实际请求 URL（含协议头），返回 (URL, 来源标签)。
// auto / 空 → 按本机运营商自动选源（首次探测后进程内缓存），识别失败回退 Cloudflare；
// 自定义 URL → 只做协议补全。enableTLS 决定缺省协议。
func (r *Runner) ResolveSpeedURL(ctx context.Context, raw string, enableTLS bool) (string, string) {
	if isAutoSpeedURL(raw) {
		return r.resolveAutoSpeedURL(ctx, enableTLS)
	}
	// 命中内置预设时用预设名做来源标签，便于日志/完成消息展示。
	switch strings.TrimSpace(raw) {
	case CloudflareSpeedURL:
		return normalizeDownloadURL(CloudflareSpeedURL, schemeFor(enableTLS)), "Cloudflare"
	case CMSpeedURL:
		return normalizeDownloadURL(CMSpeedURL, schemeFor(enableTLS)), "移动（CM提供）"
	case MobileSpeedURL:
		return normalizeDownloadURL(MobileSpeedURL, schemeFor(enableTLS)), "移动专属"
	}
	return normalizeDownloadURL(raw, schemeFor(enableTLS)), "自定义"
}

// resolveAutoSpeedURL 按本机运营商自动选择测速源，结果缓存在 Runner 上（单进程一次探测）。
func (r *Runner) resolveAutoSpeedURL(ctx context.Context, enableTLS bool) (string, string) {
	r.autoOnce.Do(func() {
		asn, org := uint(0), ""
		if r.ownASN != nil {
			asn, org = r.ownASN(ctx)
		}
		if isChinaMobileASN(asn, org) {
			r.autoURL = CMSpeedURL
			r.autoLabel = "移动（CM提供）"
			return
		}
		r.autoURL = CloudflareSpeedURL
		r.autoLabel = "Cloudflare"
	})
	return normalizeDownloadURL(r.autoURL, schemeFor(enableTLS)), r.autoLabel + "（自动选择）"
}

// isChinaMobileASN 判断 ASN / 机构是否属于中国移动，规则与 CFData-WEB 一致。
func isChinaMobileASN(asn uint, org string) bool {
	o := strings.ToLower(strings.TrimSpace(org))
	for _, keyword := range []string{"cmi", "cmnet", "chinamobile", "china mobile", "cmcc", "mobile communications", "移动"} {
		if strings.Contains(o, keyword) {
			return true
		}
	}
	switch asn {
	case 9808, 24400, 56040, 56041, 56044:
		return true
	}
	return false
}

// lookupOwnASN 用一次普通 trace 请求（直连公网，不走目标 IP 劫持）拿本机出口 IP，
// 再查本地 GeoLite2-ASN.mmdb 得到 ASN / 机构；任一步失败返回 (0, "")。
func (r *Runner) lookupOwnASN(ctx context.Context) (uint, string) {
	if r.asnDB == nil {
		return 0, ""
	}
	url := "https://" + r.traceURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 3 * time.Second, Transport: netutil.Transport(nil)}
	resp, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, ""
	}
	ip := parseTraceResponse(string(body))["ip"]
	if ip == "" {
		return 0, ""
	}
	return r.lookupASN(ip)
}

// speedSourceEvent 返回一条记录「本次实际使用的测速源」的 EventAuto 日志事件，
// 前端 sideLogFromAuto 会把它显示为侧边日志行（[测速] …）。
func speedSourceEvent(label string) Event {
	body, _ := json.Marshal(map[string]string{"group": "测速", "log": "本次测速使用测速源：" + label})
	return Event{Type: EventAuto, Message: string(body)}
}
