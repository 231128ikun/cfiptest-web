package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 库管理目录与索引文件名。
const (
	ManagerDir = "ipdb" // data/ipdb/
	IndexFile  = "index.json"
	DefaultID  = "default" // 迁移产生的默认库 ID
)

// Info 描述一个 IP 库。
type Info struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	File string `json:"file"` // 相对 ipdb 目录的 jsonl 文件名
}

// Manager 管理 data/ipdb/ 下的多个命名 IP 库。
// 任务通过稳定 ID 绑定库，改名不破坏引用。
type Manager struct {
	dir  string // data/ipdb
	data string // 应用数据目录（baseDir）
}

// OpenManager 打开库管理器；首次使用时把旧版 data/ipdb.jsonl 迁移为「默认库」。
func OpenManager(dataDir string) (*Manager, error) {
	dir := filepath.Join(dataDir, ManagerDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建库目录失败: %w", err)
	}
	m := &Manager{dir: dir, data: dataDir}
	if err := m.migrateLegacy(); err != nil {
		return nil, err
	}
	if err := m.pruneOfficialLibrary(); err != nil {
		return nil, err
	}
	return m, nil
}

// pruneOfficialLibrary 清理旧版自动注册的「官方 IP 库」：官方段是维护来源，
// 数据不落盘（官方段已有本地缓存与内置兜底），不应出现在本地库列表中。
// 从索引移除并删除可能残留的 官方IP库.jsonl 文件。
func (m *Manager) pruneOfficialLibrary() error {
	list, err := m.readIndex()
	if err != nil {
		return err
	}
	out := list[:0]
	removedFile := ""
	for _, info := range list {
		if info.ID == "official" || info.File == "官方IP库.jsonl" {
			removedFile = info.File
			continue
		}
		out = append(out, info)
	}
	if len(out) == len(list) {
		return nil
	}
	if err := m.writeIndex(out); err != nil {
		return err
	}
	if removedFile != "" {
		_ = os.Remove(filepath.Join(m.dir, removedFile))
	}
	return nil
}

// migrateLegacy 保证默认库存在：把旧版 data/ipdb.jsonl 迁移为「默认库」，
// 无旧数据时也会创建空的默认库（仅首次）。
func (m *Manager) migrateLegacy() error {
	indexPath := filepath.Join(m.dir, IndexFile)
	if _, err := os.Stat(indexPath); err == nil {
		return nil
	}
	info := Info{ID: DefaultID, Name: "默认库", File: "默认库.jsonl"}
	legacy := filepath.Join(m.data, FileName)
	body, err := os.ReadFile(legacy)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取旧 IP 库失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, info.File), body, 0644); err != nil {
		return fmt.Errorf("创建默认库失败: %w", err)
	}
	if err := m.writeIndex([]Info{info}); err != nil {
		return err
	}
	if err == nil {
		_ = os.Remove(legacy)
	}
	return nil
}

// syncDiscovered 自动把 ipdb 目录下未被索引的 *.jsonl 文件注册为库（用户直接放入文件即可使用）。
func (m *Manager) syncDiscovered() error {
	list, err := m.readIndex()
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(list))
	for _, info := range list {
		known[info.File] = true
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	changed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == IndexFile || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".jsonl") {
			continue
		}
		if known[name] {
			continue
		}
		list = append(list, Info{ID: nextID(list), Name: strings.TrimSuffix(name, ".jsonl"), File: name})
		known[name] = true
		changed = true
	}
	if changed {
		return m.writeIndex(list)
	}
	return nil
}

func (m *Manager) indexPath() string { return filepath.Join(m.dir, IndexFile) }

