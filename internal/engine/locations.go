package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const locationsURL = "https://locations-adw.pages.dev/"

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
// 文件不存在时从远端下载并缓存到本地。返回 IATA -> Location 的映射。
func loadLocations(dataDir string) (map[string]Location, error) {
	path := filepath.Join(dataDir, "locations.json")

	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取 locations.json 失败: %w", err)
		}
		body, err = downloadFile(locationsURL, 30*time.Second)
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

func downloadFile(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
