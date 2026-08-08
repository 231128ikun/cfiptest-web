package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pathBrowserEntry 是服务器文件浏览器中的一项（目录或文件）。
type pathBrowserEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size,omitempty"`
}

type pathBrowserResponse struct {
	Current   string             `json:"current"` // 当前目录（正斜杠）
	Parent    string             `json:"parent"`  // 上级目录；根目录为空串
	Home      string             `json:"home"`    // 用户主目录快捷入口；不可用时为空串
	DataDir   string             `json:"dataDir"` // 程序 data 目录快捷入口
	Entries   []pathBrowserEntry `json:"entries,omitempty"`
	Truncated bool               `json:"truncated,omitempty"`
	Error     string             `json:"error,omitempty"` // 目录不可读时的原因（HTTP 仍为 200，便于弹窗原地展示）
}

const maxPathBrowserEntries = 2000

// handleAutoPaths 列出服务端目录内容，供任务编辑器「浏览服务器…」选择初始化文件。
// 浏览器无法直接拿到服务器磁盘路径，本地 Web 工具（qBittorrent/Jellyfin 等）普遍采用
// 服务端目录浏览 API + 弹窗选路径的方案。本接口只返回目录/文件名，不返回文件内容。
func (s *Server) handleAutoPaths(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		raw = s.dataDir
	}
	if strings.ContainsRune(raw, '\x00') {
		writeJSON(w, http.StatusBadRequest, pathBrowserResponse{Error: "路径包含非法字符"})
		return
	}
	current := filepath.Clean(raw)
	if !filepath.IsAbs(current) {
		current = filepath.Join(s.dataDir, filepath.FromSlash(current))
	}
	resp := pathBrowserResponse{
		Current: filepath.ToSlash(current),
		Parent:  parentOf(current),
		Home:    homeDir(),
		DataDir: filepath.ToSlash(s.dataDir),
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		resp.Error = "无法读取该目录：" + dirReadError(err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Entries = make([]pathBrowserEntry, 0, len(entries))
	for _, e := range entries {
		if len(resp.Entries) >= maxPathBrowserEntries {
			resp.Truncated = true
			break
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		resp.Entries = append(resp.Entries, pathBrowserEntry{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size()})
	}
	sort.Slice(resp.Entries, func(i, j int) bool {
		if resp.Entries[i].IsDir != resp.Entries[j].IsDir {
			return resp.Entries[i].IsDir
		}
		return strings.ToLower(resp.Entries[i].Name) < strings.ToLower(resp.Entries[j].Name)
	})
	writeJSON(w, http.StatusOK, resp)
}

// parentOf 返回上级目录；已是根目录（上级等于自身）时返回空串。
func parentOf(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return filepath.ToSlash(parent)
}

// homeDir 返回用户主目录（正斜杠）；获取失败时为空串。
func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.ToSlash(home)
	}
	return ""
}

// dirReadError 把目录读取错误转成面向用户的中文提示。
func dirReadError(err error) string {
	if os.IsNotExist(err) {
		return "目录不存在"
	}
	if os.IsPermission(err) {
		return "没有读取权限"
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "未知错误"
	}
	return msg
}
