package server

import (
	"net/http"
)

// configResponse 对应 GET /api/config 的响应。
type configResponse struct {
	Version         string         `json:"version"`
	LocationsLoaded bool           `json:"locationsLoaded"`
	LocationCount   int            `json:"locationCount"`
	ASNLoaded       bool           `json:"asnLoaded"`
	Defaults        map[string]any `json:"defaults"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		Version:         s.version,
		LocationsLoaded: true, // NewRunner 失败时进程根本不会启动
		LocationCount:   s.runner.LocationCount(),
		ASNLoaded:       s.runner.ASNLoaded(),
		Defaults: map[string]any{
			"latency": s.latencyDefaults,
			"speed":   s.speedDefaults,
		},
	})
}
