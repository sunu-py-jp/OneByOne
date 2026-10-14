package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"onebyone/internal/model"
)

type solutionTraceFixture struct {
	service                     *Service
	cfg                         model.Config
	workspace, execution, child string
	write                       func(int, model.AgentTraceEvent) error
	source                      solutionTraceSource
}

func makeSolutionTraceFixture(t *testing.T) solutionTraceFixture {
	t.Helper()
	s, cfg := solutionFixture(t)
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	if err := s.beginExecution(0); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	execution, child := s.activeExecution.Run.ID, uid()
	run := &model.SolutionRun{ID: child, ExecutionID: execution, Path: "Demo.sln", Status: "running", StartedAt: now()}
	s.state.SolutionRun = run
	s.state.SolutionBatch = &model.SolutionBatch{ID: execution, Status: "running", Paths: []string{"Demo.sln"}, Runs: []model.SolutionRun{*run}}
	workspace := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	write, err := s.newSolutionTraceWriter(cfg, execution, child)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.solutionTraceSource(workspace, execution, child)
	if err != nil {
		t.Fatal(err)
	}
	return solutionTraceFixture{s, cfg, workspace, execution, child, write, source}
}

func readAllSolutionTraceContent(t *testing.T, f solutionTraceFixture, sequence int, section string) string {
	t.Helper()
	var content strings.Builder
	offset := 0
	for {
		page, err := f.service.ReadSolutionTrace(f.workspace, f.execution, f.child, sequence, section, offset)
		if err != nil {
			t.Fatal(err)
		}
		if page.Offset != offset || !utf8.ValidString(page.Content) || len(page.Content) > solutionTraceContentSize || page.NextOffset != offset+len(page.Content) {
			t.Fatalf("invalid UTF-8 page: %+v", page)
		}
		content.WriteString(page.Content)
		if page.Complete {
			if page.NextOffset != page.TotalBytes || content.Len() != page.TotalBytes {
				t.Fatalf("final byte count: %+v", page)
			}
			return content.String()
		}
		if page.NextOffset <= offset {
			t.Fatal("paging made no progress")
		}
		offset = page.NextOffset
	}
}

