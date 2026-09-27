package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
)

func TestRuleContentChangeBlocksStartBeforeCreatingExecution(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeFixtureRule(t, cfg.RulesPath, "R019", fixtureRuleJSON(t, "New instructions", `Legacy\.Save`, "Use the newly specified contract.", "", "", "", ""))
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("stale catalog reached provider")
		return model.Proposal{}, nil
	}
	if err := s.Start(0); err == nil || !strings.Contains(err.Error(), "対象抽出") {
		t.Fatalf("stale rules were not rejected synchronously: %v", err)
	}
	if st := s.Snapshot(); st.Running || len(st.ExecutionRuns) != 0 || st.Tasks[0].Attempts != 0 {
		t.Fatal("stale rule set started an execution")
	}
}

func TestInvalidRulesBlockStartAndReportTheRule(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeFixtureRule(t, cfg.RulesPath, "R001", []byte("---\ndescription: Missing name\n---\n"))
	if err := s.Start(0); err == nil {
		t.Fatal("invalid rule set started")
	}
	st := s.Snapshot()
	if st.Running || len(st.ExecutionRuns) != 0 {
		t.Fatal("invalid rule set created an execution")
	}
	found := false
	for _, workspace := range st.Workspaces {
		for _, issue := range workspace.Issues {
			found = found || (issue.RuleID == "R001" && issue.Page == "rules")
		}
	}
	if !found {
		t.Fatalf("invalid rule was not reported on the rules page: %+v", st.Workspaces)
	}
}

func TestOutOfScopeHistoricalFilesCannotBeSelectedOrRetried(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	if st := runTest(t, s, 1); st.Tasks[0].Status != "done" {
		t.Fatal("fixture did not create history")
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range pkg.Rules {
		d, err := ruleformat.Decode([]byte(entry.Markdown))
		if err != nil {
			t.Fatal(err)
		}
		d.PathPattern = "A.txt"
		data, err := ruleformat.Encode(d)
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureRule(t, cfg.RulesPath, entry.ID, data)
	}
	if _, err = s.Scan(); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot().Tasks
	if len(before) != 2 || !before[1].Excluded || len(before[1].Rules) != 0 {
		t.Fatal("excluded history fixture not retained", before)
	}
	if _, err = s.SetTaskSelection([]string{"B.txt"}); err == nil {
		t.Fatal("out-of-scope selection accepted")
	}
	if _, err = s.RetryTasks([]string{"A.txt", "B.txt"}); err == nil {
		t.Fatal("out-of-scope retry accepted")
	}
	if !reflect.DeepEqual(before, s.Snapshot().Tasks) {
		t.Fatal("rejected batch partially changed selection/history")
	}
}
