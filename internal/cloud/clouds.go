// Package cloud 提供云端存储渠道（edgeone Blob 等）的配置管理与上传能力。
// 配置保存在 data/clouds.json（仅本机），Token 只在本地落盘，接口返回时脱敏。
package cloud

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CloudsFile 是云端渠道配置文件（相对 data 目录）。
const CloudsFile = "clouds.json"

// 支持的渠道。
const (
	ChannelEdgeOne = "edgeone" // edgeone-file-hub：Pages Blob + 边缘函数，单文件 ≤ 1MB
)

// SupportedChannels 返回当前支持的渠道列表（含展示名）。
var SupportedChannels = []ChannelInfo{
	{ID: ChannelEdgeOne, Name: "EdgeOne Blob", MaxBytes: 1024 * 1024},
}

// ChannelInfo 描述一个渠道的能力，供前端展示。
type ChannelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MaxBytes int64  `json:"maxBytes"` // 单文件大小上限（字节）
}

// Config 是一条云端存储配置。Token 含明文，仅能通过 Store.Get 内部读取。
type Config struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Channel   string    `json:"channel"`
	BaseURL   string    `json:"baseUrl"` // 站点根地址，如 https://files.example.com
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PublicConfig 是返回给前端的配置（Token 已脱敏）。
type PublicConfig struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Channel   string    `json:"channel"`
	BaseURL   string    `json:"baseUrl"`
	Token     string    `json:"token"` // 脱敏后的 Token
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store 管理云端配置的读取与写入，带内存互斥锁。
type Store struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	configs []Config
}

// NewStore 创建配置存储，首次读写时惰性加载 data/clouds.json。
func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, CloudsFile)}
}

var channelNameRe = regexp.MustCompile(`^[^\s/\\<>]{1,32}$`)

// IsSupportedChannel 判断渠道是否支持。
func IsSupportedChannel(channel string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case ChannelEdgeOne:
		return true
	}
	return false
}

// ChannelInfoByID 返回渠道信息；未知渠道返回 false。
func ChannelInfoByID(channel string) (ChannelInfo, bool) {
	for _, info := range SupportedChannels {
		if info.ID == strings.ToLower(strings.TrimSpace(channel)) {
			return info, true
		}
	}
	return ChannelInfo{}, false
}

// Validate 校验并规范化配置（编辑时 token 为空表示保留原值，由调用方处理）。
func (c *Config) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("配置名称不能为空")
	}
	if !channelNameRe.MatchString(c.Name) {
		return fmt.Errorf("配置名称不能包含空白、斜杠或反斜杠，且不超过 32 个字符")
	}
	c.Channel = strings.ToLower(strings.TrimSpace(c.Channel))
	if !IsSupportedChannel(c.Channel) {
		return fmt.Errorf("不支持的渠道 %q（当前支持：edgeone）", c.Channel)
	}
	c.BaseURL = strings.TrimSpace(strings.TrimRight(c.BaseURL, "/"))
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("站点地址必须是完整的 http/https URL，如 https://files.example.com")
	}
	if strings.Contains(c.BaseURL, "/api/") {
		return fmt.Errorf("站点地址应填站点根地址，不要包含 /api 路径")
	}
	return nil
}

// Load 读取（首次才真正读盘）。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() error {
	if s.loaded {
		return nil
	}
	body, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.configs = nil
			s.loaded = true
			return nil
		}
		return fmt.Errorf("读取 %s 失败: %w", s.path, err)
	}
	var file struct {
		Configs []Config `json:"configs"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return fmt.Errorf("%s 格式错误: %w", s.path, err)
	}
	s.configs = file.Configs
	if s.configs == nil {
		s.configs = []Config{}
	}
	s.loaded = true
	return nil
}

// newID 生成随机 12 字节 hex 作为配置 ID。
func newID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Public 返回脱敏后的视图（Token 打码）。
func (c Config) Public() PublicConfig {
	return PublicConfig{
		ID:        c.ID,
		Name:      c.Name,
		Channel:   c.Channel,
		BaseURL:   c.BaseURL,
		Token:     MaskToken(c.Token),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// List 返回脱敏后的配置列表。
func (s *Store) List() ([]PublicConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	out := make([]PublicConfig, 0, len(s.configs))
	for _, c := range s.configs {
		out = append(out, c.Public())
	}
	return out, nil
}

// Get 返回含明文 Token 的配置（后端内部使用）。
func (s *Store) Get(id string) (Config, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Config{}, false, err
	}
	for _, c := range s.configs {
		if c.ID == id {
			return c, true, nil
		}
	}
	return Config{}, false, nil
}

// Create 校验并新增配置，返回含明文 Token 的完整配置（调用方应脱敏后再返回前端）。
func (s *Store) Create(c Config) (Config, error) {
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	c.Token = strings.TrimSpace(c.Token)
	if c.Token == "" {
		return Config{}, fmt.Errorf("请填写 Token")
	}
	c.ID = newID()
	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Config{}, err
	}
	s.configs = append(s.configs, c)
	if err := s.saveLocked(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Update 按 ID 更新配置；token 为空白时保留原 Token。
func (s *Store) Update(id string, patch Config) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Config{}, err
	}
	idx := -1
	for i := range s.configs {
		if s.configs[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Config{}, fmt.Errorf("配置不存在")
	}
	old := s.configs[idx]
	if patch.Name != "" || patch.Channel != "" || patch.BaseURL != "" {
		old.Name = patch.Name
		if patch.Channel != "" {
			old.Channel = patch.Channel
		}
		if patch.BaseURL != "" {
			old.BaseURL = patch.BaseURL
		}
		if err := old.Validate(); err != nil {
			return Config{}, err
		}
	}
	token := strings.TrimSpace(patch.Token)
	if token != "" {
		old.Token = token
	}
	old.UpdatedAt = time.Now()
	s.configs[idx] = old
	if err := s.saveLocked(); err != nil {
		return Config{}, err
	}
	return old, nil
}

// Delete 删除配置。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	kept := s.configs[:0]
	found := false
	for _, c := range s.configs {
		if c.ID == id {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return fmt.Errorf("配置不存在")
	}
	s.configs = kept
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(map[string]any{"configs": s.configs}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, body)
}

// MaskToken 脱敏 Token：保留前 3 与后 3 字符，中间以 *** 代替；过短则整体掩码。
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "••••••"
	}
	return token[:3] + "***" + token[len(token)-3:]
}