func (m *Manager) readIndex() ([]Info, error) {
	body, err := os.ReadFile(m.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", IndexFile, err)
	}
	var idx struct {
		Libraries []Info `json:"libraries"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("%s 格式错误: %w", IndexFile, err)
	}
	return idx.Libraries, nil
}

func (m *Manager) writeIndex(list []Info) error {
	body, err := json.MarshalIndent(map[string]any{"libraries": list}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, m.indexPath())
}

func (m *Manager) fileOf(id string) (Info, bool, error) {
	list, err := m.readIndex()
	if err != nil {
		return Info{}, false, err
	}
	for _, info := range list {
		if info.ID == id {
			return info, true, nil
		}
	}
	return Info{}, false, nil
}

// List 返回全部库（含各自统计）。会先自动发现目录下新增的 *.jsonl 文件。
func (m *Manager) List() ([]Info, error) {
	if err := m.syncDiscovered(); err != nil {
		return nil, err
	}
	return m.readIndex()
}

// ListWithStats 返回全部库及统计信息（自动发现新增文件）。
func (m *Manager) ListWithStats() ([]Info, map[string]Stats, error) {
	if err := m.syncDiscovered(); err != nil {
		return nil, nil, err
	}
	list, err := m.readIndex()
	if err != nil {
		return nil, nil, err
	}
	stats := make(map[string]Stats, len(list))
	for _, info := range list {
		if s, err := m.Open(info.ID); err == nil {
			stats[info.ID] = s.Stats()
		}
	}
	return list, stats, nil
}

// Open 按 ID 打开一个库。
func (m *Manager) Open(id string) (*Store, error) {
	info, ok, err := m.fileOf(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("找不到 IP 库: %s", id)
	}
	s, err := Open(filepath.Join(m.dir, info.File))
	if err != nil {
		return nil, err
	}
	s.baseDir = m.data
	return s, nil
}

// SanitizeName 清理库名：保留中英文/数字/常用符号，其余去掉；空则回退。
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r >= 0x4E00 && r <= 0x9FFF, r == '-', r == '_', r == '.', r == ' ', r == '(' || r == ')':
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "库"
	}
	return out
}

// Create 新建一个命名库，返回其 Info。
func (m *Manager) Create(name string) (Info, error) {
	name = SanitizeName(name)
	list, err := m.readIndex()
	if err != nil {
		return Info{}, err
	}
	// 唯一名称
	base := name
	for i := 2; ; i++ {
		dup := false
		for _, info := range list {
			if info.Name == name {
				dup = true
				break
			}
		}
		if !dup {
			break
		}
		name = fmt.Sprintf("%s %d", base, i)
	}
	// 唯一文件
	file := name + ".jsonl"
	exists := map[string]bool{}
	for _, info := range list {
		exists[info.File] = true
	}
	if exists[file] {
		file = fmt.Sprintf("%s-%d.jsonl", base, len(list)+1)
	}
	id := nextID(list)
	info := Info{ID: id, Name: name, File: file}
	list = append(list, info)
	if err := m.writeIndex(list); err != nil {
		return Info{}, err
	}
	return info, nil
}

func nextID(list []Info) string {
	max := 0
	for _, info := range list {
		if n, err := strconv.Atoi(strings.TrimPrefix(info.ID, "lib-")); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("lib-%d", max+1)
}

// Rename 修改库显示名（文件与 ID 不变）。
func (m *Manager) Rename(id, name string) error {
	list, err := m.readIndex()
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].ID == id {
			list[i].Name = SanitizeName(name)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("找不到 IP 库: %s", id)
	}
	return m.writeIndex(list)
}

// Delete 删除库（文件 + 索引）。default 库不允许删除。
func (m *Manager) Delete(id string) error {
	if id == DefaultID {
		return fmt.Errorf("默认库不允许删除")
	}
	list, err := m.readIndex()
	if err != nil {
		return err
	}
	out := list[:0]
	var removed *Info
	for i := range list {
		if list[i].ID == id {
			removed = &list[i]
			continue
		}
		out = append(out, list[i])
	}
	if removed == nil {
		return fmt.Errorf("找不到 IP 库: %s", id)
	}
	if err := m.writeIndex(out); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.dir, removed.File))
	return nil
}

// Clear 清空指定库的全部条目。
func (m *Manager) Clear(id string) error {
	s, err := m.Open(id)
	if err != nil {
		return err
	}
	for _, e := range s.All() {
		s.RemoveKey(e.Key())
	}
	return s.Save()
}
