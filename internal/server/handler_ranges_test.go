package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOfficialRangeCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want4 := []string{"104.16.0.0/13", "172.64.0.0/13"}
	want6 := []string{"2606:4700::/32"}
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
}

func TestParseCIDRFileRejectsWrongFamily(t *testing.T) {
	if _, err := parseCIDRFile("2606:4700::/32\n", false); err == nil {
		t.Fatal("IPv6 accepted as IPv4")
	}
	if _, err := parseCIDRFile("104.16.0.0/13\n", true); err == nil {
		t.Fatal("IPv4 accepted as IPv6")
	}
}
