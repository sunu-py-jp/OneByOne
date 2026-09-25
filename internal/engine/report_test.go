package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestExportReportUsesSelectedFileAndReplacesExistingReport(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	want := filepath.Join(t.TempDir(), "選択した結果.json")
	writeTest(t, want, []byte("previous report"))
	queuePath := s.Snapshot().Config.QueuePath
	queueBefore := readTest(t, queuePath)
	got, err := s.ExportReport(want)
	if err != nil || got != want {
		t.Fatalf("selected report path not used: %q, %v", got, err)
	}
	body := readTest(t, got)
	var report model.State
	if err := json.Unmarshal([]byte(body), &report); err != nil || len(report.Tasks) != 1 || report.Tasks[0].File != "A.txt" {
		t.Fatalf("report did not contain the current results: %v", err)
	}
	if strings.Contains(body, cfg.Credential) || len(report.LLMConnections) != 0 || report.SelectedLLMConnectionID != "" {
		t.Fatal("export contained personal connection information")
	}
	if readTest(t, queuePath) != queueBefore || s.Snapshot().Config.QueuePath != queuePath {
		t.Fatal("export changed the working queue")
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(queuePath), "reports"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("custom export also wrote to the default location: %v", err)
	}
}

func TestExportReportCannotOverwriteWorkspaceState(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg := s.Snapshot().Config
	setting, err := s.workspacePath(s.Snapshot().ActiveWorkspaceID, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.configPath, setting, cfg.QueuePath, cfg.QueuePath + ".session.json"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			before := readTest(t, path)
			if _, err := s.ExportReport(path); err == nil {
				t.Fatal("export accepted a managed state file")
			}
			if readTest(t, path) != before {
				t.Fatal("export damaged workspace state")
			}
		})
	}
}
