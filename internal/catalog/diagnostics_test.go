package catalog

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

func writeDiagnostics(t *testing.T, path string, records ...model.SourceDiagnostic) {
	t.Helper()
	var data strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	write(t, path, data.String())
}

func TestDiagnosticPatternsIntersectPathsContentAndIndividualRecords(t *testing.T) {
	cfg := fixture(t)
	saveRules(t, cfg, &rulepack.Package{})
	d := model.RuleDefinition{Name: "missing API", Description: "repair", PathPattern: "src/**/*.cs", ContentPattern: "Legacy", DiagnosticPattern: `^CS1061\b.*Missing`}
	writeRule(t, cfg, "R1", d)
	d.DiagnosticPattern = "Missing method"
	writeRule(t, cfg, "R2", d)
	for _, file := range []string{"src/a.cs", "src/b.cs", "src/c.cs", "src/no-diagnostic.cs", "outside/a.cs", "src/a.txt"} {
		content := "Legacy.Call();"
		if file == "src/b.cs" {
			content = "Modern.Call();"
		}
		write(t, filepath.Join(cfg.Root, file), content)
	}
	cfg.DiagnosticsPath = filepath.Join(cfg.Root, "diagnostics.jsonl")
	a := model.SourceDiagnostic{File: "src/a.cs", Code: "CS1061", Message: "Missing method", Line: 4}
	writeDiagnostics(t, cfg.DiagnosticsPath, a, a,
		model.SourceDiagnostic{File: "src/a.cs", Code: "CS9999", Message: "unrelated", Line: 8},
		model.SourceDiagnostic{File: "src/b.cs", Code: "CS1061", Message: "Missing method"},
		model.SourceDiagnostic{File: "src/c.cs", Code: "CS1061", Message: "unrelated"},
		model.SourceDiagnostic{File: "src/c.cs", Code: "CS9999", Message: "Missing member"},
		model.SourceDiagnostic{File: "outside/a.cs", Code: "CS1061", Message: "Missing method"},
		model.SourceDiagnostic{File: "src/a.txt", Code: "CS1061", Message: "Missing method"},
		model.SourceDiagnostic{File: "missing.cs", Code: "CS1061", Message: "Missing method"})
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := cat.Scan(context.Background(), cfg)
	if err != nil || scanned != 6 || excluded != 5 || len(tasks) != 1 {
		t.Fatalf("unexpected extraction: %+v %d/%d %v", tasks, scanned, excluded, err)
	}
	if tasks[0].File != "src/a.cs" || !reflect.DeepEqual(tasks[0].Rules, []string{"R1", "R2"}) || len(tasks[0].SourceDiagnostics) != 2 {
		t.Fatalf("wrong diagnostic scope or duplicate records: %+v", tasks[0])
	}
	want := a
	want.RuleIDs = []string{"R1", "R2"}
	if !reflect.DeepEqual(tasks[0].SourceDiagnostics[0], want) || cat.Rules[0].CandidateCount != 1 || cat.Rules[1].CandidateCount != 1 {
		t.Fatalf("wrong diagnostic association: %+v %+v", tasks[0], cat.Rules)
	}
	if len(tasks[0].SourceDiagnostics[1].RuleIDs) != 0 {
		t.Fatalf("unrelated diagnostic acquired a rule: %+v", tasks[0].SourceDiagnostics[1])
	}
	// Task evidence is an independent copy of the frozen catalog evidence.
	tasks[0].SourceDiagnostics[0].RuleIDs[0] = "changed"
	again, _, _, err := cat.Scan(context.Background(), cfg)
	if err != nil || !reflect.DeepEqual(again[0].SourceDiagnostics[0], want) {
		t.Fatalf("task mutation changed catalog evidence: %+v %v", again, err)
	}
}

