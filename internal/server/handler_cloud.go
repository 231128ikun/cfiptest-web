package server

import (
	"net/http"
	"strings"

	"iptest-web/internal/cloud"
)

// ---- 云端存储（edgeone 等渠道） ----

func (s *Server) handleCloudConfigsGet(w http.ResponseWriter, _ *http.Request) {
	configs, err := s.cloudStore.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configs":  configs,
		"channels": cloud.SupportedChannels,
	})
}

func (s *Server) handleCloudConfigsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Channel string `json:"channel"`
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, err := s.cloudStore.Create(cloud.Config{
		Name:    req.Name,
		Channel: req.Channel,
		BaseURL: req.BaseURL,
		Token:   req.Token,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg.Public()})
}

func (s *Server) handleCloudConfigsUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		Name    string `json:"name"`
		Channel string `json:"channel"`
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, err := s.cloudStore.Update(id, cloud.Config{
		Name:    req.Name,
		Channel: req.Channel,
		BaseURL: req.BaseURL,
		Token:   req.Token,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg.Public()})
}

func (s *Server) handleCloudConfigsDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.cloudStore.Delete(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// handleCloudTest 测试连接：传 id 用已保存配置；也可传内联配置（保存前测试）。
func (s *Server) handleCloudTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Channel string `json:"channel"`
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg := cloud.Config{
		Name:    req.Name,
		Channel: req.Channel,
		BaseURL: req.BaseURL,
		Token:   req.Token,
	}
	if req.ID != "" {
		stored, ok, err := s.cloudStore.Get(req.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "配置不存在")
			return
		}
		cfg = stored
	} else {
		if err := cfg.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(cfg.Token) == "" {
			writeError(w, http.StatusBadRequest, "请填写 Token")
			return
		}
	}
	ch, err := cloud.NewChannel(cfg.Channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ch.Test(r.Context(), cfg); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCloudUpload 上传文本内容到云端路径（测速工作台「导出至云端」）。
func (s *Server) handleCloudUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfigID string `json:"configId"`
		Key      string `json:"key"`
		Content  string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, ok, err := s.cloudStore.Get(req.ConfigID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "云端配置不存在")
		return
	}
	ch, err := cloud.NewChannel(cfg.Channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	content := []byte(req.Content)
	if len(content) == 0 {
		writeError(w, http.StatusBadRequest, "没有可上传的内容")
		return
	}
	url, err := ch.Upload(r.Context(), cfg, req.Key, content)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "size": len(content)})
}
