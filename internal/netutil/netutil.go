// Package netutil provides shared HTTP/TLS helpers.
package netutil

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net/http"
	"sync"
)

// ca-certificates.crt is a Mozilla-compatible CA bundle used only when the
// operating system does not expose a usable certificate pool (notably on some
// Android and minimal Linux environments).
//
//go:embed ca-certificates.crt
var embeddedCA []byte

var rootsState struct {
	sync.Once
	pool *x509.CertPool
}

// RootCAPool returns the system certificate pool, falling back to the embedded
// bundle when the system pool is unavailable or empty.
func RootCAPool() *x509.CertPool {
	rootsState.Do(func() {
		pool, err := x509.SystemCertPool()
		if err == nil && pool != nil && len(pool.Subjects()) > 0 {
			rootsState.pool = pool
			return
		}
		fallback := x509.NewCertPool()
		if fallback.AppendCertsFromPEM(embeddedCA) {
			rootsState.pool = fallback
		}
	})
	return rootsState.pool
}

// Transport clones base and attaches the shared root CA pool while preserving
// custom dialers, timeouts and keep-alive settings. A nil base clones Go's
// default HTTP transport.
func Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.RootCAs = RootCAPool()
	return transport
}
