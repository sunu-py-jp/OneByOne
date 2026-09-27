package engine

import (
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestRuleJSONImportKeepsIDsAndMergeSuffixesCollisions(t *testing.T) {
	s, _, base := rulePackageService(t)
	input := filepath.Join(base, "rules.json")
	rulePackageFixture(t, input, "Legacy")
	first, e := s.ImportRulePackage(input, "replace")
	if e != nil || len(first.Rules) != 1 || first.Rules[0].ID != "R019" {
		t.Fatalf("import must keep the front matter id: %+v %v", first.Rules, e)
	}
	merged, e := s.ImportRulePackage(input, "merge")
	if e != nil {
		t.Fatal(e)
	}
	if len(merged.Rules) != 2 || merged.Rules[0].ID != "R019" || merged.Rules[1].ID != "R019_2" || merged.Rules[0].Title != merged.Rules[1].Title {
		t.Fatalf("merge must keep both rules and suffix the colliding id: %+v", merged.Rules)
	}
	pkg, e := rulepack.Snapshot(merged.Config.RulesPath)
	if e != nil {
		t.Fatal(e)
	}
	if d, e := ruleformat.Decode([]byte(pkg.Rules[1].Markdown)); e != nil || d.ID != "R019_2" {
		t.Fatalf("renamed rule did not carry its id in the front matter: %+v %v", d, e)
	}
	again, e := s.ImportRulePackage(input, "merge")
	if e != nil || len(again.Rules) != 3 || again.Rules[2].ID != "R019_3" {
		t.Fatalf("a later merge reused an assigned suffix: %+v %v", again.Rules, e)
	}
	replaced, e := s.ImportRulePackage(input, "replace")
	if e != nil || len(replaced.Rules) != 1 || replaced.Rules[0].ID != "R019" {
		t.Fatalf("replace must use the file's rule set: %+v %v", replaced.Rules, e)
	}
}
func TestMergedRuleIDsReserveIncomingNamesCaseInsensitively(t *testing.T) {
	got := mergedRuleIDs([]string{"1", "r2"}, []string{"1", "1_2", "R2"})
	if got["1"] != "1_3" || got["1_2"] != "1_2" || got["R2"] != "R2_2" {
		t.Fatalf("merge ids: %v", got)
	}
}
func TestRuleJSONImportHonorsRuleLock(t *testing.T) {
	s, _, base := rulePackageService(t)
	input := filepath.Join(base, "rules.json")
	rulePackageFixture(t, input, "Legacy")
	st, e := s.ImportRulePackage(input, "replace")
	if e != nil {
		t.Fatal(e)
	}
	lockPath, e := s.ruleLockPath(st.ActiveWorkspaceID, st.Rules[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	lease, _, e := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{Owner: "peer", Host: "host"})
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Release()
	if _, e = s.ImportRulePackage(input, "replace"); e == nil {
		t.Fatal("busy rule replaced")
	}
	if s.Snapshot().Config.RulesPath != st.Config.RulesPath {
		t.Fatal("failed replace mutated config")
	}
}
func TestRuleJSONReplacementRepairsCorruptDocumentAndHonorsOriginalLocks(t *testing.T) {
	s, _, base := rulePackageService(t)
	input := filepath.Join(base, "rules.json")
	rulePackageFixture(t, input, "Legacy")
	st, e := s.ImportRulePackage(input, "replace")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(st.Config.RulesPath, []byte("broken JSON"), 0600); e != nil {
		t.Fatal(e)
	}
	lockPath, _ := s.ruleLockPath(st.ActiveWorkspaceID, st.Rules[0].ID)
	lease, _, e := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{Owner: "peer", Host: "host"})
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Release()
	if _, e = s.ImportRulePackage(input, "replace"); e == nil {
		t.Fatal("repair bypassed existing lease")
	}
	if e = lease.Release(); e != nil {
		t.Fatal(e)
	}
	fixed, e := s.ImportRulePackage(input, "replace")
	if e != nil || len(fixed.Rules) != 1 {
		t.Fatalf("repair failed: %v", e)
	}
}