func TestSolutionTraceUpdatesSeparateDetailsAndUTF8Pages(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	arguments, _ := json.Marshal(map[string]string{"file": "共有/画面.xaml", "content": strings.Repeat("日本語の引数", 9000)})
	output := strings.Repeat("戻り値の本文🙂", 9000)
	start := model.AgentTraceEvent{Key: "call", Phase: "review", Session: 2, Turn: 3, Kind: "tool", Tool: "read_file", CallID: "call-id", Status: "running", Summary: "候補ファイルを読む", Arguments: string(arguments), StartedAt: "2026-10-14T10:00:00Z"}
	if err := f.write(0, start); err != nil {
		t.Fatal(err)
	}
	// A recreated writer must update the persisted key, retaining arguments
	// that are intentionally absent from a completion notification.
	writer, err := f.service.newSolutionTraceWriter(f.cfg, f.execution, f.child)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer(0, model.AgentTraceEvent{Key: "call", Session: 2, Turn: 3, Status: "completed", Output: output, FinishedAt: "2026-10-14T10:00:01Z", Usage: model.Usage{InputTokens: 7}}); err != nil {
		t.Fatal(err)
	}
	if err := writer(1, model.AgentTraceEvent{Key: "call", Kind: "tool", Tool: "read_file", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || page.Total != 2 || !page.Complete || page.NextOffset != 2 || len(page.Entries) != 2 {
		t.Fatalf("list: %+v %v", page, err)
	}
	entry := page.Entries[0]
	if entry.Sequence != 1 || entry.Iteration != 0 || entry.Session != 2 || entry.Turn != 3 || entry.Tool != "read_file" || entry.Phase != "review" || entry.Status != "completed" || entry.StartedAt != start.StartedAt || entry.FinishedAt == "" || entry.ArgumentsBytes != len(arguments) || entry.OutputBytes != len(output) || entry.Usage.InputTokens != 7 {
		t.Fatalf("updated metadata: %+v", entry)
	}
	if page.Entries[1].Sequence != 2 || page.Entries[1].Iteration != 1 {
		t.Fatalf("iteration was not part of event identity: %+v", page.Entries)
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "日本語の引数") || strings.Contains(string(data), "戻り値の本文") {
		t.Fatal("list embedded detailed bodies")
	}
	if got := readAllSolutionTraceContent(t, f, 1, "arguments"); got != string(arguments) {
		t.Fatal("argument body changed during storage/paging")
	}
	if got := readAllSolutionTraceContent(t, f, 1, "output"); got != output {
		t.Fatal("output body changed during storage/paging")
	}
	if _, err := f.service.ReadSolutionTrace(f.workspace, f.execution, f.child, 1, "output", 1); err == nil {
		t.Fatal("accepted offset inside UTF-8 character")
	}
	if got := readAllSolutionTraceContent(t, f, 2, "output"); got != "" {
		t.Fatal("new iteration inherited earlier body")
	}
}

func TestSolutionTraceListPagesStayLightweightAndBoundMetadata(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	for i := 0; i < 103; i++ {
		event := model.AgentTraceEvent{Key: fmt.Sprint(i), Kind: "tool", Tool: strings.Repeat("読", 200), CallID: strings.Repeat("呼", 1000), Summary: strings.Repeat("概", 1000), Arguments: "private argument", Status: "completed"}
		if err := f.write(0, event); err != nil {
			t.Fatal(err)
		}
	}
	record, err := readSolutionTraceRecord(f.source, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Listing must not open argument/output files at all.
	body := solutionTraceBodyPath(f.source, 1, "arguments", record.ArgumentsID)
	if err := os.WriteFile(body, []byte("invalid body JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 50, 100} {
		page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, offset)
		expected := min(50, 103-offset)
		if err != nil || len(page.Entries) != expected || page.Total != 103 || page.NextOffset != offset+expected || page.Complete != (offset == 100) {
			t.Fatalf("list offset %d: %+v %v", offset, page, err)
		}
		for i, entry := range page.Entries {
			if entry.Sequence != offset+i+1 || len(entry.Summary) > 1024 || len(entry.Tool) > 128 || len(entry.CallID) > 1024 || !utf8.ValidString(entry.Summary+entry.Tool+entry.CallID) {
				t.Fatalf("oversize or invalid metadata: %+v", entry)
			}
		}
	}
	if _, err := f.service.ReadSolutionTrace(f.workspace, f.execution, f.child, 1, "arguments", 0); err == nil {
		t.Fatal("corrupt detail body was accepted")
	}
}

func TestSolutionTraceMasksEscapedAndNestedJSONWithoutTruncation(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	secret := "API-秘密\n\"quoted\\token"
	t.Setenv("OPENAI_API_KEY", secret)
	f.service.mu.Lock()
	credential := f.service.state.Config.Credential
	f.service.mu.Unlock()
	nested, _ := json.Marshal(map[string]string{"key": secret, "source": strings.Repeat("全文", 10000)})
	arguments, _ := json.Marshal(map[string]string{"credential": credential, "nested": string(nested)})
	encodedSecret, _ := json.Marshal(secret)
	output := fmt.Sprintf("{\n  \"n\":9007199254740993,\"dup\":%s,\"dup\":%s,\"content\":%s\n}", encodedSecret, encodedSecret, strconvQuoteSolutionTrace(string(nested)))
	if err := f.write(0, model.AgentTraceEvent{Key: "masked", Tool: secret, CallID: credential, Summary: credential + secret, Arguments: string(arguments), Output: output}); err != nil {
		t.Fatal(err)
	}
	gotArgs := readAllSolutionTraceContent(t, f, 1, "arguments")
	gotOutput := readAllSolutionTraceContent(t, f, 1, "output")
	var decoded map[string]string
	if err := json.Unmarshal([]byte(gotArgs), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["credential"] != "[redacted]" {
		t.Fatalf("credential escaped masking failed: %q", decoded["credential"])
	}
	var decodedNested map[string]string
	if err := json.Unmarshal([]byte(decoded["nested"]), &decodedNested); err != nil {
		t.Fatal(err)
	}
	if decodedNested["key"] != "[redacted]" || decodedNested["source"] != strings.Repeat("全文", 10000) {
		t.Fatal("nested JSON was unmasked or truncated")
	}
	if !json.Valid([]byte(gotOutput)) || strings.Count(gotOutput, `"dup"`) != 2 || !strings.Contains(gotOutput, "9007199254740993") || !strings.Contains(gotOutput, "\n  ") {
		t.Fatal("redaction changed JSON structure, precision, duplicate keys or formatting")
	}
	page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Entries[0].Tool != "[redacted]" || page.Entries[0].CallID != "[redacted]" || page.Entries[0].Summary != "[redacted][redacted]" {
		t.Fatalf("metadata was not masked: %+v", page.Entries[0])
	}
	if strings.Contains(gotArgs+gotOutput, string(encodedSecret)) || strings.Contains(gotArgs+gotOutput, credential) {
		t.Fatal("stored trace exposed a secret")
	}
}

func strconvQuoteSolutionTrace(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestSolutionTracePreservesUnmaskedJSONLexicalContent(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	body := "  {\n" + `"path":"a\/b","ascii":"\u0041","jp":"\u65e5本","html":"<>&","nested":"{\"quoted\":\"\\u0041\\/x\"}","number":1.2300e+09` + "\n}\t"
	if !json.Valid([]byte(body)) {
		t.Fatal("fixture JSON is invalid")
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "lexical", Arguments: body, Output: body}); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"arguments", "output"} {
		if got := readAllSolutionTraceContent(t, f, 1, section); got != body {
			t.Fatalf("unmasked %s was reserialized:\ngot %q\nwant %q", section, got, body)
		}
	}
}

func TestSolutionTraceRejectsBadIdentityAndLateWorkspaceResponses(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || page.Total != 0 || !page.Complete || page.Entries == nil {
		t.Fatalf("legacy trace-less execution: %+v %v", page, err)
	}
	for _, ids := range [][3]string{{"other", f.execution, f.child}, {f.workspace, "../bad", f.child}, {f.workspace, f.execution, "../bad"}, {f.workspace, uid(), f.child}, {f.workspace, f.execution, uid()}} {
		if _, err := f.service.ListSolutionTrace(ids[0], ids[1], ids[2], 0); err == nil {
			t.Fatalf("invalid identity accepted: %v", ids)
		}
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "valid", Arguments: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		sequence int
		section  string
		offset   int
	}{{0, "arguments", 0}, {2, "arguments", 0}, {1, "../index", 0}, {1, "arguments", -1}, {1, "arguments", 6}} {
		if _, err := f.service.ReadSolutionTrace(f.workspace, f.execution, f.child, request.sequence, request.section, request.offset); err == nil {
			t.Fatalf("invalid content request accepted: %+v", request)
		}
	}
	if _, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, -1); err == nil {
		t.Fatal("negative list offset accepted")
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "bad-utf8", Output: string([]byte{0xff})}); err == nil {
		t.Fatal("invalid UTF-8 silently replaced")
	}
	// Simulate a response arriving after selection changed. Neither a bound
	// reader nor the old execution's writer may use the new workspace state.
	f.service.mu.Lock()
	oldWorkspace := f.service.state.ActiveWorkspaceID
	f.service.state.ActiveWorkspaceID = "different-workspace"
	f.service.mu.Unlock()
	if err := f.service.checkSolutionTraceSource(f.source); err == nil {
		t.Fatal("late read accepted changed workspace")
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "late", Output: "wrong workspace"}); err == nil {
		t.Fatal("late writer accepted changed workspace")
	}
	f.service.mu.Lock()
	f.service.state.ActiveWorkspaceID = oldWorkspace
	f.service.mu.Unlock()
}

