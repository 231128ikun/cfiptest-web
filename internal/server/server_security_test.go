package server

import "testing"

func TestIsLocalHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1:18080", "localhost:18080", "[::1]:18080"} {
		if !isLocalHost(host) {
			t.Fatalf("本机 Host 被拒绝: %s", host)
		}
	}
	for _, host := range []string{"example.com", "192.168.1.2:18080", "evil.test:18080"} {
		if isLocalHost(host) {
			t.Fatalf("非本机 Host 被允许: %s", host)
		}
	}
}
