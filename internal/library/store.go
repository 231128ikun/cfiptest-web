package library

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileName 是 IP 库文件名（位于 data/ 目录）。
const FileName = "ipdb.jsonl"

// Store 是一个内存映射 + JSONL 落盘的 IP 库。
// 个人量级（几万条以内）全量读入内存与整体重写都足够快，故不做增量写。
type Store struct {
	path    string // JSONL 文件完整路径
	baseDir string // 应用数据目录（输出订阅文件等相对该目录）
	memory  bool   // true = 仅内存库（不落盘，Save 为空操作）
	mu      sync.RWMutex
	by      map[string]*Entry
}

// Open 加载指定路径的 JSONL 库；文件不存在时返回空库（不报错）。
// 损坏的行会被跳过并计数，避免单行错误导致整个库不可用。
// 库文件所在目录同时作为 baseDir（输出文件默认相对它）。
func Open(path string) (*Store, error) {
	s := &Store{
		path:    path,
		baseDir: filepath.Dir(path),
		by:      make(map[string]*Entry),
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

// NewInMemory 创建仅内存的 IP 库（不落盘，Save 为空操作）。
// 用于远程维护来源（官方 IP 段 / 远程 URL 库）：候选来自远程拉取，结果只写出、不保存到本地库。
func NewInMemory(baseDir string, entries []Entry) *Store {
	s := &Store{baseDir: baseDir, memory: true, by: make(map[string]*Entry, len(entries))}
	for i := range entries {
		e := entries[i]
		if e.FirstSeenAt.IsZero() {
			e.FirstSeenAt = time.Now()
		}
		s.by[e.Key()] = &e
	}
	return s
}

// Path 返回库文件完整路径。
func (s *Store) Path() string { return s.path }

// BaseDir 返回应用数据目录（输出订阅文件等相对它）。
func (s *Store) BaseDir() string { return s.baseDir }

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
	if e.DownloadSpeedKBs == 0 && e.SpeedKBs != 0 {
		e.DownloadSpeedKBs = e.SpeedKBs
	}
	e.SpeedKBs = e.DownloadSpeedKBs
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
	sort.Slice(out, func(i, j int) bool { return entryLess(&out[i], &out[j]) })
	return out
}

// entryLess 按 (ip, port) 比较条目，供 All / Save 共用，保证输出稳定。
func entryLess(a, b *Entry) bool {
	if a.IP != b.IP {
		return a.IP < b.IP
	}
	return a.Port < b.Port
}

// Stats 是库的概览统计（含按字段分布，各取 Top N）。
type Stats struct {
	Total      int            `json:"total"`
	Active     int            `json:"active"`
	New        int            `json:"new"`
	SpeedValid int            `json:"speedValid"`
	ByCountry  map[string]int `json:"byCountry"` // countryCode -> 数量
	ByCity     map[string]int `json:"byCity"`
	ByDC       map[string]int `json:"byDC"`   // 数据中心 IATA
	ByASN      map[string]int `json:"byASN"`  // ASN 数字串 -> 数量
	ByPort     map[string]int `json:"byPort"` // 端口 -> 数量
}

const statsTopN = 30

// Stats 汇总当前库。
func (s *Store) Stats() Stats {
	st := Stats{
		ByCountry: make(map[string]int),
		ByCity:    make(map[string]int),
		ByDC:      make(map[string]int),
		ByASN:     make(map[string]int),
		ByPort:    make(map[string]int),
	}
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
		if e.CityZh != "" {
			st.ByCity[e.CityZh]++
		}
		if e.DataCenter != "" {
			st.ByDC[e.DataCenter]++
		}
		if e.ASN != 0 {
			st.ByASN[strconv.FormatUint(uint64(e.ASN), 10)]++
		}
		st.ByPort[strconv.Itoa(e.Port)]++
	}
	st.ByCountry = topN(st.ByCountry, statsTopN)
	st.ByCity = topN(st.ByCity, statsTopN)
	st.ByDC = topN(st.ByDC, statsTopN)
	st.ByASN = topN(st.ByASN, statsTopN)
	st.ByPort = topN(st.ByPort, statsTopN)
	return st
}

// topN 按数量降序取前 n 项。
func topN(m map[string]int, n int) map[string]int {
	if len(m) <= n {
		return m
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(m))
	for k, v := range m {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	out := make(map[string]int, n)
	for i := 0; i < n; i++ {
		out[list[i].k] = list[i].v
	}
	return out
}

// Save 将全量条目原子写回 JSONL（按 key 排序，输出稳定）。
// 仅内存库（远程维护来源）为空操作：候选来自远程拉取，结果只写出、不保存到本地库。
func (s *Store) Save() error {
	if s.memory {
		return nil
	}
	s.mu.RLock()
	entries := make([]*Entry, 0, len(s.by))
	for _, e := range s.by {
		cp := *e
		entries = append(entries, &cp)
	}
	s.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entryLess(entries[i], entries[j]) })

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
