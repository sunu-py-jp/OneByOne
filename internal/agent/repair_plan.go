package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

const (
	maxRepairPlanBytes = 64 << 10
	maxRepairPlanItems = 256
	maxPlanDecisions   = 256
)

// UpdateRepairPlan accepts a complete snapshot. It only records the model's
// judgments; it cannot mark a candidate verified or alter queue status.
func UpdateRepairPlan(current model.RepairPlan, update model.PlanUpdate, catalog []model.Rule, required []string, readRuleIDs []string) (model.RepairPlan, error) {
	if current.Revision < 0 || update.ExpectedRevision != current.Revision {
		return model.RepairPlan{}, fmt.Errorf("stale plan revision: expected %d", current.Revision)
	}
	if current.Revision == int(^uint(0)>>1) {
		return model.RepairPlan{}, errors.New("plan revision limit reached")
	}
	if err := boundPlan(update, len(update.RuleDecisions), len(update.Items)); err != nil {
		return model.RepairPlan{}, err
	}
	rules := make(map[string]model.Rule, len(catalog))
	mustReview := make(map[string]bool, len(required))
	for _, rule := range catalog {
		if !validRuleID(rule.ID) {
			return model.RepairPlan{}, errors.New("rule catalog contains an invalid ID")
		}
		if _, duplicate := rules[rule.ID]; duplicate {
			return model.RepairPlan{}, fmt.Errorf("rule catalog contains duplicate ID %q", rule.ID)
		}
		rules[rule.ID] = rule
		if rule.Always {
			mustReview[rule.ID] = true
		}
	}
	for _, id := range required {
		if _, ok := rules[id]; !ok {
			return model.RepairPlan{}, fmt.Errorf("required rule %q is not in the catalog", id)
		}
		mustReview[id] = true
	}
	readRules := make(map[string]bool, len(readRuleIDs))
	for _, id := range readRuleIDs {
		readRules[id] = true
	}
	plan := model.RepairPlan{
		Revision:      current.Revision + 1,
		RuleDecisions: append([]model.PlanDecision{}, update.RuleDecisions...),
		Items:         append([]model.PlanItem{}, update.Items...),
	}
	for i := range plan.RuleDecisions {
		plan.RuleDecisions[i].Reason = strings.TrimSpace(plan.RuleDecisions[i].Reason)
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		item.Location = strings.TrimSpace(item.Location)
		item.Risk = strings.TrimSpace(item.Risk)
		item.Change = strings.TrimSpace(item.Change)
		item.Expected = strings.TrimSpace(item.Expected)
		item.HoldReason = strings.TrimSpace(item.HoldReason)
	}
	decisions, items, err := validatePlanSnapshot(plan)
	if err != nil {
		return model.RepairPlan{}, err
	}
	for _, decision := range plan.RuleDecisions {
		rule, ok := rules[decision.RuleID]
		if !ok {
			return model.RepairPlan{}, fmt.Errorf("unknown rule %q in plan", decision.RuleID)
		}
		if !rule.Always && !readRules[rule.ID] {
			return model.RepairPlan{}, fmt.Errorf("read_rule %q before recording its decision", rule.ID)
		}
	}
	for id := range mustReview {
		if _, ok := decisions[id]; !ok {
			return model.RepairPlan{}, fmt.Errorf("plan is missing a decision and reason for required rule %q", id)
		}
	}
	oldDecisions := make(map[string]model.PlanDecision, len(current.RuleDecisions))
	for _, previous := range current.RuleDecisions {
		oldDecisions[previous.RuleID] = previous
		if _, exists := decisions[previous.RuleID]; !exists {
			return model.RepairPlan{}, fmt.Errorf("retain decision for previously reviewed rule %q; use no_change with a revised reason if no longer applicable", previous.RuleID)
		}
	}
	for _, previous := range current.Items {
		if item, retained := items[previous.ID]; retained {
			if item.RuleID != previous.RuleID {
				return model.RepairPlan{}, fmt.Errorf("item %q cannot be reassigned to a different rule; use a new item ID", previous.ID)
			}
			continue
		}
		// A full snapshot can correct a false positive or split/merge a planned
		// change. Require a new reason so incomplete work never silently vanishes.
		decision := decisions[previous.RuleID]
		if decision.Reason == strings.TrimSpace(oldDecisions[previous.RuleID].Reason) {
			return model.RepairPlan{}, fmt.Errorf("removing item %q requires a revised reason for rule %q explaining the replanning", previous.ID, previous.RuleID)
		}
		for _, item := range plan.Items {
			if item.RuleID == previous.RuleID && item.Location == strings.TrimSpace(previous.Location) && item.Change == strings.TrimSpace(previous.Change) {
				return model.RepairPlan{}, fmt.Errorf("keep stable item ID %q for the same planned change", previous.ID)
			}
		}
	}
	return plan, nil
}

