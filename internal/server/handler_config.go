package server

import (
	"encoding/json"
	"net/http"

	"iptest-web/internal/config"
)

// configResponse 对应 GET /api/config 的响应。
type configResponse struct {
	Version         string         `json:"version"`
	LocationsLoaded bool           `json:"locationsLoaded"`
	LocationCount   int            `json:"locationCount"`
	ASNLoaded       bool           `json:"asnLoaded"`
	Defaults        map[string]any `json:"defaults"`
	Config          config.Config  `json:"config"`
	Settings        map[string]any `json:"settings"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.configMu.RLock()
	cfg := s.cfg
	latencyDefaults := s.latencyDefaults
	speedDefaults := s.speedDefaults
	s.configMu.RUnlock()
	writeJSON(w, http.StatusOK, configResponse{
		Version:         s.version,
		LocationsLoaded: true, // NewRunner 失败时进程根本不会启动
		LocationCount:   s.runner.LocationCount(),
		ASNLoaded:       s.runner.ASNLoaded(),
		Defaults: map[string]any{
			"latency": latencyDefaults,
			"speed":   speedDefaults,
		},
		Config:   cfg,
		Settings: config.LoadSettings(s.dataDir),
	})
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置不是合法 JSON: "+err.Error())
		return
	}
	cfg.FillDefaults(config.Default())
	if err := config.Save(s.dataDir, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}
	s.configMu.Lock()
	s.cfg = cfg
	s.speedDefaults.DownloadURL = cfg.SpeedTestURL
	s.configMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "config": cfg})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var settings map[string]any
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, "设置不是合法 JSON: "+err.Error())
		return
	}
	if err := config.SaveSettings(s.dataDir, settings); err != nil {
		writeError(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	// 调试日志开关即时生效
	if on, ok := settings["debugLog"].(bool); ok {
		s.log.SetEnabled(on)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}
