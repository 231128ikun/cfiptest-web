package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"iptest-web/internal/subscription"
)

// RunsFile 是运行历史文件名。
const RunsFile = "runs.jsonl"

// RunRecord 是一次维护运行的落盘记录。
type RunRecord struct {
	TaskID      string                        `json:"taskId"`
	Name        string                        `json:"name"`
	Status      string                        `json:"status"` // completed | stopped | error
	Error       string                        `json:"error,omitempty"`
	StartedAt   time.Time                     `json:"startedAt"`
	FinishedAt  time.Time                     `json:"finishedAt"`
	DurationMs  int64                         `json:"durationMs"`
	OutputPath  string                        `json:"outputPath"`
	TotalLines  int                           `json:"totalLines"`
	RemovedDead int                           `json:"removedDead"`
	InputAdded  int                           `json:"inputAdded"`
	Shortages   []string                      `json:"shortages"`
	Groups      []subscription.GroupReport    `json:"groups"`
}

// recordFromReport 从编排报告构造运行记录。
func recordFromReport(report *subscription.Report, status, errMsg string) RunRecord {
	if report == nil {
		return RunRecord{Status: status, Error: errMsg, StartedAt: time.Now(), FinishedAt: time.Now()}
	}
	return RunRecord{
		TaskID:      report.TaskID,
		Name:        report.Subscription,
		Status:      status,
		Error:       errMsg,
		StartedAt:   report.StartedAt,
		FinishedAt:  report.FinishedAt,
		DurationMs:  report.DurationMs,
		OutputPath:  report.OutputPath,
		TotalLines:  report.TotalLines,
		RemovedDead: report.RemovedDead,
		InputAdded:  report.InputAdded,
		Shortages:   report.Shortages,
		Groups:      report.Groups,
	}
}

// appendRun 追加一条运行记录到 data/runs.jsonl。
func appendRun(dataDir string, rec RunRecord) error {
	path := filepath.Join(dataDir, RunsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// listRuns 读取运行历史（最新在前，最多 limit 条）。
func listRuns(dataDir string, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	body, err := os.ReadFile(filepath.Join(dataDir, RunsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var recs []RunRecord
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
	}
	// 反转：最新在前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	return recs, nil
}

var _ = fmt.Sprint
