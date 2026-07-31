package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"iptest-web/internal/engine"
)

// errBlockedAddr 表示目标解析到了不允许访问的内网地址。
var errBlockedAddr = fmt.Errorf("目标解析到内网地址，已拒绝访问")

// isBlockedIP 判断一个已解析出的 IP 是否属于禁止访问的范围。
//
// 导入端点抓的应该是公网上的 IP 列表；会解析到内网的地址只有两种来源：
// 用户填错，或有人拿这个端点当内网探测器用。两种都该拒绝。
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// 云厂商 metadata 端点。169.254.169.254 已被上面的链路本地判断覆盖，
	// 这里显式列出 IPv6 形式（fd00:ec2::254 属于 IsPrivate 之外的 ULA 之内，
	// 实际已被 IsPrivate 覆盖，保留此处是为了让意图可读）。
	if ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true
	}
	// IPv4 保留段：0.0.0.0/8、100.64.0.0/10（CGNAT）、192.0.0.0/24、
	// 198.18.0.0/15（benchmark）、240.0.0.0/4（保留）
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0,
			v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127,
			v4[0] == 192 && v4[1] == 0 && v4[2] == 0,
			v4[0] == 198 && (v4[1] == 18 || v4[1] == 19),
			v4[0] >= 240:
			return true
		}
	}
	return false
}

// safeDialContext 在 TCP 连接建立前校验对端地址。
//
// 校验放在 Control 里而不是提前做一次 DNS 查询，是为了挡住 DNS rebinding：
// 事前解析到公网 IP、真正连接时再解析到 127.0.0.1 的攻击，只有在拨号这一刻
// 检查「实际要连的地址」才拦得住。重定向后的每一跳也都会走到这里。
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isBlockedIP(ip) {
				return errBlockedAddr
			}
			return nil
		},
	}
	return dialer.DialContext(ctx, network, addr)
}

// importRemoteRequest 对应 POST /api/import/remote。
type importRemoteRequest struct {
	URL string `json:"url"`

	// 远程文本里的 CIDR 网段如何抽样，语义同 latencyRequest。
	SampleMode string `json:"sampleMode"`
	SampleN    int    `json:"sampleN"`
}

// importResponse 返回解析后的目标，而非原始文本：
// 解析规则（多格式识别、CIDR 展开、去重）后端已有一份，
// 让前端再实现一遍只会两边走样。
type importResponse struct {
	Targets []engine.Target `json:"targets"`
	Bytes   int             `json:"bytes"`
	Source  string          `json:"source"`
	Text    string          `json:"text,omitempty"` // 远程导入保留原文，供前端按备注/端口筛选
}

const (
	// 导入的 TXT 一般几十 KB；给到 8 MB 足够，同时防止把整个进程内存拖垮。
	maxImportBytes = 8 << 20
	importTimeout  = 20 * time.Second

	// maxExpandedTargets 限制一次解析能返回给前端的目标数。
	//
	// 官方段「全取」是 152 万个地址，序列化成 JSON 再堆进浏览器内存有上百 MB，
	// 页面会直接卡死。与其假装支持，不如明确拒绝并告诉用户换抽样粒度。
	maxExpandedTargets = 200000
)

// importTextRequest 对应 POST /api/import/text。
type importTextRequest struct {
	Text       string `json:"text"`
	SampleMode string `json:"sampleMode"`
	SampleN    int    `json:"sampleN"`
}

// parseImportText 解析 IP 文本并展开其中的网段，附带规模上限检查。
// 两个导入端点（粘贴文本 / 远程 TXT）共用，避免上限与措辞两处漂移。
func parseImportText(w http.ResponseWriter, text, sampleMode string, sampleN int) ([]engine.Target, bool) {
	mode := engine.ParseSampleMode(sampleMode)

	// 先估算规模再展开，避免为了报错而先吃掉几百 MB 内存
	if cidrs := engine.CollectCIDRs(text); len(cidrs) > 0 {
		if total, _ := engine.CountCIDRs(cidrs, mode, sampleN); total > maxExpandedTargets {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"按当前抽样粒度需展开 %d 个 IP，超过 %d 的上限；请改用更粗的抽样粒度（如每 /24 取 1 个）",
				total, maxExpandedTargets))
			return nil, false
		}
	}

	targets := engine.ParseTargetsWithCIDR(text, mode, sampleN)
	if len(targets) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("已读到 %d 字节，但其中没有可识别的 IP", len(text)))
		return nil, false
	}
	return targets, true
}

