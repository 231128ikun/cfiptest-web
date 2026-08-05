package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerMigratesLegacy(t *testing.T) {
	dir := t.TempDir()
	// 旧版单文件库
	legacy := filepath.Join(dir, FileName)
	body := "{\"ip\":\"1.1.1.1\",\"port\":443,\"status\":\"active\"}\n"
	if err := os.WriteFile(legacy, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil || len(list) != 2 || list[0].ID != DefaultID || list[0].Name != "默认库" {
		t.Fatalf("迁移后应有默认库+官方库: %+v err=%v", list, err)
	}
	foundOfficial := false
	for _, info := range list {
		if info.ID == OfficialID && info.Type == "official" {
			foundOfficial = true
		}
	}
	if !foundOfficial {
		t.Fatalf("官方 IP 库（远程库）应自动注册: %+v", list)
	}
	s, err := m.Open(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("默认库应包含旧数据: %d", s.Len())
	}
	if s.BaseDir() != dir {
		t.Fatalf("baseDir 应为数据目录: %q", s.BaseDir())
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("旧文件应已移除")
	}
}

func TestManagerCreateRenameDelete(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.Create("备用库")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID == "" || info.File == "" || info.Name != "备用库" {
		t.Fatalf("创建库信息错误: %+v", info)
	}
	s, err := m.Open(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Upsert(Entry{IP: "2.2.2.2", Port: 443, Status: StatusActive})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// 改名不破坏引用
	if err := m.Rename(info.ID, "日本备用"); err != nil {
		t.Fatal(err)
	}
	s2, err := m.Open(info.ID)
	if err != nil {
		t.Fatalf("改名后仍可打开: %v", err)
	}
	if s2.Len() != 1 {
		t.Fatal("改名后数据应保留")
	}
	// 重名自动加后缀
	info2, _ := m.Create("日本备用")
	if info2.Name == info.Name {
		t.Fatalf("重名应自动区分: %+v vs %+v", info2, info)
	}
	// 删除
	if err := m.Delete(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.fileOf(info.ID); ok {
		t.Fatal("删除后不应存在")
	}
	// 默认库不可删
	if err := m.Delete(DefaultID); err == nil {
		t.Fatal("默认库不应允许删除")
	}
}

func TestManagerOfficialRemoteLibrary(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 官方库可打开并写入（远程同步会把数据放进来）
	s, err := m.Open(OfficialID)
	if err != nil {
		t.Fatalf("官方库应可打开: %v", err)
	}
	s.Upsert(Entry{IP: "1.1.1.1", Port: 443, Status: StatusNew, Source: SourceOfficial})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("官方库应保存条目: %d", s.Len())
	}
	// 官方库不可删除、不可重命名（远程库）
	if err := m.Delete(OfficialID); err == nil {
		t.Fatal("官方库不应允许删除")
	}
	if err := m.Rename(OfficialID, "改名"); err == nil {
		t.Fatal("官方库不应允许重命名")
	}
	// 清空允许（之后可重新同步）
	if err := m.Clear(OfficialID); err != nil {
		t.Fatal(err)
	}
	// 再次打开管理器，官方库仍在
	m2, err := OpenManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m2.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range list {
		if info.ID == OfficialID {
			return
		}
	}
	t.Fatalf("官方库应在索引中持久存在: %+v", list)
}

func TestManagerNameSanitize(t *testing.T) {
	cases := map[string]string{
		"美国 日本": "美国 日本",
		"a/b\\c": "abc",
		"":    "库",
		"上海-2": "上海-2",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Fatalf("SanitizeName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestManagerAutoDiscoversFiles(t *testing.T) {
	dir := t.TempDir()
	m, _ := OpenManager(dir)
	// 用户直接放入一个 jsonl 文件（未注册进 index）
	if err := os.WriteFile(filepath.Join(dir, ManagerDir, "手工库.jsonl"), []byte("{\"ip\":\"8.8.8.8\",\"port\":443,\"status\":\"active\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range list {
		if info.Name == "手工库" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应自动发现放入的文件: %+v", list)
	}
	s, err := m.Open("default")
	if err != nil || s.Len() != 0 {
		t.Fatalf("默认库不应被干扰: %v", err)
	}
}

func TestManagerClear(t *testing.T) {
	dir := t.TempDir()
	m, _ := OpenManager(dir)
	info, _ := m.Create("x")
	s, _ := m.Open(info.ID)
	s.Upsert(Entry{IP: "9.9.9.9", Port: 80, Status: StatusActive})
	s.Save()
	if err := m.Clear(info.ID); err != nil {
		t.Fatal(err)
	}
	s2, _ := m.Open(info.ID)
	if s2.Len() != 0 {
		t.Fatal("清空后应为 0 条")
	}
}
