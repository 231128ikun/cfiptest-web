package engine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultTraceURL 是验证 Cloudflare 节点用的 trace 接口（不含协议头）。
// 可在 config.json 中覆盖，例如换成自建 Worker 的 /cdn-cgi/trace。
const DefaultTraceURL = "speed.cloudflare.com/cdn-cgi/trace"

var reColoLoc = regexp.MustCompile(`colo=([A-Z]+)[\s\S]*?loc=([A-Z]+)`)

// testSingleIP 对单个目标执行延迟测试 + Cloudflare 节点验证。
// 返回 nil 表示该目标不可用（连接失败、超时、延迟超标或非 CF 节点）。
func (r *Runner) testSingleIP(ctx context.Context, target Target, opts LatencyOptions) *Result {
	timeout := time.Duration(opts.TimeoutMs) * time.Millisecond

	// 1. TCP 握手测延迟
	dialer := &net.Dialer{Timeout: timeout}
	addr := target.String()
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil
	}
	defer conn.Close()
	tcpDuration := time.Since(start)

	// 2. 延迟阈值过滤
	if opts.MaxLatencyMs > 0 && tcpDuration.Milliseconds() > int64(opts.MaxLatencyMs) {
		return nil
	}

	// 3. 劫持 HTTP 传输层：复用刚才建立的连接请求 trace 接口。
	//    若目标不是 Cloudflare 边缘节点，这里不会返回合法 trace 文本。
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return conn, nil
			},
		},
		Timeout: timeout, // 覆盖 TLS 握手 + 请求 + 响应体读取全程
	}

	scheme := "https://"
	if !opts.EnableTLS {
		scheme = "http://"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+r.traceURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Close = true

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	bodyStr := string(body)

	// 4. 防伪校验：trace 接口会回显客户端 UA，匹配才说明对面真是 CF 节点
	if !strings.Contains(bodyStr, "uag=Mozilla/5.0") {
		return nil
	}

	// 5. 提取数据中心与国家码
	matches := reColoLoc.FindStringSubmatch(bodyStr)
	if len(matches) <= 2 {
		return nil
	}
	dataCenter, locCode := matches[1], matches[2]

	// 6. 解析全部 key=value 字段
	traceData := parseTraceResponse(bodyStr)
	outboundIP := traceData["ip"]

	res := &Result{
		IP:           target.IP,
		Port:         target.Port,
		TCPLatencyMs: tcpDuration.Milliseconds(),
		DataCenter:   dataCenter,
		LocCode:      locCode,
		OutboundIP:   outboundIP,
		IPType:       getIPType(outboundIP),
		VisitScheme:  traceData["visit_scheme"],
		TLSVersion:   traceData["tls"],
		SNI:          traceData["sni"],
		HTTPVersion:  traceData["http"],
		WARP:         traceData["warp"],
		Gateway:      traceData["gateway"],
		RBI:          traceData["rbi"],
		KEX:          traceData["kex"],
		Timestamp:    traceData["ts"],
		IPSType:      "N/A",
	}

	// 7. 地理位置查表
	if loc, ok := r.locations[dataCenter]; ok {
		res.Region = loc.Region
		res.City = loc.City
		res.RegionZh = loc.RegionZh
		res.Country = loc.Country
		res.CityZh = loc.CityZh
		res.Emoji = loc.Emoji
	}

	// 8. ASN 查询
	res.ASN, res.ASNOrg = lookupASN(r.asnDB, outboundIP)

	// 9. 可选：IPS 类型检测
	if opts.EnableIPAPI && outboundIP != "" {
		res.IPSType = queryIPAPI(ctx, target.IP)
	}

	return res
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
func queryIPAPI(ctx context.Context, ip string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipapi.is/?q="+ip, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 2 * time.Second}
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
