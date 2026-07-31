package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultLocationSources 是地理位置数据的默认下载源，按顺序尝试。
// config 包以此为默认值，可在本地 data/config.json 中覆盖。
var DefaultLocationSources = []string{
	"https://locations-adw.pages.dev/",
	"https://speed.cloudflare.com/locations",
}

// Location 对应 locations.json 中一个 IATA 三字码的地理位置记录。
type Location struct {
	Iata     string  `json:"iata"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Cca2     string  `json:"cca2"`
	Region   string  `json:"region"`
	City     string  `json:"city"`
	RegionZh string  `json:"region_zh"`
	Country  string  `json:"country"`
	CityZh   string  `json:"city_zh"`
	Emoji    string  `json:"emoji"`
}

// loadLocations 从 dataDir/locations.json 加载位置数据；
// 文件不存在时依次尝试 urls 下载并缓存到本地。返回 IATA -> Location 的映射。
//
// 本地文件优先：只要 dataDir 下已有 locations.json 就绝不联网，
// 用户因此可以自行放置或替换这份数据。
func loadLocations(dataDir string, urls []string) (map[string]Location, error) {
	path := filepath.Join(dataDir, "locations.json")

	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取 locations.json 失败: %w", err)
		}
		body, err = downloadFirst(urls, 30*time.Second)
		if err != nil {
			return nil, fmt.Errorf("下载 locations.json 失败: %w", err)
		}
		if werr := os.WriteFile(path, body, 0644); werr != nil {
			// 数据已拿到，缓存失败不致命
			fmt.Printf("警告: 无法缓存 locations.json: %v\n", werr)
		}
	}

	var list []Location
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("解析 locations.json 失败: %w", err)
	}

	m := make(map[string]Location, len(list))
	for _, loc := range list {
		m[loc.Iata] = loc
	}
	return m, nil
}

// SafeDialContext 在建立连接前校验目标必须是公网地址，阻断内网/回环/链路本地等保留段。
// 校验放在 Control 里而不是提前做一次 DNS 查询，是为了挡住 DNS rebinding：
// 事前解析到公网 IP、真正连接时再解析到内网的攻击，只在拨号这一刻检查才拦得住。
// 重定向后的每一跳也会走到这里，下载源与服务端远程导入共用同一套防护。
func SafeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || isBlockedIP(ip) {
				return ErrBlockedAddr
			}
			return nil
		},
	}
	return dialer.DialContext(ctx, network, address)
}

// ErrBlockedAddr 表示目标解析到了不允许访问的内网地址。
var ErrBlockedAddr = fmt.Errorf("目标解析到内网地址，已拒绝访问")

// isBlockedIP 判断 IP 是否属于禁止访问的范围；用于下载源与远程导入的公网校验。
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// 云厂商 metadata 端点。169.254.169.254 已被上面的链路本地判断覆盖，
	// 这里显式列出 IPv6 形式（fd00:ec2::254 属于 IsPrivate 之外，保留意图可读）。
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

// downloadFirst 依次尝试 urls，返回第一个成功的响应体。
// 全部失败时返回聚合了每个源失败原因的错误，便于用户判断是网络问题还是源失效。
func downloadFirst(urls []string, timeout time.Duration) ([]byte, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("没有可用的下载源")
	}
	var errs []string
	for i, url := range urls {
		body, err := downloadFile(url, timeout)
		if err == nil {
			if i > 0 {
				fmt.Printf("提示: 已从备用源下载（%s）\n", url)
			}
			return body, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", url, err))
	}
	return nil, fmt.Errorf("全部 %d 个源均失败:\n  %s", len(urls), strings.Join(errs, "\n  "))
}

func downloadFile(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 导入端点与外下载共用安全拨号：远程 IP 列表和位置/ASN 数据
			// 都必须是公网地址，防止恶意源把本地服务变成内网探测跳板。
			DialContext:         SafeDialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// 部分源（如 speed.cloudflare.com）对缺省 UA 返回 403
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
