package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
)

// Run carries durable plans and staged edits across bounded provider conversations.
// Provider IDs and reasoning never serve as the source of truth for recovery.
func Run(ctx context.Context, in Input) (proposal model.Proposal, err error) {
	c, err := newClient(in.Config)
	if err != nil {
		return proposal, &fatalError{err}
	}
	defer c.http.CloseIdleConnections()
	in.Config = c.cfg
	if !utf8.ValidString(in.Content) || !utf8.ValidString(in.SystemPrompt) {
		return proposal, errors.New("Agent input must be valid UTF-8")
	}
	if in.ReadRule == nil || in.SaveRepairState == nil {
		return proposal, &fatalError{errors.New("Rule reader and repair-state persistence must be configured")}
	}
	if err := safeRelativePath(in.File); err != nil {
		return proposal, err
	}
	required := make([]string, 0, len(in.Rules))
	for _, rule := range in.Rules {
		required = append(required, rule.ID)
	}
	for _, id := range required {
		if !validRuleID(id) {
			return proposal, fmt.Errorf("Invalid candidate rule ID %q", id)
		}
	}
	sum := sha256.Sum256([]byte(in.Content))
	actualHash := hex.EncodeToString(sum[:])
	if in.BaseHash == "" {
		in.BaseHash = actualHash
	} else if decoded, decodeErr := hex.DecodeString(in.BaseHash); decodeErr != nil || len(decoded) != sha256.Size || strings.ToLower(in.BaseHash) != in.BaseHash {
		return proposal, &fatalError{errors.New("Target baseHash must be a SHA-256 hex digest")}
	}
	// The engine hashes original bytes before decoding BOM/newline conventions.
	// Content is the normalized text used for exact replacements; do not rehash it
	// to override a trusted original-byte digest.
	state := model.RepairState{Version: 1}
	if in.RepairState != nil {
		state = cloneRepairState(*in.RepairState)
		if state.LastCandidate != nil {
			state.LastCandidate.Result = journalValidation(state.LastCandidate.Result)
		}
	}
	// Old edit-range attribution cannot be adopted or resumed into review. Keep
	// the plan, reviewed rules and accounting, then ask the editor for a fresh
	// candidate in this same repair loop instead of failing the execution. Failed
	// candidates cannot be adopted and retain their diagnostics for correction.
	upgradedAttribution := state.LastCandidate != nil && state.LastCandidate.Result.Passed && !state.LastCandidate.NoChange && state.LastCandidate.Result.AttributionVersion != model.LineAttributionVersion
	if upgradedAttribution {
		state.LastCandidate = nil
	}
	if state.Version != 1 {
		return proposal, &fatalError{errors.New("Unsupported repair-state version")}
	}
	if err := validateRepairCounters(state); err != nil {
		return proposal, &fatalError{err}
	}
	if err := in.BudgetBaseline.validate(state); err != nil {
		return proposal, &fatalError{err}
	}
	if state.RequestPending || state.Usage.Uncertain {
		if !in.AllowUncertainResume {
			return proposal, unknownUsage(errors.New("Previous LLM request has unverified usage; explicit resume is required"))
		}
		state.RequestPending = false
		state.Usage.Uncertain = true
	}
	initialUsage := state.Usage
	initialReviewCount := len(state.Reviews)
	initialElapsed := state.ElapsedMS
	started := time.Now()
	newUncertain := false
	persist := func(reserveInFlightTime bool) error {
		state.ElapsedMS = initialElapsed + time.Since(started).Milliseconds()
		snapshot := cloneRepairState(state)
		if saveErr := in.SaveRepairState(snapshot); saveErr != nil {
			return &fatalError{fmt.Errorf("Cannot persist repair state: %w", saveErr)}
		}
		return nil
	}
	checkpoint := func() error { return persist(false) }
	defer func() {
		if IsUsageUnknown(err) {
			state.Usage.Uncertain, newUncertain = true, true
		}
		if saveErr := checkpoint(); saveErr != nil {
			err = errors.Join(err, saveErr)
			proposal.Outcome = ""
		}
		proposal.Usage = usageDifference(state.Usage, initialUsage)
		proposal.Usage.Uncertain = newUncertain || state.Usage.Uncertain && !initialUsage.Uncertain
		for _, review := range state.Reviews[initialReviewCount:] {
			proposal.Usage.Uncertain = proposal.Usage.Uncertain || review.Usage.Uncertain
		}
	}()
	ruleCache := map[string]string{}
	catalog := map[string]bool{}
	for _, rule := range in.Rules {
		catalog[rule.ID] = true
	}
	for _, id := range state.ReadRuleIDs {
		if !validRuleID(id) || !catalog[id] {
			return proposal, &fatalError{errors.New("Restored rule is outside the current catalog")}
		}
		value, readErr := in.ReadRule(id)
		if readErr != nil || !utf8.ValidString(value) {
			return proposal, &fatalError{errors.New("Previously read rule could not be restored")}
		}
		ruleCache[id] = value
	}
	if len(state.StagedEdits) == 0 && state.LastCandidate != nil && !state.LastCandidate.NoChange {
		for index, edit := range state.LastCandidate.Request.Edits {
			state.StagedEdits = append(state.StagedEdits, model.StagedEdit{ID: fmt.Sprintf("edit-%d", index+1), Edit: edit})
		}
	}
	reason := "Begin or resume this file using persisted incremental plans and edits."
	if upgradedAttribution {
		reason += " Previous attribution was obsolete; restage precise attributed edits and validate. Saved plan/review findings remain available."
	}
	pagedPrompt := len(in.SystemPrompt) > 64<<10
	if pagedPrompt {
		in.Rules = append([]model.Rule{}, in.Rules...)
		for i := range in.Rules {
			in.Rules[i].Always = false
		}
	}
	history := repairContext(in, state, ruleCache, reason)
	lastRejection := ""
	// Resume a final candidate's interrupted review without asking the editor to
	// recreate its final answer or resetting any request/cost counters.
	if candidate := state.LastCandidate; candidate != nil && candidate.ReviewRequested && candidate.Request.PlanRevision == state.Plan.Revision {
		var final model.Proposal
		var parseErr error
		if candidate.NoChange {
			parseErr = noChangePlan(state.Plan, in)
			if parseErr == nil && (!currentNoChangeCandidate(candidate, state.Plan, in.BaseHash) || strings.TrimSpace(candidate.ReviewNote) == "") {
				parseErr = errors.New("Restored unchanged-source review does not match the current source and plan")
			}
			final = model.Proposal{Outcome: "skipped", CandidateID: candidate.Result.CandidateID, Note: candidate.ReviewNote}
		} else {
			final, parseErr = parseCandidateFinal(string(raw(map[string]any{"outcome": "modified", "candidateId": candidate.Result.CandidateID, "note": candidate.ReviewNote})), state, in, required)
		}
		if parseErr != nil {
			return proposal, parseErr
		}
		checked, accepted, reviewErr := reviewFinal(ctx, in, &state, final, checkpoint, func() error { return persist(true) })
		if reviewErr != nil {
			return proposal, reviewErr
		}
		if accepted {
			return checked, nil
		}
		lastRejection = independentReviewRejection(state)
		history = append(history, raw(map[string]any{"role": "user", "content": reviewFeedback(state)}))
	}
	seenCallIDs := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return proposal, err
		}
		if historyBytes(history) > historyRolloverBytes {
			history = repairContext(in, state, ruleCache, "Conversation rolled over to keep context bounded; saved plans, edits and review findings are intact.")
			seenCallIDs = map[string]bool{}
			if in.Log != nil {
				in.Log("会話を保存済みの計画・編集状態から引き継ぎました")
			}
		}
		prompt := runPrompt(in)
		if pagedPrompt {
			prompt = incrementalPagedPrompt()
		}
		body, err := c.migrationRequest(history, prompt)
		if err != nil {
			return proposal, err
		}
		if err := c.reserve(body, state.Usage); err != nil {
			return proposal, err
		}
		requestID, err := repairRequestID()
		if err != nil {
			return proposal, &fatalError{err}
		}
		state.RequestPending, state.RequestID, state.RequestKind = true, requestID, "editor"
		state.Usage.Turns++ // Reserve before sending, including requests with unknown usage.
		if err := persist(true); err != nil {
			return proposal, err
		}
		if in.Log != nil {
			in.Log(fmt.Sprintf("%s turn %d", c.providerLabel(), state.Usage.Turns))
		}
		res, requestErr := c.request(ctx, body, in.Log)
		if requestErr != nil {
			state.Usage.Turns += max(0, res.RequestAttempts-1)
			if IsUsageUnknown(requestErr) {
				state.Usage.Uncertain = true
				newUncertain = true
			}
			if IsContextLimit(requestErr) {
				state.RequestPending = false
				pagedPrompt = true
				in.Rules = append([]model.Rule{}, in.Rules...)
				for i := range in.Rules {
					in.Rules[i].Always = false
				}
				history = minimalRepairContext(in, state, "Provider context limit: continue from saved state using small paged reads and staged edits.")
				seenCallIDs = map[string]bool{}
				if err := checkpoint(); err != nil {
					return proposal, err
				}
				continue
			}
			if !IsUsageUnknown(requestErr) {
				state.RequestPending = false
			}
			return proposal, requestErr
		}
		usage := model.Usage{}
		usageErr := c.addUsage(&usage, res.Usage)
		state.Usage.InputTokens += usage.InputTokens
		state.Usage.CachedTokens += usage.CachedTokens
		state.Usage.OutputTokens += usage.OutputTokens
		state.Usage.CostUSD += usage.CostUSD
		state.Usage.Turns += max(0, usage.Turns-1)
		state.Usage.Uncertain = state.Usage.Uncertain || usage.Uncertain
		newUncertain = newUncertain || usage.Uncertain
		state.RequestPending = IsUsageUnknown(usageErr)
		if state.RequestPending {
			state.Usage.Uncertain, newUncertain = true, true
		}
		if err := checkpoint(); err != nil {
			return proposal, errors.Join(usageErr, err)
		}
		if usageErr != nil {
			return proposal, usageErr
		}
		if completionErr := completed(res); completionErr != nil {
			if IsContextLimit(completionErr) {
				pagedPrompt = true
				in.Rules = append([]model.Rule{}, in.Rules...)
				for i := range in.Rules {
					in.Rules[i].Always = false
				}
				history = minimalRepairContext(in, state, "Provider context window reached while generating a response. Incomplete output was discarded; all prior saved plans and edits remain. Continue via small paged reads and incremental tool calls.")
				seenCallIDs = map[string]bool{}
				continue
			}
			if !IsOutputLimit(completionErr) {
				return proposal, completionErr
			}
			c.growOutputLimit()
			history = repairContext(in, state, ruleCache, "The previous response reached the provider output limit and was discarded without applying incomplete tool calls. Continue from saved state. Send ONE small update_state or stage_edits batch at a time; never resend the full plan or full candidate. Split large structural edits using exact minimal fragments.")
			seenCallIDs = map[string]bool{}
			if in.Log != nil {
				in.Log("LLM応答が出力上限に達したため、保存済みの計画・編集から小さい単位で継続します")
			}
			continue
		}
		calls, finalText, err := parseOutput(res.Output)
		if err != nil {
			if strings.Contains(err.Error(), "refused") {
				return proposal, err
			}
			history = repairContext(in, state, ruleCache, "Provider output could not be decoded and no calls from that response were applied. Continue with a small valid tool call. Error: "+bounded(err.Error(), 1024))
			seenCallIDs = map[string]bool{}
			continue
		}
		// Only a rejection from the latest completed response explains a stop
		// at this boundary. Older, corrected errors remain in the diagnostic log.
		lastRejection = ""
		if len(calls) == 0 {
			final, finalErr := parseCandidateFinal(finalText, state, in, required)
			if finalErr == nil {
				checked, accepted, reviewErr := reviewFinal(ctx, in, &state, final, checkpoint, func() error { return persist(true) })
				if reviewErr != nil {
					return proposal, reviewErr
				}
				if accepted {
					return checked, nil
				}
				lastRejection = independentReviewRejection(state)
				if in.Log != nil {
					in.Log(lastRejection)
				}
				history = appendFinalFeedback(c, history, res, reviewFeedback(state))
				continue
			}
			// A premature/malformed final is feedback, not a new session or a reset.
			lastRejection = "最終回答の拒否: " + bounded(finalErr.Error(), 2048)
			if in.Log != nil {
				in.Log(lastRejection)
			}
			history = appendFinalFeedback(c, history, res, "Final response rejected: "+bounded(finalErr.Error(), 2048)+". Continue correcting the plan/candidate with the available tools. Only a genuine missing-context, conflicting-rule, cross-file or unavailable-API blocker recorded in update_state justifies needs_human; unfinished work and anticipated budget exhaustion do not.")
			continue
		}
		results := []toolResult{}
		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return proposal, err
			}
			if call.CallID == "" || len(call.CallID) > 256 || seenCallIDs[call.CallID] {
				return proposal, errors.New("LLM returned an empty, duplicate or oversized tool call ID")
			}
			seenCallIDs[call.CallID] = true
			state.ToolCalls++
			if err := checkpoint(); err != nil {
				return proposal, err
			}
			result, toolErr := executeRepairTool(ctx, in, call, &state, required, ruleCache, c.cfg, checkpoint, func() error { return persist(true) })
			if IsFatal(toolErr) || errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded) {
				return proposal, toolErr
			}
			isError := toolErr != nil
			if toolErr != nil {
				result = "Tool error: " + bounded(toolErr.Error(), 2048)
			}
			if len(result) > toolPreviewBytes {
				page, _ := pageText(result, 0)
				result = string(raw(map[string]any{"preview": page, "instruction": "Read full saved details with read_state. For source/context use smaller line ranges or returned nextOffset; size is a transfer boundary, not a human blocker."}))
			}
			toolRejection := ""
			if isError {
				toolRejection = "ツール " + safeIdentifier(call.Name) + " の拒否: " + bounded(result, 2048)
			} else if (call.Name == "validate_candidate" || call.Name == "validate_staged_candidate") && state.LastCandidate != nil && !state.LastCandidate.Result.Passed {
				toolRejection = candidateValidationRejection(state.LastCandidate.Result)
			}
			if toolRejection != "" {
				lastRejection = toolRejection
				if in.Log != nil {
					in.Log(toolRejection)
				}
			}
			state.ReadBytes += len(result)
			if err := checkpoint(); err != nil {
				return proposal, err
			}
			results = append(results, toolResult{CallID: call.CallID, Content: result, IsError: isError})
			if in.Log != nil {
				in.Log(fmt.Sprintf("Tool: %s (%d bytes)", safeIdentifier(call.Name), len(result)))
			}
		}
		history = c.appendTurn(history, res, results)
	}
}

