package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

// Recovery keeps complete findings locally and lets the reviewer produce small
// pieces. No editor explanation, edit plan or predecessor verdict is supplied.
// Page size affects one read, never whether a source/rule can be reviewed.
const reviewPageBytes = 24 << 10

const reviewRecoveryInstructions = `The prior response could not be accepted. Continue this independent review using the supplied read-only tools and durable review findings. You may record assessments, issues and hold assessments in small batches with record_review; each matching ruleId, issue id or itemId replaces that saved entry. Record every discovered issue, not only a sample. removeIssueIds removes an erroneous saved issue. A saved assessment alone is not proof: inspect all before/after source pages and every supplied rule before the final decision. Re-read relevant ranges whenever earlier context is missing. read_review_source and read_review_rule return exact UTF-8 byte ranges with nextOffset; continue until complete. Source line numbers remain those of the original complete source. For global correctness, examine the whole candidate, interactions among all rules and all unchanged held regions before finishing, not just isolated edits. Source and rule access remains available throughout this final cross-rule examination. Finish only with {verdict, summary}; the runner combines that decision with every saved assessment, issue and hold assessment and validates complete coverage and exact locations. Never omit a rule or issue to fit one response. If a response is truncated, send smaller tool calls; already recorded work remains saved. This is still an independent review; never edit code or use the editor's reasoning.`

type reviewRecordedIssue struct {
	ID    string            `json:"id"`
	Issue model.ReviewIssue `json:"issue"`
}
type reviewRecordUpdate struct {
	Assessments     []model.ReviewAssessment     `json:"assessments"`
	Issues          []reviewRecordedIssue        `json:"issues"`
	HoldAssessments []model.ReviewHoldAssessment `json:"holdAssessments"`
	RemoveIssueIDs  []string                     `json:"removeIssueIds"`
}
type reviewByteRange struct{ Start, End int }
type reviewRecoveryState struct {
	assessments map[string]model.ReviewAssessment
	issues      map[string]model.ReviewIssue
	holds       map[string]model.ReviewHoldAssessment
	read        map[string][]reviewByteRange
}

