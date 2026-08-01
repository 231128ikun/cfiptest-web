package subscription

import (
	"testing"

	"iptest-web/internal/library"
)

func TestExpandTaskRulesExtendedFields(t *testing.T) {
	task := Task{Name: "x", Rules: []TaskRule{{
		Name: "美国西岸", Limit: 5,
		Conditions: []Condition{
			{Field: "dataCenter", Values: []string{"LAX", "SJC"}},
			{Field: "asn", Values: []string{"13335"}},
			{Field: "region", Values: []string{"北美洲"}},
		},
	}}}
	groups, err := expandTaskRules(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("应展开 2 个组合（LAX/SJC），实际 %d", len(groups))
	}
	g := groups[0]
	if len(g.DataCenters) != 1 || g.DataCenters[0] != "LAX" || len(g.ASNs) != 1 || g.ASNs[0] != 13335 || len(g.Regions) != 1 || g.Regions[0] != "北美洲" {
		t.Fatalf("扩展字段映射错误: %+v", g)
	}
	// 非法 ASN
	bad := Task{Name: "x", Rules: []TaskRule{{Conditions: []Condition{{Field: "asn", Values: []string{"abc"}}}}}}
	if _, err := expandTaskRules(bad); err == nil {
		t.Fatal("非法 ASN 应报错")
	}
}

func TestSoftMatchExtendedFields(t *testing.T) {
	g := Group{DataCenters: []string{"LAX"}, ASNs: []uint{13335}, Regions: []string{"北美洲"}}
	e := library.Entry{IP: "1.1.1.1", Port: 443, DataCenter: "LAX", ASN: 13335, RegionZh: "北美洲"}
	if !g.SoftMatch(e) {
		t.Fatal("匹配字段应通过粗筛")
	}
	if g.CandidatePriority(e) != 2 {
		t.Fatalf("已知匹配应为 2: %d", g.CandidatePriority(e))
	}
	e2 := library.Entry{IP: "1.1.1.2", Port: 443} // 全未知
	if !g.SoftMatch(e2) {
		t.Fatal("字段未知应仍为候选")
	}
	if g.CandidatePriority(e2) != 1 {
		t.Fatalf("字段未知应为 1: %d", g.CandidatePriority(e2))
	}
	e3 := library.Entry{IP: "1.1.1.3", Port: 443, DataCenter: "NRT", ASN: 13335, RegionZh: "亚洲"}
	if g.SoftMatch(e3) {
		t.Fatal("数据中心已知但不匹配应排除")
	}
}
