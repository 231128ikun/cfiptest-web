package netutil

import (
	"net/http"
	"testing"
)

func TestRootCAPoolIsUsable(t *testing.T) {
	pool := RootCAPool()
	if pool == nil || len(pool.Subjects()) == 0 {
		t.Fatal("root CA pool is empty")
	}
}

func TestTransportPreservesSettings(t *testing.T) {
	base := &http.Transport{DisableKeepAlives: true}
	got := Transport(base)
	if got == base {
		t.Fatal("transport was not cloned")
	}
	if !got.DisableKeepAlives || got.TLSClientConfig == nil || got.TLSClientConfig.RootCAs == nil {
		t.Fatalf("unexpected transport: %#v", got)
	}
}