func newReviewRecoveryState() *reviewRecoveryState {
	return &reviewRecoveryState{assessments: map[string]model.ReviewAssessment{}, issues: map[string]model.ReviewIssue{}, holds: map[string]model.ReviewHoldAssessment{}, read: map[string][]reviewByteRange{}}
}
func addReviewUsage(a, b model.Usage) model.Usage {
	a.InputTokens += b.InputTokens
	a.CachedTokens += b.CachedTokens
	a.OutputTokens += b.OutputTokens
	a.Turns += b.Turns
	a.CostUSD += b.CostUSD
	a.Uncertain = a.Uncertain || b.Uncertain
	return a
}
func (s *reviewRecoveryState) result(in ReviewInput) model.IndependentReview {
	r := model.IndependentReview{BaseHash: in.BaseHash, CandidateHash: in.CandidateHash, Assessments: []model.ReviewAssessment{}, Issues: []model.ReviewIssue{}, HoldAssessments: []model.ReviewHoldAssessment{}}
	for _, rule := range in.Rules {
		if a, ok := s.assessments[rule.ID]; ok {
			r.Assessments = append(r.Assessments, a)
		}
	}
	ids := make([]string, 0, len(s.issues))
	for id := range s.issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r.Issues = append(r.Issues, s.issues[id])
	}
	for _, hold := range in.Holds {
		if a, ok := s.holds[hold.ItemID]; ok {
			r.HoldAssessments = append(r.HoldAssessments, a)
		}
	}
	return r
}
func (s *reviewRecoveryState) restore(r *model.IndependentReview) {
	if r == nil {
		return
	}
	for _, a := range r.Assessments {
		s.assessments[a.RuleID] = a
	}
	for i, issue := range r.Issues {
		s.issues[fmt.Sprintf("saved-%d", i+1)] = issue
	}
	for _, h := range r.HoldAssessments {
		s.holds[h.ItemID] = h
	}
}
func (s *reviewRecoveryState) manifest(in ReviewInput, paged bool, feedback string) map[string]any {
	payload := map[string]any{"file": in.File, "baseHash": in.BaseHash, "candidateHash": in.CandidateHash, "feedback": feedback}
	if paged {
		payload["sources"] = map[string]any{"beforeBytes": len(in.Before), "afterBytes": len(in.After)}
		payload["ruleCount"], payload["holdCount"] = len(in.Rules), len(in.Holds)
		payload["savedAssessmentCount"], payload["savedIssueCount"], payload["savedHoldCount"] = len(s.assessments), len(s.issues), len(s.holds)
		payload["allSourcesAndRulesRead"] = s.completeRead(in)
		payload["instructions"] = "Read the rule catalog and declared holds with read_review_catalog, saved findings with read_review_state, then the exact sources/rules through their read tools. Every tool is paged; continue until complete."
	} else {
		payload["before"], payload["after"], payload["rules"], payload["holds"], payload["savedReview"] = in.Before, in.After, in.Rules, in.Holds, s.result(in)
	}
	return payload
}
func reviewCatalogText(in ReviewInput) string {
	catalog := []map[string]any{}
	for _, r := range in.Rules {
		catalog = append(catalog, map[string]any{"id": r.ID, "title": r.Title, "always": r.Always, "bytes": len(r.Body)})
	}
	return string(raw(map[string]any{"rules": catalog, "holds": in.Holds}))
}
func (s *reviewRecoveryState) markAllRead(in ReviewInput) {
	s.read["catalog"] = []reviewByteRange{{0, len(reviewCatalogText(in))}}
	s.read["before"] = []reviewByteRange{{0, len(in.Before)}}
	s.read["after"] = []reviewByteRange{{0, len(in.After)}}
	for _, r := range in.Rules {
		s.read["rule:"+r.ID] = []reviewByteRange{{0, len(r.Body)}}
	}
}
func coveredReviewBytes(ranges []reviewByteRange, total int) bool {
	sorted := append([]reviewByteRange(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	end := 0
	for _, r := range sorted {
		if r.Start > end {
			return false
		}
		end = max(end, r.End)
	}
	return end >= total
}
func (s *reviewRecoveryState) completeRead(in ReviewInput) bool {
	if len(in.Holds) > 0 && !coveredReviewBytes(s.read["catalog"], len(reviewCatalogText(in))) {
		return false
	}
	if !coveredReviewBytes(s.read["before"], len(in.Before)) || !coveredReviewBytes(s.read["after"], len(in.After)) {
		return false
	}
	for _, r := range in.Rules {
		if !coveredReviewBytes(s.read["rule:"+r.ID], len(r.Body)) {
			return false
		}
	}
	return true
}
func (c *client) recoverReview(ctx context.Context, in ReviewInput, paged bool, usage model.Usage, why error) (model.IndependentReview, error) {
	saved := newReviewRecoveryState()
	saved.restore(in.PriorProgress)
	feedback := "Review output must be completed with small record_review batches. " + why.Error()
	history := []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(saved.manifest(in, paged, feedback)))})}
	for {
		if err := ctx.Err(); err != nil {
			return model.IndependentReview{Usage: usage}, err
		}
		body, err := c.reviewRecoveryRequest(history)
		if err != nil {
			return model.IndependentReview{Usage: usage}, err
		}
		res, delta, requestErr := c.reviewResponse(ctx, in, body)
		usage = addReviewUsage(usage, delta)
		if requestErr != nil {
			if !IsContextLimit(requestErr) {
				return model.IndependentReview{Usage: usage}, requestErr
			}
			paged = true
			history = []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(saved.manifest(in, true, "Provider context limit: retrieve source and rules page by page, then continue saved review findings.")))})}
			continue
		}
		if !paged {
			saved.markAllRead(in)
		}
		if err = completed(res); err != nil {
			if !IsOutputLimit(err) && !IsContextLimit(err) {
				return model.IndependentReview{Usage: usage}, err
			}
			if IsOutputLimit(err) {
				c.growOutputLimit()
			}
			paged = paged || IsContextLimit(err)
			history = []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(saved.manifest(in, paged, "The last response was truncated and not recorded. Continue with smaller record_review batches.")))})}
			continue
		}
		calls, finalText, parseErr := parseOutput(res.Output)
		if parseErr != nil {
			history = []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(saved.manifest(in, paged, "Invalid response format. Use the provided tools and finish schema.")))})}
			continue
		}
		if len(calls) > 0 {
			results := []toolResult{}
			for _, call := range calls {
				value, toolErr := saved.execute(in, call)
				if toolErr == nil && call.Name == "record_review" && in.SaveProgress != nil {
					if err := in.SaveProgress(saved.result(in)); err != nil {
						return model.IndependentReview{Usage: usage}, &fatalError{fmt.Errorf("Cannot persist review findings: %w", err)}
					}
				}
				if toolErr != nil {
					value = "Tool error: " + toolErr.Error()
				}
				results = append(results, toolResult{CallID: call.CallID, Content: value, IsError: toolErr != nil})
			}
			history = c.appendTurn(history, res, results)
			continue
		}
		var final struct {
			Verdict string `json:"verdict"`
			Summary string `json:"summary"`
		}
		err = strictRequiredJSON(finalText, &final, "verdict", "summary")
		if err == nil && !saved.completeRead(in) {
			err = errors.New("Read every before/after source and every rule completely before the final whole-candidate review")
		}
		candidate := saved.result(in)
		candidate.Verdict, candidate.Summary = final.Verdict, final.Summary
		if err == nil {
			var parsed model.IndependentReview
			parsed, err = parseIndependentReview(string(raw(map[string]any{"baseHash": candidate.BaseHash, "candidateHash": candidate.CandidateHash, "verdict": candidate.Verdict, "summary": candidate.Summary, "assessments": candidate.Assessments, "issues": candidate.Issues, "holdAssessments": candidate.HoldAssessments})), in)
			if err == nil {
				parsed.Usage = usage
				return parsed, nil
			}
		}
		history = appendFinalFeedback(c, history, res, "Final independent review rejected: "+err.Error()+". Correct saved findings, complete all reads, and perform the final cross-rule and whole-candidate check.")
	}
}
func (s *reviewRecoveryState) execute(in ReviewInput, call functionCall) (string, error) {
	switch call.Name {
	case "read_review_source", "read_review_rule", "read_review_catalog", "read_review_state":
		var q struct {
			Source string `json:"source,omitempty"`
			RuleID string `json:"ruleId,omitempty"`
			Offset int    `json:"offset"`
		}
		if err := strictRequiredJSON(call.Arguments, &q, "offset"); err != nil {
			return "", err
		}
		key, content := "", ""
		if call.Name == "read_review_catalog" {
			key, content = "catalog", reviewCatalogText(in)
		} else if call.Name == "read_review_state" {
			entries := []reviewRecordedIssue{}
			for id, issue := range s.issues {
				entries = append(entries, reviewRecordedIssue{ID: id, Issue: issue})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
			key, content = "state", string(raw(map[string]any{"review": s.result(in), "issuesWithIds": entries, "readCoverage": s.read}))
		} else if call.Name == "read_review_source" {
			key = q.Source
			switch q.Source {
			case "before":
				content = in.Before
			case "after":
				content = in.After
			default:
				return "", errors.New("source must be before or after")
			}
		} else {
			for _, rule := range in.Rules {
				if rule.ID == q.RuleID {
					key, content = "rule:"+rule.ID, rule.Body
					break
				}
			}
			if key == "" {
				return "", errors.New("ruleId is outside this review")
			}
		}
		if q.Offset < 0 || q.Offset > len(content) || q.Offset < len(content) && !utf8.RuneStart(content[q.Offset]) {
			return "", errors.New("offset must be a valid UTF-8 byte boundary")
		}
		end := min(len(content), q.Offset+reviewPageBytes)
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if key != "state" {
			s.read[key] = append(s.read[key], reviewByteRange{q.Offset, end})
		}
		return string(raw(map[string]any{"content": content[q.Offset:end], "offset": q.Offset, "nextOffset": end, "complete": end == len(content), "startLine": 1 + strings.Count(content[:q.Offset], "\n")})), nil
	case "record_review":
		var update reviewRecordUpdate
		if err := strictRequiredJSON(call.Arguments, &update, "assessments", "issues", "holdAssessments", "removeIssueIds"); err != nil {
			return "", err
		}
		knownRules := map[string]bool{}
		for _, r := range in.Rules {
			knownRules[r.ID] = true
		}
		knownHolds := map[string]bool{}
		for _, h := range in.Holds {
			knownHolds[h.ItemID] = true
		}
		for _, a := range update.Assessments {
			if !knownRules[a.RuleID] || strings.TrimSpace(a.Reason) == "" {
				return "", errors.New("assessment requires a supplied rule and concrete reason")
			}
			switch a.Status {
			case "satisfied", "not_applicable", "violated", "needs_human":
			default:
				return "", errors.New("invalid assessment status")
			}
		}
		for _, a := range update.HoldAssessments {
			if !knownHolds[a.ItemID] || strings.TrimSpace(a.Reason) == "" || (a.Status != "preserved" && a.Status != "unsafe") {
				return "", errors.New("hold assessment must identify a declared hold and concrete result")
			}
		}
		for _, entry := range update.Issues {
			issue := entry.Issue
			if strings.TrimSpace(entry.ID) == "" || issue.RuleID != "" && !knownRules[issue.RuleID] {
				return "", errors.New("issue needs an id and a known rule")
			}
			if issue.Kind != "needs_changes" && issue.Kind != "needs_human" {
				return "", errors.New("issue kind must be needs_changes or needs_human")
			}
			for _, field := range []string{issue.Location, issue.Excerpt, issue.Reason, issue.RequestedChange} {
				if strings.TrimSpace(field) == "" {
					return "", errors.New("issue fields must be concrete")
				}
			}
			source := in.Before
			if issue.LineBasis == "after" {
				source = in.After
			} else if issue.LineBasis != "before" {
				return "", errors.New("issue lineBasis must be before or after")
			}
			if _, err := model.ResolveSourceLocation(source, model.SourceLocation{StartLine: issue.StartLine, EndLine: issue.EndLine, Excerpt: issue.Excerpt}); err != nil {
				return "", err
			}
		}
		for _, id := range update.RemoveIssueIDs {
			delete(s.issues, id)
		}
		for _, a := range update.Assessments {
			s.assessments[a.RuleID] = a
		}
		for _, a := range update.HoldAssessments {
			s.holds[a.ItemID] = a
		}
		for _, entry := range update.Issues {
			s.issues[entry.ID] = entry.Issue
		}
		return string(raw(map[string]any{"recordedAssessments": len(s.assessments), "recordedIssues": len(s.issues), "recordedHolds": len(s.holds), "allSourcesAndRulesRead": s.completeRead(in)})), nil
	default:
		return "", errors.New("Only independent-review read and record tools are available")
	}
}
func reviewRecoveryTools() []map[string]any {
	text := map[string]any{"type": "string"}
	offset := map[string]any{"type": "integer", "minimum": 0}
	schema := independentReviewSchema()["properties"].(map[string]any)
	issues := schema["issues"].(map[string]any)["items"]
	record := objectSchema(map[string]any{"assessments": schema["assessments"], "issues": map[string]any{"type": "array", "items": objectSchema(map[string]any{"id": text, "issue": issues}, "id", "issue")}, "holdAssessments": schema["holdAssessments"], "removeIssueIds": map[string]any{"type": "array", "items": text}}, "assessments", "issues", "holdAssessments", "removeIssueIds")
	return []map[string]any{
		{"type": "function", "name": "read_review_catalog", "description": "Read all supplied rule identities and declared human holds as paged JSON.", "strict": true, "parameters": objectSchema(map[string]any{"offset": offset}, "offset")},
		{"type": "function", "name": "read_review_state", "description": "Read saved findings, stable issue IDs and source-read coverage as paged JSON.", "strict": true, "parameters": objectSchema(map[string]any{"offset": offset}, "offset")},
		{"type": "function", "name": "read_review_source", "description": "Read the complete before or after source by UTF-8 byte pages. Continue using nextOffset until complete.", "strict": true, "parameters": objectSchema(map[string]any{"source": map[string]any{"type": "string", "enum": []string{"before", "after"}}, "offset": offset}, "source", "offset")},
		{"type": "function", "name": "read_review_rule", "description": "Read a supplied rule body by UTF-8 byte pages.", "strict": true, "parameters": objectSchema(map[string]any{"ruleId": text, "offset": offset}, "ruleId", "offset")},
		{"type": "function", "name": "record_review", "description": "Save small independent-review findings; each id replaces its prior entry. This does not approve the candidate.", "strict": true, "parameters": record},
	}
}
func (c *client) reviewRecoveryRequest(history []json.RawMessage) ([]byte, error) {
	schema := objectSchema(map[string]any{"verdict": map[string]any{"type": "string", "enum": []string{"passed", "passed_with_holds", "needs_changes", "needs_human"}}, "summary": map[string]any{"type": "string"}}, "verdict", "summary")
	instructions := reviewInstructions + "\n\n" + reviewRecoveryInstructions
	tools := reviewRecoveryTools()
	if c.cfg.Provider == "claude" {
		native := []map[string]any{}
		for _, tool := range tools {
			native = append(native, map[string]any{"name": tool["name"], "description": tool["description"], "strict": true, "input_schema": tool["parameters"]})
		}
		return json.Marshal(map[string]any{"model": c.cfg.Deployment, "max_tokens": c.outputLimit(), "system": []any{map[string]any{"type": "text", "text": instructions}}, "messages": history, "tools": native, "tool_choice": map[string]any{"type": "auto", "disable_parallel_tool_use": true}, "output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}})
	}
	return json.Marshal(map[string]any{"model": c.cfg.Deployment, "store": false, "instructions": instructions, "input": history, "tools": tools, "parallel_tool_calls": false, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "independent_review_completion", "strict": true, "schema": schema}}, "include": []string{"reasoning.encrypted_content"}})
}
