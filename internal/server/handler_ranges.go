package server

import (
	"encoding/json"
	"fmt"
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
	writeJSON(w, http.StatusOK, out)
}

// officialRanges reuses the two local text caches by default. A refresh request
// downloads the current official list and replaces both files; a failed refresh
// keeps serving the last known-good cache.
func (s *Server) officialRanges(refresh bool) *officialRangesResponse {
	s.rangesMu.Lock()
	defer s.rangesMu.Unlock()

	if !refresh && s.rangesCache != nil {
		return s.rangesCache
	}
	if !refresh {
		if ipv4, ipv6, updatedAt, err := loadOfficialRangeCache(s.dataDir); err == nil {
			s.rangesCache = &officialRangesResponse{IPv4: ipv4, IPv6: ipv6, Source: "cache", UpdatedAt: updatedAt, Ports: cfSupportedPorts}
			return s.rangesCache
		}
	}

	s.configMu.RLock()
	officialSources := append([]string(nil), s.cfg.Sources.OfficialRanges...)
	s.configMu.RUnlock()
	ipv4, ipv6, err := fetchOfficialRanges(officialSources)
	if err == nil && len(ipv4) > 0 {
		warning := ""
		if cacheErr := saveOfficialRangeCache(s.dataDir, ipv4, ipv6); cacheErr != nil {
			warning = fmt.Sprintf("官方 IP 段已更新，但写入本地缓存失败：%v", cacheErr)
		}
		s.rangesCache = &officialRangesResponse{IPv4: ipv4, IPv6: ipv6, Source: "remote", UpdatedAt: time.Now().Format(time.RFC3339), Ports: cfSupportedPorts, Warning: warning}
		return s.rangesCache
	}

	if cached4, cached6, updatedAt, cacheErr := loadOfficialRangeCache(s.dataDir); cacheErr == nil {
		s.rangesCache = &officialRangesResponse{IPv4: cached4, IPv6: cached6, Source: "cache", UpdatedAt: updatedAt, Ports: cfSupportedPorts, Warning: fmt.Sprintf("远程更新失败（%v），继续使用本地缓存", err)}
		return s.rangesCache
	}

	s.rangesCache = &officialRangesResponse{IPv4: engine.BuiltinCFRanges.IPv4, IPv6: engine.BuiltinCFRanges.IPv6, Source: "builtin", Ports: cfSupportedPorts, Warning: fmt.Sprintf("远程获取失败（%v），已使用内置兜底数据", err)}
	return s.rangesCache
}

func loadOfficialRangeCache(dataDir string) ([]string, []string, string, error) {
	v4Path := filepath.Join(dataDir, officialIPv4File)
	v6Path := filepath.Join(dataDir, officialIPv6File)
	v4Body, err := os.ReadFile(v4Path)
	if err != nil {
		return nil, nil, "", err
	}
	v6Body, err := os.ReadFile(v6Path)
	if err != nil {
		return nil, nil, "", err
	}
	ipv4, err := parseCIDRFile(string(v4Body), false)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", officialIPv4File, err)
	}
	ipv6, err := parseCIDRFile(string(v6Body), true)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", officialIPv6File, err)
	}
	stat4, _ := os.Stat(v4Path)
	stat6, _ := os.Stat(v6Path)
	updated := time.Time{}
	if stat4 != nil {
		updated = stat4.ModTime()
	}
	if stat6 != nil && stat6.ModTime().After(updated) {
		updated = stat6.ModTime()
	}
	return ipv4, ipv6, updated.Format(time.RFC3339), nil
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
	if err := os.WriteFile(filepath.Join(dataDir, officialIPv4File), []byte(strings.Join(ipv4, "\n")+"\n"), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, officialIPv6File), []byte(strings.Join(ipv6, "\n")+"\n"), 0644)
}

func fetchOfficialRanges(urls []string) ([]string, []string, error) {
	if len(urls) == 0 {
		return nil, nil, fmt.Errorf("未配置官方 IP 段获取地址")
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
			lastErr = fmt.Errorf("%s: 响应中没有 IP 段", sourceURL)
			continue
		}
		return parsed.Result.IPv4CIDRs, parsed.Result.IPv6CIDRs, nil
	}
	return nil, nil, lastErr
}
