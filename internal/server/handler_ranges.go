package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"iptest-web/internal/engine"
)

const (
	officialIPv4File = "cloudflare-ips-v4.txt"
	officialIPv6File = "cloudflare-ips-v6.txt"
	// officialCacheMarker 是缓存文件版本标记。旧的 IPv6 缓存是官方聚合 /32 段
	// （随机抽样几乎全部失败的问题数据），必须拒绝读取并回退内置活跃 /48 列表。
	officialCacheMarker = "# iptest-web v2 active-48"
)

type officialRangesResponse struct {
	IPv4      []string         `json:"ipv4"`
	IPv6      []string         `json:"ipv6"`
	Source    string           `json:"source"` // remote | cache | builtin
	UpdatedAt string           `json:"updatedAt,omitempty"`
	Estimate  map[string]int   `json:"estimate"`
	Ports     map[string][]int `json:"ports"`
	Warning   string           `json:"warning,omitempty"`
}

type cfIPsAPIResponse struct {
	Success bool `json:"success"`
	Result  struct {
		IPv4CIDRs []string `json:"ipv4_cidrs"`
		IPv6CIDRs []string `json:"ipv6_cidrs"`
	} `json:"result"`
}

var cfSupportedPorts = map[string][]int{
	"https": {443, 2053, 2083, 2087, 2096, 8443},
	"http":  {80, 8080, 8880, 2052, 2082, 2086, 2095},
}

func (s *Server) handleOfficialRanges(w http.ResponseWriter, r *http.Request) {
	n := 1
	if parsed, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && parsed > 0 {
		n = parsed
	}
	refresh := r.URL.Query().Get("refresh") == "1"

	resp := s.officialRanges(refresh)
	out := *resp
	out.Estimate = map[string]int{}
	out.Estimate["onePerSubnet"], _ = engine.CountCIDRs(out.IPv4, engine.SampleOnePerSubnet, 1)
	out.Estimate["nPerSubnet"], _ = engine.CountCIDRs(out.IPv4, engine.SampleNPerSubnet, n)
	out.Estimate["all"], _ = engine.CountCIDRs(out.IPv4, engine.SampleAll, 0)
	out.Estimate["ipv6OnePerSubnet"], _ = engine.CountCIDRs(out.IPv6, engine.SampleOnePerSubnet, 1)
	out.Estimate["ipv6NPerSubnet"], _ = engine.CountCIDRs(out.IPv6, engine.SampleNPerSubnet, n)
	out.Estimate["ipv6All"], _ = engine.CountCIDRs(out.IPv6, engine.SampleAll, 0)
	writeJSON(w, http.StatusOK, out)
}

// officialRanges 返回官方 IP 段：IPv4 与 IPv6 分族获取，互不拖累。
//   - IPv4：Cloudflare 官方 JSON（cfIPsAPIResponse）；
//   - IPv6：baipiao 活跃 /48 列表（纯文本，每行一个 CIDR），
//     远程失败时用内置活跃 /48 列表兜底（不再读旧版聚合缓存）。
// refresh=true 时强制拉取远端并按族更新缓存；false 时优先读本地缓存。
func (s *Server) officialRanges(refresh bool) *officialRangesResponse {
	s.rangesMu.Lock()
	defer s.rangesMu.Unlock()

	if !refresh && s.rangesCache != nil {
		return s.rangesCache
	}
	if !refresh {
		if resp := s.officialRangesFromCache(); resp != nil {
			s.rangesCache = resp
			return resp
		}
	}

	s.configMu.RLock()
	officialSources := append([]string(nil), s.cfg.Sources.OfficialRanges...)
	activeV6Sources := append([]string(nil), s.cfg.Sources.ActiveIPv6RangeSources...)
	s.configMu.RUnlock()

	warnings := make([]string, 0, 2)
	ipv4, v4Err := fetchOfficialRanges(officialSources)
	if len(ipv4) == 0 {
		ipv4 = engine.BuiltinCFRanges.IPv4
		if v4Err == nil {
			v4Err = fmt.Errorf("未获取到 IPv4 网段")
		}
		warnings = append(warnings, fmt.Sprintf("IPv4 远程获取失败（%v），已用内置兜底", v4Err))
	}
	ipv6, v6Err := fetchOfficialIPv6Ranges(activeV6Sources)
	if len(ipv6) == 0 {
		ipv6 = engine.BuiltinCFRanges.IPv6
		if v6Err == nil {
			v6Err = fmt.Errorf("未获取到 IPv6 网段")
		}
		warnings = append(warnings, fmt.Sprintf("IPv6 远程获取失败（%v），已用内置活跃 /48 列表兜底", v6Err))
	}

	// 只有远程成功的那一族才写缓存，避免用兜底数据覆盖较新的缓存。
	if v4Err == nil {
		_ = saveOfficialRangeCacheFamily(s.dataDir, officialIPv4File, ipv4)
	}
	if v6Err == nil {
		_ = saveOfficialRangeCacheFamily(s.dataDir, officialIPv6File, ipv6)
	}

	source := "remote"
	if v4Err != nil && v6Err != nil {
		source = "builtin"
	}
	updatedAt := ""
	if v4Err == nil || v6Err == nil {
		updatedAt = time.Now().Format(time.RFC3339)
	}
	s.rangesCache = &officialRangesResponse{
		IPv4: ipv4, IPv6: ipv6, Source: source,
		UpdatedAt: updatedAt, Ports: cfSupportedPorts,
		Warning: strings.Join(warnings, "；"),
	}
	return s.rangesCache
}

