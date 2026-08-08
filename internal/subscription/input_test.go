package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"iptest-web/internal/library"
)

func TestRunImportsInputFileWithoutOverwritingIt(t *testing.T) {
	dir := t.TempDir()
	inputDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(inputDir, "原订阅.txt")
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
	spec := testRun{
		Name:      "x",
		InputPath: "out/原订阅.txt",
		Groups: []Group{
			{Name: "美国", CountryCode: "US", Count: 2},
			{Name: "日本", CountryCode: "JP", Count: 1},
		},
		Output: Output{Path: "out/维护结果.txt", Template: "{ip}:{port}"},
	}
	report, err := runTest(t.Context(), fake, lib, spec, RunOptions{}, nil)
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	if report.InputAdded != 3 || report.TotalLines != 3 {
		t.Fatalf("导入或输出数量错误: %+v", report)
	}
	wantOutput := filepath.Join(inputDir, "维护结果.txt")
	if report.OutputPath != wantOutput {
		t.Fatalf("输出位置错误: got=%q want=%q", report.OutputPath, wantOutput)
	}
	if original, err := os.ReadFile(inputPath); err != nil || string(original) != body {
		t.Fatalf("初始化文件不应被覆盖: err=%v content=%q", err, original)
	}
	if output, err := os.ReadFile(wantOutput); err != nil || len(output) == 0 {
		t.Fatalf("维护结果未写出: err=%v", err)
	}
	if entry, ok := lib.Get("1.0.0.11", 443); !ok || entry.Status != library.StatusActive {
		t.Fatalf("输入文件 IP 未入库: %+v", entry)
	}
}

func TestRunImportsAbsoluteInputFile(t *testing.T) {
	dir := t.TempDir()
	seedDir := filepath.Join(dir, "外部文件夹")
	if err := os.MkdirAll(seedDir, 0755); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(seedDir, "seed.txt")
	body := "1.0.0.21:443#外部绝对路径\n1.0.0.22:2053\n"
	if err := os.WriteFile(seed, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	fake := newFake()
	fake.add("1.0.0.21", 443, "JP", true, 0)
	fake.add("1.0.0.22", 2053, "US", true, 0)

	lib, err := library.Open(filepath.Join(dir, library.FileName))
	if err != nil {
		t.Fatal(err)
	}
	spec := testRun{
		Name:      "absolute",
		InputPath: seed, // 服务器绝对路径（data 目录之外）
		Groups: []Group{
			{Name: "日本", CountryCode: "JP", Count: 1},
			{Name: "美国", CountryCode: "US", Count: 1},
		},
		Output: Output{Path: "out/result.txt", Template: "{ip}:{port}"},
	}
	report, err := runTest(t.Context(), fake, lib, spec, RunOptions{}, nil)
	if err != nil {
		t.Fatalf("绝对路径输入应可读取: %v", err)
	}
	if report.InputAdded != 2 || report.TotalLines != 2 {
		t.Fatalf("绝对路径导入数量错误: %+v", report)
	}
	if _, ok := lib.Get("1.0.0.21", 443); !ok {
		t.Fatal("绝对路径文件 IP 未入库")
	}
}

func TestRunRejectsInputTraversal(t *testing.T) {
	dir := t.TempDir()
	lib, _ := library.Open(filepath.Join(dir, library.FileName))
	spec := testRun{
		Name: "x", InputPath: "../evil.txt", Groups: []Group{{Name: "g", Count: 1}},
		Output: Output{Path: "out/result.txt", Template: "{ip}:{port}"},
	}
	if _, err := runTest(t.Context(), newFake(), lib, spec, RunOptions{}, nil); err == nil {
		t.Fatal("目录穿越应被拒绝")
	}
}