func independentReviewRejection(state model.RepairState) string {
	if state.LastCandidate != nil && state.LastCandidate.Review != nil {
		return "独立レビューの修正要求: " + bounded(state.LastCandidate.Review.Summary, 2048)
	}
	return "独立レビューによる最終候補の承認が必要です"
}

func candidateValidationRejection(result model.CandidateValidation) string {
	for _, diagnostic := range result.Diagnostics {
		if strings.TrimSpace(diagnostic.Message) != "" {
			return "候補検証の不合格: " + bounded(diagnostic.Message, 2048)
		}
	}
	for _, check := range result.Checks {
		if check.Status == "failed" {
			return "候補検証の不合格: " + bounded(check.Name+": "+check.Detail, 2048)
		}
	}
	return "候補検証の不合格: 検証結果の指摘を修正してください"
}

func executeRepairTool(ctx context.Context, in Input, call functionCall, state *model.RepairState, required []string, cache map[string]string, cfg model.Config, checkpoint, reserveElapsed func() error) (string, error) {
	switch call.Name {
	case "stage_edits":
		var update model.StageEditsRequest
		if err := strictRequiredJSON(call.Arguments, &update, "planRevision", "baseHash", "edits", "removeEditIds"); err != nil {
			return "", err
		}
		if err := requireNewCandidateAttribution(model.CandidateRequest{Edits: stagedEditsOnly(update.Edits)}, call.Arguments); err != nil {
			return "", err
		}
		if err := stageEdits(state, update, in.Content, in.BaseHash); err != nil {
			return "", err
		}
		if err := checkpoint(); err != nil {
			return "", err
		}
		return string(raw(map[string]any{"planRevision": state.Plan.Revision, "stagedEditCount": len(state.StagedEdits), "instruction": "Edits saved, not yet validated. Continue staging; then validate_staged_candidate."})), nil
	case "read_state":
		return readSavedState(in, *state, call.Arguments)
	case "read_target":
		return readTarget(in, *state, call.Arguments, false)
	case "read_candidate":
		return readTarget(in, *state, call.Arguments, true)
	case "read_rule", "read_rules", "read_rule_page":
		output, err := readRulesPaged(in, state, cache, call)
		state.ReadRuleIDs = sortedRuleIDs(cache)
		return output, err
	case "update_state":
		var update model.PlanUpdate
		if strictRequiredJSON(call.Arguments, &update, "expectedRevision", "ruleDecisions", "items") != nil {
			return "", errors.New("update_state requires expectedRevision, ruleDecisions and items")
		}
		if err := requirePlanSourceLocationFields(call.Arguments); err != nil {
			return "", err
		}
		plan, err := MergeRepairPlan(state.Plan, update, in.Rules, sortedRuleIDs(cache))
		if err != nil {
			return "", err
		}
		if err := resolvePlanSourceLocations(&plan, in.Content); err != nil {
			return "", err
		}
		state.Plan = plan
		if err := checkpoint(); err != nil {
			return "", err
		}
		return string(raw(map[string]any{"revision": plan.Revision, "itemCount": len(plan.Items), "reviewedRuleCount": len(plan.RuleDecisions), "stagedEditCount": len(state.StagedEdits), "instruction": "Saved incrementally. Use read_state to inspect the full plan; omitted entries were retained."})), nil
	case "validate_candidate", "validate_staged_candidate":
		var request model.CandidateRequest
		if call.Name == "validate_staged_candidate" {
			var args struct {
				PlanRevision int    `json:"planRevision"`
				BaseHash     string `json:"baseHash"`
			}
			if err := strictRequiredJSON(call.Arguments, &args, "planRevision", "baseHash"); err != nil {
				return "", err
			}
			if args.PlanRevision != state.Plan.Revision || args.BaseHash != in.BaseHash {
				return "", errors.New("validation requires current planRevision and original baseHash")
			}
			request = stagedRequest(*state, in.BaseHash)
		} else if strictRequiredJSON(call.Arguments, &request, "planRevision", "baseHash", "edits", "addressedItemIds") != nil {
			return "", errors.New("validate_candidate requires planRevision, baseHash, edits and addressedItemIds")
		}
		if request.BaseHash != in.BaseHash {
			return "", errors.New("Candidate baseHash differs from the original target; use the supplied baseHash")
		}
		if call.Name == "validate_candidate" {
			if err := requireNewCandidateAttribution(request, call.Arguments); err != nil {
				return "", err
			}
		}
		if err := requireCompletePlan(state.Plan, required); err != nil {
			return "", err
		}
		if err := CheckCandidatePlan(state.Plan, request); err != nil {
			return "", err
		}
		if err := validateExactEdits(request.Edits, in.Content); err != nil {
			return "", err
		}
		if err := CheckCandidateHolds(state.Plan, request, in.Content); err != nil {
			return "", err
		}
		if in.ValidateCandidate == nil {
			return "", &fatalError{errors.New("Candidate validator is not configured")}
		}
		// A newly accepted validation supersedes the previous candidate even if
		// it subsequently stops before returning a result. Never recover an older
		// passed candidate as though it were the latest validation.
		state.LastCandidate = nil
		state.ValidationCount++
		// Reserve elapsed time before validating the candidate, without marking
		// an LLM request pending.
		if err := reserveElapsed(); err != nil {
			return "", err
		}
		result, err := in.ValidateCandidate(ctx, request)
		if err != nil {
			return "", &fatalError{err}
		}
		if result.CandidateID == "" || len(result.CandidateID) > 256 || len(result.CandidateHash) > 128 || result.PlanRevision != request.PlanRevision || (result.Passed && result.AttributionVersion != model.LineAttributionVersion) {
			return "", &fatalError{errors.New("Candidate validator returned inconsistent candidate metadata")}
		}
		state.LastCandidate = &model.CandidateRecord{Request: request, Result: journalValidation(result)}
		if call.Name == "validate_candidate" {
			state.StagedEdits = nil
			for index, edit := range request.Edits {
				state.StagedEdits = append(state.StagedEdits, model.StagedEdit{ID: fmt.Sprintf("edit-%d", index+1), Edit: edit})
			}
		}
		if err := checkpoint(); err != nil {
			return "", err
		}
		return string(raw(toolValidation(result))), nil
	default:
		value, err := executeTool(in, call, cache)
		state.ReadRuleIDs = sortedRuleIDs(cache)
		return value, err
	}
}

