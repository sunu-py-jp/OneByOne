package agent

import (
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func repairPlanCatalog() []model.Rule {
	return []model.Rule{{ID: "R001", Always: true}, {ID: "R019"}, {ID: "R025"}}
}

func repairPlanUpdate() model.PlanUpdate {
	return model.PlanUpdate{
		RuleDecisions: []model.PlanDecision{
			{RuleID: "R001", Decision: "no_change", Reason: "既存の例外処理を維持する"},
			{RuleID: "R019", Decision: "modify", Reason: "通常とデバッグ分岐の両方に廃止APIがある"},
		},
		Items: []model.PlanItem{
			{ID: "P01", RuleID: "R019", Location: "dispatch: send(payload)", Change: "通常経路の送信を変更", Expected: "同じ内容を新APIに送信", Status: "pending"},
			{ID: "P02", RuleID: "R019", Location: "dispatch: if (debug)", Change: "デバッグ経路の機密ログを除去", Expected: "秘密情報を記録しない", Status: "pending"},
		},
	}
}

func checkedRepairPlan(t *testing.T) model.RepairPlan {
	t.Helper()
	plan, err := UpdateRepairPlan(model.RepairPlan{}, repairPlanUpdate(), repairPlanCatalog(), []string{"R019"}, []string{"R019"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRepairPlanReviewsCommonAndCandidateRulesWithoutMutatingInputs(t *testing.T) {
	update := repairPlanUpdate()
	update.RuleDecisions[0].Reason = "  保持する  "
	update.Items[0].Location = "\n dispatch \n"
	plan, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 1 || plan.RuleDecisions[0].Reason != "保持する" || plan.Items[0].Location != "dispatch" {
		t.Fatalf("unexpected normalized plan: %+v", plan)
	}
	if update.RuleDecisions[0].Reason != "  保持する  " || update.Items[0].Location != "\n dispatch \n" {
		t.Fatal("plan update mutated its caller's slices")
	}
	if got := PlanRemainingItems(plan); !reflect.DeepEqual(got, []string{"P01", "P02"}) {
		t.Fatalf("remaining items = %v", got)
	}
	plan.Items[0].Status = "proposed"
	if got := PlanRemainingItems(plan); !reflect.DeepEqual(got, []string{"P02"}) {
		t.Fatalf("remaining items = %v", got)
	}
}

func TestRepairPlanRejectsIncompleteOrMalformedJudgments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.PlanUpdate)
		want   string
	}{
		{"missing common", func(p *model.PlanUpdate) { p.RuleDecisions = p.RuleDecisions[1:] }, "required rule"},
		{"missing candidate", func(p *model.PlanUpdate) { p.RuleDecisions = p.RuleDecisions[:1]; p.Items = nil }, "required rule"},
		{"duplicate decision", func(p *model.PlanUpdate) { p.RuleDecisions = append(p.RuleDecisions, p.RuleDecisions[0]) }, "duplicate decision"},
		{"unknown decision", func(p *model.PlanUpdate) {
			p.RuleDecisions = append(p.RuleDecisions, model.PlanDecision{RuleID: "R999", Decision: "no_change", Reason: "none"})
		}, "unknown rule"},
		{"missing reason", func(p *model.PlanUpdate) { p.RuleDecisions[0].Reason = " \n " }, "nonempty reason"},
		{"invalid UTF8", func(p *model.PlanUpdate) { p.RuleDecisions[0].Reason = string([]byte{0xff}) }, "nonempty reason"},
		{"invalid decision", func(p *model.PlanUpdate) { p.RuleDecisions[0].Decision = "done" }, "decision must"},
		{"modification without item", func(p *model.PlanUpdate) { p.Items = nil }, "at least one"},
		{"item on no_change", func(p *model.PlanUpdate) { p.Items[0].RuleID = "R001" }, "decision modify"},
		{"item on unknown rule", func(p *model.PlanUpdate) { p.Items[0].RuleID = "R999" }, "decision modify"},
		{"duplicate item", func(p *model.PlanUpdate) { p.Items[1].ID = p.Items[0].ID }, "duplicate plan item"},
		{"path item ID", func(p *model.PlanUpdate) { p.Items[0].ID = "../../P01" }, "valid ID"},
		{"blank location", func(p *model.PlanUpdate) { p.Items[0].Location = " \n " }, "nonempty location"},
		{"blank change", func(p *model.PlanUpdate) { p.Items[0].Change = "" }, "nonempty location"},
		{"blank expected", func(p *model.PlanUpdate) { p.Items[0].Expected = "" }, "nonempty location"},
		{"forged verified", func(p *model.PlanUpdate) { p.Items[0].Status = "verified" }, "status must"},
		{"oversized text", func(p *model.PlanUpdate) { p.Items[0].Change = strings.Repeat("x", 64<<10) }, "64 KiB"},
		{"too many items", func(p *model.PlanUpdate) { p.Items = make([]model.PlanItem, 257) }, "256 items"},
		{"too many decisions", func(p *model.PlanUpdate) { p.RuleDecisions = make([]model.PlanDecision, 257) }, "256 rule decisions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			update := repairPlanUpdate()
			tc.mutate(&update)
			_, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRepairPlanRequiresReadingEveryJudgedIndividualRule(t *testing.T) {
	update := repairPlanUpdate()
	if _, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, nil); err == nil || !strings.Contains(err.Error(), "read_rule") {
		t.Fatalf("unread candidate accepted: %v", err)
	}
	update.RuleDecisions = append(update.RuleDecisions, model.PlanDecision{RuleID: "R025", Decision: "no_change", Reason: "使用箇所なし"})
	if _, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), `read_rule "R025"`) {
		t.Fatalf("unread additional rule accepted: %v", err)
	}
	if _, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019", "R025"}); err != nil {
		t.Fatalf("read additional rule rejected: %v", err)
	}
}

