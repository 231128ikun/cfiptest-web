package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"iptest-web/internal/engine"
)

// officialRangesResponse 对应 GET /api/official-ranges。
type officialRangesResponse struct {
	IPv4     []string         `json:"ipv4"`
	IPv6     []string         `json:"ipv6"`
	Source   string           `json:"source"`   // "remote" | "builtin"
	Estimate map[string]int   `json:"estimate"` // 各抽样模式下的 IPv4 预估数量
	Ports    map[string][]int `json:"ports"`    // Cloudflare 支持的端口，按协议分组
	Warning  string           `json:"warning,omitempty"`
}

// cfIPsAPIResponse 是 api.cloudflare.com/client/v4/ips 的响应结构。
type cfIPsAPIResponse struct {
	Success bool `json:"success"`
	Result  struct {
		IPv4CIDRs []string `json:"ipv4_cidrs"`
		IPv6CIDRs []string `json:"ipv6_cidrs"`
	} `json:"result"`
}

// 官方段几乎不变，进程内缓存一小时，避免每次切到官方模式都打一次网络。
var (
	rangesMu     sync.Mutex
	rangesCache  *officialRangesResponse
	rangesCached time.Time
)

const rangesCacheTTL = time.Hour

// cfSupportedPorts 是 Cloudflare 官方文档列出的可回源端口。
// 官方优选模式的端口选项从这里出，避免用户测一个 CF 根本不监听的端口。
var cfSupportedPorts = map[string][]int{
	"https": {443, 2053, 2083, 2087, 2096, 8443},
	"http":  {80, 8080, 8880, 2052, 2082, 2086, 2095},
}

func (s *Server) handleOfficialRanges(w http.ResponseWriter, r *http.Request) {
	// n 用于「每 /24 取 N 个」模式的预估，缺省 1
	n := 1
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}

	resp := s.officialRanges()

	// 预估数量随 n 变化，不进缓存
	out := *resp
	out.Estimate = map[string]int{}
	onePer, _ := engine.CountCIDRs(out.IPv4, engine.SampleOnePerSubnet, 1)
	nPer, _ := engine.CountCIDRs(out.IPv4, engine.SampleNPerSubnet, n)
	all, _ := engine.CountCIDRs(out.IPv4, engine.SampleAll, 0)
	out.Estimate["onePerSubnet"] = onePer
	out.Estimate["nPerSubnet"] = nPer
	out.Estimate["all"] = all
	ipv6One, _ := engine.CountCIDRs(out.IPv6, engine.SampleOnePerSubnet, 1)
	out.Estimate["ipv6OnePerSubnet"] = ipv6One

	writeJSON(w, http.StatusOK, out)
}

// officialRanges 返回官方 IP 段，优先取远端（带缓存），失败时回落到内置兜底。
func (s *Server) officialRanges() *officialRangesResponse {
	rangesMu.Lock()
	defer rangesMu.Unlock()

	if rangesCache != nil && time.Since(rangesCached) < rangesCacheTTL {
		return rangesCache
	}

	resp := &officialRangesResponse{Ports: cfSupportedPorts}

	ipv4, ipv6, err := fetchOfficialRanges(s.cfg.Sources.OfficialRanges)
	if err != nil || len(ipv4) == 0 {
		resp.IPv4 = engine.BuiltinCFRanges.IPv4
		resp.IPv6 = engine.BuiltinCFRanges.IPv6
		resp.Source = "builtin"
		if err != nil {
			resp.Warning = fmt.Sprintf("无法获取官方 IP 段（%v），已使用内置兜底数据", err)
		}
	} else {
		resp.IPv4 = ipv4
		resp.IPv6 = ipv6
		resp.Source = "remote"
	}

	rangesCache = resp
	rangesCached = time.Now()
	return resp
}

// fetchOfficialRanges 依次尝试各个源，返回第一个成功解析的结果。
func fetchOfficialRanges(urls []string) ([]string, []string, error) {
	if len(urls) == 0 {
		return nil, nil, fmt.Errorf("未配置官方 IP 段获取地址")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for _, url := range urls {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")

		httpResp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		var parsed cfIPsAPIResponse
		derr := json.NewDecoder(httpResp.Body).Decode(&parsed)
		httpResp.Body.Close()

		if httpResp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", url, httpResp.StatusCode)
			continue
		}
		if derr != nil {
			lastErr = fmt.Errorf("%s: 响应不是预期的 JSON: %w", url, derr)
			continue
		}
		if !parsed.Success || len(parsed.Result.IPv4CIDRs) == 0 {
			lastErr = fmt.Errorf("%s: 响应中没有 IP 段", url)
			continue
		}
		return parsed.Result.IPv4CIDRs, parsed.Result.IPv6CIDRs, nil
	}
	return nil, nil, lastErr
}