func parseCandidateFinal(text string, state model.RepairState, in Input, required []string) (model.Proposal, error) {
	var final struct {
		Outcome     string `json:"outcome"`
		CandidateID string `json:"candidateId"`
		Note        string `json:"note"`
	}
	if err := strictRequiredJSON(text, &final, "outcome", "candidateId", "note"); err != nil {
		return model.Proposal{}, errors.New("Final JSON must have only outcome, candidateId and note")
	}
	if strings.TrimSpace(final.Note) == "" || len(final.CandidateID) > 256 {
		return model.Proposal{}, errors.New("Final note/candidate ID is empty or oversized")
	}
	result := model.Proposal{Outcome: final.Outcome, Note: final.Note, Edits: []model.Edit{}, RulesApplied: []string{}}
	if final.Outcome == "needs_human" {
		if final.CandidateID != "" {
			return result, errors.New("needs_human cannot select a candidate")
		}
		if state.Plan.Revision > 0 && !planHasHumanBlocker(state.Plan) {
			return result, errors.New("needs_human requires a recorded human-decision blocker: use update_state with a blocked rule and reason or a blocked item and holdReason for genuine missing context, conflicting rules, cross-file changes or unavailable APIs. Unfinished edits, pending revalidation and anticipated budget exhaustion are not human blockers; continue the current work and let the runner enforce limits")
		}
		if state.Plan.Revision > 0 {
			if _, _, err := validatePlanSnapshot(state.Plan); err != nil {
				return result, err
			}
			if err := validatePlanSourceLocations(state.Plan, in.Content); err != nil {
				return result, err
			}
		}
		if holds, err := CandidateHolds(state.Plan); err == nil && len(holds) > 0 {
			for _, item := range state.Plan.Items {
				if item.Status != "blocked" {
					return result, errors.New("localized human holds do not discard independent safe work: validate and submit a modified candidate for every non-blocked item; if an item depends on unresolved judgment, explicitly mark that dependent item blocked too")
				}
			}
		}
		return result, nil
	}
	if state.Plan.Revision < 1 {
		return result, errors.New("Required rules must be reviewed with update_state before finishing")
	}
	reviewed := map[string]bool{}
	for _, decision := range state.Plan.RuleDecisions {
		reviewed[decision.RuleID] = true
	}
	for _, id := range required {
		if !reviewed[id] {
			return result, fmt.Errorf("Rule %s has not been reviewed", id)
		}
	}
	switch final.Outcome {
	case "skipped":
		if hasUnresolvedReview(state) {
			return result, errors.New("Unresolved independent review requires a new validated candidate or needs_human; skipped cannot discard review findings")
		}
		if final.CandidateID != "" || len(state.Plan.Items) > 0 {
			return result, errors.New("skipped requires an empty change plan and no candidate")
		}
		for _, decision := range state.Plan.RuleDecisions {
			if decision.Decision != "no_change" {
				return result, errors.New("skipped requires no_change decisions for every reviewed rule")
			}
		}
	case "modified":
		candidate := state.LastCandidate
		if candidate == nil || candidate.NoChange || candidate.Result.AttributionVersion != model.LineAttributionVersion || !candidate.Result.Passed || candidate.Result.CandidateID != final.CandidateID || candidate.Result.PlanRevision != state.Plan.Revision || candidate.Request.PlanRevision != state.Plan.Revision || candidate.Request.BaseHash != in.BaseHash {
			return result, errors.New("modified requires the current plan's last validated, passed candidate ID")
		}
		if err := CheckCandidatePlan(state.Plan, candidate.Request); err != nil {
			return result, err
		}
		if err := validateExactEdits(candidate.Request.Edits, in.Content); err != nil {
			return result, err
		}
		if err := CheckCandidateHolds(state.Plan, candidate.Request, in.Content); err != nil {
			return result, err
		}
		result.Edits = append([]model.Edit{}, candidate.Request.Edits...)
		result.CandidateID = final.CandidateID
		addressed, applied := map[string]bool{}, map[string]bool{}
		for _, id := range candidate.Request.AddressedItemIDs {
			addressed[id] = true
		}
		for _, item := range state.Plan.Items {
			if addressed[item.ID] && !applied[item.RuleID] {
				result.RulesApplied = append(result.RulesApplied, item.RuleID)
				applied[item.RuleID] = true
			}
		}
	default:
		return result, errors.New("Invalid final outcome")
	}
	return result, nil
}