func TestDiagnosticOnlyModeKeepsNormalRulesWithoutBroadeningDiagnosticRules(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "a.txt"), "plain")
	write(t, filepath.Join(cfg.Root, "b.txt"), "plain")
	writeRule(t, cfg, "R3", model.RuleDefinition{Name: "diagnostic", Description: "repair", DiagnosticPattern: "Missing"})
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 2 || !reflect.DeepEqual(tasks[0].Rules, []string{"R001"}) || cat.Rules[2].Always || cat.Rules[2].CandidateCount != 0 {
		t.Fatalf("no diagnostics broadened scope: %+v %v", tasks, err)
	}
	cfg.DiagnosticsPath = filepath.Join(t.TempDir(), "diagnostics.jsonl")
	writeDiagnostics(t, cfg.DiagnosticsPath, model.SourceDiagnostic{File: "a.txt", Message: "Missing"})
	cat, err = Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err = cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 2 || len(tasks[0].SourceDiagnostics) != 1 || len(tasks[1].SourceDiagnostics) != 0 || !reflect.DeepEqual(tasks[0].Rules, []string{"R001", "R3"}) {
		t.Fatalf("diagnostic import changed normal scope: %+v %v", tasks, err)
	}
	cfg.DiagnosticsOnly = true
	if _, _, _, err := cat.Scan(context.Background(), cfg); err == nil {
		t.Fatal("scan accepted changed diagnostic mode")
	}
	cat, err = Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 1 || scanned != 2 || excluded != 1 || tasks[0].File != "a.txt" || !reflect.DeepEqual(tasks[0].SourceDiagnostics[0].RuleIDs, []string{"R3"}) {
		t.Fatalf("diagnostic-only scope: %+v %d/%d %v", tasks, scanned, excluded, err)
	}
	cfg.DiagnosticsPath = ""
	cat, err = Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err = cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("diagnostic-only without evidence broadened scope: %+v %v", tasks, err)
	}
}

func TestDiagnosticEvidenceIsFrozenAndHasContentBasedProvenance(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "a.txt"), "plain")
	cfg.DiagnosticsPath = filepath.Join(t.TempDir(), "diagnostics.jsonl")
	writeDiagnostics(t, cfg.DiagnosticsPath, model.SourceDiagnostic{File: "a.txt", Message: "original", Code: "E1", Line: 3})
	first, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []model.SourceDiagnostic{
		{File: "a.txt", Message: "new", Code: "E1", Line: 3},
		{File: "a.txt", Message: "original", Code: "E2", Line: 3},
		{File: "a.txt", Message: "original", Code: "E1", Line: 4},
		{File: "b.txt", Message: "original", Code: "E1", Line: 3},
	} {
		writeDiagnostics(t, cfg.DiagnosticsPath, change)
		changed, err := Load(context.Background(), cfg)
		if err != nil || changed.Hash == first.Hash {
			t.Fatalf("changed evidence retained provenance: %+v %v", change, err)
		}
	}
	tasks, _, _, err := first.Scan(context.Background(), cfg)
	if err != nil || tasks[0].SourceDiagnostics[0].Message != "original" {
		t.Fatalf("external edit changed frozen evidence: %+v %v", tasks, err)
	}
	cfg.DiagnosticsPath = filepath.Join(t.TempDir(), "renamed.jsonl")
	writeDiagnostics(t, cfg.DiagnosticsPath, model.SourceDiagnostic{File: "a.txt", Message: "original", Code: "E1", Line: 3})
	renamed, err := Load(context.Background(), cfg)
	if err != nil || renamed.Hash != first.Hash {
		t.Fatalf("equivalent evidence in renamed file changed provenance: %v", err)
	}
	cfg.DiagnosticsOnly = true
	only, err := Load(context.Background(), cfg)
	if err != nil || only.Hash == first.Hash {
		t.Fatalf("diagnostic-only mode missing from provenance: %v", err)
	}
}

func TestImportedDiagnosticSourceIsExcludedAfterSnapshotCopy(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "a.txt"), "plain")
	write(t, filepath.Join(cfg.Root, "build-errors.jsonl"), "external report not source")
	cfg.DiagnosticsPath = filepath.Join(t.TempDir(), "snapshot.jsonl")
	cfg.DiagnosticsSourceFile = "build-errors.jsonl"
	writeDiagnostics(t, cfg.DiagnosticsPath, model.SourceDiagnostic{File: "a.txt", Message: "error"})
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, _, err := cat.Scan(context.Background(), cfg)
	if err != nil || scanned != 1 || len(tasks) != 1 || tasks[0].File != "a.txt" {
		t.Fatalf("import source was not excluded: %+v %v", tasks, err)
	}
	cfg.DiagnosticsSourceFile = ""
	if _, _, _, err = cat.Scan(context.Background(), cfg); err == nil {
		t.Fatal("changed import exclusion reused old scope")
	}
	changed, err := Load(context.Background(), cfg)
	if err != nil || changed.Hash == cat.Hash {
		t.Fatalf("exclusion omitted from provenance: %v", err)
	}
}
