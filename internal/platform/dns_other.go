//go:build !android

package platform

// InitDNS 在非 Android 平台无需特殊处理。
func InitDNS() {}
