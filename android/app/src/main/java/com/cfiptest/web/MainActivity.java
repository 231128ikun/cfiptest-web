package com.cfiptest.web;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.JavascriptInterface;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.ProgressBar;
import android.widget.TextView;
import android.widget.Toast;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.io.OutputStreamWriter;
import java.io.PrintWriter;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

public final class MainActivity extends Activity {
    private static final String BASE_URL = "http://127.0.0.1:18080";
    private static final int FILE_CHOOSER_REQUEST = 1001;
    private static final int CREATE_DOCUMENT_REQUEST = 1002;
    private static final long STARTUP_TIMEOUT_MS = 90_000;

    private final Handler mainHandler = new Handler(Looper.getMainLooper());
    private final ExecutorService ioExecutor = Executors.newCachedThreadPool();

    private WebView webView;
    private TextView statusView;
    private ProgressBar progressBar;
    private Process backendProcess;
    private ValueCallback<Uri[]> fileChooserCallback;
    private PendingFile pendingFile;
    private long startupDeadline;
    private volatile boolean destroyed;
    private volatile File backendLogFile;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        installCrashHandler();
        try {
            buildLayout();
            configureWebView();
            startBackend();
        } catch (Throwable error) {
            writeCrash("onCreate", error);
            showStartupError("初始化失败：" + error.getMessage());
        }
    }

    private void installCrashHandler() {
        final Thread.UncaughtExceptionHandler previous = Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler((thread, throwable) -> {
            try {
                writeCrash("uncaught@" + thread.getName(), throwable);
            } catch (Throwable ignored) {
            }
            if (previous != null) {
                previous.uncaughtException(thread, throwable);
            } else {
                android.os.Process.killProcess(android.os.Process.myPid());
            }
        });
    }

    private void writeCrash(String tag, Throwable throwable) {
        File log = new File(getLogDir(), "crash.log");
        try (OutputStream out = new FileOutputStream(log, true);
             PrintWriter writer = new PrintWriter(new OutputStreamWriter(out, StandardCharsets.UTF_8))) {
            writer.println("==== " + tag + " @ " + new java.util.Date() + " ====");
            throwable.printStackTrace(writer);
            writer.flush();
        } catch (IOException ignored) {
        }
    }

    private File getLogDir() {
        File external = getExternalFilesDir(null);
        return external != null ? external : getFilesDir();
    }

    private void buildLayout() {
        FrameLayout root = new FrameLayout(this);
        webView = new WebView(this);
        webView.setVisibility(View.INVISIBLE);
        root.addView(webView, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        FrameLayout loading = new FrameLayout(this);
        loading.setId(View.generateViewId());
        progressBar = new ProgressBar(this);
        FrameLayout.LayoutParams progressParams = new FrameLayout.LayoutParams(64, 64);
        progressParams.gravity = Gravity.CENTER;
        progressParams.bottomMargin = 72;
        loading.addView(progressBar, progressParams);

        statusView = new TextView(this);
        statusView.setText("正在启动本地服务…");
        statusView.setTextSize(16);
        statusView.setTextColor(Color.DKGRAY);
        statusView.setGravity(Gravity.CENTER);
        FrameLayout.LayoutParams statusParams = new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        statusParams.gravity = Gravity.CENTER;
        statusParams.topMargin = 80;
        statusParams.leftMargin = 24;
        statusParams.rightMargin = 24;
        loading.addView(statusView, statusParams);
        root.addView(loading, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        setContentView(root);
        statusView.setTag(loading);
    }

    private void configureWebView() {
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(true);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setMediaPlaybackRequiresUserGesture(true);
        settings.setBuiltInZoomControls(false);
        settings.setDisplayZoomControls(false);

        webView.addJavascriptInterface(new AndroidBridge(), "iptestAndroid");
        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                return openExternalIfNeeded(request.getUrl());
            }

            @Override
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return openExternalIfNeeded(Uri.parse(url));
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                if (isLocalUrl(Uri.parse(url))) {
                    showWebView();
                }
            }
        });
        webView.setDownloadListener((url, userAgent, contentDisposition, mimeType, contentLength) -> {
            Uri uri = Uri.parse(url);
            if (isLocalUrl(uri)) {
                downloadLocalFile(url, contentDisposition, mimeType);
            } else {
                startActivity(new Intent(Intent.ACTION_VIEW, uri));
            }
        });
        webView.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback,
                                             FileChooserParams params) {
                if (fileChooserCallback != null) {
                    fileChooserCallback.onReceiveValue(null);
                }
                fileChooserCallback = callback;
                Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT);
                intent.addCategory(Intent.CATEGORY_OPENABLE);
                intent.setType("text/*");
                intent.putExtra(Intent.EXTRA_MIME_TYPES, new String[]{
                        "text/plain", "text/csv", "application/csv", "application/octet-stream"
                });
                startActivityForResult(intent, FILE_CHOOSER_REQUEST);
                return true;
            }
        });
    }

    private void startBackend() {
        ioExecutor.execute(() -> {
            try {
                File nativeBinary = new File(getApplicationInfo().nativeLibraryDir, "libiptest.so");
                if (!nativeBinary.exists()) {
                    throw new IOException("未找到后端程序: " + nativeBinary.getAbsolutePath());
                }
                File dataDir = new File(getFilesDir(), "data");
                if (!dataDir.exists() && !dataDir.mkdirs()) {
                    throw new IOException("无法创建应用数据目录");
                }
                backendLogFile = new File(getLogDir(), "backend.log");
                Process process = startProcess(nativeBinary, dataDir);
                backendProcess = process;
                if (destroyed) {
                    process.destroy();
                    return;
                }
                drainProcessOutput(process.getInputStream());
                startupDeadline = System.currentTimeMillis() + STARTUP_TIMEOUT_MS;
                mainHandler.post(this::pollBackend);
            } catch (Throwable error) {
                writeCrash("startBackend", error);
                showStartupError("本地服务启动失败：" + error.getMessage());
            }
        });
    }

    private Process startProcess(File binary, File dataDir) throws IOException {
        String[] args = {
                binary.getAbsolutePath(),
                "-port", "18080",
                "-no-browser",
                "-strict-port",
                "-data-dir", dataDir.getAbsolutePath()
        };
        try {
            ProcessBuilder builder = new ProcessBuilder(args);
            builder.directory(getFilesDir());
            builder.redirectErrorStream(true);
            return builder.start();
        } catch (IOException directError) {
            // Android W^X: 应用私有目录禁止直接执行，nativeLibraryDir 也受限时改走系统 linker 通道。
            writeCrash("directExecFailed", directError);
            File linker = new File("/system/bin/linker64");
            if (!linker.exists()) {
                linker = new File("/system/bin/linker");
            }
            if (!linker.exists()) {
                throw directError;
            }
            String[] linkerArgs = new String[args.length + 1];
            linkerArgs[0] = linker.getAbsolutePath();
            System.arraycopy(args, 0, linkerArgs, 1, args.length);
            ProcessBuilder builder = new ProcessBuilder(linkerArgs);
            builder.directory(getFilesDir());
            builder.redirectErrorStream(true);
            return builder.start();
        }
    }
    private void drainProcessOutput(InputStream input) {
        ioExecutor.execute(() -> {
            try (BufferedReader reader = new BufferedReader(new InputStreamReader(input, StandardCharsets.UTF_8));
                 OutputStream out = new FileOutputStream(backendLogFile, false);
                 PrintWriter writer = new PrintWriter(new OutputStreamWriter(out, StandardCharsets.UTF_8))) {
                String line;
                while ((line = reader.readLine()) != null) {
                    writer.println(line);
                    writer.flush();
                }
            } catch (IOException ignored) {
            }
        });
    }

    private void pollBackend() {
        if (destroyed) {
            return;
        }
        ioExecutor.execute(() -> {
            if (destroyed) {
                return;
            }
            boolean ready = false;
            try {
                HttpURLConnection connection = (HttpURLConnection) new URL(BASE_URL + "/api/config").openConnection();
                connection.setConnectTimeout(800);
                connection.setReadTimeout(800);
                connection.setUseCaches(false);
                ready = connection.getResponseCode() == 200;
                connection.disconnect();
            } catch (IOException ignored) {
            }

            if (destroyed) {
                return;
            }
            if (ready) {
                mainHandler.post(() -> {
                    if (!destroyed) {
                        webView.loadUrl(BASE_URL + "/");
                    }
                });
            } else if (backendProcess != null && !backendProcess.isAlive()) {
                showStartupError("本地服务已意外退出，请重新打开应用。\n日志目录: Android/data/com.cfiptest.web/files/");
            } else if (System.currentTimeMillis() >= startupDeadline) {
                showStartupError("本地服务启动超时，请重试或检查设备安全设置。\n日志目录: Android/data/com.cfiptest.web/files/");
            } else {
                mainHandler.postDelayed(this::pollBackend, 250);
            }
        });
    }

    private void showWebView() {
        webView.setVisibility(View.VISIBLE);
        Object loading = statusView.getTag();
        if (loading instanceof View) {
            ((View) loading).setVisibility(View.GONE);
        }
    }

    private void showStartupError(String message) {
        if (destroyed) {
            return;
        }
        mainHandler.post(() -> {
            if (destroyed) {
                return;
            }
            progressBar.setVisibility(View.GONE);
            statusView.setText(message);
            statusView.setTextColor(Color.rgb(185, 28, 28));
        });
    }

    private boolean openExternalIfNeeded(Uri uri) {
        if (isLocalUrl(uri)) {
            return false;
        }
        String scheme = uri.getScheme();
        if ("http".equalsIgnoreCase(scheme) || "https".equalsIgnoreCase(scheme)) {
            startActivity(new Intent(Intent.ACTION_VIEW, uri));
        }
        return true;
    }

    private boolean isLocalUrl(Uri uri) {
        String host = uri.getHost();
        boolean localHost = "127.0.0.1".equals(host) || "localhost".equalsIgnoreCase(host);
        return localHost && "http".equalsIgnoreCase(uri.getScheme()) && uri.getPort() == 18080;
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == FILE_CHOOSER_REQUEST) {
            Uri[] result = null;
            if (resultCode == RESULT_OK && data != null && data.getData() != null) {
                result = new Uri[]{data.getData()};
            }
            if (fileChooserCallback != null) {
                fileChooserCallback.onReceiveValue(result);
                fileChooserCallback = null;
            }
            return;
        }
        if (requestCode == CREATE_DOCUMENT_REQUEST) {
            PendingFile file = pendingFile;
            pendingFile = null;
            if (resultCode == RESULT_OK && data != null && data.getData() != null && file != null) {
                writeDocument(data.getData(), file);
            }
        }
    }

    private void downloadLocalFile(String url, String contentDisposition, String mimeType) {
        ioExecutor.execute(() -> {
            try {
                HttpURLConnection connection = (HttpURLConnection) new URL(url).openConnection();
                connection.setConnectTimeout(2_000);
                connection.setReadTimeout(10_000);
                connection.setUseCaches(false);
                int status = connection.getResponseCode();
                if (status != 200) {
                    throw new IOException("HTTP " + status);
                }
                String responseType = connection.getContentType();
                String type = responseType == null || responseType.trim().isEmpty()
                        ? (mimeType == null || mimeType.trim().isEmpty() ? "text/plain" : mimeType)
                        : responseType;
                String fileName = fileNameFromDisposition(contentDisposition);
                if (fileName == null) {
                    fileName = fileNameFromDisposition(connection.getHeaderField("Content-Disposition"));
                }
                if (fileName == null) {
                    fileName = "iptest-output" + (type.toLowerCase().contains("csv") ? ".csv" : ".txt");
                }
                StringBuilder body = new StringBuilder();
                try (BufferedReader reader = new BufferedReader(new InputStreamReader(
                        connection.getInputStream(), StandardCharsets.UTF_8))) {
                    char[] buffer = new char[8192];
                    int read;
                    while ((read = reader.read(buffer)) >= 0) {
                        body.append(buffer, 0, read);
                    }
                } finally {
                    connection.disconnect();
                }
                PendingFile file = new PendingFile(sanitizeFileName(fileName), body.toString(), type.split(";", 2)[0]);
                mainHandler.post(() -> createDocument(file));
            } catch (IOException error) {
                mainHandler.post(() -> Toast.makeText(this,
                        "读取输出失败：" + error.getMessage(), Toast.LENGTH_LONG).show());
            }
        });
    }

    private static String fileNameFromDisposition(String disposition) {
        if (disposition == null) {
            return null;
        }
        for (String part : disposition.split(";")) {
            String value = part.trim();
            if (value.toLowerCase().startsWith("filename=")) {
                value = value.substring("filename=".length()).trim();
                if (value.startsWith("\"") && value.endsWith("\"") && value.length() >= 2) {
                    value = value.substring(1, value.length() - 1);
                }
                return value.isEmpty() ? null : value;
            }
        }
        return null;
    }

    private void createDocument(PendingFile file) {
        pendingFile = file;
        Intent intent = new Intent(Intent.ACTION_CREATE_DOCUMENT);
        intent.addCategory(Intent.CATEGORY_OPENABLE);
        intent.setType(file.mimeType);
        intent.putExtra(Intent.EXTRA_TITLE, file.fileName);
        startActivityForResult(intent, CREATE_DOCUMENT_REQUEST);
    }

    private void writeDocument(Uri uri, PendingFile file) {
        ioExecutor.execute(() -> {
            try (OutputStream output = getContentResolver().openOutputStream(uri, "w")) {
                if (output == null) {
                    throw new IOException("无法打开目标文件");
                }
                output.write(file.content.getBytes(StandardCharsets.UTF_8));
                output.flush();
                mainHandler.post(() -> Toast.makeText(this, "文件已保存", Toast.LENGTH_SHORT).show());
            } catch (IOException error) {
                mainHandler.post(() -> Toast.makeText(this,
                        "保存失败：" + error.getMessage(), Toast.LENGTH_LONG).show());
            }
        });
    }

    private void stopBackend() {
        Process process = backendProcess;
        ioExecutor.execute(() -> {
            try {
                HttpURLConnection connection = (HttpURLConnection) new URL(BASE_URL + "/api/shutdown").openConnection();
                connection.setRequestMethod("POST");
                connection.setConnectTimeout(500);
                connection.setReadTimeout(500);
                connection.getResponseCode();
                connection.disconnect();
            } catch (IOException ignored) {
            }
            if (process == null) {
                return;
            }
            try {
                if (!process.waitFor(1, TimeUnit.SECONDS)) {
                    process.destroy();
                }
            } catch (InterruptedException ignored) {
                Thread.currentThread().interrupt();
                process.destroy();
            }
        });
    }

    @Override
    protected void onDestroy() {
        destroyed = true;
        stopBackend();
        if (fileChooserCallback != null) {
            fileChooserCallback.onReceiveValue(null);
            fileChooserCallback = null;
        }
        mainHandler.removeCallbacksAndMessages(null);
        if (webView != null) {
            webView.removeJavascriptInterface("iptestAndroid");
            webView.destroy();
        }
        ioExecutor.shutdown();
        super.onDestroy();
    }

    private final class AndroidBridge {
        @JavascriptInterface
        public void saveTextFile(String fileName, String content, String mimeType) {
            String safeName = sanitizeFileName(fileName);
            String safeMime = mimeType == null || mimeType.trim().isEmpty()
                    ? "text/plain" : mimeType.split(";", 2)[0].trim();
            mainHandler.post(() -> createDocument(new PendingFile(safeName,
                    content == null ? "" : content, safeMime)));
        }

        @JavascriptInterface
        public void copyText(String text) {
            mainHandler.post(() -> {
                ClipboardManager clipboard = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
                clipboard.setPrimaryClip(ClipData.newPlainText("IPTest-WEB", text == null ? "" : text));
                Toast.makeText(MainActivity.this, "已复制到剪贴板", Toast.LENGTH_SHORT).show();
            });
        }

        @JavascriptInterface
        public void closeApp() {
            mainHandler.post(MainActivity.this::finishAndRemoveTask);
        }
    }

    private static String sanitizeFileName(String input) {
        String name = input == null ? "iptest-result.txt" : input.trim();
        name = name.replaceAll("[\\\\/:*?\"<>|]", "_");
        return name.isEmpty() ? "iptest-result.txt" : name;
    }

    private static final class PendingFile {
        final String fileName;
        final String content;
        final String mimeType;

        PendingFile(String fileName, String content, String mimeType) {
            this.fileName = fileName;
            this.content = content;
            this.mimeType = mimeType;
        }
    }
}