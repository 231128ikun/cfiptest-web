// Package config manages local application configuration under data/.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"iptest-web/internal/engine"
)

const (
	FileName     = "config.json"
	SettingsName = "settings.json"
)

type Sources struct {
	Locations      []string `json:"locations"`
	ASNDatabase    []string `json:"asnDatabase"`
	OfficialRanges []string `json:"officialRanges"`
	// ActiveIPv6RangeSources 是官方 IPv6 活跃 /48 段获取地址（与 IPv4 分离，
	// 见 engine.DefaultActiveIPv6RangeSources），避免旧配置里只有 CF 聚合段 JSON 的情况。
	ActiveIPv6RangeSources []string `json:"activeIPv6RangeSources"`
}

type Config struct {
	Sources      Sources `json:"sources"`
	SpeedTestURL string  `json:"speedTestURL"`
	TraceURL     string  `json:"traceURL"`
	IPSTypeURL   string  `json:"ipsTypeURL"`
}

func Default() Config {
	return Config{
		Sources: Sources{
			Locations:              append([]string(nil), engine.DefaultLocationSources...),
			ASNDatabase:            append([]string(nil), engine.DefaultASNSources...),
			OfficialRanges:         append([]string(nil), engine.DefaultOfficialRangeSources...),
			ActiveIPv6RangeSources: append([]string(nil), engine.DefaultActiveIPv6RangeSources...),
		},
		SpeedTestURL: engine.DefaultSpeedOptions().DownloadURL,
		TraceURL:     engine.DefaultTraceURL,
		IPSTypeURL:   engine.DefaultIPSTypeURL,
	}
}

// PrepareDataDir creates exeDir/data and copies legacy runtime files from the
// executable directory when no managed copy exists yet.
func PrepareDataDir(exeDir string) (string, error) {
	dataDir := filepath.Join(exeDir, "data")
	if err := PrepareDataDirAt(dataDir); err != nil {
		return "", err
	}
	for _, name := range []string{
		FileName, SettingsName, "locations.json", "GeoLite2-ASN.mmdb",
		"cloudflare-ips-v4.txt", "cloudflare-ips-v6.txt",
	} {
		dst := filepath.Join(dataDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		src := filepath.Join(exeDir, name)
		body, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, body, 0644); err != nil {
			return "", fmt.Errorf("迁移 %s: %w", name, err)
		}
	}
	return dataDir, nil
}

// PrepareDataDirAt creates an explicit writable data directory. Android uses
// this path because the packaged native-library directory is read-only.
func PrepareDataDirAt(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("数据目录不能为空")
	}
	return os.MkdirAll(filepath.Clean(dataDir), 0755)
}

func Load(dataDir string) Config {
	def := Default()
	body, err := os.ReadFile(filepath.Join(dataDir, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			_ = Save(dataDir, def)
		} else {
			fmt.Printf("警告: 读取 %s 失败: %v\n", FileName, err)
		}
		return def
	}
	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		fmt.Printf("警告: %s 格式错误（%v），使用默认配置\n", FileName, err)
		return def
	}
	cfg.FillDefaults(def)
	return cfg
}

func Save(dataDir string, cfg Config) error {
	cfg.FillDefaults(Default())
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dataDir, FileName), body)
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (c *Config) FillDefaults(def Config) {
	c.Sources.Locations = nonEmpty(c.Sources.Locations)
	c.Sources.ASNDatabase = nonEmpty(c.Sources.ASNDatabase)
	c.Sources.OfficialRanges = nonEmpty(c.Sources.OfficialRanges)
	c.Sources.ActiveIPv6RangeSources = nonEmpty(c.Sources.ActiveIPv6RangeSources)
	if len(c.Sources.Locations) == 0 {
		c.Sources.Locations = def.Sources.Locations
	}
	if len(c.Sources.ASNDatabase) == 0 {
		c.Sources.ASNDatabase = def.Sources.ASNDatabase
	}
	if len(c.Sources.OfficialRanges) == 0 {
		c.Sources.OfficialRanges = def.Sources.OfficialRanges
	}
	if len(c.Sources.ActiveIPv6RangeSources) == 0 {
		c.Sources.ActiveIPv6RangeSources = def.Sources.ActiveIPv6RangeSources
	}
	if strings.TrimSpace(c.SpeedTestURL) == "" {
		c.SpeedTestURL = def.SpeedTestURL
	}
	if strings.TrimSpace(c.TraceURL) == "" {
		c.TraceURL = def.TraceURL
	}
	if strings.TrimSpace(c.IPSTypeURL) == "" {
		c.IPSTypeURL = def.IPSTypeURL
	}
}

func LoadSettings(dataDir string) map[string]any {
	body, err := os.ReadFile(filepath.Join(dataDir, SettingsName))
	if err != nil {
		return map[string]any{}
	}
	var settings map[string]any
	if json.Unmarshal(body, &settings) != nil {
		return map[string]any{}
	}
	return settings
}

func SaveSettings(dataDir string, settings map[string]any) error {
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dataDir, SettingsName), body)
}

// writeJSONFile 先写同目录临时文件，再原子替换目标，避免异常退出留下半个 JSON。
func writeJSONFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".iptest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