func TestSolutionTraceRejectsSymlinkDirectoriesMetadataBodiesAndArchives(t *testing.T) {
	for _, kind := range []string{"directory", "index", "entry", "body", "archive"} {
		t.Run(kind, func(t *testing.T) {
			f := makeSolutionTraceFixture(t)
			if err := f.write(0, model.AgentTraceEvent{Key: "call", Arguments: "safe"}); err != nil {
				t.Fatal(err)
			}
			record, err := readSolutionTraceRecord(f.source, 1)
			if err != nil {
				t.Fatal(err)
			}
			path := f.source.directory
			switch kind {
			case "index":
				path = filepath.Join(path, "index.json")
			case "entry":
				path = solutionTraceRecordPath(f.source, 1)
			case "body":
				path = solutionTraceBodyPath(f.source, 1, "arguments", record.ArgumentsID)
			case "archive":
				directory, err := executionDirectory(f.cfg.QueuePath, f.execution)
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(directory, "snapshot.json")
				f.service.mu.Lock()
				f.service.state.SolutionBatch, f.service.state.SolutionRun = nil, nil
				f.service.mu.Unlock()
			}
			backup := path + ".real"
			if err := os.Rename(path, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(backup, path); err != nil {
				t.Skip(err)
			}
			if kind == "body" {
				if _, err := f.service.ReadSolutionTrace(f.workspace, f.execution, f.child, 1, "arguments", 0); err == nil {
					t.Fatal("followed body symlink")
				}
			} else if _, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0); err == nil {
				t.Fatal("followed trace/archive symlink")
			}
			// Restore fixtures before their execution cleanup writes snapshots.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSolutionTraceWriteErrorsAndInterruptedIndexRecovery(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	details := filepath.Join(f.source.directory, "details")
	writeTest(t, details, []byte("not a directory"))
	if err := f.write(0, model.AgentTraceEvent{Key: "call", Arguments: "body"}); err == nil {
		t.Fatal("body write error was ignored")
	}
	page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || page.Total != 0 {
		t.Fatalf("failed write published incomplete row: %+v %v", page, err)
	}
	if err := os.Remove(details); err != nil {
		t.Fatal(err)
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "call", Arguments: "body"}); err != nil {
		t.Fatal(err)
	}
	// Row/body publication can precede count publication when interrupted.
	if err := writeOutputJSON(f.cfg, filepath.Join(f.source.directory, "index.json"), solutionTraceIndex{Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.write(0, model.AgentTraceEvent{Key: "second", Output: "next"}); err != nil {
		t.Fatal(err)
	}
	page, err = f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || page.Total != 2 {
		t.Fatalf("interrupted count did not recover: %+v %v", page, err)
	}
	if got := readAllSolutionTraceContent(t, f, 1, "arguments"); got != "body" {
		t.Fatal("interrupted sequence was reused")
	}
	if got := readAllSolutionTraceContent(t, f, 2, "output"); got != "next" {
		t.Fatal("new event was lost")
	}
}

func TestSolutionTraceConcurrentWritersAndArchivedRead(t *testing.T) {
	f := makeSolutionTraceFixture(t)
	writer, err := f.service.newSolutionTraceWriter(f.cfg, f.execution, f.child)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			write := f.write
			if i%2 == 0 {
				write = writer
			}
			failures <- write(0, model.AgentTraceEvent{Key: fmt.Sprint(i), Kind: "tool", Output: fmt.Sprint(i)})
		}(i)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || page.Total != 12 {
		t.Fatalf("concurrent event allocation: %+v %v", page, err)
	}
	f.service.mu.Lock()
	f.service.state.SolutionRun.Status = "completed"
	f.service.syncSolutionBatchLocked()
	finishSolutionBatch(f.service.state.SolutionBatch, false)
	f.service.mu.Unlock()
	if err := f.service.persist(); err != nil {
		t.Fatal(err)
	}
	if err := f.service.finishExecution(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	configPath := f.service.configPath
	f.service.Close()
	f.service = workspaceNewService(t, configPath)
	// Force authorization via the frozen execution, as for an older run while
	// another solution batch is selected in the current workspace.
	f.service.mu.Lock()
	f.service.state.SolutionBatch, f.service.state.SolutionRun = nil, nil
	f.service.mu.Unlock()
	got, err := f.service.ListSolutionTrace(f.workspace, f.execution, f.child, 0)
	if err != nil || got.Total != 12 {
		t.Fatalf("archived trace after reopening: %+v %v", got, err)
	}
	if len(readAllSolutionTraceContent(t, f, 1, "output")) == 0 {
		t.Fatal("archived detail missing")
	}
}
