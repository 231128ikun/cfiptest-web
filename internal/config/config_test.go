package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesLocalConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := Load(dir)
	if cfg.TraceURL == "" || cfg.SpeedTestURL == "" || cfg.IPSTypeURL == "" {
		t.Fatal("defaults not loaded")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string]any{"speedEnabled": true, "maxResults": float64(10)}
	if err := SaveSettings(dir, want); err != nil {
		t.Fatal(err)
	}
	got := LoadSettings(dir)
	if got["speedEnabled"] != true || got["maxResults"] != float64(10) {
		t.Fatalf("got=%v", got)
	}
	want["maxResults"] = float64(20)
	if err := SaveSettings(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := LoadSettings(dir); got["maxResults"] != float64(20) {
		t.Fatalf("覆盖保存失败: %v", got)
	}
}

func TestPrepareDataDirMigratesLegacyFiles(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(exeDir, "locations.json"), []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	dataDir, err := PrepareDataDir(exeDir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "locations.json"))
	if err != nil || string(body) != "legacy" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}
