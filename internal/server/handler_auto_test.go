package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

func autoServer(t *testing.T) *Server {
	t.Helper()
	return &Server{dataDir: t.TempDir()}
}

func doJSON(t *testing.T, handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestAutoSubsSaveGetRoundTrip(t *testing.T) {
	s := autoServer(t)
	sub := subscription.Subscription{
		Name: "综合订阅", EnableSpeed: true,
		Groups: []subscription.Group{{Name: "美国", CountryCode: "US", Count: 10}},
		Output: subscription.Output{Path: "out/sub.txt", Template: "{ip}:{port}#{country}"},
	}
	if rec := doJSON(t, s.handleAutoSubsSave, http.MethodPut, "/api/auto/subs", autoSubsResponse{Subscriptions: []subscription.Subscription{sub}}); rec.Code != http.StatusOK {
		t.Fatalf("保存失败: %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, s.handleAutoSubsGet, http.MethodGet, "/api/auto/subs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取失败: %d", rec.Code)
	}
	var got autoSubsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].Name != "综合订阅" {
		t.Fatalf("往返不一致: %+v", got.Subscriptions)
	}
	// 保存非法定义应 400
	if rec := doJSON(t, s.handleAutoSubsSave, http.MethodPut, "/api/auto/subs", autoSubsResponse{Subscriptions: []subscription.Subscription{{Name: "", Groups: nil}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法订阅应 400: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoSubsValidate(t *testing.T) {
	s := autoServer(t)
	bad := subscription.Subscription{Name: "x", Groups: nil}
	if rec := doJSON(t, s.handleAutoSubsValidate, http.MethodPost, "/api/auto/subs/validate", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法订阅应 400: %d %s", rec.Code, rec.Body.String())
	}
	good := subscription.Subscription{Name: "x", Groups: []subscription.Group{{Name: "g", Count: 1}}}
	if rec := doJSON(t, s.handleAutoSubsValidate, http.MethodPost, "/api/auto/subs/validate", good); rec.Code != http.StatusOK {
		t.Fatalf("合法订阅应 200: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoLibraryImportAndList(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleAutoLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Targets: []engine.Target{{IP: "1.1.1.1", Port: 443}, {IP: "2.2.2.2", Port: 2053}},
		Source:  library.SourceManual,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("导入失败: %d %s", rec.Code, rec.Body.String())
	}
	var imp autoImportResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &imp)
	if imp.Added != 2 || imp.Total != 2 {
		t.Fatalf("导入统计错误: %+v", imp)
	}
	// 重复导入相同目标只更新
	rec = doJSON(t, s.handleAutoLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Targets: []engine.Target{{IP: "1.1.1.1", Port: 443}},
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &imp)
	if imp.Added != 0 || imp.Updated != 1 {
		t.Fatalf("重复导入统计错误: %+v", imp)
	}
	// 按国家/状态过滤（导入后 status=new、country 未知）
	rec = doJSON(t, s.handleAutoLibraryGet, http.MethodGet, "/api/auto/library?status=new", nil)
	var lib autoLibraryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)
	if lib.Total != 2 || lib.Stats.New != 2 {
		t.Fatalf("列表过滤错误: total=%d stats=%+v", lib.Total, lib.Stats)
	}
}

func TestAutoLibraryTextImport(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleAutoLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Text: "1.1.1.1:443\n2.2.2.2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("文本导入失败: %d %s", rec.Code, rec.Body.String())
	}
	var imp autoImportResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &imp)
	if imp.Added != 2 {
		t.Fatalf("文本导入应识别 2 条: %+v", imp)
	}
	// 库文件应已落盘
	if _, err := os.Stat(filepath.Join(s.dataDir, library.FileName)); err != nil {
		t.Fatalf("库文件未保存: %v", err)
	}
}

func TestAutoLibraryRemoveAndClear(t *testing.T) {
	s := autoServer(t)
	doJSON(t, s.handleAutoLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Targets: []engine.Target{{IP: "1.1.1.1", Port: 443}, {IP: "2.2.2.2", Port: 443}},
	})
	rec := doJSON(t, s.handleAutoLibraryRemove, http.MethodPost, "/api/auto/library/remove", map[string]any{"keys": []string{"1.1.1.1|443"}})
	var removed map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &removed)
	if removed["removed"] != 1 {
		t.Fatalf("删除应命中 1 条: %+v", removed)
	}
	rec = doJSON(t, s.handleAutoLibraryClear, http.MethodPost, "/api/auto/library/clear", map[string]bool{"confirm": false})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未确认清空应 400: %d", rec.Code)
	}
	rec = doJSON(t, s.handleAutoLibraryClear, http.MethodPost, "/api/auto/library/clear", map[string]bool{"confirm": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("清空失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.handleAutoLibraryGet, http.MethodGet, "/api/auto/library", nil)
	var lib autoLibraryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)
	if lib.Total != 0 {
		t.Fatalf("清空后应为 0: %+v", lib.Stats)
	}
}

func TestAutoRunMatchesChineseName(t *testing.T) {
	s := autoServer(t)
	sub := subscription.Subscription{Name: "日本专线", Groups: []subscription.Group{{Name: "日本", CountryCode: "JP", Count: 1}}}
	if err := subscription.SaveSubscriptions(s.dataDir, []subscription.Subscription{sub}); err != nil {
		t.Fatal(err)
	}
	// 占用任务槽后，若名字匹配会走到 409（而不是 404），从而证明中文名查找成功
	ctx, ok := s.tryStartTask("occupied")
	if !ok || ctx == nil {
		t.Fatal("占用任务槽失败")
	}
	defer s.finishTask("occupied")
	rec := doJSON(t, s.handleAutoRun, http.MethodPost, "/api/auto/run", autoRunRequest{SubscriptionName: "日本专线"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("中文订阅名应被找到（409），实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAutoRunNotFoundAndConflict(t *testing.T) {
	s := autoServer(t)
	// 订阅器不存在 → 404
	rec := doJSON(t, s.handleAutoRun, http.MethodPost, "/api/auto/run", autoRunRequest{SubscriptionName: "nope"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("应 404: %d %s", rec.Code, rec.Body.String())
	}
	// 已有任务运行 → 409
	subscription.SaveSubscriptions(s.dataDir, []subscription.Subscription{{
		Name: "x", Groups: []subscription.Group{{Name: "g", Count: 1}},
		Output: subscription.Output{Path: "out/t.txt"},
	}})
	ctx, ok := s.tryStartTask("occupied")
	if !ok || ctx == nil {
		t.Fatal("占用任务槽失败")
	}
	rec = doJSON(t, s.handleAutoRun, http.MethodPost, "/api/auto/run", autoRunRequest{SubscriptionName: "x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("应 409: %d %s", rec.Code, rec.Body.String())
	}
	s.finishTask("occupied")
}

func TestAutoOutputSecurity(t *testing.T) {
	s := autoServer(t)
	// 目录穿越应被拒绝
	rec := doJSON(t, s.handleAutoOutput, http.MethodGet, "/api/auto/output?path=..%2F..%2Fsecret.txt", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("路径穿越应 400: %d", rec.Code)
	}
	// 正常输出文件可下载
	sub := subscription.Subscription{Name: "x", Groups: []subscription.Group{{Name: "g", Count: 1}},
		Output: subscription.Output{Path: "out/t.txt", Template: "{ip}:{port}"}}
	if _, err := subscription.WriteOutput(s.dataDir, sub, []library.Entry{{IP: "9.9.9.9", Port: 443}}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s.handleAutoOutput, http.MethodGet, "/api/auto/output?path=out%2Ft.txt", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "9.9.9.9:443") {
		t.Fatalf("输出下载失败: %d %s", rec.Code, rec.Body.String())
	}
}
