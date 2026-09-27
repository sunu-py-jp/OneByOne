package model

import (
	"encoding/json"
	"testing"
)

func TestEffectiveConcurrencyUsesDefaultOnlyWhenUnspecified(t *testing.T) {
	for _, tc := range []struct{ configured, want int }{{0, 2}, {1, 1}, {2, 2}, {10, 10}} {
		cfg := Config{Concurrency: tc.configured, MaxCostUSD: 3}
		runtime := EffectiveExecutionConfig(cfg)
		if cfg.EffectiveConcurrency() != tc.want || runtime.Concurrency != tc.want {
			t.Fatalf("configured %d: got runtime %d, want %d", tc.configured, runtime.Concurrency, tc.want)
		}
		if cfg.Concurrency != tc.configured || runtime.MaxCostUSD != cfg.MaxCostUSD {
			t.Fatal("runtime defaults changed saved settings or the cost allowance")
		}
	}
}

func TestParallelExecutionMetadataRoundTrips(t *testing.T) {
	state := State{
		Config:        Config{Concurrency: 4},
		CurrentFiles:  []string{"src/a.go", "src/b.go"},
		FilePhases:    map[string]string{"src/a.go": "editing", "src/b.go": "reviewing"},
		ExecutionRuns: []ExecutionRun{{ID: "run-1", Concurrency: 4}},
		Tasks:         []Task{{File: "src/a.go", History: []Attempt{{BaseCommit: "execution-base", CommitBase: "adoption-base"}}}},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored State
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Config.Concurrency != 4 || restored.ExecutionRuns[0].Concurrency != 4 || len(restored.CurrentFiles) != 2 || restored.FilePhases["src/b.go"] != "reviewing" {
		t.Fatal("parallel execution metadata did not survive serialization")
	}
	attempt := restored.Tasks[0].History[0]
	if attempt.BaseCommit != "execution-base" || attempt.CommitBase != "adoption-base" {
		t.Fatal("execution baseline and adoption parent must remain distinct")
	}
}
