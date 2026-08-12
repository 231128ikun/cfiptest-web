package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"iptest-web/internal/netutil"
)

// DefaultTraceURL 是验证 Cloudflare 节点用的 trace 接口（不含协议头）。
// 可在本地 data/config.json 中覆盖，例如换成自建 Worker 的 /cdn-cgi/trace。
const DefaultTraceURL = "speed.cloudflare.com/cdn-cgi/trace"

// DefaultIPSTypeURL 是 IPS 类型检测接口；{ip} 会替换为当前被测 IP。
const DefaultIPSTypeURL = "https://api.ipapi.is/?q={ip}"

// testSingleIP 对单个目标执行延迟测试 + Cloudflare 节点验证。
// 返回 (nil, failure) 表示该目标不可用，failure 说明失败在哪一步（TCP/延迟/trace/缺 colo）。
func (r *Runner) testSingleIP(ctx context.Context, target Target, opts LatencyOptions) (*Result, *ProbeFailure) {
	timeout := time.Duration(opts.TimeoutMs) * time.Millisecond
	probeCount := opts.ProbeCount
	if probeCount < 1 {
		probeCount = DefaultLatencyOptions().ProbeCount
	}

	// 多次独立 TCP 探测：单次抖动或偶发失败不直接淘汰，全部失败才判定不可用。
	var totalProbe time.Duration
	successfulProbes := 0
	for i := 0; i < probeCount; i++ {
		dialer := &net.Dialer{Timeout: timeout}
		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", target.String())
		if err != nil {
			continue
		}
		totalProbe += time.Since(start)
		successfulProbes++
		_ = conn.Close()
	}
	if successfulProbes == 0 {
		return nil, &ProbeFailure{Stage: FailureTCPConnect,
			Reason: fmt.Sprintf("TCP 连接 %d 次全部失败(%s)", probeCount, target.String())}
	}
	averageLatency := totalProbe / time.Duration(successfulProbes)
	if opts.MaxLatencyMs > 0 && averageLatency.Milliseconds() > int64(opts.MaxLatencyMs) {
		return nil, &ProbeFailure{Stage: FailureLatency,
			Reason: fmt.Sprintf("平均延迟 %dms 超过阈值 %dms", averageLatency.Milliseconds(), opts.MaxLatencyMs)}
	}

	// 独立连接执行 trace 校验，避免把 HTTP/TLS 开销计入 TCP 平均延迟。
	scheme := "https://"
	if !opts.EnableTLS {
		scheme = "http://"
	}
	httpProbeCount := opts.HTTPProbeCount
	if httpProbeCount < 1 {
		httpProbeCount = DefaultLatencyOptions().HTTPProbeCount
	}
	bodyStr := ""
	traceTimeout := timeout
	if opts.HTTPTimeoutMs > 0 {
		traceTimeout = time.Duration(opts.HTTPTimeoutMs) * time.Millisecond
	}
	for i := 0; i < httpProbeCount; i++ {
		if body, ok := fetchTrace(ctx, target, scheme, r.traceURL, traceTimeout); ok {
			bodyStr = body
			break
		}
	}
	if bodyStr == "" {
		return nil, &ProbeFailure{Stage: FailureTraceRequest,
			Reason: fmt.Sprintf("%d 次 trace 请求均失败（%s%s）", httpProbeCount, scheme, r.traceURL)}
	}
	// 只要求响应里能解析出 colo=（CF 边缘节点标识）；loc 缺失不判失败，
	// 避免因个别响应缺 loc 误杀可用节点（CFData-WEB 同样只认 colo）。
	traceData := parseTraceResponse(bodyStr)
	dataCenter := strings.TrimSpace(traceData["colo"])
	if dataCenter == "" {
		return nil, &ProbeFailure{Stage: FailureTraceColo,
			Reason: "trace 响应中缺少 colo 字段", Detail: truncateForFailure(bodyStr, 300)}
	}
	locCode := strings.TrimSpace(traceData["loc"])
	outboundIP := traceData["ip"]
	res := &Result{
		IP: target.IP, Port: target.Port, TCPLatencyMs: averageLatency.Milliseconds(),
		DataCenter: dataCenter, LocCode: locCode, OutboundIP: outboundIP,
		IPType: getIPType(outboundIP), VisitScheme: traceData["visit_scheme"],
		TLSVersion: traceData["tls"], SNI: traceData["sni"], HTTPVersion: traceData["http"],
		WARP: traceData["warp"], Gateway: traceData["gateway"], RBI: traceData["rbi"],
		KEX: traceData["kex"], Timestamp: traceData["ts"], IPSType: "N/A",
	}
	if loc, ok := r.lookupLocation(dataCenter); ok {
		res.Region, res.City, res.RegionZh, res.Country, res.CountryCode, res.CityZh, res.Emoji = loc.Region, loc.City, loc.RegionZh, loc.Country, loc.Cca2, loc.CityZh, loc.Emoji
	}
	res.ASN, res.ASNOrg = r.lookupASN(target.IP)
	if opts.EnableIPAPI && outboundIP != "" {
		res.IPSType = queryIPAPI(ctx, r.ipsTypeURL, target.IP)
	}
	return res, nil
}