// officialRangesFromCache 组装非刷新路径的缓存：IPv4 直接用本地缓存；
// IPv6 缓存必须带版本标记，旧版聚合 /32 缓存直接弃用并回落内置活跃 /48 列表。
func (s *Server) officialRangesFromCache() *officialRangesResponse {
	warnings := make([]string, 0, 2)
	updated := time.Time{}
	source := "cache"

	cached4, t4, err4 := loadOfficialRangeCacheV4(s.dataDir)
	if err4 != nil {
		cached4 = engine.BuiltinCFRanges.IPv4
		source = "builtin"
		warnings = append(warnings, "IPv4 缓存不可用，已用内置兜底")
	} else {
		updated = t4
	}

	cached6, t6, err6 := loadOfficialRangeCacheV6(s.dataDir)
	if err6 != nil {
		cached6 = engine.BuiltinCFRanges.IPv6
		if source != "builtin" {
			source = "builtin"
		}
		warnings = append(warnings, "IPv6 本地缓存是旧版聚合网段（抽样几乎全部失败），已改用内置活跃 /48 列表")
	} else if t6.After(updated) {
		updated = t6
	}

	if len(warnings) == 2 {
		return nil // 两个族都没有可用缓存，走远程/内置逻辑
	}
	resp := &officialRangesResponse{IPv4: cached4, IPv6: cached6, Source: source, Ports: cfSupportedPorts, Warning: strings.Join(warnings, "；")}
	if !updated.IsZero() {
		resp.UpdatedAt = updated.Format(time.RFC3339)
	}
	return resp
}

func loadOfficialRangeCacheV4(dataDir string) ([]string, time.Time, error) {
	path := filepath.Join(dataDir, officialIPv4File)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	ipv4, err := parseCIDRFile(string(body), false)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%s: %w", officialIPv4File, err)
	}
	if st, err := os.Stat(path); err == nil {
		return ipv4, st.ModTime(), nil
	}
	return ipv4, time.Time{}, nil
}

func loadOfficialRangeCacheV6(dataDir string) ([]string, time.Time, error) {
	path := filepath.Join(dataDir, officialIPv6File)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !strings.HasPrefix(string(body), officialCacheMarker) {
		return nil, time.Time{}, fmt.Errorf("%s 缺少版本标记（旧版聚合网段缓存已弃用）", officialIPv6File)
	}
	ipv6, err := parseCIDRFile(string(body), true)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%s: %w", officialIPv6File, err)
	}
	if st, err := os.Stat(path); err == nil {
		return ipv6, st.ModTime(), nil
	}
	return ipv6, time.Time{}, nil
}

func loadOfficialRangeCache(dataDir string) ([]string, []string, string, error) {
	cached4, t4, err4 := loadOfficialRangeCacheV4(dataDir)
	if err4 != nil {
		return nil, nil, "", err4
	}
	cached6, t6, err6 := loadOfficialRangeCacheV6(dataDir)
	if err6 != nil {
		return nil, nil, "", err6
	}
	updated := t4
	if t6.After(updated) {
		updated = t6
	}
	return cached4, cached6, updated.Format(time.RFC3339), nil
}

func parseCIDRFile(body string, wantIPv6 bool) ([]string, error) {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !engine.IsCIDR(line) || strings.Contains(line, ":") != wantIPv6 {
			return nil, fmt.Errorf("无效网段 %q", line)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("文件为空")
	}
	return out, nil
}

func saveOfficialRangeCache(dataDir string, ipv4, ipv6 []string) error {
	if err := saveOfficialRangeCacheFamily(dataDir, officialIPv4File, ipv4); err != nil {
		return err
	}
	return saveOfficialRangeCacheFamily(dataDir, officialIPv6File, ipv6)
}

func saveOfficialRangeCacheFamily(dataDir, name string, cidrs []string) error {
	body := officialCacheMarker + "\n" + strings.Join(cidrs, "\n") + "\n"
	return os.WriteFile(filepath.Join(dataDir, name), []byte(body), 0644)
}

// fetchOfficialRanges 拉取 Cloudflare 官方 IPv4 网段（结构化 JSON）。
func fetchOfficialRanges(urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("未配置官方 IPv4 段获取地址")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for _, sourceURL := range urls {
		req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var parsed cfIPsAPIResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", sourceURL, resp.StatusCode)
			continue
		}
		if decodeErr != nil {
			lastErr = fmt.Errorf("%s: 响应不是预期 JSON: %w", sourceURL, decodeErr)
			continue
		}
		if !parsed.Success || len(parsed.Result.IPv4CIDRs) == 0 {
			lastErr = fmt.Errorf("%s: 响应中没有 IPv4 网段", sourceURL)
			continue
		}
		return parsed.Result.IPv4CIDRs, nil
	}
	return nil, lastErr
}

// fetchOfficialIPv6Ranges 依次拉取 IPv6 活跃 /48 列表（纯文本，每行一个 CIDR）。
func fetchOfficialIPv6Ranges(urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("未配置 IPv6 活跃段获取地址")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for _, sourceURL := range urls {
		req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s: HTTP %d", sourceURL, resp.StatusCode)
			continue
		}
		if readErr != nil {
			lastErr = fmt.Errorf("%s: 读取响应失败: %w", sourceURL, readErr)
			continue
		}
		cidrs, parseErr := parseCIDRFile(string(body), true)
		if parseErr != nil {
			lastErr = fmt.Errorf("%s: %w", sourceURL, parseErr)
			continue
		}
		if len(cidrs) == 0 {
			lastErr = fmt.Errorf("%s: 响应中没有 IPv6 网段", sourceURL)
			continue
		}
		return cidrs, nil
	}
	return nil, lastErr
}
