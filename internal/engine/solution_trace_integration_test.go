package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestSolutionTraceBatchHistorySurvivesLaterRunAndReopen(t *testing.T) {
	s, _ := solutionBatchFixture(t, []string{"Demo.sln", "Second.sln"})
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{Success: true}, nil
	}
	marker := "本文だけに保存される履歴\n"
	outputs := map[string]string{}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		if in.Trace == nil {
			return agent.SolutionProposal{}, fmt.Errorf("trace callback was not connected")
		}
		payload, _ := json.Marshal(map[string]any{"content": strings.Repeat(marker, 1600), "solution": in.Config.Solution.Path, "execution": s.Snapshot().SolutionRun.ID, "complete": true})
		outputs[in.Config.Solution.Path] = string(payload)
		event := model.AgentTraceEvent{Key: "same-key-in-each-child", Phase: "repair", Session: 1, Turn: 1, Kind: "tool", Tool: "read_file", CallID: "call-1", Status: "running", Summary: "Demo/A.vb を読む", Arguments: `{"file":"Demo/A.vb","version":"original","offset":0}`, StartedAt: now()}
		if err := in.Trace(event); err != nil {
			return agent.SolutionProposal{}, err
		}
		event.Status, event.Output, event.FinishedAt = "completed", string(payload), now()
		if err := in.Trace(event); err != nil {
			return agent.SolutionProposal{}, err
		}
		return agent.SolutionProposal{Status: "skipped", Note: "確認済み"}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	first := s.Snapshot()
	if first.LastError != "" || first.SolutionBatch == nil || first.SolutionBatch.Status != "completed" {
		t.Fatalf("trace run failed: %s", first.LastError)
	}
	pages := map[string]model.AgentTracePage{}
	firstOutputs := map[string]string{}
	for _, child := range first.SolutionBatch.Runs {
		firstOutputs[child.Path] = outputs[child.Path]
		page, err := s.ListSolutionTrace(first.ActiveWorkspaceID, first.SolutionBatch.ID, child.ID, 0)
		if err != nil || len(page.Entries) != 1 || page.Total != 1 || !page.Complete {
			t.Fatalf("child trace was not stored independently: %+v %v", page, err)
		}
		entry := page.Entries[0]
		if entry.Status != "completed" || entry.Sequence != 1 || entry.Iteration != 0 || entry.Tool != "read_file" || entry.OutputBytes != len(outputs[child.Path]) || entry.ArgumentsBytes == 0 {
			t.Fatalf("trace lifecycle was not paired: %+v", entry)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), strings.TrimSpace(marker)) || strings.Contains(string(encoded), `"content"`) {
			t.Fatal("overview returned full tool output")
		}
		got := readSolutionTracePayload(t, s, first.ActiveWorkspaceID, first.SolutionBatch.ID, child.ID, entry.Sequence, "output")
		if got != outputs[child.Path] {
			t.Fatal("paged trace output did not retain the exact tool result")
		}
		pages[child.ID] = page
	}
	// A later execution uses new trace directories, leaving the earlier one
	// inspectable even after the mutable batch state points at another run.
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if st := s.Snapshot(); st.LastError != "" || st.SolutionBatch.ID == first.SolutionBatch.ID {
		t.Fatalf("second execution failed: %s", st.LastError)
	}
	configPath := s.configPath
	s.Close()
	reopened := workspaceNewService(t, configPath)
	for _, child := range first.SolutionBatch.Runs {
		page, err := reopened.ListSolutionTrace(first.ActiveWorkspaceID, first.SolutionBatch.ID, child.ID, 0)
		if err != nil || !reflect.DeepEqual(page, pages[child.ID]) {
			t.Fatalf("historical trace changed after reopening: %+v %v", page, err)
		}
		got := readSolutionTracePayload(t, reopened, first.ActiveWorkspaceID, first.SolutionBatch.ID, child.ID, 1, "output")
		if got != firstOutputs[child.Path] {
			t.Fatal("historical output lost or replaced by another child")
		}
	}
	archive, err := reopened.GetExecutionRun(first.SolutionBatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(archive.State)
	if strings.Contains(string(encoded), strings.TrimSpace(marker)) {
		t.Fatal("trace bodies leaked into the polled/archived application state")
	}
}

func readSolutionTracePayload(t *testing.T, s *Service, workspace, execution, child string, sequence int, section string) string {
	t.Helper()
	var output strings.Builder
	for offset := 0; ; {
		page, err := s.ReadSolutionTrace(workspace, execution, child, sequence, section, offset)
		if err != nil || page.Offset != offset || !utf8.ValidString(page.Content) {
			t.Fatalf("bad payload page at %d: %+v %v", offset, page, err)
		}
		output.WriteString(page.Content)
		if page.Complete {
			if output.Len() != page.TotalBytes {
				t.Fatalf("incomplete body: %d != %d", output.Len(), page.TotalBytes)
			}
			return output.String()
		}
		if page.NextOffset <= offset {
			t.Fatalf("paging did not advance: %+v", page)
		}
		offset = page.NextOffset
	}
}
