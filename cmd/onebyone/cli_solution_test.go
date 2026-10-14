package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestCLISolutionSettingsPreserveNestedDefaultsAndRejectInvalidPatches(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	update := func(body string) error {
		_, err := executeSettingsCommand(context.Background(), s, []string{"update", "--input", "-"}, strings.NewReader(body))
		return err
	}
	if err := update(`{"mode":"solution","solution":{"path":"Legacy.sln"}}`); err != nil {
		t.Fatal(err)
	}
	cfg := s.Snapshot().Config
	if !cfg.SolutionMode() || cfg.Solution == nil || cfg.Solution.Path != "Legacy.sln" || cfg.Solution.MaxIterations != 20 || cfg.Solution.BuildTimeoutSeconds != 300 {
		t.Fatalf("partial solution settings did not preserve bounded defaults: %+v", cfg.Solution)
	}
	if err := update(`{"solution":{"maxIterations":0,"configuration":"Release"}}`); err != nil {
		t.Fatal(err)
	}
	if err := update(`{"inputPricePerMillion":2}`); err != nil {
		t.Fatal(err)
	}
	cfg = s.Snapshot().Config
	if cfg.Solution.Path != "Legacy.sln" || cfg.Solution.MaxIterations != 0 || cfg.Solution.Configuration != "Release" || cfg.Solution.Platform != "Any CPU" {
		t.Fatalf("nested patch reset other settings: %+v", cfg.Solution)
	}
	shown, err := executeSettingsCommand(context.Background(), s, []string{"show"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var shownSolution model.SolutionSettings
	fields := shown.(map[string]json.RawMessage)
	if string(fields["mode"]) != `"solution"` || json.Unmarshal(fields["solution"], &shownSolution) != nil || !reflect.DeepEqual(shownSolution, *cfg.Solution) {
		t.Fatal("settings show does not round-trip solution settings")
	}
	before, _ := json.Marshal(cfg)
	for _, body := range []string{`{"solution":{"maxIterations":null}}`, `{"solution":{"path":null}}`, `{"solution":null}`, `{"solution":[]}`, `{"solution":{"unknown":1}}`, `{"solution":{"path":"../outside.sln"}}`, `{"solution":{"maxIterations":-1}}`, `{"solution":{"buildTimeoutSeconds":86401}}`, `{"mode":"invalid"}`} {
		if err := update(body); err == nil {
			t.Fatalf("invalid solution patch accepted: %s", body)
		}
		after, _ := json.Marshal(s.Snapshot().Config)
		if !bytes.Equal(before, after) {
			t.Fatalf("rejected nested update mutated saved settings: %s", body)
		}
	}
}

func TestCLISolutionRunExitCodeUsesSolutionOutcomeEvenWithoutChangedTasks(t *testing.T) {
	for _, item := range []struct {
		status string
		want   int
	}{{"completed", 0}, {"needs_human", 2}, {"failed", 2}, {"stopped", 130}, {"running", 1}} {
		result := model.ExecutionRunResult{Run: model.ExecutionRun{Status: "completed"}, State: model.State{Config: model.Config{Mode: "solution"}, SolutionRun: &model.SolutionRun{Status: item.status}}}
		if item.status == "failed" {
			result.Run.Status, result.Run.Error = "failed", "compiler failed"
		}
		if got := executionResultExitCode(result); got != item.want {
			t.Fatalf("%s: got %d, want %d", item.status, got, item.want)
		}
	}
	if got := executionResultExitCode(model.ExecutionRunResult{Run: model.ExecutionRun{Status: "failed", Error: "preparation failed"}, State: model.State{Config: model.Config{Mode: "solution"}}}); got != 1 {
		t.Fatalf("start failure without solution result: %d", got)
	}
}

func TestCLISolutionRulesShareConditionsAndKeepLegacySkillsCompatible(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	if _, err := executeSettingsCommand(context.Background(), s, []string{"update", "--input", "-"}, strings.NewReader(`{"mode":"solution"}`)); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"vbnet-null-handling","description":"Null 判定を統一する","content":"# 手順\n関連する呼び出し元を修正する。"}`
	if _, err := executeRulesCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	value, err := executeRulesCommand(context.Background(), s, []string{"list"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := value.([]model.Rule)
	if len(rules) != 1 || rules[0].ID != "vbnet-null-handling" || !strings.Contains(rules[0].Body, "手順") {
		t.Fatalf("unexpected three-field rule: %+v", rules)
	}
	value, err = executeRulesCommand(context.Background(), s, []string{"show", "--id", rules[0].ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	editor := value.(model.RuleEditor)
	updated := map[string]any{"id": rules[0].ID, "description": "Null 判定と呼び出し元を統一する", "content": rules[0].Body, "expectedRevision": editor.Revision}
	input, _ := json.Marshal(updated)
	if _, err = executeRulesCommand(context.Background(), s, []string{"update", "--input", "-"}, bytes.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "skills-export")
	if _, err = executeRulesCommand(context.Background(), s, []string{"export-skills", "--output", destination}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, rules[0].ID, "SKILL.md"))
	if err != nil || !bytes.Contains(data, []byte("name: vbnet-null-handling")) || !bytes.Contains(data, []byte("呼び出し元")) {
		t.Fatalf("invalid exported skill: %s, %v", data, err)
	}
	if _, err = executeRulesCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(`{"id":"extra-rule","name":"Shared rule","description":"test","body":"text","pathPattern":"**/*.vb","contentPattern":"OldName","diagnosticPattern":"BC30451"}`)); err != nil {
		t.Fatal(err)
	}

}

func TestCLISolutionSelectionPatchPreservesNullEmptyAndMultiplePaths(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	update := func(body string) {
		t.Helper()
		if _, err := executeSettingsCommand(context.Background(), s, []string{"update", "--input", "-"}, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	update(`{"mode":"solution","solution":{"paths":["B.sln","A.sln"],"path":""}}`)
	if got := s.Snapshot().Config.Solution.Paths; !reflect.DeepEqual(got, []string{"B.sln", "A.sln"}) {
		t.Fatalf("selected path order: %#v", got)
	}
	update(`{"solution":{"configuration":"Release"}}`)
	if len(s.Snapshot().Config.Solution.Paths) != 2 {
		t.Fatal("partial update reset selected solutions")
	}
	update(`{"solution":{"paths":[]}}`)
	if got := s.Snapshot().Config.Solution.Paths; got == nil || len(got) != 0 {
		t.Fatalf("explicit deselection was lost: %#v", got)
	}
	update(`{"solution":{"paths":null}}`)
	if s.Snapshot().Config.Solution.Paths != nil {
		t.Fatal("discovery reset did not retain null")
	}
}

func TestCLISolutionBatchOutcomeTakesPrecedenceOverLatestRun(t *testing.T) {
	for _, item := range []struct {
		status string
		want   int
	}{{"completed", 0}, {"needs_human", 2}, {"failed", 2}, {"stopped", 130}} {
		result := model.ExecutionRunResult{State: model.State{Config: model.Config{Mode: "solution"}, SolutionRun: &model.SolutionRun{Status: "completed"}, SolutionBatch: &model.SolutionBatch{Status: item.status}}}
		if got := executionResultExitCode(result); got != item.want {
			t.Fatalf("%s: %d", item.status, got)
		}
	}
	var out bytes.Buffer
	printSummary(&out, model.State{Config: model.Config{Mode: "solution"}, SolutionBatch: &model.SolutionBatch{Status: "failed", Paths: []string{"A.sln", "B.sln", "C.sln"}, Runs: []model.SolutionRun{{Path: "A.sln", Status: "completed"}, {Path: "B.sln", Status: "failed"}}}})
	for _, expected := range []string{"3 件", "A.sln | 状態 completed", "B.sln | 状態 failed", "C.sln | 状態 not_run"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("missing %s: %s", expected, out.String())
		}
	}
}
