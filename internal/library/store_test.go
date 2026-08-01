package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"iptest-web/internal/engine"
)

func TestOpenEmptyWhenMissing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("Open 空目录失败: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("期望空库，实际 %d 条", s.Len())
	}
}

func TestUpsertSaveReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := Entry{
		IP: "1.2.3.4", Port: 443,
		CountryCode: "US", Country: "美国", Emoji: "🇺🇸",
		TCPLatencyMs: 120, SpeedKBs: 5000, SpeedValid: true,
		Source: SourceImport, Status: StatusActive,
		FirstSeenAt: now, LastCheckedAt: now, Checks: 1,
	}
	if created := s.Upsert(e); !created {
		t.Fatal("首次 Upsert 应返回 created=true")
	}
	// 更新同键：覆盖 country 等字段
	e2 := e
	e2.CountryCode = "JP"
	e2.Country = "日本"
	e2.TCPLatencyMs = 80
	if created := s.Upsert(e2); created {
		t.Fatal("重复 Upsert 应返回 created=false")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("库文件未生成: %v", err)
	}

	s2, err := Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if s2.Len() != 1 {
		t.Fatalf("重载后期望 1 条，实际 %d", s2.Len())
	}
	got, ok := s2.Get("1.2.3.4", 443)
	if !ok {
		t.Fatal("Get 未命中")
	}
	if got.Country != "日本" || got.CountryCode != "JP" {
		t.Fatalf("覆盖写未生效: %+v", got)
	}
	if got.FirstSeenAt.IsZero() {
		t.Fatal("FirstSeenAt 不应为空")
	}
}

func TestRemove(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), FileName))
	s.Upsert(Entry{IP: "1.1.1.1", Port: 443, Status: StatusNew})
	if !s.Remove("1.1.1.1", 443) {
		t.Fatal("Remove 应命中")
	}
	if s.Remove("1.1.1.1", 443) {
		t.Fatal("重复 Remove 不应命中")
	}
	if s.Len() != 0 {
		t.Fatal("删除后应为空")
	}
}

func TestStats(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), FileName))
	s.Upsert(Entry{IP: "1.0.0.1", Port: 443, Status: StatusActive, CountryCode: "US", SpeedValid: true})
	s.Upsert(Entry{IP: "1.0.0.2", Port: 443, Status: StatusActive, CountryCode: "JP"})
	s.Upsert(Entry{IP: "1.0.0.3", Port: 443, Status: StatusNew})
	st := s.Stats()
	if st.Total != 3 || st.Active != 2 || st.New != 1 || st.SpeedValid != 1 {
		t.Fatalf("统计错误: %+v", st)
	}
	if st.ByCountry["US"] != 1 || st.ByCountry["JP"] != 1 || st.ByCountry["unknown"] != 1 {
		t.Fatalf("国家统计错误: %+v", st.ByCountry)
	}
}

func TestSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	body := "{\"ip\":\"1.1.1.1\",\"port\":443,\"status\":\"active\"}\nnot-json\n{\"ip\":\"2.2.2.2\",\"port\":80,\"status\":\"active\"}\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("期望跳过损坏行后剩 2 条，实际 %d", s.Len())
	}
}

func TestEntryFromResult(t *testing.T) {
	now := time.Now()
	e := EntryFromResult(engine.Result{
		IP: "1.2.3.4", Port: 443, CountryCode: "US", Country: "美国", CityZh: "洛杉矶",
		Emoji: "🇺🇸", DataCenter: "LAX", ASN: 13335, ASNOrg: "Cloudflare, Inc.",
		TCPLatencyMs: 88, DownloadSpeedKBs: 5200,
	}, now)
	if e.Status != StatusActive || e.CountryCode != "US" || !e.SpeedValid || e.SpeedKBs != 5200 {
		t.Fatalf("结果转条目错误: %+v", e)
	}
	if e.TCPLatencyMs != 88 || e.Checks != 1 || e.FirstSeenAt.IsZero() {
		t.Fatalf("字段错误: %+v", e)
	}
	// 未测速时 SpeedValid 应为 false
	e2 := EntryFromResult(engine.Result{IP: "5.6.7.8", Port: 80}, now)
	if e2.SpeedValid {
		t.Fatal("未测速结果不应标记 SpeedValid")
	}
}

// 回归：trace 的 loc 是本机国家，不能当作 IP 国家；CountryCode 必须来自边缘节点 cca2。
func TestEntryFromResultUsesEdgeCountryNotTraceLoc(t *testing.T) {
	now := time.Now()
	// 模拟：本机在中国（loc=CN），边缘节点是洛杉矶（colo=LAX, cca2=US）
	e := EntryFromResult(engine.Result{
		IP: "104.16.0.1", Port: 443,
		LocCode: "CN",            // 访客/本机国家（不得用作 IP 国家）
		CountryCode: "US",        // 边缘节点国家码
		Country: "美国", CityZh: "洛杉矶",
	}, now)
	if e.CountryCode != "US" {
		t.Fatalf("入库国家码应来自边缘节点 cca2(US)，实际 %q（trace 的 loc=CN 是本机国家，不应作为 IP 国家）", e.CountryCode)
	}
}

func TestUpsertFillsFirstSeen(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), FileName))
	created := s.Upsert(Entry{IP: "9.9.9.9", Port: 8443, Status: StatusNew})
	if !created {
		t.Fatal("应创建新条目")
	}
	got, _ := s.Get("9.9.9.9", 8443)
	if got.FirstSeenAt.IsZero() {
		t.Fatal("Upsert 应自动填充 FirstSeenAt")
	}
}
