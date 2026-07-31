package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/geoip2-golang"
)

// DefaultASNSources 是 ASN 数据库的默认下载源，按顺序尝试。
// 两个都是第三方镜像（MaxMind 官方下载需注册账号拿 license key），
// 因此保留多个源；用户也可自行把 GeoLite2-ASN.mmdb 放到 data 目录绕过下载。
var DefaultASNSources = []string{
	"https://jsd.onmicrosoft.cn/gh/seketiti/GeoLiet2@release/GeoLite2-ASN.mmdb",
	"https://cdn.jsdelivr.net/gh/P3TERX/GeoLite.mmdb@download/GeoLite2-ASN.mmdb",
}

const asnDatabaseName = "GeoLite2-ASN.mmdb"

// loadASN 打开 dataDir 下的 GeoLite2-ASN.mmdb；文件不存在时依次尝试 urls 下载。
// 加载失败返回 (nil, err)，调用方可降级运行（ASN 信息留空）。
func loadASN(dataDir string, urls []string) (*geoip2.Reader, error) {
	path := filepath.Join(dataDir, asnDatabaseName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		body, derr := downloadFirst(urls, 120*time.Second)
		if derr != nil {
			return nil, fmt.Errorf("下载 ASN 数据库失败: %w", derr)
		}
		if werr := os.WriteFile(path, body, 0644); werr != nil {
			return nil, fmt.Errorf("保存 ASN 数据库失败: %w", werr)
		}
	}

	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 ASN 数据库失败: %w", err)
	}
	return db, nil
}
