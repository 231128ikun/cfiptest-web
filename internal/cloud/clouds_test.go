package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func mustConfig(t *testing.T, name string) Config {
	t.Helper()
	c := Config{Name: name, Channel: ChannelEdgeOne, BaseURL: "https://files.example.com", Token: "secret-token-123456"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStoreCRUDAndMasking(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if _, err := store.Create(Config{Name: "", Channel: ChannelEdgeOne, BaseURL: "https://a.com", Token: "x"}); err == nil {
		t.Fatal("空名称应报错")
	}
	if _, err := store.Create(Config{Name: "bad", Channel: "unknown", BaseURL: "https://a.com", Token: "x"}); err == nil {
		t.Fatal("未知渠道应报错")
	}
	if _, err := store.Create(Config{Name: "bad", Channel: ChannelEdgeOne, BaseURL: "not-a-url", Token: "x"}); err == nil {
		t.Fatal("非法站点应报错")
	}

	cfg, err := store.Create(mustConfig(t, "main"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID == "" || cfg.Token != "secret-token-123456" {
		t.Fatalf("创建结果异常: %+v", cfg)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "main" {
		t.Fatalf("列表异常: %+v", list)
	}
	if list[0].Token == "secret-token-123456" || !strings.Contains(list[0].Token, "***") {
		t.Fatalf("Token 未脱敏: %q", list[0].Token)
	}

	// 更新：不传 Token 保留原值；改名 + 换 Token 生效
	updated, err := store.Update(cfg.ID, Config{Name: "renamed", Channel: ChannelEdgeOne, BaseURL: "https://files2.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.Token != cfg.Token {
		t.Fatalf("更新异常: %+v", updated)
	}
	updated, err = store.Update(cfg.ID, Config{Token: "new-secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Token != "new-secret-token" || updated.Name != "renamed" {
		t.Fatalf("更新 Token 异常: %+v", updated)
	}

	// 持久化：新 Store 从同一目录读取
	reloaded := NewStore(dir)
	got, ok, err := reloaded.Get(cfg.ID)
	if err != nil || !ok {
		t.Fatalf("重载失败: ok=%v err=%v", ok, err)
	}
	if got.Name != "renamed" || got.Token != "new-secret-token" {
		t.Fatalf("重载内容异常: %+v", got)
	}

	if err := reloaded.Delete(cfg.ID); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Delete(cfg.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
	list, _ = reloaded.List()
	if len(list) != 0 {
		t.Fatalf("删除后应清空: %+v", list)
	}
	if body, err := filepath.Glob(filepath.Join(dir, CloudsFile)); err != nil || len(body) != 1 {
		t.Fatalf("配置文件未落盘: %v", body)
	}
}

func TestEdgeOneUploadAndTest(t *testing.T) {
	var sawAuth, sawSource string
	var sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth" {
			var in struct {
				Token string `json:"token"`
			}
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Token != "secret-token-123456" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
			return
		}
		sawAuth = r.Header.Get("Authorization")
		sawSource = r.Header.Get("X-Source")
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		sawBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cfg := Config{Name: "main", Channel: ChannelEdgeOne, BaseURL: srv.URL, Token: "secret-token-123456"}
	ch, err := NewChannel(ChannelEdgeOne)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Test(context.Background(), cfg); err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	bad := cfg
	bad.Token = "wrong"
	if err := ch.Test(context.Background(), bad); err == nil {
		t.Fatal("错误 Token 应测试失败")
	}

	url, err := ch.Upload(context.Background(), cfg, "iptest/final.txt", []byte("1.2.3.4:443\n"))
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if url != srv.URL+"/iptest/final.txt" {
		t.Fatalf("公开 URL 异常: %s", url)
	}
	if sawAuth != "Bearer secret-token-123456" {
		t.Fatalf("鉴权头异常: %q", sawAuth)
	}
	if sawSource != "iptest-web" {
		t.Fatalf("来源头异常: %q", sawSource)
	}
	if sawBody != "1.2.3.4:443\n" {
		t.Fatalf("上传内容异常: %q", sawBody)
	}

	// 1MB 上限
	big := make([]byte, 1024*1024+1)
	if _, err := ch.Upload(context.Background(), cfg, "big.txt", big); err == nil {
		t.Fatal("超过 1MB 应拒绝")
	}
	// 非法 key
	if _, err := ch.Upload(context.Background(), cfg, "../etc/passwd", []byte("x")); err == nil {
		t.Fatal("退层 key 应拒绝")
	}
	if _, err := ch.Upload(context.Background(), cfg, "a\\b", []byte("x")); err == nil {
		t.Fatal("反斜杠 key 应拒绝")
	}
}

func TestEdgeOneUploadRetryOn545(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(545)
			w.Write([]byte("Error return from script"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{Name: "main", Channel: ChannelEdgeOne, BaseURL: srv.URL, Token: "secret-token-123456"}
	ch, err := NewChannel(ChannelEdgeOne)
	if err != nil {
		t.Fatal(err)
	}
	url, err := ch.Upload(context.Background(), cfg, "retry.txt", []byte("ok"))
	if err != nil {
		t.Fatalf("545 后应重试成功: %v", err)
	}
	if url != srv.URL+"/retry.txt" {
		t.Fatalf("URL 异常: %s", url)
	}
	if attempts != 2 {
		t.Fatalf("应重试 1 次（共 2 次请求），实际 %d 次", attempts)
	}
}
func TestNormalizeKey(t *testing.T) {
	for _, in := range []string{"", "/", "  ", "a//b", "a/../b", "a\\b", "a\x01b"} {
		if k, err := NormalizeKey(in); err == nil {
			t.Fatalf("%q 应拒绝，got %q", in, k)
		}
	}
	if k, err := NormalizeKey(" /iptest/final.txt "); err != nil || k != "iptest/final.txt" {
		t.Fatalf("规范化异常: %q %v", k, err)
	}
	if k, err := NormalizeKey("a/b/"); err != nil || k != "a/b" {
		t.Fatalf("尾部斜杠应被清理: %q %v", k, err)
	}
}
