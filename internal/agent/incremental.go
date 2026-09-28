package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

// MergeRepairPlan persists a small batch. Omitted decisions/items are retained;
// removal is explicit and uses the same reason/stable-ID checks as full plans.
func MergeRepairPlan(current model.RepairPlan, update model.PlanUpdate, catalog []model.Rule, readIDs []string) (model.RepairPlan, error) {
	merged := model.PlanUpdate{ExpectedRevision: update.ExpectedRevision, RuleDecisions: append([]model.PlanDecision{}, current.RuleDecisions...), Items: append([]model.PlanItem{}, current.Items...)}
	seen := map[string]bool{}
	for _, decision := range update.RuleDecisions {
		if seen[decision.RuleID] {
			return model.RepairPlan{}, fmt.Errorf("duplicate decision for rule %q", decision.RuleID)
		}
		seen[decision.RuleID] = true
		found := false
		for i, d := range merged.RuleDecisions {
			if d.RuleID == decision.RuleID {
				merged.RuleDecisions[i] = decision
				found = true
				break
			}
		}
		if !found {
			merged.RuleDecisions = append(merged.RuleDecisions, decision)
		}
	}
	seen = map[string]bool{}
	for _, item := range update.Items {
		if seen[item.ID] {
			return model.RepairPlan{}, fmt.Errorf("duplicate plan item %q", item.ID)
		}
		seen[item.ID] = true
		found := false
		for i, p := range merged.Items {
			if p.ID == item.ID {
				merged.Items[i] = item
				found = true
				break
			}
		}
		if !found {
			merged.Items = append(merged.Items, item)
		}
	}
	removed := map[string]bool{}
	for _, id := range update.RemoveItemIDs {
		if seen[id] || removed[id] {
			return model.RepairPlan{}, errors.New("removeItemIds cannot repeat or also upsert an item")
		}
		removed[id] = true
		found := false
		for i, p := range merged.Items {
			if p.ID == id {
				merged.Items = append(merged.Items[:i], merged.Items[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return model.RepairPlan{}, fmt.Errorf("cannot remove unknown item %q", id)
		}
	}
	// Planning can span calls. Completion checks still require all real catalog
	// decisions; only this incremental registration boundary permits omissions.
	registrationCatalog := append([]model.Rule{}, catalog...)
	for i := range registrationCatalog {
		registrationCatalog[i].Always = false
	}
	allRead := append([]string{}, readIDs...)
	for _, rule := range catalog {
		if rule.Always {
			allRead = append(allRead, rule.ID)
		}
	}
	return UpdateRepairPlan(current, merged, registrationCatalog, nil, allRead)
}

func stagedRequest(state model.RepairState, baseHash string) model.CandidateRequest {
	request := model.CandidateRequest{PlanRevision: state.Plan.Revision, BaseHash: baseHash, Edits: []model.Edit{}, AddressedItemIDs: []string{}}
	seen := map[string]bool{}
	for _, entry := range state.StagedEdits {
		request.Edits = append(request.Edits, entry.Edit)
		for _, id := range entry.ItemIDs {
			if !seen[id] {
				seen[id] = true
				request.AddressedItemIDs = append(request.AddressedItemIDs, id)
			}
		}
	}
	return request
}

func stageEdits(state *model.RepairState, update model.StageEditsRequest, original, baseHash string) error {
	if update.PlanRevision != state.Plan.Revision || state.Plan.Revision < 1 {
		return fmt.Errorf("stage_edits requires current plan revision %d", state.Plan.Revision)
	}
	if update.BaseHash != baseHash {
		return errors.New("stage_edits baseHash differs from original target")
	}
	staged := append([]model.StagedEdit{}, state.StagedEdits...)
	touched := map[string]bool{}
	for _, id := range update.RemoveEditIDs {
		if touched[id] {
			return errors.New("duplicate removed edit ID")
		}
		touched[id] = true
		found := false
		for i, entry := range staged {
			if entry.ID == id {
				staged = append(staged[:i], staged[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("cannot remove unknown staged edit %q", id)
		}
	}
	items := map[string]model.PlanItem{}
	for _, item := range state.Plan.Items {
		items[item.ID] = item
	}
	for _, entry := range update.Edits {
		if !validRuleID(entry.ID) || touched[entry.ID] {
			return errors.New("staged edit IDs must be valid and unique within each update")
		}
		touched[entry.ID] = true
		linked := map[string]bool{}
		if len(entry.ItemIDs) == 0 {
			return errors.New("staged edits require itemIds")
		}
		for _, id := range entry.ItemIDs {
			item, ok := items[id]
			if !ok || linked[id] || item.Status == "blocked" {
				return fmt.Errorf("staged edit %q references unknown, repeated or blocked item %q", entry.ID, id)
			}
			linked[id] = true
		}
		if err := checkEditAttributionShape(0, entry.Edit, linked); err != nil {
			return err
		}
		found := false
		for i, prior := range staged {
			if prior.ID == entry.ID {
				staged[i] = entry
				found = true
				break
			}
		}
		if !found {
			staged = append(staged, entry)
		}
	}
	next := *state
	next.StagedEdits = staged
	request := stagedRequest(next, baseHash)
	if len(staged) > 0 {
		if err := validateExactEdits(request.Edits, original); err != nil {
			return err
		}
		if err := CheckCandidateHolds(state.Plan, request, original); err != nil {
			return err
		}
	}
	state.StagedEdits = staged
	state.LastCandidate = nil // Even a valid older candidate must never survive a new edit.
	return nil
}

func materializeEdits(original string, edits []model.Edit) (string, error) {
	if len(edits) == 0 {
		return original, nil
	}
	if err := validateExactEdits(edits, original); err != nil {
		return "", err
	}
	type replacement struct {
		start int
		edit  model.Edit
	}
	ordered := make([]replacement, 0, len(edits))
	for _, edit := range edits {
		ordered = append(ordered, replacement{strings.Index(original, edit.OldText), edit})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start > ordered[j].start })
	for _, entry := range ordered {
		original = original[:entry.start] + entry.edit.NewText + original[entry.start+len(entry.edit.OldText):]
	}
	return original, nil
}

const contextPageBytes = 24 << 10
const historyRolloverBytes = 256 << 10

// pageText bounds one transfer, never the document. Offsets are byte offsets in
// UTF-8 normalized text; the returned nextOffset is always a rune boundary.
func pageText(text string, offset int) (map[string]any, error) {
	if offset < 0 || offset > len(text) || offset < len(text) && !utf8.RuneStart(text[offset]) {
		return nil, errors.New("offset must be a valid UTF-8 byte boundary within the document")
	}
	end := min(len(text), offset+contextPageBytes)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	// JSON escaping can expand control characters sixfold. Bound the transfer
	// representation, while preserving the exact original bytes for paging.
	for len(raw(text[offset:end])) > contextPageBytes && end > offset {
		end = offset + (end-offset)/2
		for end < len(text) && end > offset && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	next := 0
	if end < len(text) {
		next = end
	}
	return map[string]any{"content": text[offset:end], "offset": offset, "nextOffset": next, "complete": end == len(text), "totalBytes": len(text)}, nil
}

func sourceRange(text string, start, end int) (string, error) {
	if start < 1 || end < start {
		return "", errors.New("source range must be 1-based and inclusive")
	}
	lines := strings.Split(text, "\n")
	if start > len(lines) {
		return "", fmt.Errorf("startLine exceeds total lines %d", len(lines))
	}
	end = min(end, len(lines))
	return strings.Join(lines[start-1:end], "\n"), nil
}

func readSavedState(in Input, state model.RepairState, arguments string) (string, error) {
	var args struct {
		Section string `json:"section"`
		Offset  int    `json:"offset"`
	}
	if err := strictRequiredJSON(arguments, &args, "section", "offset"); err != nil {
		return "", err
	}
	var value any
	switch args.Section {
	case "plan":
		value = state.Plan
	case "reads":
		value = map[string]any{"readRuleIds": state.ReadRuleIDs, "ruleReadOffsets": state.RuleReadOffsets}
	case "edits":
		value = state.StagedEdits
	case "validation":
		if state.LastCandidate != nil {
			value = state.LastCandidate.Result
		}
	case "review":
		if len(state.Reviews) > 0 {
			value = state.Reviews[len(state.Reviews)-1]
		}
	case "catalog":
		rows := make([]model.Rule, len(in.Rules))
		copy(rows, in.Rules)
		for i := range rows {
			rows[i].Body = ""
		}
		value = rows
	default:
		return "", errors.New("section must be plan, edits, validation, review, reads or catalog")
	}
	page, err := pageText(string(raw(value)), args.Offset)
	return string(raw(page)), err
}

func readTarget(in Input, state model.RepairState, arguments string, candidate bool) (string, error) {
	var args struct {
		StartLine int `json:"startLine"`
		EndLine   int `json:"endLine"`
		Offset    int `json:"offset"`
	}
	if err := strictRequiredJSON(arguments, &args, "startLine", "endLine", "offset"); err != nil {
		return "", err
	}
	content := in.Content
	if candidate {
		var err error
		content, err = materializeEdits(content, stagedRequest(state, in.BaseHash).Edits)
		if err != nil {
			return "", err
		}
	}
	text, err := sourceRange(content, args.StartLine, args.EndLine)
	if err != nil {
		return "", err
	}
	page, err := pageText(text, args.Offset)
	if err == nil {
		page["startLine"] = args.StartLine
		page["totalLines"] = strings.Count(content, "\n") + 1
	}
	return string(raw(page)), err
}

func historyBytes(history []json.RawMessage) int {
	size := 0
	for _, message := range history {
		size += len(message)
	}
	return size
}

func repairContext(in Input, state model.RepairState, cache map[string]string, reason string) []json.RawMessage {
	data := map[string]any{"file": in.File, "baseHash": in.BaseHash, "totalLines": strings.Count(in.Content, "\n") + 1, "originalBytes": len(in.Content), "ruleCount": len(in.Rules), "candidateRuleCount": len(in.CandidateRules), "planRevision": state.Plan.Revision, "stagedEditCount": len(state.StagedEdits), "planItemCount": len(state.Plan.Items), "reviewedRuleCount": len(state.Plan.RuleDecisions), "reason": reason, "instruction": "Progress is saved locally. Use read_state sections plan, edits, validation, review, reads or catalog and read_target/read_candidate to inspect exact saved content, paging via nextOffset. Continue pending work; never rebuild or forget completed edits. All edits refer to the immutable original."}
	// Small files/plans stay directly visible. Larger artifacts remain complete in
	// durable state and are available through paged tools, never summarized away.
	if len(in.Content) <= contextPageBytes {
		data["content"] = in.Content
	} else {
		page, _ := pageText(in.Content, 0)
		data["contentPreview"] = page
	}
	if b := raw(state.Plan); len(b) <= contextPageBytes {
		data["plan"] = state.Plan
	}
	if state.LastCandidate != nil {
		data["lastValidation"] = toolValidation(state.LastCandidate.Result)
	}
	if len(state.Reviews) > 0 {
		data["latestReviewSummary"] = bounded(state.Reviews[len(state.Reviews)-1].Summary, 4096)
	}
	if in.PreviousFailure != "" {
		data["previousFailure"] = bounded(in.PreviousFailure, 4096)
	}
	if len(raw(state.RuleReadOffsets)) <= contextPageBytes {
		data["ruleReadOffsets"] = state.RuleReadOffsets
	}
	if len(raw(cache)) <= contextPageBytes {
		data["previouslyReadRules"] = cache
	}
	return []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(data))})}
}

func readRulePage(in Input, state *model.RepairState, cache map[string]string, id string, offset int) (string, error) {
	allowed := false
	for _, rule := range in.Rules {
		if rule.ID == id {
			allowed = true
			break
		}
	}
	if !allowed || !validRuleID(id) {
		return "", errors.New("rule ID is outside the supplied file-specific catalog")
	}
	value, cached := cache[id]
	if !cached {
		var err error
		value, err = in.ReadRule(id)
		if err != nil {
			return "", fmt.Errorf("rule %q could not be read", id)
		}
	}
	if !utf8.ValidString(value) {
		return "", errors.New("rule content is not UTF-8")
	}
	page, err := pageText(value, offset)
	if err != nil {
		return "", err
	}
	if state.RuleReadOffsets == nil {
		state.RuleReadOffsets = map[string]int{}
	}
	// Reading disjoint tails never counts as reading the whole rule. A complete
	// contiguous prefix is persisted so a rollover can resume the next page.
	prefix := state.RuleReadOffsets[id]
	end := offset + len(page["content"].(string))
	if offset <= prefix && end > prefix {
		state.RuleReadOffsets[id] = end
	}
	if state.RuleReadOffsets[id] >= len(value) {
		cache[id] = value
	}
	page["id"] = id
	page["readThroughOffset"] = state.RuleReadOffsets[id]
	return string(raw(page)), nil
}

func readRulesPaged(in Input, state *model.RepairState, cache map[string]string, call functionCall) (string, error) {
	if call.Name == "read_rule_page" {
		var args struct {
			ID     string `json:"id"`
			Offset int    `json:"offset"`
		}
		if err := strictRequiredJSON(call.Arguments, &args, "id", "offset"); err != nil {
			return "", err
		}
		return readRulePage(in, state, cache, args.ID, args.Offset)
	}
	ids := []string{}
	if call.Name == "read_rule" {
		var args struct {
			ID string `json:"id"`
		}
		if err := strictRequiredJSON(call.Arguments, &args, "id"); err != nil {
			return "", err
		}
		ids = append(ids, args.ID)
	} else {
		var args struct {
			IDs []string `json:"ids"`
		}
		if err := strictRequiredJSON(call.Arguments, &args, "ids"); err != nil {
			return "", err
		}
		ids = args.IDs
	}
	if len(ids) == 0 {
		return "", errors.New("read_rules requires rule IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validRuleID(id) || seen[id] {
			return "", errors.New("rule IDs must be valid and distinct")
		}
		seen[id] = true
		allowed := false
		for _, rule := range in.Rules {
			if rule.ID == id {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", errors.New("rule ID is outside supplied catalog")
		}
	}
	stagedState := *state
	stagedState.RuleReadOffsets = map[string]int{}
	for id, offset := range state.RuleReadOffsets {
		stagedState.RuleReadOffsets[id] = offset
	}
	stagedCache := map[string]string{}
	for id, value := range cache {
		stagedCache[id] = value
	}
	rows := map[string]any{}
	unread := []string{}
	bytes := 0
	for _, id := range ids {
		if bytes >= contextPageBytes {
			unread = append(unread, id)
			continue
		}
		output, err := readRulePage(in, &stagedState, stagedCache, id, 0)
		if err != nil {
			return "", err
		}
		var page map[string]any
		_ = json.Unmarshal([]byte(output), &page)
		if page["complete"] == true {
			rows[id] = page["content"]
		} else {
			rows[id] = page
		}
		bytes += len(output)
	}
	state.RuleReadOffsets = stagedState.RuleReadOffsets
	for id, value := range stagedCache {
		cache[id] = value
	}
	if len(unread) > 0 {
		return string(raw(map[string]any{"rules": rows, "remainingRuleIds": unread, "instruction": "Call read_rules with remainingRuleIds; follow read_rule_page nextOffset for incomplete rule bodies."})), nil
	}
	if len(ids) == 1 && call.Name == "read_rule" {
		if value, ok := rows[ids[0]].(string); ok {
			return value, nil
		}
	}
	return string(raw(rows)), nil
}

func runPrompt(in Input) string {
	if len(in.SystemPrompt) <= 64<<10 {
		return instructions + "\n\n" + in.SystemPrompt
	}
	// Never silently shorten a rule body. Oversized rule prompts are fetched from
	// the authoritative catalog through the same paged reading tools.
	return incrementalPagedPrompt()
}

func stagedEditsOnly(edits []model.StagedEdit) []model.Edit {
	out := make([]model.Edit, 0, len(edits))
	for _, edit := range edits {
		out = append(out, edit.Edit)
	}
	return out
}
func requireCompletePlan(plan model.RepairPlan, required []string) error {
	seen := map[string]bool{}
	for _, d := range plan.RuleDecisions {
		seen[d.RuleID] = true
	}
	for _, id := range required {
		if !seen[id] {
			return fmt.Errorf("rule %s has not been reviewed; complete the plan before validation", id)
		}
	}
	return nil
}

func incrementalPagedPrompt() string {
	return instructions + "\nRule bodies are available through read_rules/read_rule_page. Read every supplied rule, including common rules, before recording its decision. Use read_state section catalog for rule IDs and metadata. No rule body is truncated."
}
func minimalRepairContext(in Input, state model.RepairState, reason string) []json.RawMessage {
	data := map[string]any{"file": in.File, "baseHash": in.BaseHash, "totalLines": strings.Count(in.Content, "\n") + 1, "planRevision": state.Plan.Revision, "stagedEditCount": len(state.StagedEdits), "ruleCount": len(in.Rules), "reason": reason, "instruction": "All progress is durably saved. Use paged read_state/read_target/read_candidate/read_rule_page to inspect it, then continue small incremental updates. No prior work was discarded."}
	return []json.RawMessage{raw(map[string]any{"role": "user", "content": string(raw(data))})}

}
