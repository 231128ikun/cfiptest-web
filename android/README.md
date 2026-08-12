# Android 客户端

此目录是独立的 Android WebView 壳。应用启动后执行随 APK 打包的 ARM64 Go 后端，后端只监听 `127.0.0.1:18080`，WebView 再加载本地页面。

## 支持范围

- Android 8.0（API 26）及以上
- 仅 ARM64 (`arm64-v8a`)
- 不作为后台常驻服务；离开并销毁 Activity 时后端进程结束
- 应用数据保存在 Android 私有目录的 `files/data/` 下
- 首次启动时，如位置或 ASN 数据缺失，后端会在应用私有目录下载并缓存

## 本地构建

需要 Java 17、Android SDK（API 35）、Gradle 8.10.2 和 Go。

在仓库根目录先构建后端：

```bash
mkdir -p android/app/src/main/jniLibs/arm64-v8a
GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" \
  -o android/app/src/main/jniLibs/arm64-v8a/libiptest.so .
```

再构建 APK：

```bash
cd android
gradle assembleDebug --no-daemon
```

输出：`android/app/build/outputs/apk/debug/app-debug.apk`。

## GitHub Actions

`.github/workflows/android.yml` 会自动交叉编译 Go 后端、构建 APK，并校验 APK 内含 `lib/arm64-v8a/libiptest.so`。

普通分支、PR 或手动构建默认生成 debug APK。Tag 构建或手动选择 release 时，需要以下 Secrets：

- `ANDROID_KEYSTORE_BASE64`
- `ANDROID_KEYSTORE_PASSWORD`
- `ANDROID_KEY_ALIAS`
- `ANDROID_KEY_PASSWORD`

`ANDROID_KEYSTORE_BASE64` 是 keystore 文件的 Base64 内容。