package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

func TestMergeSolutionRuleScopesRetainsOnlyEarlierCompletedSharedFiles(t *testing.T) {
	current := model.SolutionRun{ID: "current", ExecutionID: "batch", Status: "running"}
	catalog := []model.Rule{{ID: "source", Body: "preserve initial migration", ContentPattern: "Legacy"}, {ID: "initial", Body: "new requirement"}}
	batch := &model.SolutionBatch{ID: "batch", Status: "running", CurrentIndex: 2, Runs: []model.SolutionRun{
		{ID: "first", ExecutionID: "batch", Status: "completed", RuleFiles: map[string][]string{"source": {"Shared.vb", "OnlyEarlier.vb"}}},
		{ID: "held", ExecutionID: "batch", Status: "needs_human", RuleFiles: map[string][]string{"source": {"Current.vb"}}},
		current,
		{ID: "later", ExecutionID: "batch", Status: "completed", RuleFiles: map[string][]string{"source": {"Current.vb"}}},
	}}
	initial := map[string][]string{"initial": {"Current.vb"}}
	rules, scopes, err := mergeSolutionRuleScopes(batch, current, nil, catalog, catalog[1:], initial, []string{"Shared.vb", "Current.vb"})
	if err != nil || len(rules) != 2 || rules[0].ID != "source" || rules[0].CandidateCount != 1 || !reflect.DeepEqual(scopes, map[string][]string{"source": {"Shared.vb"}, "initial": {"Current.vb"}}) {
		t.Fatalf("wrong scope inheritance: %+v %+v %v", rules, scopes, err)
	}
	scopes["source"][0], scopes["initial"][0] = "mutated", "mutated"
	if batch.Runs[0].RuleFiles["source"][0] != "Shared.vb" || initial["initial"][0] != "Current.vb" {
		t.Fatal("returned scope aliased earlier evidence or initial matching")
	}
}

func TestMergeSolutionRuleScopesDoesNotCrossExecutionBoundaries(t *testing.T) {
	current := model.SolutionRun{ID: "current", ExecutionID: "new", Status: "running"}
	rules := []model.Rule{{ID: "R1", Body: "requirement"}}
	base := &model.SolutionBatch{ID: "new", Status: "running", CurrentIndex: 1, Runs: []model.SolutionRun{
		{ID: "first", ExecutionID: "new", Status: "completed", RuleFiles: map[string][]string{"R1": {"Shared.vb"}}}, current,
	}}
	for _, mutate := range []func(*model.SolutionBatch){
		func(b *model.SolutionBatch) { b.ID = "old" },
		func(b *model.SolutionBatch) { b.Status = "completed" },
		func(b *model.SolutionBatch) { b.Runs[1].ID = "different-child" },
		func(b *model.SolutionBatch) { b.Runs[0].ExecutionID = "old" },
		func(b *model.SolutionBatch) { b.Runs[0].Status = "failed" },
	} {
		batch := copySolutionBatch(base)
		mutate(batch)
		selected, scopes, err := mergeSolutionRuleScopes(batch, current, nil, rules, nil, map[string][]string{}, []string{"Shared.vb"})
		if err != nil || len(selected) != 0 || len(scopes) != 0 {
			t.Fatalf("unrelated execution supplied rules: %+v %+v %v", selected, scopes, err)
		}
	}
	selected, scopes, err := mergeSolutionRuleScopes(base, current, []string{"R1"}, rules, nil, map[string][]string{}, []string{"Shared.vb"})
	if err != nil || len(selected) != 0 || len(scopes) != 0 {
		t.Fatalf("disabled rule was revived: %+v %+v %v", selected, scopes, err)
	}
	if _, _, err := mergeSolutionRuleScopes(base, current, nil, nil, nil, map[string][]string{}, []string{"Shared.vb"}); err == nil {
		t.Fatal("missing inherited definition was silently discarded")
	}
}

