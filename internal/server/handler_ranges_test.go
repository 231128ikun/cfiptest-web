package server

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOfficialRangeCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want4 := []string{"104.16.0.0/13", "172.64.0.0/13"}
	want6 := []string{"2606:4700::/48", "2606:4700:1::/48"}
	if err := saveOfficialRangeCache(dir, want4, want6); err != nil {
		t.Fatal(err)
	}
	got4, got6, updated, err := loadOfficialRangeCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got4, want4) || !reflect.DeepEqual(got6, want6) {
		t.Fatalf("cache mismatch: %v %v", got4, got6)
	}
	if updated == "" {
		t.Fatal("missing cache timestamp")
	}
	for _, name := range []string{officialIPv4File, officialIPv6File} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// 版本标记必须独占一行：第一条 CIDR 不能粘在标记后面被当作注释吞掉。
	for _, name := range []string{officialIPv4File, officialIPv6File} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(raw), officialCacheMarker+"\n") {
			t.Fatalf("%s 缺少独立版本标记行: %q", name, string(raw)[:30])
		}
	}
}

// TestOfficialRangeCacheV6RejectsLegacy 旧版 IPv6 缓存（无版本标记的官方
// 聚合 /32 段）是「抽样一个都不成功」的数据源，加载时必须拒绝并回退到
// baipiao 活跃 /48 内置列表。
func TestOfficialRangeCacheV6RejectsLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, officialIPv6File), []byte("2606:4700::/32\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOfficialRangeCacheFamily(dir, officialIPv6File, true); err == nil {
		t.Fatal("旧版 IPv6 缓存（无版本标记）应被拒绝，避免继续抽样必然全失败的聚合段")
	}
}

func TestParseCIDRFileRejectsWrongFamily(t *testing.T) {
	if _, err := parseCIDRFile("2606:4700::/32\n", false); err == nil {
		t.Fatal("IPv6 accepted as IPv4")
	}
	if _, err := parseCIDRFile("104.16.0.0/13\n", true); err == nil {
		t.Fatal("IPv4 accepted as IPv6")
	}
}
