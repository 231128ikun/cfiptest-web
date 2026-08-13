package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"iptest-web/internal/netutil"
)

const (
	httpClientTimeout = 20 * time.Second
	// MaxResponseBytes 限制错误响应读取，避免异常站点灌爆内存。
	maxResponseBytes = 64 * 1024
	// maxKeyBytes 与 edgeone-file-hub 的 MAX_KEY_LENGTH 保持一致。
	maxKeyBytes = 512
	// maxCloudAttempts EdgeOne 边缘防护会间歇性对写请求返回 HTTP 545
	// （"Error return from script"，与所用鉴权头无关），故对 5xx 自动重试。
	maxCloudAttempts = 5
)

// Channel 是云端存储渠道的上传契约。
// 未来新增渠道（CF KV / Gist 等）实现同一接口即可。
type Channel interface {
	// Test 校验连接（站点可达 + Token 有效），不应产生副作用文件。
	Test(ctx context.Context, cfg Config) error
	// Upload 上传 content 到 key（云端路径，如 iptest/final.txt），返回公开访问 URL。
	Upload(ctx context.Context, cfg Config, key string, content []byte) (string, error)
}

// NewChannel 按渠道返回实现；未知渠道返回错误。
func NewChannel(channel string) (Channel, error) {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case ChannelEdgeOne:
		return EdgeOneChannel{}, nil
	}
	return nil, fmt.Errorf("不支持的渠道 %q", channel)
}

var client = &http.Client{
	Timeout: httpClientTimeout,
	// EdgeOne 边缘防护拦截写请求返回 545 后，同一 keep-alive 连接会被“污染”，
	// 后续复用该连接的请求会持续 545；因此云端请求不复用连接，每次新建连接，
	// 再配合 5xx 自动重试（maxCloudAttempts），确保上传成功率。
	Transport: netutil.Transport(&http.Transport{DisableKeepAlives: true}),
}

// validateContent 校验内容大小（edgeone 单文件 ≤ 1MB）。
func validateContent(channel string, content []byte) error {
	info, ok := ChannelInfoByID(channel)
	if !ok {
		return fmt.Errorf("不支持的渠道 %q", channel)
	}
	if int64(len(content)) > info.MaxBytes {
		return fmt.Errorf("内容超过该渠道单文件上限 %d 字节（约 %.1f KB），当前 %d 字节",
			info.MaxBytes, float64(info.MaxBytes)/1024, len(content))
	}
	return nil
}

// NormalizeKey 校验并规范化云端路径：去首尾斜杠、禁止 . / .. / 空片段 / 控制字符。
func NormalizeKey(key string) (string, error) {
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" {
		return "", fmt.Errorf("云端路径不能为空")
	}
	if len(key) > maxKeyBytes {
		return "", fmt.Errorf("云端路径超过 %d 字符上限", maxKeyBytes)
	}
	if strings.Contains(key, "\\") || strings.Contains(key, "\x00") {
		return "", fmt.Errorf("云端路径包含非法字符")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("云端路径包含非法片段")
		}
	}
	for _, r := range key {
		if r < 32 {
			return "", fmt.Errorf("云端路径包含控制字符")
		}
	}
	return key, nil
}

// EdgeOneChannel 实现 edgeone-file-hub（Pages Blobs + 边缘函数）约定：
//   - 上传/覆盖：POST {base}/{key}，Authorization: Bearer {token}
//   - 校验 Token：POST {base}/api/auth，body {"token": "..."}（无副作用）
//   - 公开读取：GET {base}/{key}
//
// 实测 EdgeOne 边缘防护会间歇性拦截写请求并返回 HTTP 545（与鉴权头无关），
// 因此 Test / Upload 对 5xx 响应自动重试以提高成功率。
type EdgeOneChannel struct{}

func (EdgeOneChannel) Test(ctx context.Context, cfg Config) error {
	if cfg.BaseURL == "" {
		return fmt.Errorf("站点地址未配置")
	}
	body, _ := json.Marshal(map[string]string{"token": cfg.Token})
	target := strings.TrimRight(cfg.BaseURL, "/") + "/api/auth"
	var lastErr error
	retried := 0
	for attempt := 1; attempt <= maxCloudAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("连接站点失败: %w", err)
			if ctx.Err() != nil {
				return lastErr
			}
			if attempt < maxCloudAttempts {
				if err := sleepRetry(ctx, attempt); err != nil {
					return lastErr
				}
				retried++
				continue
			}
			break
		}
		status := resp.StatusCode
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if status == http.StatusOK {
			var out struct {
				OK bool
			}
			if json.Unmarshal(data, &out) == nil && out.OK {
				return nil
			}
		}
		if status == http.StatusUnauthorized {
			return fmt.Errorf("Token 无效（站点返回 401）")
		}
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		lastErr = fmt.Errorf("Token 校验失败（HTTP %d）：%s", status, truncateUTF8(msg, 140))
		if isRetryableStatus(status) && attempt < maxCloudAttempts {
			if err := sleepRetry(ctx, attempt); err != nil {
				return lastErr
			}
			retried++
			continue
		}
		break
	}
	if retried > 0 {
		return fmt.Errorf("%v（已重试 %d 次）", lastErr, retried)
	}
	return lastErr
}

func (EdgeOneChannel) Upload(ctx context.Context, cfg Config, key string, content []byte) (string, error) {
	key, err := NormalizeKey(key)
	if err != nil {
		return "", err
	}
	if err := validateContent(cfg.Channel, content); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return "", fmt.Errorf("未配置 Token")
	}
	target := strings.TrimRight(cfg.BaseURL, "/") + "/" + key
	var lastErr error
	retried := 0
	for attempt := 1; attempt <= maxCloudAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(content))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("X-Source", "iptest-web")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("上传失败: %w", err)
			if ctx.Err() != nil {
				return "", lastErr
			}
			if attempt < maxCloudAttempts {
				if err := sleepRetry(ctx, attempt); err != nil {
					return "", lastErr
				}
				retried++
				continue
			}
			break
		}
		status := resp.StatusCode
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			return strings.TrimRight(cfg.BaseURL, "/") + "/" + encodeKeyPath(key), nil
		}
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		switch status {
		case http.StatusUnauthorized:
			return "", fmt.Errorf("上传被拒绝：Token 无效（401）")
		case http.StatusRequestEntityTooLarge:
			return "", fmt.Errorf("上传被拒绝：超过该渠道单文件大小上限")
		}
		lastErr = fmt.Errorf("上传失败（HTTP %d）：%s", status, truncateUTF8(msg, 160))
		if isRetryableStatus(status) && attempt < maxCloudAttempts {
			if err := sleepRetry(ctx, attempt); err != nil {
				return "", lastErr
			}
			retried++
			continue
		}
		break
	}
	if retried > 0 {
		return "", fmt.Errorf("%v（已重试 %d 次）", lastErr, retried)
	}
	return "", lastErr
}

// isRetryableStatus 判断是否需要重试。EdgeOne 边缘防护误拦截写请求时返回
// HTTP 545（"Error return from script"），属于 5xx 范围，连接类错误也一并重试。
func isRetryableStatus(code int) bool {
	return code >= 500
}

// sleepRetry 在两次重试之间退避等待，可被 ctx 取消打断。
func sleepRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt) * 300 * time.Millisecond
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func encodeKeyPath(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func truncateUTF8(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i >= max {
			break
		}
		cut = i + 1
	}
	return s[:cut] + "…"
}
