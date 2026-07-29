package engine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const asnDatabaseURL = "https://jsd.onmicrosoft.cn/gh/seketiti/GeoLiet2@release/GeoLite2-ASN.mmdb"
const asnDatabaseName = "GeoLite2-ASN.mmdb"

// loadASN 打开 dataDir 下的 GeoLite2-ASN.mmdb；文件不存在时先下载。
// 加载失败返回 (nil, err)，调用方可降级运行（ASN 信息留空）。
func loadASN(dataDir string) (*geoip2.Reader, error) {
	path := filepath.Join(dataDir, asnDatabaseName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		body, derr := downloadFile(asnDatabaseURL, 120*time.Second)
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

// lookupASN 查询 IP 的 ASN 号码与组织名；db 为 nil 或查询失败时返回零值。
func lookupASN(db *geoip2.Reader, ipStr string) (uint, string) {
	if db == nil || ipStr == "" {
		return 0, ""
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0, ""
	}
	record, err := db.ASN(ip)
	if err != nil {
		return 0, ""
	}
	return record.AutonomousSystemNumber, record.AutonomousSystemOrganization
}
