//go:build android

package platform

import (
	"context"
	"net"
	"time"
)

// InitDNS 在 Android 上切换为公共 DNS 直连。
// Android 应用子进程读不到系统 DNS 配置，纯 Go 解析器会回退到
// [::1]:53 导致 lookup 失败；这里用公共 DNS 兜底（UDP 失败自动切 TCP）。
func InitDNS() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			servers := []string{"119.29.29.29:53", "223.5.5.5:53", "8.8.8.8:53", "1.1.1.1:53"}
			var lastErr error
			for _, server := range servers {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				lastErr = err
				if network == "udp" {
					conn, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", server)
					if err == nil {
						return conn, nil
					}
					lastErr = err
				}
			}
			return nil, lastErr
		},
	}
}
