package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"iptest-web/internal/config"
	"iptest-web/internal/engine"
)

func TestHandleConfigPlatformCapabilities(t *testing.T) {
	for _, tc := range []struct {
		platform          string
		wantPickDirectory bool
	}{
		{platform: "windows", wantPickDirectory: true},
		{platform: "android", wantPickDirectory: false},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			s := &Server{
				runner:          &engine.Runner{},
				version:         "test",
				cfg:             config.Default(),
				dataDir:         t.TempDir(),
				platform:        tc.platform,
				latencyDefaults: engine.DefaultLatencyOptions(),
				speedDefaults:   engine.DefaultSpeedOptions(),
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			s.handleConfig(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/config = %d, body=%s", rec.Code, rec.Body.String())
			}
			var got configResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Platform != tc.platform {
				t.Fatalf("platform = %q, want %q", got.Platform, tc.platform)
			}
			if got.Capabilities["pickDirectory"] != tc.wantPickDirectory {
				t.Fatalf("pickDirectory = %v, want %v", got.Capabilities["pickDirectory"], tc.wantPickDirectory)
			}
		})
	}
}
