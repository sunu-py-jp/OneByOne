package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"onebyone/internal/model"
	"strings"
)

func requirePlanSourceLocationFields(arguments string) error {
	var wire struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(arguments), &wire); err != nil {
		return err
	}
	for _, rawItem := range wire.Items {
		var item struct {
			SourceLocations []json.RawMessage `json:"sourceLocations"`
		}
		// Only inspect the required key here; the outer strict decoder already
		// verifies the complete item schema and rejects unknown properties.
		var values map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &values); err != nil {
			return err
		}
		value, ok := values["sourceLocations"]
		if !ok || strings.TrimSpace(string(value)) == "null" {
			return errors.New("each plan item requires sourceLocations; use [] only when the item is not blocked")
		}
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return err
		}
		for _, rawLocation := range item.SourceLocations {
			var location model.SourceLocation
			if err := strictRequiredJSON(string(rawLocation), &location, "startLine", "endLine", "excerpt"); err != nil {
				return fmt.Errorf("sourceLocations requires startLine, endLine and excerpt: %w", err)
			}
		}
	}
	return nil
}

func validatePlanSourceLocations(plan model.RepairPlan, content string) error {
	for _, item := range plan.Items {
		if item.Status == "blocked" && (len(item.SourceLocations) == 0 || strings.TrimSpace(item.HoldReason) == "") {
			return fmt.Errorf("blocked item %q requires holdReason and nonempty sourceLocations identifying actual original code; for truly unlocalizable uncertainty use a blocked rule decision without an artificial item", item.ID)
		}
		if len(item.SourceLocations) > 32 {
			return fmt.Errorf("item %q exceeds 32 source locations", item.ID)
		}
		seen := map[model.SourceLocation]bool{}
		for _, location := range item.SourceLocations {
			if seen[location] {
				return fmt.Errorf("item %q repeats a source location", item.ID)
			}
			if err := model.ValidateSourceLocation(content, location); err != nil {
				return fmt.Errorf("item %q sourceLocations: %w", item.ID, err)
			}
			seen[location] = true
		}
	}
	return nil
}

// Normalize only fresh tool arguments. Persisted plans remain strict at the
// final-answer boundary and in engine reporting.
func resolvePlanSourceLocations(plan *model.RepairPlan, content string) error {
	for i := range plan.Items {
		item := &plan.Items[i]
		for j, location := range item.SourceLocations {
			resolved, err := model.ResolveSourceLocation(content, location)
			if err != nil {
				return fmt.Errorf("item %q sourceLocations: %w", item.ID, err)
			}
			item.SourceLocations[j] = resolved
		}
	}
	return validatePlanSourceLocations(*plan, content)
}
