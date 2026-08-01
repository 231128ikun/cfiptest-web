package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"iptest-web/internal/library"
)

func TestValidateOutputDefaultsToInput(t *testing.T) {
	s := Subscription{Name: "x", InputPath: "out/原订阅.txt", Groups: []Group{{Name: "g", Count: 1}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Output.Path != filepath.FromSlash("out/原订阅.txt") {
		t.Fatalf("未指定输出时应回写原输入文件: %q", s.Output.Path)
	}
}

func TestRunImportsInputFile(t *testing.T) {
	dir := t.TempDir()
	// 原订阅文件：ip:port#备注
	input := filepath.Join(dir, "out")
	if err := os.MkdirAll(input, 0755); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(input, "原订阅.txt")
	body := "1.0.0.11:443#美国-洛杉矶\n1.0.0.12:443#日本-东京\n2.0.0.13:2053\n"
	if err := os.WriteFile(inputPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	fake := newFake()
	fake.add("1.0.0.11", 443, "US", true, 0)
	fake.add("1.0.0.12", 443, "JP", true, 0)
	fake.add("2.0.0.13", 2053, "US", true, 0)

	lib, err := library.Open(filepath.Join(dir, library.FileName))
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{
		Name:      "x",
		InputPath: "out/原订阅.txt",
		Groups: []Group{
			{Name: "美国", CountryCode: "US", Count: 2},
			{Name: "日本", CountryCode: "JP", Count: 1},
		},
	}
	report, err := Run(t.Context(), fake, lib, sub, RunOptions{}, nil)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if report.InputAdded != 3 {
		t.Fatalf("应导入 3 条: %+v", report)
	}
	// 输出文件默认回写原订阅文件
	if report.OutputPath != inputPath {
		t.Fatalf("输出应回写原文件: %q", report.OutputPath)
	}
	if report.TotalLines != 3 {
		t.Fatalf("应输出 3 行: %+v", report)
	}
	out, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("原订阅文件应被更新")
	}
	// 库中状态应为 active
	if e, ok := lib.Get("1.0.0.11", 443); !ok || e.Status != library.StatusActive {
		t.Fatalf("输入文件 IP 未入库: %+v", e)
	}
}

func TestRunRejectsInputTraversal(t *testing.T) {
	dir := t.TempDir()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	sub := Subscription{Name: "x", InputPath: "../evil.txt", Groups: []Group{{Name: "g", Count: 1}}}
	if _, err := Run(t.Context(), newFake(), lib, sub, RunOptions{}, nil); err == nil {
		t.Fatal("目录穿越应被拒绝")
	}
}
