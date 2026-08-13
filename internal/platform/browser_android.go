//go:build android

package platform

// OpenBrowser 在 Android 上为空操作：页面由 WebView 加载。
func OpenBrowser(url string) {}
