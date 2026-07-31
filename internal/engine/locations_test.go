package engine

import (
	"net"
	"testing"
)

func TestBlockedAddressCoverage(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "240.0.0.1", "::1", "fd00::1"}
	for _, raw := range blocked {
		if !isBlockedIP(net.ParseIP(raw)) {
			t.Errorf("应拦截 %s", raw)
		}
	}
	if isBlockedIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("公网地址 1.1.1.1 不应被拦截")
	}
}
