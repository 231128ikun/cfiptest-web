package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultLocationSources 是地理位置数据的默认下载源，按顺序尝试。
// config 包以此为默认值，用户可在 config.json 中覆盖。
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
	client := &http.Client{Timeout: timeout}
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
