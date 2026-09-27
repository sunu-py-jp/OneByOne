package catalog

import (
	"context"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPatternIntersectionAndRuleUnion(t *testing.T) {
	for _, tc := range []struct {
		name, path, content string
		want                []string
	}{{"common", "", "", []string{"docs/a.tsx", "src/a.tsx", "src/b.tsx", "src/deep/c.tsx"}}, {"path", "src/**/*.tsx", "", []string{"src/a.tsx", "src/b.tsx", "src/deep/c.tsx"}}, {"content", "", "Save", []string{"docs/a.tsx", "src/a.tsx", "src/deep/c.tsx"}}, {"both", "src/**/*.tsx", "Save", []string{"src/a.tsx", "src/deep/c.tsx"}}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixture(t)
			saveRules(t, cfg, &rulepack.Package{})
			writeRule(t, cfg, "R1", model.RuleDefinition{Name: "scope", Description: "scope description", PathPattern: tc.path, ContentPattern: tc.content})
			for _, f := range []string{"docs/a.tsx", "src/a.tsx", "src/deep/c.tsx"} {
				write(t, filepath.Join(cfg.Root, f), "Save()")
			}
			write(t, filepath.Join(cfg.Root, "src/b.tsx"), "Other()")
			c, err := Load(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			tasks, _, _, err := c.Scan(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			files := []string{}
			for _, task := range tasks {
				files = append(files, task.File)
				if !reflect.DeepEqual(task.Rules, []string{"R1"}) {
					t.Fatal(task)
				}
			}
			if !reflect.DeepEqual(files, tc.want) {
				t.Fatal(files, tc.want)
			}
			if c.Rules[0].Always != (tc.path == "" && tc.content == "") {
				t.Fatal("scoped rule marked common")
			}
			writeRule(t, cfg, "R2", model.RuleDefinition{Name: "other", Description: "match other code", ContentPattern: "Other"})
			c, _ = Load(context.Background(), cfg)
			tasks, _, _, err = c.Scan(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, task := range tasks {
				found = found || task.File == "src/b.tsx"
			}
			if !found {
				t.Fatal("rule union lost second rule")
			}
		})
	}
}
