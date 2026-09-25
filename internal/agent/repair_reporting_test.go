package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestRepairPlanRecordsSpecificRisksAndHeldLocationsWithoutAdoptingThem(t *testing.T) {
	update := repairPlanUpdate()
	update.Items[0].Risk = "  応答前に処理済み扱いとなり、失敗した送信が失われる  "
	update.Items[1].Risk = "秘密情報がログへ残る"
	update.Items[1].Status = "blocked"
	update.Items[1].HoldReason = "  ログの利用先と削除可能な項目を確認してください  "
	update.RuleDecisions = append(update.RuleDecisions, model.PlanDecision{RuleID: "R025", Decision: "blocked", Reason: "タイムアウト値の単位が不明"})
	update.Items = append(update.Items, model.PlanItem{ID: "P03", RuleID: "R025", Location: "settings.timeout", Risk: "単位の取り違えで期限が短くなる", Change: "単位を確認して新しい設定へ変換", Expected: "既存と同じ実時間の期限", Status: "blocked", HoldReason: "入力値が秒かミリ秒か確認してください"})
	plan, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019", "R025"}, []string{"R019", "R025"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Risk != strings.TrimSpace(update.Items[0].Risk) || plan.Items[1].HoldReason != strings.TrimSpace(update.Items[1].HoldReason) || !strings.HasPrefix(update.Items[0].Risk, "  ") {
		t.Fatalf("report explanations not normalized independently: %+v", plan.Items)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.RepairPlan
	if err := json.Unmarshal(data, &restored); err != nil || restored.Items[2].Risk != plan.Items[2].Risk || restored.Items[2].HoldReason != plan.Items[2].HoldReason {
		t.Fatalf("checkpoint lost risk or hold reason: %s, %v", data, err)
	}
	request := model.CandidateRequest{PlanRevision: plan.Revision, AddressedItemIDs: []string{"P01", "P02", "P03"}}
	if err := CheckCandidatePlan(restored, request); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("reporting blocked locations allowed adoption: %v", err)
	}
}

func TestRepairPlanReportTextRejectsInvalidContent(t *testing.T) {
	for _, field := range []string{"risk", "holdReason"} {
		t.Run(field, func(t *testing.T) {
			update := repairPlanUpdate()
			if field == "risk" {
				update.Items[0].Risk = "risk\x00hidden"
			} else {
				update.Items[0].HoldReason = string([]byte{0xff})
			}
			if _, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil || !strings.Contains(err.Error(), "risk and holdReason") {
				t.Fatalf("invalid report text accepted: %v", err)
			}
		})
	}
}

func TestBlockedRuleCannotContainAnExecutableReportItem(t *testing.T) {
	update := repairPlanUpdate()
	update.RuleDecisions[1].Decision = "blocked"
	if _, err := UpdateRepairPlan(model.RepairPlan{}, update, repairPlanCatalog(), []string{"R019"}, []string{"R019"}); err == nil {
		t.Fatal("blocked rule accepted items claiming executable changes")
	}
}

func TestPlanToolRequiresRiskAndHoldReasonFields(t *testing.T) {
	for _, definition := range toolDefinitions() {
		if definition["name"] != "update_state" {
			continue
		}
		parameters := definition["parameters"].(map[string]any)
		properties := parameters["properties"].(map[string]any)
		items := properties["items"].(map[string]any)["items"].(map[string]any)
		required := items["required"].([]string)
		for _, field := range []string{"risk", "holdReason"} {
			found := false
			for _, name := range required {
				found = found || name == field
			}
			if !found {
				t.Fatalf("tool schema does not require %s", field)
			}
		}
		return
	}
	t.Fatal("plan tool missing")
}
