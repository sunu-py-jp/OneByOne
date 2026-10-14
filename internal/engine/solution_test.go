package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/solutionrules"
)

const testSolution = "Microsoft Visual Studio Solution File, Format Version 12.00\n# Visual Studio Version 17\nProject(\"{VB}\") = \"Demo\", \"Demo/Demo.vbproj\", \"{DEMO}\"\nEndProject\nGlobal\nEndGlobal\n"

func solutionFixture(t *testing.T) (*Service, model.Config) {
	t.Helper()
	s, _ := fixture(t, map[string]string{
		"Demo.sln":         testSolution,
		"Demo/Demo.vbproj": "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net48</TargetFramework></PropertyGroup></Project>",
		"Demo/A.vb":        "Public Class A\n    Public Shared Value As Integer = 1\nEnd Class\n",
		"Demo/B.vb":        "Public Class B\n    Public Shared Value As Integer = 1\nEnd Class\n",
		"other.txt":        "untouched\n",
	})
	cfg := s.Snapshot().Config
	cfg.Mode = "solution"
	settings := model.DefaultSolutionSettings()
	settings.Path = "Demo.sln"
	cfg.Solution = &settings
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	rules, err := solutionrules.Encode([]solutionrules.Rule{{ID: "raise-value", Description: "Update both values together", Content: "Both A.Value and B.Value must equal 2. Preserve public APIs."}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	writeTest(t, path, rules)
	if _, err = s.ImportRulePackage(path, "replace"); err != nil {
		t.Fatal(err)
	}
	return s, s.Snapshot().Config
}

func solutionProposal(files ...string) agent.SolutionProposal {
	p := agent.SolutionProposal{Status: "modified", Note: "関連する値を更新", RulesApplied: []string{"raise-value"}, Usage: model.Usage{InputTokens: 10, OutputTokens: 4, Turns: 1}}
	for _, f := range files {
		p.Files = append(p.Files, agent.SolutionFileEdit{File: f, RuleIDs: []string{"raise-value"}, Edits: []model.Edit{{OldText: "Value As Integer = 1", NewText: "Value As Integer = 2"}}})
	}
	return p
}

func TestSolutionRepairsUntilGreenAndPublishesAtomically(t *testing.T) {
	s, cfg := solutionFixture(t)
	builds, proposals := 0, 0
	s.buildSolution = func(ctx context.Context, root string, settings model.SolutionSettings) (model.SolutionBuild, error) {
		if sameRoot(root, cfg.Root) {
			t.Error("built original checkout")
		}
		builds++
		// Real builds commonly generate outputs without a .gitignore.
		writeTest(t, filepath.Join(root, "Demo", "obj", "generated.txt"), []byte("generated"))
		return model.SolutionBuild{Success: builds == 3, ExitCode: map[bool]int{true: 0, false: 1}[builds == 3], Output: strings.Repeat("build step\n", 1000) + "build log tail", Diagnostics: []model.SourceDiagnostic{{File: "Demo/B.vb", Code: "BC30451", Message: "compiler detail outside preview"}}}, nil
	}
	s.proposeSolution = func(ctx context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		proposals++
		if builds != proposals {
			t.Fatalf("build must precede proposal: %d %d", builds, proposals)
		}
		if !strings.Contains(in.BuildOutput, "compiler detail outside preview") {
			t.Error("compiler diagnostics omitted")
		}
		if !strings.Contains(in.BuildOutput, "build log tail") {
			t.Error("build evidence was truncated by ordinary UI log redaction")
		}
		if len(in.Files) != 4 {
			t.Fatalf("solution context files: %#v", in.Files)
		}
		if proposals == 1 {
			p := solutionProposal("Demo/A.vb")
			p.Reviews = []model.SolutionReview{{Verdict: "passed", Summary: "A の呼び出し関係を確認", CandidateHashes: map[string]string{"Demo/A.vb": "reviewed-a"}}}
			return p, nil
		}
		if !strings.Contains(in.Files["Demo/A.vb"], "= 2") {
			t.Error("previous repair lost")
		}
		p := solutionProposal("Demo/B.vb")
		p.Reviews = []model.SolutionReview{{Verdict: "passed", Summary: "B の呼び出し関係を確認", CandidateHashes: map[string]string{"Demo/B.vb": "reviewed-b"}}}
		return p, nil
	}
	if err := s.Start(1); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.LastError != "" || st.SolutionRun.Status != "completed" {
		t.Fatalf("%+v", st)
	}
	if builds != 3 || proposals != 2 || len(st.SolutionRun.Files) != 2 {
		t.Fatalf("builds=%d proposals=%d run=%+v", builds, proposals, st.SolutionRun)
	}
	if st.Usage.InputTokens != 20 {
		t.Fatalf("usage=%+v", st.Usage)
	}
	archived, err := s.GetExecutionRun(st.SolutionRun.ID)
	if err != nil || archived.State.SolutionRun == nil || len(archived.State.SolutionRun.Reviews) != 2 {
		t.Fatalf("review evidence missing from execution history: %v %+v", err, archived)
	}
	for i, review := range archived.State.SolutionRun.Reviews {
		if review.Iteration != i || review.Verdict != "passed" || len(review.CandidateHashes) != 1 {
			t.Fatalf("review evidence was not preserved: %+v", review)
		}
	}
	commits := map[string]bool{}
	for _, task := range st.Tasks {
		if task.Status == "done" {
			h := task.History[len(task.History)-1]
			if len(h.RulesApplied) != 1 || h.RulesApplied[0] != "raise-value" {
				t.Fatalf("changed file lost rule attribution: %+v", h)
			}
			commits[h.Commit] = true
			d, err := s.GetSolutionFileDetail(task.File)
			if err != nil || !strings.Contains(d.Diff, "= 2") {
				t.Fatalf("diff %s: %v %+v", task.File, err, d)
			}
		} else if len(task.History[len(task.History)-1].RulesApplied) != 0 {
			t.Fatalf("unchanged file gained unrelated rule attribution: %+v", task)
		}
	}
	if len(commits) != 1 {
		t.Fatalf("expected one solution commit: %v", commits)
	}
	if gitTest(t, cfg.Root, "status", "--porcelain") != "" {
		t.Fatal("original modified")
	}
	if gitTest(t, st.Worktree, "status", "--porcelain") != "" {
		t.Fatal("build outputs leaked into adopted worktree")
	}
	p, err := s.GetResultPublicationPreview()
	if err != nil || len(p.Files) != 2 {
		t.Fatalf("publication: %v %+v", err, p)
	}
	if _, err = s.DiscardFileChanges("Demo/A.vb"); err == nil {
		t.Fatal("individual solution file discard allowed")
	}
	if _, err = s.RetryTasks([]string{"Demo/A.vb"}); err == nil {
		t.Fatal("individual solution file retry allowed")
	}
	// A fresh execution must start from the previously accepted full solution.
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		for _, f := range []string{"Demo/A.vb", "Demo/B.vb"} {
			if !strings.Contains(in.Files[f], "= 2") {
				t.Error("retry lost accepted edit")
			}
		}
		return agent.SolutionProposal{Status: "skipped", Note: "全ルールを確認済み"}, nil
	}
	if err = s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if s.Snapshot().LastError != "" {
		t.Fatal(s.Snapshot().LastError)
	}
}

func TestSolutionInitialGreenStillAppliesRules(t *testing.T) {
	s, _ := solutionFixture(t)
	builds, proposals := 0, 0
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		builds++
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(context.Context, agent.SolutionInput) (agent.SolutionProposal, error) {
		proposals++
		return solutionProposal("Demo/A.vb", "Demo/B.vb"), nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if builds != 2 || proposals != 1 || st.LastError != "" || st.SolutionRun.Commit == "" {
		t.Fatalf("builds %d proposals %d state %+v", builds, proposals, st)
	}
}

func TestSolutionFailedBuildsNeverAdoptAndKeepUsage(t *testing.T) {
	s, cfg := solutionFixture(t)
	cfg.Solution.MaxIterations = 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{ExitCode: 1, Output: "BC30002 type missing"}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		if err := in.BeforeRequest("edit"); err != nil {
			return agent.SolutionProposal{}, err
		}
		b, err := os.ReadFile(cfg.QueuePath + ".session.json")
		if err != nil {
			t.Fatal(err)
		}
		var m manifest
		if json.Unmarshal(b, &m) != nil || m.SolutionRun == nil || !m.SolutionRun.RequestPending {
			t.Fatal("request not journaled")
		}
		if err := in.AfterRequest(model.Usage{InputTokens: 19, Turns: 1}, nil); err != nil {
			return agent.SolutionProposal{}, err
		}
		p := solutionProposal("Demo/A.vb")
		p.Usage.InputTokens = 19
		return p, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.SolutionRun.Status != "needs_human" || st.Usage.InputTokens != 19 {
		t.Fatalf("%+v", st)
	}
	if gitTest(t, st.Worktree, "rev-parse", "HEAD") != gitTest(t, cfg.Root, "rev-parse", "HEAD") {
		t.Fatal("failed candidate committed")
	}
	if gitTest(t, st.Worktree, "status", "--porcelain") != "" {
		t.Fatal("failed candidate leaked")
	}
	r, err := s.GetExecutionRun(st.ExecutionRuns[0].ID)
	if err != nil || r.State.Usage.InputTokens != 19 {
		t.Fatalf("history lost failure usage: %v %+v", err, r)
	}
}

func TestSolutionCancellationAndBuildMutation(t *testing.T) {
	for _, kind := range []string{"cancel", "mutation", "launch"} {
		t.Run(kind, func(t *testing.T) {
			s, cfg := solutionFixture(t)
			started := make(chan struct{})
			s.buildSolution = func(ctx context.Context, root string, _ model.SolutionSettings) (model.SolutionBuild, error) {
				close(started)
				if kind == "cancel" {
					<-ctx.Done()
					return model.SolutionBuild{}, ctx.Err()
				}
				if kind == "launch" {
					return model.SolutionBuild{}, errors.New("MSBuild missing")
				}
				writeTest(t, filepath.Join(root, "other.txt"), []byte("changed by build"))
				return model.SolutionBuild{Success: true}, nil
			}
			s.proposeSolution = func(context.Context, agent.SolutionInput) (agent.SolutionProposal, error) {
				t.Error("must not ask AI after failed infrastructure/scope gate")
				return agent.SolutionProposal{}, nil
			}
			if err := s.Start(0); err != nil {
				t.Fatal(err)
			}
			<-started
			if kind == "cancel" {
				s.Stop()
			}
			s.Wait()
			st := s.Snapshot()
			if st.LastError == "" || st.SolutionRun.Commit != "" {
				t.Fatalf("%+v", st)
			}
			if kind == "cancel" && st.SolutionRun.Status != "stopped" {
				t.Fatal(st.SolutionRun.Status)
			}
			if gitTest(t, cfg.Root, "status", "--porcelain") != "" || gitTest(t, st.Worktree, "status", "--porcelain") != "" {
				t.Fatal("source dirt")
			}
		})
	}
}

func TestSolutionRuleStorageAndSkillsRoundTrip(t *testing.T) {
	s, cfg := solutionFixture(t)
	b, err := os.ReadFile(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rules []map[string]any `json:"rules"`
	}
	if json.Unmarshal(b, &doc) != nil || len(doc.Rules) != 1 || len(doc.Rules[0]) != 3 {
		t.Fatalf("not three-field rules: %s", b)
	}
	if _, err = s.CreateRule(model.RuleEdit{ID: "second-rule", Description: "Use for B", Body: "Keep B consistent."}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = s.ExportSolutionSkills(dir); err != nil {
		t.Fatal(err)
	}
	rules, err := solutionrules.Read(canonicalAlias(dir))
	if err != nil || len(rules) != 2 {
		t.Fatalf("%v %+v", err, rules)
	}
	if _, err = s.ImportSolutionSkills(dir, "merge"); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if len(st.Rules) != 4 {
		t.Fatal(st.Rules)
	}
	for _, r := range st.Rules {
		if strings.Contains(r.ID, "_") || r.ContentPattern != "" || r.PathPattern != "" {
			t.Fatal(r)
		}
	}
}
