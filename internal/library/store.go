package library

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileName 是 IP 库文件名（位于 data/ 目录）。
const FileName = "ipdb.jsonl"

// Store 是一个内存映射 + JSONL 落盘的 IP 库。
// 个人量级（几万条以内）全量读入内存与整体重写都足够快，故不做增量写。
type Store struct {
	path string
	mu   sync.RWMutex
	by   map[string]*Entry
}

// Open 加载 dataDir/ipdb.jsonl；文件不存在时返回空库（不报错）。
// 损坏的行会被跳过并计数，避免单行错误导致整个库不可用。
func Open(dataDir string) (*Store, error) {
	s := &Store{
		path: filepath.Join(dataDir, FileName),
		by:   make(map[string]*Entry),
	}
	body, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取 IP 库失败: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	lineNo := 0
	skipped := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			skipped++
			fmt.Printf("警告: %s 第 %d 行解析失败，已跳过: %v\n", FileName, lineNo, err)
			continue
		}
		if e.IP == "" || e.Status == "" {
			skipped++
			continue
		}
		s.by[e.Key()] = &e
	}
	if skipped > 0 {
		fmt.Printf("警告: IP 库共跳过 %d 行损坏数据\n", skipped)
	}
	return s, nil
}

// Path 返回库文件完整路径。
func (s *Store) Path() string { return s.path }

// Dir 返回库所在目录。
func (s *Store) Dir() string { return filepath.Dir(s.path) }

// Len 返回条目总数。
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.by)
}

// Get 返回指定键的条目。
func (s *Store) Get(ip string, port int) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.by[Key(ip, port)]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Upsert 写入一条记录：已存在则整体替换（保留原 FirstSeenAt 与 Checks 之外的字段由调用方负责）。
// 返回是否为新条目。若新条目 FirstSeenAt 为空则设为当前时间。
func (s *Store) Upsert(e Entry) bool {
	if e.FirstSeenAt.IsZero() {
		e.FirstSeenAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := e.Key()
	_, exists := s.by[key]
	s.by[key] = &e
	return !exists
}

// Remove 按 ip:port 删除条目，返回是否命中。
func (s *Store) Remove(ip string, port int) bool {
	return s.RemoveKey(Key(ip, port))
}

// RemoveKey 按 key 删除条目，返回是否命中。
func (s *Store) RemoveKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.by[key]; !ok {
		return false
	}
	delete(s.by, key)
	return true
}

// All 返回按 (ip, port) 排序的全量快照。
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.by))
	for _, e := range s.by {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IP != out[j].IP {
			return out[i].IP < out[j].IP
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// Stats 是库的概览统计。
type Stats struct {
	Total      int            `json:"total"`
	Active     int            `json:"active"`
	New        int            `json:"new"`
	ByCountry  map[string]int `json:"byCountry"` // countryCode -> 数量
	SpeedValid int            `json:"speedValid"`
}

// Stats 汇总当前库。
func (s *Store) Stats() Stats {
	st := Stats{ByCountry: make(map[string]int)}
	s.mu.RLock()
	defer s.mu.RUnlock()
	st.Total = len(s.by)
	for _, e := range s.by {
		switch e.Status {
		case StatusActive:
			st.Active++
		case StatusNew:
			st.New++
		}
		if e.SpeedValid {
			st.SpeedValid++
		}
		cc := e.CountryCode
		if cc == "" {
			cc = "unknown"
		}
		st.ByCountry[cc]++
	}
	return st
}

// Save 将全量条目原子写回 JSONL（按 key 排序，输出稳定）。
func (s *Store) Save() error {
	s.mu.RLock()
	entries := make([]*Entry, 0, len(s.by))
	for _, e := range s.by {
		cp := *e
		entries = append(entries, &cp)
	}
	s.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IP != entries[j].IP {
			return entries[i].IP < entries[j].IP
		}
		return entries[i].Port < entries[j].Port
	})

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ipdb-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
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
	return os.Rename(tmpPath, s.path)
}


