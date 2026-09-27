package catalog

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExcludedRulesRetainDefinitionsButRestrictExtractionAndAgentScope(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "a.txt"), "OldClient.Save();\n")
	write(t, filepath.Join(cfg.Root, "b.txt"), "unrelated\n")
	ctx := context.Background()
	all, err := Load(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExcludedRuleIDs = []string{"R001"}
	selected, err := Load(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := selected.Scan(ctx, cfg)
	if err != nil || len(selected.Rules) != 2 || selected.SelectedRuleCount() != 1 || len(tasks) != 1 || scanned != 2 || excluded != 1 || !reflect.DeepEqual(tasks[0].Rules, []string{"R019"}) {
		t.Fatalf("selection leaked or hid definitions: rules=%+v tasks=%+v %d/%d %v", selected.Rules, tasks, scanned, excluded, err)
	}
	if selected.Hash == all.Hash {
		t.Fatal("selection did not affect provenance")
	}
	if _, err = selected.ForRules([]string{"R001", "R019"}); err == nil {
		t.Fatal("excluded rule reached agent scope")
	}
	scoped, err := selected.ForRules(tasks[0].Rules)
	if err != nil || len(scoped.Rules) != 1 || scoped.Rules[0].ID != "R019" {
		t.Fatal("wrong file scope", scoped, err)
	}
	if _, err = scoped.ReadRule("R001"); err == nil {
		t.Fatal("excluded rule body readable by agent")
	}
	cfg.ExcludedRuleIDs = []string{"R019", "R001"}
	none, err := Load(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, excluded, err = none.Scan(ctx, cfg)
	if err != nil || len(tasks) != 0 || excluded != 2 || none.SelectedRuleCount() != 0 || len(none.Rules) != 2 {
		t.Fatal("zero selection did not produce empty extraction", tasks, err)
	}
	cfg.ExcludedRuleIDs = nil
	restored, err := Load(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err = restored.Scan(ctx, cfg)
	if err != nil || len(tasks) != 2 || restored.Hash != all.Hash {
		t.Fatal("restoring rules did not restore extraction", err)
	}
}

func TestExcludedRuleHashIsCanonicalAndScanRejectsChangedSelection(t *testing.T) {
	cfg := fixture(t)
	cfg.ExcludedRuleIDs = []string{"R001"}
	first, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExcludedRuleIDs = []string{"unknown", " R001 ", "R001", ""}
	same, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != same.Hash {
		t.Fatal("duplicate/unknown exclusions changed selection identity")
	}
	cfg.ExcludedRuleIDs = nil
	if _, _, _, err = first.Scan(context.Background(), cfg); err == nil {
		t.Fatal("mutable config selection bypassed frozen catalog")
	}
}
