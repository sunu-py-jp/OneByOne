package engine

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestRepairCheckpointRetainsLargeIncrementalStateAndExternalReviewHistory(t *testing.T) {
	_, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	c := &repairCheckpoint{Version: 1, AttemptID: "large-progress", File: "A.txt", State: model.RepairState{Version: 1,
		Plan:            model.RepairPlan{Revision: 7, Items: []model.PlanItem{{ID: "P1", Change: strings.Repeat("記録", 1<<20)}}},
		StagedEdits:     []model.StagedEdit{{ID: "E1", Edit: model.Edit{OldText: "Legacy", NewText: "Modern", ItemIDs: []string{"P1"}}}},
		RuleReadOffsets: map[string]int{"R019": 24576},
	}}
	for i := 0; i < 150; i++ {
		c.State.Reviews = append(c.State.Reviews, model.IndependentReview{ID: fmt.Sprintf("review-%d", i), Summary: strings.Repeat("review finding ", 2000)})
	}
	path, err := saveRepairCheckpoint(cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 4<<20 || strings.Contains(string(raw), "review finding") {
		t.Fatal("large checkpoint was capped or historical reviews were embedded")
	}
	restored, err := loadRepairCheckpoint(cfg, model.Attempt{ID: c.AttemptID, RepairPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.State, c.State) {
		t.Fatal("checkpoint lost plan/edit/read progress or review history")
	}
	// Missing evidence cannot be silently discarded when resuming.
	if err := os.Remove(path + ".reviews/" + digest([]byte("review-0")) + ".json"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRepairCheckpoint(cfg, model.Attempt{ID: c.AttemptID, RepairPath: path}); err == nil {
		t.Fatal("missing review record was silently accepted")
	}
}