func TestRepairPlanRevisionAndReplanningGuard(t *testing.T) {
	plan := checkedRepairPlan(t)
	update := repairPlanUpdate()
	if _, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale update accepted: %v", err)
	}
	update.ExpectedRevision = plan.Revision
	update.Items = update.Items[:1]
	if _, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), "revised reason") {
		t.Fatalf("unfinished item silently discarded: %v", err)
	}
	update.RuleDecisions[1].Reason = "調査の結果、デバッグ分岐は対象外のメタデータだけを記録しているためP02を取り下げる"
	next, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"})
	if err != nil || next.Revision != 2 || len(next.Items) != 1 {
		t.Fatalf("explicit replanning failed: %+v %v", next, err)
	}
	update = repairPlanUpdate()
	update.ExpectedRevision = plan.Revision
	update.Items[0].ID = "P03"
	update.RuleDecisions[1].Reason = "項目を整理する"
	if _, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), "stable item ID") {
		t.Fatalf("unchanged item renumbered: %v", err)
	}
	update = repairPlanUpdate()
	update.ExpectedRevision = plan.Revision
	update.RuleDecisions[0].Decision = "modify"
	update.Items[0].RuleID = "R001"
	if _, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), "reassigned") {
		t.Fatalf("item ID reused across rules: %v", err)
	}
}

func TestRepairPlanCanExplicitlyWithdrawAllChanges(t *testing.T) {
	plan := checkedRepairPlan(t)
	update := repairPlanUpdate()
	update.ExpectedRevision = plan.Revision
	update.RuleDecisions[1].Decision = "no_change"
	update.RuleDecisions[1].Reason = "確認したSDKが別であるため、両項目とも変更不要"
	update.Items = nil
	next, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"})
	if err != nil || len(next.Items) != 0 {
		t.Fatalf("explicit withdrawal failed: %+v %v", next, err)
	}
	if err := CheckCandidatePlan(next, model.CandidateRequest{PlanRevision: next.Revision}); err == nil || !strings.Contains(err.Error(), "return skipped") {
		t.Fatalf("empty candidate accepted: %v", err)
	}
}

func TestCandidatePlanRequiresCompleteCoverageAndNoUnresolvedBlock(t *testing.T) {
	plan := checkedRepairPlan(t)
	request := model.CandidateRequest{PlanRevision: plan.Revision, AddressedItemIDs: []string{"P01", "P02"}}
	if err := CheckCandidatePlan(plan, request); err != nil {
		t.Fatalf("pending items cannot be proposed for verification: %v", err)
	}
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"partial", []string{"P01"}, "address item"},
		{"duplicate", []string{"P01", "P01", "P02"}, "duplicate item"},
		{"unknown", []string{"P01", "P02", "P03"}, "unknown item"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request.AddressedItemIDs = tc.ids
			if err := CheckCandidatePlan(plan, request); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	request.AddressedItemIDs = []string{"P01", "P02"}
	request.PlanRevision = 0
	if err := CheckCandidatePlan(plan, request); err == nil || !strings.Contains(err.Error(), "current plan revision") {
		t.Fatalf("stale candidate accepted: %v", err)
	}
	request.PlanRevision = plan.Revision
	plan.Items[1].Status = "blocked"
	if err := CheckCandidatePlan(plan, request); err == nil || !strings.Contains(err.Error(), `item "P02" is blocked`) {
		t.Fatalf("blocked item accepted: %v", err)
	}
	plan.Items[1].Status = "pending"
	plan.RuleDecisions[0].Decision = "blocked"
	if err := CheckCandidatePlan(plan, request); err == nil || !strings.Contains(err.Error(), `rule "R001" is blocked`) {
		t.Fatalf("blocked rule accepted: %v", err)
	}
}

func TestRepairPlanRejectsBrokenCatalogAndRetainsOptionalReviews(t *testing.T) {
	for _, tc := range []struct {
		catalog  []model.Rule
		required []string
	}{
		{append(repairPlanCatalog(), model.Rule{ID: "R019"}), []string{"R019"}},
		{append(repairPlanCatalog(), model.Rule{ID: "../R009"}), []string{"R019"}},
		{repairPlanCatalog(), []string{"R999"}},
	} {
		if _, err := UpdateRepairPlan(model.RepairPlan{}, repairPlanUpdate(), tc.catalog, tc.required, []string{"R019"}); err == nil {
			t.Fatal("invalid catalog/required IDs accepted")
		}
	}
	update := repairPlanUpdate()
	update.RuleDecisions = append(update.RuleDecisions, model.PlanDecision{RuleID: "R025", Decision: "no_change", Reason: "対象外と判断"})
	plan, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019", "R025"})
	if err != nil {
		t.Fatal(err)
	}
	update = repairPlanUpdate()
	update.ExpectedRevision = plan.Revision
	if _, err := UpdateRepairPlan(plan, update, repairPlanCatalog(), []string{"R019"}, []string{"R019", "R025"}); err == nil || !strings.Contains(err.Error(), "previously reviewed") {
		t.Fatalf("optional review silently dropped: %v", err)
	}
}
