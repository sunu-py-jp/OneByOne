package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

func TestSourceConditionsIgnoreCaseAndPreserveFileIdentity(t *testing.T) {
	for _, pattern := range []string{`src/**/*.cs`, `SRC/**/*.CS`} {
		t.Run(pattern, func(t *testing.T) {
			cfg := fixture(t)
			saveRules(t, cfg, &rulepack.Package{})
			writeRule(t, cfg, "R1", model.RuleDefinition{Name: "save", Description: "repair", PathPattern: pattern, ContentPattern: `\.Save\(`})
			for file, content := range map[string]string{
				"Src/Nested/Invoice.CS": "client.SAVE();",
				"Src/lower.cs":          "client.save();",
				"Src/other.cs":          "client.Write();",
				"Src/notes.txt":         "client.Save();",
				"Outside/sample.cs":     "client.Save();",
			} {
				write(t, filepath.Join(cfg.Root, file), content)
			}
			cat, err := Load(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			tasks, scanned, excluded, err := cat.Scan(context.Background(), cfg)
			if err != nil || scanned != 5 || excluded != 3 || len(tasks) != 2 {
				t.Fatalf("unexpected extraction: %+v %d/%d %v", tasks, scanned, excluded, err)
			}
			if tasks[0].File != "Src/Nested/Invoice.CS" || tasks[1].File != "Src/lower.cs" || cat.Rules[0].CandidateCount != 2 {
				t.Fatalf("case-insensitive intersection lost scope or original path: %+v", tasks)
			}
			for _, task := range tasks {
				if !reflect.DeepEqual(task.Rules, []string{"R1"}) {
					t.Fatalf("wrong rules: %+v", task)
				}
			}
		})
	}
}

func TestContentConditionCanExplicitlyRequireCase(t *testing.T) {
	cfg := fixture(t)
	saveRules(t, cfg, &rulepack.Package{})
	writeRule(t, cfg, "R1", model.RuleDefinition{Name: "save", Description: "repair", ContentPattern: `(?-i)\.Save\(`})
	write(t, filepath.Join(cfg.Root, "exact.cs"), "client.Save();")
	write(t, filepath.Join(cfg.Root, "lower.cs"), "client.save();")
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _, _, err := cat.Scan(context.Background(), cfg)
	if err != nil || len(tasks) != 1 || tasks[0].File != "exact.cs" {
		t.Fatalf("explicit case-sensitive expression ignored: %+v %v", tasks, err)
	}
}

func TestSourceMatchingChangeInvalidatesOldExtractionHash(t *testing.T) {
	cfg := fixture(t)
	pkg, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the hash persisted before case-insensitive source matching.
	oldHash := sha256.New()
	for _, entry := range pkg.Rules {
		fmt.Fprintf(oldHash, "%d:%s%d:%s:selected=%t;", len(entry.ID), entry.ID, len(entry.Markdown), entry.Markdown, true)
	}
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Hash == hex.EncodeToString(oldHash.Sum(nil)) {
		t.Fatal("old case-sensitive extraction can be reused after matching changed")
	}
}
