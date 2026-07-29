// Package config 负责 exe 同目录下 config.json 的读写。
//
// 设计取向：配置文件的唯一职责是「把硬编码的外部依赖挪到程序外面」，
// 让用户换源、换 trace 接口不需要重新编译。因此
//
//   - 每个源都是**字符串数组**，运行时依次尝试，全失败才报错；
//   - 缺失字段回落到内置默认值，所以删掉整个 config.json 也能正常启动；
//   - 首次启动会把当前生效的完整配置写回磁盘，用户可以直接看到有哪些可调项。
//
// 默认值本身定义在 engine 包（DefaultLocationSources 等），此处只做引用，
// 避免同一个 URL 在两个包里各写一份而漂移。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"iptest-web/internal/engine"
)

// FileName 是配置文件名，位于 exe 同目录。
const FileName = "config.json"

// Sources 汇总所有需要联网获取的资源地址。
type Sources struct {
	Locations      []string `json:"locations"`      // 地理位置数据（IATA -> 国家/城市/国旗）
	ASNDatabase    []string `json:"asnDatabase"`    // GeoLite2-ASN.mmdb
	OfficialRanges []string `json:"officialRanges"` // Cloudflare 官方 IP 段
}

// Config 是 config.json 的完整结构。
type Config struct {
	Sources      Sources `json:"sources"`
	SpeedTestURL string  `json:"speedTestURL"` // 下载测速地址（不含协议头）
	TraceURL     string  `json:"traceURL"`     // CF 节点验证接口（不含协议头）
}

// Default 返回内置默认配置。
func Default() Config {
	return Config{
		Sources: Sources{
			Locations:      append([]string(nil), engine.DefaultLocationSources...),
			ASNDatabase:    append([]string(nil), engine.DefaultASNSources...),
			OfficialRanges: append([]string(nil), engine.DefaultOfficialRangeSources...),
		},
		SpeedTestURL: engine.DefaultSpeedOptions().DownloadURL,
		TraceURL:     engine.DefaultTraceURL,
	}
}

// Load 读取 dataDir/config.json。
//
// 文件不存在时写入一份默认配置并返回它。文件存在但字段缺失/为空时，
// 该字段回落到默认值——这样用户只写自己关心的几项也能工作。
// 解析失败不致命：告警后按默认值运行，避免一个手写错的逗号让程序起不来。
func Load(dataDir string) Config {
	path := filepath.Join(dataDir, FileName)
	def := Default()

	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("警告: 读取 %s 失败（%v），使用默认配置\n", FileName, err)
			return def
		}
		if werr := Save(dataDir, def); werr != nil {
			fmt.Printf("警告: 无法写入默认 %s: %v\n", FileName, werr)
		}
		return def
	}

	var cfg Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		fmt.Printf("警告: %s 不是合法 JSON（%v），本次使用默认配置\n", FileName, err)
		return def
	}

	cfg.fillDefaults(def)
	return cfg
}

// fillDefaults 用 def 补齐 c 中缺失或为空的字段。
func (c *Config) fillDefaults(def Config) {
	if len(c.Sources.Locations) == 0 {
		c.Sources.Locations = def.Sources.Locations
	}
	if len(c.Sources.ASNDatabase) == 0 {
		c.Sources.ASNDatabase = def.Sources.ASNDatabase
	}
	if len(c.Sources.OfficialRanges) == 0 {
		c.Sources.OfficialRanges = def.Sources.OfficialRanges
	}
	if c.SpeedTestURL == "" {
		c.SpeedTestURL = def.SpeedTestURL
	}
	if c.TraceURL == "" {
		c.TraceURL = def.TraceURL
	}
}

// Save 把配置写入 dataDir/config.json（缩进格式，便于手工编辑）。
func Save(dataDir string, cfg Config) error {
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, FileName), append(body, '\n'), 0644)
}
