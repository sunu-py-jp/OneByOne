package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"onebyone/internal/model"
)

// One recorder is shared by all repair/review conversations in a RunSolution.
// It stops permanently after a persistence failure: subsequent actions cannot
// silently continue with an incomplete activity record.
type solutionTrace struct {
	callback func(model.AgentTraceEvent) error
	nextKey  int
	sessions int
	failed   error
}

type solutionSessionTrace struct {
	owner   *solutionTrace
	phase   string
	session int
	turn    int
	entry   model.AgentTraceEvent
}

func (t *solutionTrace) emit(event model.AgentTraceEvent) error {
	if t.failed != nil {
		return t.failed
	}
	if t.callback != nil {
		if err := t.callback(event); err != nil {
			t.failed = &fatalError{fmt.Errorf("AI 活動履歴の保存に失敗したため停止しました: %w", err)}
			return t.failed
		}
	}
	return nil
}

func (t *solutionTrace) beginSession(reviewer bool) (*solutionSessionTrace, error) {
	t.sessions++
	s := &solutionSessionTrace{owner: t, phase: "repair", session: t.sessions}
	summary := "修正担当の会話"
	if reviewer {
		s.phase, summary = "review", "独立レビュー担当の会話"
	}
	s.entry = s.event("session", summary)
	return s, t.emit(s.entry)
}

func (s *solutionSessionTrace) event(kind, summary string) model.AgentTraceEvent {
	s.owner.nextKey++
	return model.AgentTraceEvent{
		Key: fmt.Sprintf("solution-%d", s.owner.nextKey), Phase: s.phase,
		Session: s.session, Turn: s.turn, Kind: kind, Status: "running",
		Summary: summary, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (s *solutionSessionTrace) complete(event model.AgentTraceEvent, output string, usage model.Usage, err error) error {
	event.Status, event.Output, event.Usage = "completed", output, usage
	event.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		event.Status = "error"
		event.Summary += "（失敗）: " + solutionTraceLabel(err.Error(), 256)
		if output == "" {
			event.Output = err.Error()
		}
	} else {
		event.Summary += "（完了）"
	}
	return s.owner.emit(event)
}

func (s *solutionSessionTrace) finishSession(usage model.Usage, err error) error {
	return s.complete(s.entry, "", usage, err)
}

func (s *solutionSessionTrace) context(reason string) error {
	return s.complete(s.event("context", "保存済みの作業を保持して会話を再構成"),
		string(raw(map[string]string{"reason": reason})), model.Usage{}, nil)
}

func (s *solutionSessionTrace) execute(session *solutionSession, call functionCall, invalidID bool) (value string, toolErr, traceErr error) {
	event := s.event("tool", solutionToolSummary(call))
	event.Tool, event.CallID, event.Arguments = call.Name, call.CallID, call.Arguments
	if err := s.owner.emit(event); err != nil {
		return "", nil, err
	}
	if invalidID {
		toolErr = errors.New("Invalid or duplicate solution tool call ID")
	} else {
		value, toolErr = session.execute(call)
	}
	if toolErr != nil {
		value = "Tool error: " + bounded(toolErr.Error(), 2048)
	}
	return value, toolErr, s.complete(event, value, model.Usage{}, toolErr)
}

func (s *solutionSessionTrace) decision(text string, decisionErr error) error {
	// Only the public decision schema is retained. Malformed provider prose,
	// reasoning blocks and raw provider responses are never activity payloads.
	var decision struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	output := ""
	if err := strictRequiredJSON(text, &decision, "status", "note"); err == nil {
		output = string(raw(decision))
	}
	return s.complete(s.event("decision", "担当 AI の最終判断"), output, model.Usage{}, decisionErr)
}

func solutionToolSummary(call functionCall) string {
	summary := "ツールを呼び出し: " + solutionTraceLabel(call.Name, 80)
	switch call.Name {
	case "read_manifest":
		summary = "対象ファイルと適用ルールの一覧を確認"
	case "read_build_output":
		summary = "ビルド結果を確認"
	case "read_rules":
		summary = "ルールの内容を確認"
	case "read_file":
		summary = "ファイルの内容を確認"
	case "read_state":
		summary = "保存済みの修正・評価を確認"
	case "stage_edits":
		summary = "修正候補を登録"
	case "record_assessments":
		summary = "ルールのレビュー結果を登録"
	}
	// The overview includes only known target identifiers, never source text,
	// proposed replacements, assessment reasons or the whole argument payload.
	var args struct {
		File    string `json:"file"`
		Version string `json:"version"`
		ID      string `json:"id"`
		Offset  int    `json:"offset"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return summary
	}
	if (call.Name == "read_file" || call.Name == "stage_edits") && args.File != "" {
		summary += ": " + solutionTraceLabel(args.File, 200)
	}
	if call.Name == "read_file" {
		switch args.Version {
		case "original":
			summary += "（変更前）"
		case "candidate":
			summary += "（変更後）"
		}
	}
	if call.Name == "read_rules" && args.ID != "" {
		summary += ": " + solutionTraceLabel(args.ID, 80)
	}
	if strings.HasPrefix(call.Name, "read_") && args.Offset > 0 {
		summary += fmt.Sprintf("（%d バイトから）", args.Offset)
	}
	return summary
}

func solutionTraceLabel(value string, limit int) string {
	return bounded(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value), limit)
}