// truncateForFailure 截断 trace 响应体，避免把整个响应塞进错误详情。
func truncateForFailure(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(截断)"
}

// fetchTrace 对单个目标执行一次 HTTP(S) trace 校验请求；
// 任何能读到响应体的响应都视为「请求成功」，是否真是 CF 节点由 colo 字段判定。
// 不再要求 uag 回显——部分边缘响应的头字段不完全一致，宽松判定可减少误杀。
func fetchTrace(ctx context.Context, target Target, scheme, traceURL string, timeout time.Duration) (string, bool) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target.String())
	if err != nil {
		return "", false
	}
	defer conn.Close()
	client := &http.Client{
		Transport: netutil.Transport(&http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}),
		Timeout:   timeout,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+traceURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	bodyStr := string(body)
	if strings.TrimSpace(bodyStr) == "" {
		return "", false
	}
	return bodyStr, true
}

// parseTraceResponse 解析 "key=value\n" 格式的 trace 响应体。
func parseTraceResponse(body string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

// getIPType 判断 IP 字符串是 IPv4 还是 IPv6。
func getIPType(ip string) string {
	if ip == "" {
		return "未知"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "无效IP"
	}
	if parsed.To4() != nil {
		return "IPv4"
	}
	return "IPv6"
}

// ipapiResponse 对应 api.ipapi.is 的响应结构（只取需要的字段）。
type ipapiResponse struct {
	IsDatacenter bool `json:"is_datacenter"`
	Company      struct {
		Type string `json:"type"`
	} `json:"company"`
	ASN struct {
		Type string `json:"type"`
	} `json:"asn"`
	Error string `json:"error"`
}

// queryIPAPI 调用 ipapi.is 判断 IP 的网络类型（机房/教育网/政府/金融/企业/家宽）。
func queryIPAPI(ctx context.Context, endpoint, ip string) string {
	requestURL := strings.ReplaceAll(endpoint, "{ip}", ip)
	if requestURL == endpoint {
		requestURL += ip
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: netutil.Transport(nil)}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var data ipapiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Error != "" {
		return ""
	}

	if data.IsDatacenter {
		return "🟥机房"
	}
	companyType := strings.ToLower(data.Company.Type)
	asnType := strings.ToLower(data.ASN.Type)
	switch {
	case companyType == "education" || asnType == "education":
		return "🎓教育网"
	case companyType == "government" || asnType == "government":
		return "🏛政府"
	case companyType == "banking" || asnType == "banking":
		return "💰金融"
	case companyType == "business" || asnType == "business":
		return "🏢企业"
	case companyType == "isp" && asnType == "isp":
		return "✅家宽"
	default:
		return "❓未知"
	}
}
