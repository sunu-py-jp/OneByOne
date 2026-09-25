package catalog

import (
	"context"
	"onebyone/internal/model"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommonRuleCoversEntireConfiguredScopeWithLegacyGateEnabled(t *testing.T) {
	for _, commonPattern := range []string{"", " \t"} {
		t.Run(commonPattern, func(t *testing.T) {
			cfg := fixture(t)
			cfg.IncludeGlobs = []string{"*.ext"}
			cfg.ExcludeGlobs = []string{"skip/**"}
			cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "legacy.txt")
			write(t, cfg.LegacyPath, `\bOldClient\b`)
			updateRule(t, cfg, "R001", func(d *model.RuleDefinition) { d.Pattern = commonPattern })
			for name, content := range map[string]string{
				"src/clean.ext":               "NewClient.Persist(options);\n",
				"src/legacy.ext":              "OldClient.Save(options);\n",
				"src/no-legacy-match.ext":     "OtherClient.Save(options);\n",
				"src/not-included.txt":        "OldClient.Save(options);\n",
				"skip/excluded.ext":           "OldClient.Save(options);\n",
				"node_modules/dependency.ext": "OldClient.Save(options);\n",
			} {
				write(t, filepath.Join(cfg.Root, name), content)
			}
			cat, err := Load(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			tasks, scanned, excluded, err := cat.Scan(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			var files []string
			for _, task := range tasks {
				files = append(files, task.File)
				if task.Status != "pending" {
					t.Fatal("common-rule source was not queued")
				}
			}
			if !reflect.DeepEqual(files, []string{"src/clean.ext", "src/legacy.ext", "src/no-legacy-match.ext"}) || scanned != 3 || excluded != 0 {
				t.Fatalf("common-rule scope still depends on legacy matches: %v scanned=%d excluded=%d", files, scanned, excluded)
			}
			if len(tasks[0].Rules) != 0 || !reflect.DeepEqual(tasks[1].Rules, []string{"R019"}) || !reflect.DeepEqual(tasks[2].Rules, []string{"R019"}) {
				t.Fatal("queue must retain only matching individual candidate IDs; common rules are provided globally")
			}
			if cat.Rules[0].CandidateCount != 3 || cat.Rules[1].CandidateCount != 2 {
				t.Fatal("candidate counts do not reflect the expanded source scope")
			}
			for _, checkCase := range []struct{ file, status string }{{"src/clean.ext", "passed"}, {"src/legacy.ext", "failed"}} {
				check, err := cat.CheckLegacy(context.Background(), cfg, filepath.Join(cfg.Root, checkCase.file))
				if err != nil || check.Status != checkCase.status {
					t.Fatalf("common rule changed legacy verification for %s: %+v %v", checkCase.file, check, err)
				}
			}
		})
	}
}

func TestOnlyCommonRuleQueuesSourcesWithoutAnyLegacyMatch(t *testing.T) {
	cfg := fixture(t)
	if err := os.RemoveAll(filepath.Join(cfg.RulesPath, "R019")); err != nil {
		t.Fatal(err)
	}
	cfg.LegacyPath = filepath.Join(filepath.Dir(cfg.Root), "legacy.txt")
	write(t, cfg.LegacyPath, "NoSourceContainsThisSymbol")
	write(t, filepath.Join(cfg.Root, "a.ext"), "modern source\n")
	write(t, filepath.Join(cfg.Root, "b.ext"), "another source\n")
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, scanned, excluded, err := cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 2 || scanned != 2 || excluded != 0 {
		t.Fatalf("common-only package produced an empty queue: %+v %d %d %v", tasks, scanned, excluded, err)
	}
	for _, task := range tasks {
		if len(task.Rules) != 0 || task.Status != "pending" {
			t.Fatal("common-only queue should need no individual candidate")
		}
	}
}
