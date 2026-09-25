package agent

import (
	"fmt"
	"onebyone/internal/model"
)

// Full command output belongs to the engine's validation artifact, not to the
// model history or resumable journal. Keep failed-check identities and excerpts
// useful even when a command emits megabytes of output.
func journalValidation(result model.CandidateValidation) model.CandidateValidation {
	return boundedValidation(result, 128, 4096, 32<<10, 1024, 16<<10)
}
func toolValidation(result model.CandidateValidation) model.CandidateValidation {
	// Line attribution is renderer evidence, not repair feedback. Keep the
	// exact mapping in the journal without echoing it into every model turn.
	result.EditRanges = nil
	return boundedValidation(result, 64, 1024, 16<<10, 512, 8<<10)
}
func boundedValidation(result model.CandidateValidation, maxChecks, checkLimit, checkBudget, diagnosticLimit, diagnosticBudget int) model.CandidateValidation {
	out := result
	out.Checks = []model.Check{}
	for i, check := range result.Checks {
		if i >= maxChecks {
			status := "passed"
			for _, remaining := range result.Checks[i:] {
				switch remaining.Status {
				case "failed", "fail", "error":
					status = "failed"
				case "passed", "pass", "ok", "success":
				default:
					if status != "failed" {
						status = "skipped"
					}
				}
				if status == "failed" {
					break
				}
			}
			out.Checks = append(out.Checks, model.Check{Name: "additional_checks", Status: status, Detail: fmt.Sprintf("%d more checks; full output is in the validation artifact", len(result.Checks)-i)})
			break
		}
		check.Name = boundedJSONText(check.Name, 128)
		check.Status = boundedJSONText(check.Status, 32)
		check.Detail = boundedJSONText(check.Detail, min(checkLimit, checkBudget))
		checkBudget -= len(raw(check.Detail))
		if checkBudget < 0 {
			checkBudget = 0
		}
		out.Checks = append(out.Checks, check)
	}
	out.Diagnostics = []model.CandidateDiagnostic{}
	for _, diagnostic := range result.Diagnostics {
		if len(out.Diagnostics) >= 20 {
			break
		}
		diagnostic.Check = boundedJSONText(diagnostic.Check, 128)
		diagnostic.LineBasis = boundedJSONText(diagnostic.LineBasis, 32)
		diagnostic.ItemID = boundedJSONText(diagnostic.ItemID, 64)
		diagnostic.Message = boundedJSONText(diagnostic.Message, min(diagnosticLimit, diagnosticBudget))
		diagnosticBudget -= len(raw(diagnostic.Message))
		if diagnosticBudget < 0 {
			diagnosticBudget = 0
		}
		diagnostic.Excerpt = boundedJSONText(diagnostic.Excerpt, min(diagnosticLimit, diagnosticBudget))
		diagnosticBudget -= len(raw(diagnostic.Excerpt))
		if diagnosticBudget < 0 {
			diagnosticBudget = 0
		}
		out.Diagnostics = append(out.Diagnostics, diagnostic)
	}
	out.RemainingItemIDs = append([]string{}, result.RemainingItemIDs...)
	if len(out.RemainingItemIDs) > 256 {
		out.RemainingItemIDs = out.RemainingItemIDs[:256]
	}
	for i, id := range out.RemainingItemIDs {
		out.RemainingItemIDs[i] = boundedJSONText(id, 64)
	}
	return out
}
func boundedJSONText(text string, limit int) string {
	if limit < 8 {
		return ""
	}
	text = bounded(text, limit-2)
	for len(raw(text)) > limit {
		if len(text)/2-3 <= 0 {
			return "…"
		}
		text = bounded(text, len(text)/2-3)
	}
	return text
}
