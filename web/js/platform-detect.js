// 首屏尽早标记 Android WebView，避免 Android 布局在 CSS 加载前闪烁。
// 原为 index.html 的内联脚本；迁移为独立文件后，服务端可启用无
// 'unsafe-inline' 的严格 script-src CSP（见 internal/server/server.go）。
if (navigator.userAgent.includes('IPTestAndroid')) {
    document.documentElement.classList.add('android-app');
}