// A plan with pending work is not a human hold. Keep the pre-plan escape for
// missing context that prevents planning, but require an explicit reason once
// the model has registered its assessment. No note-language heuristics are used.
func planHasHumanBlocker(plan model.RepairPlan) bool {
	for _, decision := range plan.RuleDecisions {
		if decision.Decision == "blocked" && strings.TrimSpace(decision.Reason) != "" {
			return true
		}
	}
	for _, item := range plan.Items {
		if item.Status == "blocked" && strings.TrimSpace(item.HoldReason) != "" {
			return true
		}
	}
	return false
}

func validateExactEdits(edits []model.Edit, original string) error {
	if len(edits) == 0 {
		return errors.New("Candidate requires at least one exact edit")
	}
	type span struct{ start, end int }
	spans := []span{}
	for _, edit := range edits {
		if edit.OldText == "" || edit.OldText == edit.NewText || !utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) {
			return errors.New("Each edit requires nonempty oldText and a different UTF-8 newText")
		}
		start := strings.Index(original, edit.OldText)
		if start < 0 || start != strings.LastIndex(original, edit.OldText) {
			return errors.New("Edit oldText must occur exactly once in the original target file")
		}
		end := start + len(edit.OldText)
		for _, prior := range spans {
			if start < prior.end && prior.start < end {
				return errors.New("Agent edits overlap in the original target file")
			}
		}
		spans = append(spans, span{start, end})
	}
	return nil
}

