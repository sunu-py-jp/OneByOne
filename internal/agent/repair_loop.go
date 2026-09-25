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

// Run uses a fresh provider conversation for each invocation. Only the compact
// repair journal survives an interruption; server IDs and reasoning never do.
func Run(ctx context.Context, in Input) (proposal model.Proposal, err error) {
	c, err := newClient(in.Config)
	if err != nil {
		return proposal, &fatalError{err}
	}
	defer c.http.CloseIdleConnections()
	in.Config = c.cfg
	if len(in.Content) > c.cfg.MaxFileBytes {
		return proposal, fmt.Errorf("Target file exceeds MaxFileBytes (%d)", c.cfg.MaxFileBytes)
	}
	if !utf8.ValidString(in.Content) || !utf8.ValidString(in.SystemPrompt) {
		return proposal, errors.New("Agent input must be valid UTF-8")
	}
	if len(in.SystemPrompt) > 512<<10 {
		return proposal, errors.New("Rule prompt exceeds 512 KiB; shorten the common rules/index")
	}
	if len(in.PreviousFailure) > maxToolBytes {
		return proposal, errors.New("Previous failure context exceeds 96 KiB")
	}
	if in.ReadRule == nil || in.SaveRepairState == nil {
		return proposal, &fatalError{errors.New("Rule reader and repair-state persistence must be configured")}
	}
	if err := safeRelativePath(in.File); err != nil {
		return proposal, err
	}
	required := append([]string{}, in.CandidateRules...)
	for _, rule := range in.Rules {
		if rule.Always {
			required = append(required, rule.ID)
		}
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
		if c.cfg.MaxCostUSD > 0 || !in.AllowUncertainResume {
			return proposal, unknownUsage(errors.New("Previous LLM request has unverified usage; explicit unpriced resume is required and cost-limited runs cannot resume"))
		}
		state.RequestPending = false
		state.Usage.Uncertain = true
	}
	initialUsage := state.Usage
	initialElapsed := state.ElapsedMS
	started := time.Now()
	newUncertain := false
	persist := func(reserveInFlightTime bool) error {
		state.ElapsedMS = initialElapsed + time.Since(started).Milliseconds()
		snapshot := cloneRepairState(state)
		reservedElapsed := in.BudgetBaseline.ElapsedMS + int64(c.cfg.TimeoutSeconds)*1000
		if reserveInFlightTime && c.cfg.TimeoutSeconds > 0 && snapshot.ElapsedMS < reservedElapsed {
			// A crash cannot tell us how much of an in-flight request or validation deadline was consumed.
			// Reserve the remaining time durably; a handled response/error replaces it
			// with measured active time. Only an explicit new execution resets its
			// baseline; automatic repair calls keep the same execution budget.
			snapshot.ElapsedMS = reservedElapsed
		}
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
		proposal.Usage.Uncertain = newUncertain
	}()
	remainingMS := int64(c.cfg.TimeoutSeconds)*1000 - in.BudgetBaseline.used(state).ElapsedMS
	if c.cfg.TimeoutSeconds > 0 && remainingMS <= 0 {
		return proposal, errors.New("Per-file elapsed-time limit reached for this execution")
	}
	if c.cfg.MaxTurns > 0 && in.BudgetBaseline.used(state).ReadBytes > maxReadBytes {
		return proposal, errors.New("Agent exceeded the 512 KiB tool-read limit for this execution")
	}
	if c.cfg.MaxTurns > 0 && in.BudgetBaseline.used(state).ToolCalls > maxToolCalls {
		return proposal, errors.New("Agent exceeded the tool-call limit for this execution")
	}
	var cancel context.CancelFunc
	if c.cfg.TimeoutSeconds > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(remainingMS)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
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
		if readErr != nil || !utf8.ValidString(value) || len(value) > maxToolBytes {
			return proposal, &fatalError{errors.New("Previously read rule could not be restored")}
		}
		ruleCache[id] = value
	}
	user := map[string]any{"file": in.File, "content": in.Content, "baseHash": in.BaseHash, "candidateRules": in.CandidateRules, "previousFailure": in.PreviousFailure}
	if in.RepairState != nil {
		used := in.BudgetBaseline.used(state)
		user["repairState"] = map[string]any{"plan": state.Plan, "lastCandidate": state.LastCandidate, "historicalUsage": state.Usage, "executionUsage": map[string]any{"turns": used.Turns, "elapsedMs": used.ElapsedMS, "validationCount": used.ValidationCount, "reviewCount": used.ReviewCount, "toolCalls": used.ToolCalls, "readBytes": used.ReadBytes}}
		user["previouslyReadRules"] = ruleCache
	}
	userJSON, _ := json.Marshal(user)
	history := []json.RawMessage{raw(map[string]any{"role": "user", "content": string(userJSON)})}
	lastRejection := ""
	// Resume a final candidate's interrupted review without asking the editor to
	// recreate its final answer or resetting any request/cost counters.
	if candidate := state.LastCandidate; candidate != nil && candidate.ReviewRequested && candidate.Request.PlanRevision == state.Plan.Revision {
		final, parseErr := parseCandidateFinal(string(raw(map[string]any{"outcome": "modified", "candidateId": candidate.Result.CandidateID, "note": candidate.ReviewNote})), state, in, required)
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
	for c.cfg.MaxTurns == 0 || in.BudgetBaseline.used(state).Turns < c.cfg.MaxTurns {
		if err := ctx.Err(); err != nil {
			return proposal, err
		}
		// Keep the changing budget at the end of the prompt, outside the cached
		// rules prefix and provider history. It includes this upcoming request.
		prompt := instructions + "\n\n" + in.SystemPrompt + "\n\n" + turnBudgetPrompt(in.BudgetBaseline.used(state).Turns, c.cfg.MaxTurns)
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
			if c.cfg.MaxTurns > 0 {
				in.Log(fmt.Sprintf("%s turn %d/%d", c.providerLabel(), in.BudgetBaseline.used(state).Turns, c.cfg.MaxTurns))
			} else {
				in.Log(fmt.Sprintf("%s turn %d（上限なし）", c.providerLabel(), in.BudgetBaseline.used(state).Turns))
			}
		}
		res, requestErr := c.request(ctx, body, in.Log)
		if requestErr != nil {
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
		if c.cfg.MaxCostUSD > 0 && state.Usage.CostUSD > c.cfg.MaxCostUSD {
			return proposal, errors.New("Reported LLM usage exceeds the configured cost limit; no further request will be sent")
		}
		if err := completed(res); err != nil {
			return proposal, err
		}
		calls, finalText, err := parseOutput(res.Output)
		if err != nil {
			return proposal, err
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
		if len(calls) > 8 || (c.cfg.MaxTurns > 0 && in.BudgetBaseline.used(state).ToolCalls+len(calls) > maxToolCalls) {
			return proposal, errors.New("Agent exceeded the tool-call limit for this execution")
		}
		results := []toolResult{}
		for _, call := range calls {
			if call.CallID == "" || len(call.CallID) > 256 || seenCallIDs[call.CallID] {
				return proposal, errors.New("LLM returned an empty, duplicate or oversized tool call ID")
			}
			seenCallIDs[call.CallID] = true
			state.ToolCalls++
			if err := checkpoint(); err != nil {
				return proposal, err
			}
			result, toolErr := executeRepairTool(ctx, in, call, &state, required, ruleCache, c.cfg, checkpoint, func() error { return persist(true) })
			if errors.Is(toolErr, errValidationLimit) || IsFatal(toolErr) || errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded) {
				return proposal, toolErr
			}
			isError := toolErr != nil
			if toolErr != nil {
				result = "Tool error: " + bounded(toolErr.Error(), 2048)
			}
			if len(result) > maxToolBytes {
				result = "Tool error: response exceeds 96 KiB; reduce the plan/context size or return needs_human"
				isError = true
			}
			toolRejection := ""
			if isError {
				toolRejection = "ツール " + safeIdentifier(call.Name) + " の拒否: " + bounded(result, 2048)
			} else if call.Name == "validate_candidate" && state.LastCandidate != nil && !state.LastCandidate.Result.Passed {
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
			if c.cfg.MaxTurns > 0 && in.BudgetBaseline.used(state).ReadBytes > maxReadBytes {
				return proposal, errors.New("Agent exceeded the 512 KiB tool-read limit for this execution")
			}
			results = append(results, toolResult{CallID: call.CallID, Content: result, IsError: isError})
			if in.Log != nil {
				in.Log(fmt.Sprintf("Tool: %s (%d bytes)", safeIdentifier(call.Name), len(result)))
			}
		}
		history = c.appendTurn(history, res, results)
	}
	return proposal, turnLimitError(in.BudgetBaseline.used(state).Turns, c.cfg.MaxTurns, lastRejection)
}

func turnBudgetPrompt(used, limit int) string {
	if limit == 0 {
		return fmt.Sprintf("Current execution has used %d per-file LLM turns. No turn limit is configured. Continue until the complete candidate passes mechanical validation and independent review, or a genuine human-decision blocker is found. Do not invent an execution window or stop because work remains or the complete candidate is lengthy. The runner enforces any separately configured time, validation and cost limits and user cancellation. Only genuine blockers recorded with update_state justify needs_human; unfinished work and anticipated limits do not. Never bypass validation or independent review.", used)
	}
	return fmt.Sprintf("Current per-file LLM turn budget: %d used of %d; %d remaining, including this request. This budget covers only the current execution. Editor, automatic repairs and independent-review requests share it; historical usage from earlier executions does not consume it. The budget resets to zero only when the user explicitly starts another execution. A modified final answer requires one additional independent-review request before adoption; reserve that turn. Use available requests to complete the plan and validate the candidate, avoiding redundant reads/status-only updates. The runner, not the model, enforces exhausted budgets and preserves progress. Do not return needs_human, mark work blocked or stop because the remaining budget seems insufficient, a complete proposal is lengthy, or revalidation is still pending. Those are unfinished work, not human-decision blockers. Continue useful work until completion or a runner-enforced stop. Never bypass validation or independent review to fit the budget.", used, limit, max(0, limit-used))
}

// TurnLimitError describes usage within this explicit execution, not history.
func TurnLimitError(used, limit int) error {
	return fmt.Errorf("今回の実行でファイル単位のLLMターン上限（MaxTurns）に達しました。今回 %d / 上限 %d ターン（編集と独立レビューの合計）です。再実行するとターン数は0から始まります。保存済みの計画と処理履歴は保持されます。", used, limit)
}

func turnLimitError(used, limit int, lastRejection string) error {
	message := TurnLimitError(used, limit).Error()
	if lastRejection != "" {
		message += " 直前に完了できなかった理由: " + bounded(lastRejection, 2048)
	}
	return errors.New(message)
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

var errValidationLimit = errors.New("Per-file candidate validation limit reached for this execution")

func executeRepairTool(ctx context.Context, in Input, call functionCall, state *model.RepairState, required []string, cache map[string]string, cfg model.Config, checkpoint, reserveElapsed func() error) (string, error) {
	switch call.Name {
	case "update_state":
		var update model.PlanUpdate
		if len(call.Arguments) > maxToolBytes || strictRequiredJSON(call.Arguments, &update, "expectedRevision", "ruleDecisions", "items") != nil {
			return "", errors.New("update_state requires expectedRevision, ruleDecisions and items within 96 KiB")
		}
		plan, err := UpdateRepairPlan(state.Plan, update, in.Rules, required, sortedRuleIDs(cache))
		if err != nil {
			return "", err
		}
		state.Plan = plan
		if err := checkpoint(); err != nil {
			return "", err
		}
		return string(raw(map[string]any{"plan": plan, "revision": plan.Revision, "remainingItemIds": PlanRemainingItems(plan)})), nil
	case "validate_candidate":
		var request model.CandidateRequest
		if len(call.Arguments) > maxRequestBytes || strictRequiredJSON(call.Arguments, &request, "planRevision", "baseHash", "edits", "addressedItemIds") != nil {
			return "", errors.New("validate_candidate requires planRevision, baseHash, edits and addressedItemIds")
		}
		if request.BaseHash != in.BaseHash {
			return "", errors.New("Candidate baseHash differs from the original target; use the supplied baseHash")
		}
		if err := requireNewCandidateAttribution(request); err != nil {
			return "", err
		}
		if err := CheckCandidatePlan(state.Plan, request); err != nil {
			return "", err
		}
		if err := validateExactEdits(request.Edits, in.Content); err != nil {
			return "", err
		}
		maxValidations := cfg.MaxAttempts
		if maxValidations > 0 && in.BudgetBaseline.used(*state).ValidationCount >= maxValidations {
			return "", errValidationLimit
		}
		if in.ValidateCandidate == nil {
			return "", &fatalError{errors.New("Candidate validator is not configured")}
		}
		// A newly accepted validation supersedes the previous candidate even if
		// it subsequently stops before returning a result. Never recover an older
		// passed candidate as though it were the latest validation.
		state.LastCandidate = nil
		state.ValidationCount++
		// Build/test commands can outlive the process as well. Reserve elapsed time
		// durably before invoking them, without marking an LLM request pending.
		if err := reserveElapsed(); err != nil {
			return "", err
		}
		result, err := in.ValidateCandidate(ctx, request)
		if err != nil {
			return "", &fatalError{err}
		}
		if result.CandidateID == "" || len(result.CandidateID) > 256 || len(result.CandidateHash) > 128 || result.PlanRevision != request.PlanRevision {
			return "", &fatalError{errors.New("Candidate validator returned inconsistent candidate metadata")}
		}
		state.LastCandidate = &model.CandidateRecord{Request: request, Result: journalValidation(result)}
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
	if len(final.Note) > 8192 || strings.TrimSpace(final.Note) == "" || len(final.CandidateID) > 256 {
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
		return result, nil
	}
	if state.Plan.Revision < 1 {
		return result, errors.New("Required rules must be reviewed with update_state before finishing")
	}
	reviewed := map[string]bool{}
	for _, decision := range state.Plan.RuleDecisions {
		reviewed[decision.RuleID] = true
		if decision.Decision == "blocked" {
			return result, errors.New("The plan contains blocked rules; resolve them or return needs_human")
		}
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
		if candidate == nil || !candidate.Result.Passed || candidate.Result.CandidateID != final.CandidateID || candidate.Result.PlanRevision != state.Plan.Revision || candidate.Request.PlanRevision != state.Plan.Revision || candidate.Request.BaseHash != in.BaseHash {
			return result, errors.New("modified requires the current plan's last validated, passed candidate ID")
		}
		if err := CheckCandidatePlan(state.Plan, candidate.Request); err != nil {
			return result, err
		}
		if err := validateExactEdits(candidate.Request.Edits, in.Content); err != nil {
			return result, err
		}
		result.Edits = append([]model.Edit{}, candidate.Request.Edits...)
		result.CandidateID = final.CandidateID
		for _, decision := range state.Plan.RuleDecisions {
			if decision.Decision == "modify" {
				result.RulesApplied = append(result.RulesApplied, decision.RuleID)
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
	if len(edits) == 0 || len(edits) > 100 {
		return errors.New("Candidate requires between 1 and 100 exact edits")
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
	if state.ElapsedMS < 0 || state.ToolCalls < 0 || state.ReadBytes < 0 || state.ValidationCount < 0 || state.ReviewCount < 0 || len(state.Reviews) > 128 || u.Turns < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedTokens < 0 || u.CachedTokens > u.InputTokens || u.CostUSD < 0 || len(state.ReadRuleIDs) > 256 {
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
