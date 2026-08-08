package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAutoPathsBrowseDefault(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleAutoPaths, http.MethodGet, "/api/auto/paths", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("默认目录应 200: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Current string `json:"current"`
		Parent  string `json:"parent"`
		DataDir string `json:"dataDir"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("默认目录不应报错: %s", resp.Error)
	}
	if resp.DataDir == "" || filepath.ToSlash(s.dataDir) != resp.Current {
		t.Fatalf("默认应定位到 data 目录: dataDir=%q current=%q", resp.DataDir, resp.Current)
	}
	if resp.Parent == "" {
		t.Fatal("data 目录应有上级目录")
	}
}

func TestBrowseServerPathsListsDirAndFile(t *testing.T) {
	s := autoServer(t)
	sub := filepath.Join(s.dataDir, "外部文件")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "seed.txt"), []byte("1.0.0.1:443\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resp := doJSON(t, s.handleAutoPaths, http.MethodGet, "/api/auto/paths?path="+filepath.ToSlash(sub), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", resp.Code, resp.Body.String())
	}
	var got struct {
		Current string `json:"current"`
		Parent  string `json:"parent"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Current != filepath.ToSlash(sub) || got.Parent != filepath.ToSlash(s.dataDir) {
		t.Fatalf("定位错误: current=%q parent=%q", got.Current, got.Parent)
	}
	found := false
	for _, e := range got.Entries {
		if e.Name == "seed.txt" && !e.IsDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("应列出 seed.txt: %+v", got.Entries)
	}
}

func TestBrowseServerPathsMissingDirIsFriendlyError(t *testing.T) {
	s := autoServer(t)
	missing := filepath.Join(s.dataDir, "不存在的目录")
	resp := doJSON(t, s.handleAutoPaths, http.MethodGet, "/api/auto/paths?path="+filepath.ToSlash(missing), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("不可读目录应 200 并携带 error: %d", resp.Code)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error == "" {
		t.Fatal("不可读目录应返回 error 说明")
	}
}