// CheckCandidatePlan ensures the replacement candidate accounts for the whole
// plan. Pending items are allowed: addressedItemIds claims the submitted edits
// implement them. Only the runner's mechanical checks can validate that claim.
func CheckCandidatePlan(plan model.RepairPlan, request model.CandidateRequest) error {
	if plan.Revision < 1 || request.PlanRevision != plan.Revision {
		return fmt.Errorf("candidate requires current plan revision %d; call update_state first", plan.Revision)
	}
	_, items, err := validatePlanSnapshot(plan)
	if err != nil {
		return err
	}
	for _, decision := range plan.RuleDecisions {
		if decision.Decision == "blocked" {
			return fmt.Errorf("rule %q is blocked; resolve the decision or return needs_human", decision.RuleID)
		}
	}
	for _, item := range plan.Items {
		if item.Status == "blocked" {
			return fmt.Errorf("item %q is blocked; resolve the item or return needs_human", item.ID)
		}
	}
	if len(items) == 0 {
		return errors.New("plan has no changes; return skipped instead of validating a candidate")
	}
	if len(request.AddressedItemIDs) > maxRepairPlanItems {
		return errors.New("candidate exceeds the addressed-item limit")
	}
	addressed := make(map[string]bool, len(request.AddressedItemIDs))
	for _, id := range request.AddressedItemIDs {
		if _, known := items[id]; !known {
			return fmt.Errorf("candidate addresses unknown item %q", id)
		}
		if addressed[id] {
			return fmt.Errorf("candidate addresses duplicate item %q", id)
		}
		addressed[id] = true
	}
	for _, item := range plan.Items {
		if !addressed[item.ID] {
			return fmt.Errorf("candidate must include all original-based edits and address item %q", item.ID)
		}
	}
	// Older persisted candidates have no per-edit links. Do not invent them.
	// New tool schemas require itemIds; when links are supplied, validate the
	// whole mapping so partial or unknown attribution never reaches reporting.
	hasLinks := false
	for _, edit := range request.Edits {
		hasLinks = hasLinks || edit.ItemIDs != nil
	}
	if hasLinks {
		linked := map[string]bool{}
		for index, edit := range request.Edits {
			if len(edit.ItemIDs) == 0 || len(edit.ItemIDs) > maxRepairPlanItems {
				return fmt.Errorf("edit %d must identify the plan itemIds it changes", index+1)
			}
			seen := map[string]bool{}
			for _, id := range edit.ItemIDs {
				if !addressed[id] || seen[id] {
					return fmt.Errorf("edit %d has an unknown or duplicate plan itemId %q", index+1, id)
				}
				seen[id], linked[id] = true, true
			}
		}
		for id := range addressed {
			if !linked[id] {
				return fmt.Errorf("candidate has no exact edit linked to item %q", id)
			}
		}
	}
	return nil
}

// New tool calls must supply attribution even if a provider ignores the JSON
// schema. Archived candidates may be inspected/resumed without these newer
// fields; only the live boundary requires them, before running any checks.
func requireNewCandidateAttribution(request model.CandidateRequest) error {
	for index, edit := range request.Edits {
		if len(edit.ItemIDs) == 0 {
			return fmt.Errorf("edit %d requires nonempty itemIds linking its exact change to the current plan", index+1)
		}
	}
	return nil
}

// PlanRemainingItems reports unproposed claims, not mechanically failed checks.
func PlanRemainingItems(plan model.RepairPlan) []string {
	remaining := []string{}
	for _, item := range plan.Items {
		if item.Status != "proposed" {
			remaining = append(remaining, item.ID)
		}
	}
	return remaining
}

func boundPlan(value any, decisions, items int) error {
	if decisions == 0 || decisions > maxPlanDecisions {
		return fmt.Errorf("plan requires 1 to %d rule decisions", maxPlanDecisions)
	}
	if items > maxRepairPlanItems {
		return fmt.Errorf("plan exceeds %d items", maxRepairPlanItems)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxRepairPlanBytes {
		return errors.New("plan exceeds 64 KiB")
	}
	return nil
}

func validPlanText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validatePlanSnapshot(plan model.RepairPlan) (map[string]model.PlanDecision, map[string]model.PlanItem, error) {
	if err := boundPlan(plan, len(plan.RuleDecisions), len(plan.Items)); err != nil {
		return nil, nil, err
	}
	decisions := make(map[string]model.PlanDecision, len(plan.RuleDecisions))
	for _, decision := range plan.RuleDecisions {
		if !validRuleID(decision.RuleID) || !validPlanText(decision.Reason) {
			return nil, nil, errors.New("each rule decision requires a valid rule ID and a nonempty reason")
		}
		if _, duplicate := decisions[decision.RuleID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate decision for rule %q", decision.RuleID)
		}
		switch decision.Decision {
		case "modify", "no_change", "blocked":
		default:
			return nil, nil, fmt.Errorf("rule %q decision must be modify, no_change or blocked", decision.RuleID)
		}
		decisions[decision.RuleID] = decision
	}
	items := make(map[string]model.PlanItem, len(plan.Items))
	ruleItems := make(map[string]int, len(decisions))
	for _, item := range plan.Items {
		if !validRuleID(item.ID) || len(item.ID) > 64 || !validPlanText(item.Location) || !validPlanText(item.Change) || !validPlanText(item.Expected) {
			return nil, nil, errors.New("each plan item requires a valid ID of at most 64 characters and nonempty location, change and expected condition")
		}
		if _, duplicate := items[item.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate plan item %q", item.ID)
		}
		if (item.Risk != "" && !validPlanText(item.Risk)) || (item.HoldReason != "" && !validPlanText(item.HoldReason)) {
			return nil, nil, fmt.Errorf("item %q risk and holdReason must be valid text", item.ID)
		}
		decision, exists := decisions[item.RuleID]
		if !exists || (decision.Decision != "modify" && !(decision.Decision == "blocked" && item.Status == "blocked")) {
			return nil, nil, fmt.Errorf("item %q must belong to a rule with decision modify, or describe a blocked location of a blocked rule", item.ID)
		}
		switch item.Status {
		case "pending", "proposed", "blocked":
		default:
			return nil, nil, fmt.Errorf("item %q status must be pending, proposed or blocked", item.ID)
		}
		items[item.ID] = item
		ruleItems[item.RuleID]++
	}
	for _, decision := range plan.RuleDecisions {
		if decision.Decision == "modify" && ruleItems[decision.RuleID] == 0 {
			return nil, nil, fmt.Errorf("rule %q requires at least one plan item", decision.RuleID)
		}
	}
	return decisions, items, nil
}