func strictRequiredJSON(text string, dest any, fields ...string) error {
	if err := strictJSON(text, dest); err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &values); err != nil {
		return err
	}
	for _, field := range fields {
		value, ok := values[field]
		if !ok || string(value) == "null" {
			return fmt.Errorf("Missing required field %s", field)
		}
	}
	// JSON schema requires both edit properties, even when newText is empty.
	if edits, ok := values["edits"]; ok {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(edits, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			for _, key := range []string{"oldText", "newText"} {
				v, exists := row[key]
				if !exists || string(v) == "null" {
					return errors.New("Each edit must contain oldText and newText")
				}
			}
		}
	}
	return nil
}
func sortedRuleIDs(cache map[string]string) []string {
	ids := make([]string, 0, len(cache))
	for id := range cache {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func cloneRepairState(state model.RepairState) model.RepairState {
	data, _ := json.Marshal(state)
	var copy model.RepairState
	_ = json.Unmarshal(data, &copy)
	return copy
}
func usageDifference(current, base model.Usage) model.Usage {
	return model.Usage{InputTokens: current.InputTokens - base.InputTokens, CachedTokens: current.CachedTokens - base.CachedTokens, OutputTokens: current.OutputTokens - base.OutputTokens, CostUSD: current.CostUSD - base.CostUSD, Turns: current.Turns - base.Turns}
}
func repairRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", errors.New("Cannot generate LLM request ID")
	}
	return hex.EncodeToString(id[:]), nil
}
func validateRepairCounters(state model.RepairState) error {
	u := state.Usage
	if state.ElapsedMS < 0 || state.ToolCalls < 0 || state.ReadBytes < 0 || state.ValidationCount < 0 || state.ReviewCount < 0 || u.Turns < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedTokens < 0 || u.CachedTokens > u.InputTokens || u.CostUSD < 0 {
		return errors.New("Repair state has invalid counters")
	}
	return nil
}
func appendFinalFeedback(c *client, history []json.RawMessage, res response, message string) []json.RawMessage {
	if c.cfg.Provider == "claude" {
		history = append(history, res.NativeContinuation)
	} else {
		history = append(history, res.Output...)
	}
	return append(history, raw(map[string]any{"role": "user", "content": message}))
}