// handleImportText 解析任意 IP 文本（含 CIDR 网段）并返回展开后的目标。
//
// 前端的 lineToTarget 只认 ip:port，不认网段；把 CIDR 展开与抽样放在后端，
// 是为了让「粘贴里混写网段」与「官方优选」共用 engine 里那一份已测过的算法，
// 而不是在 JS 里再实现一遍抽样。
func (s *Server) handleImportText(w http.ResponseWriter, r *http.Request) {
	var req importTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "内容为空")
		return
	}

	targets, ok := parseImportText(w, req.Text, req.SampleMode, req.SampleN)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, importResponse{
		Targets: targets,
		Bytes:   len(req.Text),
		Source:  "text",
	})
}

// handleImportRemote 代前端抓取远程 IP 列表。
//
// 存在的唯一理由是绕开浏览器 CORS：绝大多数订阅地址不会给
// Access-Control-Allow-Origin，前端 fetch 直接被拦。由 Go 来取则无此限制。
func (s *Server) handleImportRemote(w http.ResponseWriter, r *http.Request) {
	var req importRemoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}

	target := strings.TrimSpace(req.URL)
	if target == "" {
		writeError(w, http.StatusBadRequest, "请填写要导入的地址")
		return
	}
	// 没写协议时按 https 补全，用户往往只粘一个域名
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}

	parsed, err := url.Parse(target)
	if err != nil {
		writeError(w, http.StatusBadRequest, "地址格式不正确: "+err.Error())
		return
	}
	// 只放行 http/https：file:// 会让这个端点变成任意本地文件读取器。
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		writeError(w, http.StatusBadRequest, "只支持 http/https 地址")
		return
	}
	if parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "地址缺少主机名")
		return
	}

	body, err := fetchTextFile(target)
	if err != nil {
		// 内网拦截是「请求不该这么填」，不是「上游挂了」，用 400 而非 502；
		// 且 errBlockedAddr 会被 net.OpError / url.Error 层层包裹，
		// 直接 err.Error() 会把一堆 dial 细节丢到前端 toast 里。
		if errors.Is(err, errBlockedAddr) {
			writeError(w, http.StatusBadRequest,
				"该地址解析到内网或保留地址，已拒绝访问；导入源应是公网上的 IP 列表")
			return
		}
		writeError(w, http.StatusBadGateway, "导入失败: "+err.Error())
		return
	}

	targets, ok := parseImportText(w, body, req.SampleMode, req.SampleN)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, importResponse{
		Targets: targets,
		Bytes:   len(body),
		Source:  target,
		Text:    body,
	})
}

// fetchTextFile 取回远程文本，限制大小与耗时，并拒绝解析到内网的目标。
func fetchTextFile(rawURL string) (string, error) {
	client := &http.Client{
		Timeout: importTimeout,
		// 自定义 DialContext 是内网拦截的唯一生效点；换掉 Transport 时别丢了它。
		Transport: &http.Transport{
			DialContext:         safeDialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		// 重定向同样受 safeDialContext 约束，这里只额外限制跳数，
		// 避免 302 链把 20 秒超时耗在无意义的跳转上。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("重定向到不支持的协议 %s", req.URL.Scheme)
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 多读 1 字节用于判断是否被截断，避免把半行 IP 当成完整内容
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxImportBytes {
		return "", fmt.Errorf("文件超过 %d MB 上限", maxImportBytes>>20)
	}
	return string(data), nil
}