func TestSolutionBatchPreservesSharedConditionalRulesAfterMatchDisappears(t *testing.T) {
	s, _ := solutionBatchFixture(t, []string{"Demo.sln", "Second.sln", "Third.sln"})
	definition := ruleDefinitionBytes(t, model.RuleDefinition{ID: "raise-value", Name: "raise value", Description: "Keep shared values migrated", Body: "Both A.Value and B.Value must remain 2.", PathPattern: "Demo/*.vb", ContentPattern: "= 1", DiagnosticPattern: "BC30456"})
	pack, err := json.Marshal(map[string]any{"rules": []string{string(definition)}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "conditional-rules.json")
	writeTest(t, path, pack)
	if _, err := s.ImportRulePackage(path, "replace"); err != nil {
		t.Fatal(err)
	}
	build := func(_ context.Context, root string, _ model.SolutionSettings) (model.SolutionBuild, error) {
		data, err := os.ReadFile(filepath.Join(root, "Demo/A.vb"))
		if err != nil {
			return model.SolutionBuild{}, err
		}
		if strings.Contains(string(data), "= 1") {
			return model.SolutionBuild{Success: false, ExitCode: 1, Diagnostics: []model.SourceDiagnostic{{File: "Demo/A.vb", Code: "BC30456", Message: "initial evidence"}, {File: "Demo/B.vb", Code: "BC30456", Message: "initial evidence"}}}, nil
		}
		return model.SolutionBuild{Success: true}, nil
	}
	s.buildSolution = build
	visited := []string{}
	want := map[string][]string{"raise-value": {"Demo/A.vb", "Demo/B.vb"}}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		visited = append(visited, in.Config.Solution.Path)
		if len(in.Rules) != 1 || !reflect.DeepEqual(in.RuleFiles, want) {
			t.Errorf("lost original conditional requirements in %s: %+v %+v", in.Config.Solution.Path, in.Rules, in.RuleFiles)
		}
		if in.Config.Solution.Path == "Demo.sln" {
			return solutionProposal("Demo/A.vb", "Demo/B.vb"), nil
		}
		if strings.Contains(in.Files["Demo/A.vb"], "= 1") || strings.Contains(in.BuildOutput, "initial evidence") {
			t.Error("later child did not use accepted source and its own fresh build")
		}
		return agent.SolutionProposal{Status: "skipped", Note: "Inherited rule remains satisfied."}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.LastError != "" || st.SolutionBatch == nil || st.SolutionBatch.Status != "completed" || !reflect.DeepEqual(visited, []string{"Demo.sln", "Second.sln", "Third.sln"}) {
		t.Fatalf("batch failed: %+v %s %v", st.SolutionBatch, st.LastError, visited)
	}
	for _, child := range st.SolutionBatch.Runs {
		if !reflect.DeepEqual(child.RuleFiles, want) {
			t.Fatalf("child did not persist frozen requirements: %+v", child)
		}
	}
	configPath := s.configPath
	s.Close()
	reopened := workspaceNewService(t, configPath)
	if got := reopened.Snapshot(); got.LastError != "" || !reflect.DeepEqual(got.SolutionBatch, st.SolutionBatch) {
		t.Fatalf("scope evidence did not survive reopening: %+v", got.SolutionBatch)
	}
	// A separate execution starts from new initial conditions, even when the
	// earlier batch remains visible in the results panel.
	cfg := reopened.Snapshot().Config
	cfg.Solution.Paths = []string{"Third.sln"}
	if _, err := reopened.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	reopened.buildSolution = build
	reopened.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		if len(in.Rules) != 0 || len(in.RuleFiles) != 0 {
			t.Errorf("new execution inherited old batch scope: %+v %+v", in.Rules, in.RuleFiles)
		}
		return agent.SolutionProposal{Status: "skipped", Note: "No initial conditional match in this execution."}, nil
	}
	if err := reopened.Start(0); err != nil {
		t.Fatal(err)
	}
	reopened.Wait()
	if got := reopened.Snapshot(); got.LastError != "" || got.SolutionRun.Status != "completed" || len(got.SolutionRun.RuleFiles) != 0 {
		t.Fatalf("new batch scope failed: %+v %s", got.SolutionRun, got.LastError)
	}
}

func TestSolutionRuleScopeRemainsFixedAcrossRepairsWithCommonRules(t *testing.T) {
	s, _ := solutionFixture(t)
	opened, err := s.OpenRule("raise-value")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.SaveRules([]model.RuleEdit{
		{ID: "raise-value", ExpectedRevision: opened.Revision, Name: "raise A", Description: "Repair the original A error", Body: "A.Value must become 2.", PathPattern: "Demo/A.vb", ContentPattern: "= 1", DiagnosticPattern: "BC30456"},
		{ID: "common-behavior", Name: "preserve behavior", Description: "Preserve APIs", Body: "Preserve every public declaration and unrelated behavior."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CloseRule(); err != nil {
		t.Fatal(err)
	}
	pkg, err := rulepack.Snapshot(st.Config.RulesPath)
	if err != nil || pkg.Solution || len(pkg.Rules) != 2 {
		t.Fatalf("common/conditional edits did not use shared rule format: %+v %v", pkg, err)
	}
	builds, proposals := 0, 0
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		builds++
		if builds == 1 {
			return model.SolutionBuild{Success: false, ExitCode: 1, Diagnostics: []model.SourceDiagnostic{{File: "Demo/A.vb", Code: "BC30456", Message: "initial A error"}}}, nil
		}
		if builds == 2 {
			return model.SolutionBuild{Success: false, ExitCode: 1, Diagnostics: []model.SourceDiagnostic{{File: "Demo/B.vb", Code: "BC30456", Message: "remaining B error"}}}, nil
		}
		return model.SolutionBuild{Success: true}, nil
	}
	want := map[string][]string{"raise-value": {"Demo/A.vb"}, "common-behavior": {"Demo.sln", "Demo/A.vb", "Demo/B.vb", "Demo/Demo.vbproj"}}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		proposals++
		if len(in.Rules) != 2 || !reflect.DeepEqual(in.RuleFiles, want) {
			t.Errorf("rule scope changed at proposal %d: %+v %+v", proposals, in.Rules, in.RuleFiles)
		}
		if proposals == 1 {
			return solutionProposal("Demo/A.vb"), nil
		}
		if strings.Contains(in.Files["Demo/A.vb"], "= 1") || strings.Contains(in.BuildOutput, "initial A error") {
			t.Error("second repair did not receive current source and fresh diagnostics")
		}
		return agent.SolutionProposal{Status: "modified", Note: "Compiler-only B fix, retaining the completed A migration.", Files: []agent.SolutionFileEdit{{File: "Demo/B.vb", Edits: []model.Edit{{OldText: "Value As Integer = 1", NewText: "Value As Integer = 2"}}}}}, nil
	}
	if err = s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st = s.Snapshot()
	if st.LastError != "" || st.SolutionRun.Status != "completed" || builds != 3 || proposals != 2 || !reflect.DeepEqual(st.SolutionRun.RuleFiles, want) {
		t.Fatalf("scope was not frozen for the child run: %+v %s builds=%d proposals=%d", st.SolutionRun, st.LastError, builds, proposals)
	}
}
