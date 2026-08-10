package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"iptest-web/internal/cloud"
)

func cloudServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{dataDir: dir, log: NewLogger(dir, false), cloudStore: cloud.NewStore(dir)}
}

func doJSONPath(t *testing.T, handler http.HandlerFunc, method, target string, body any, pathValue map[string]string) *httptest.ResponseRecorder {
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
	for k, v := range pathValue {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestCloudConfigsCRUDHandlers(t *testing.T) {
	s := cloudServer(t)

	rec := doJSON(t, s.handleCloudConfigsGet, http.MethodGet, "/api/cloud/configs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 失败: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s.handleCloudConfigsCreate, http.MethodPost, "/api/cloud/configs", map[string]any{
		"name": "主存储", "channel": "edgeone", "baseUrl": "https://files.example.com", "token": "tok-123456789",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Config cloud.PublicConfig `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Config.ID == "" || created.Config.Token == "tok-123456789" {
		t.Fatalf("创建返回异常: %+v", created.Config)
	}
	id := created.Config.ID

	rec = doJSONPath(t, s.handleCloudConfigsUpdate, http.MethodPut, "/api/cloud/configs/"+id, map[string]any{
		"name": "备用", "baseUrl": "https://files2.example.com",
	}, map[string]string{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", rec.Code, rec.Body.String())
	}

	// 更新时留空 token 应保留原 token（内部读取验证）
	stored, ok, err := s.cloudStore.Get(id)
	if err != nil || !ok {
		t.Fatalf("读取存储失败: %v", err)
	}
	if stored.Token != "tok-123456789" || stored.Name != "备用" {
		t.Fatalf("更新后内容异常: %+v", stored)
	}

	rec = doJSONPath(t, s.handleCloudConfigsDelete, http.MethodDelete, "/api/cloud/configs/"+id, nil, map[string]string{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := s.cloudStore.Get(id); ok {
		t.Fatal("删除后仍存在")
	}
}

func TestCloudUploadHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123456789" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := cloudServer(t)
	cfg, err := s.cloudStore.Create(cloud.Config{
		Name: "main", Channel: cloud.ChannelEdgeOne, BaseURL: srv.URL, Token: "tok-123456789",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s.handleCloudUpload, http.MethodPost, "/api/cloud/upload", map[string]any{
		"configId": cfg.ID, "key": "iptest/result.txt", "content": "1.2.3.4:443\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		URL  string `json:"url"`
		Size int    `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != srv.URL+"/iptest/result.txt" || out.Size != 12 {
		t.Fatalf("上传结果异常: %+v", out)
	}

	rec = doJSON(t, s.handleCloudUpload, http.MethodPost, "/api/cloud/upload", map[string]any{
		"configId": "not-exist", "key": "x", "content": "y",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知配置应 404: %d", rec.Code)
	}

	rec = doJSON(t, s.handleCloudUpload, http.MethodPost, "/api/cloud/upload", map[string]any{
		"configId": cfg.ID, "key": "x", "content": "",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空内容应 400: %d", rec.Code)
	}
}

func TestCloudTestHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := cloudServer(t)
	// 内联测试（保存前）
	rec := doJSON(t, s.handleCloudTest, http.MethodPost, "/api/cloud/test", map[string]any{
		"name": "inline", "channel": "edgeone", "baseUrl": srv.URL, "token": "tok-123456789",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("内联测试失败: %d %s", rec.Code, rec.Body.String())
	}
	// 已保存配置测试
	cfg, _ := s.cloudStore.Create(cloud.Config{Name: "main", Channel: cloud.ChannelEdgeOne, BaseURL: srv.URL, Token: "tok-123456789"})
	rec = doJSON(t, s.handleCloudTest, http.MethodPost, "/api/cloud/test", map[string]any{"id": cfg.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("已保存配置测试失败: %d %s", rec.Code, rec.Body.String())
	}
	// 内联缺 Token
	rec = doJSON(t, s.handleCloudTest, http.MethodPost, "/api/cloud/test", map[string]any{
		"name": "inline", "channel": "edgeone", "baseUrl": srv.URL,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 Token 应 400: %d", rec.Code)
	}
}
