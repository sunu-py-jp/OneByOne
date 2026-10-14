package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

const solutionTracePageSize = 50
const solutionTraceContentSize = 16 << 10

type solutionTraceIndex struct {
	Version int `json:"version"`
	Count   int `json:"count"`
}

type solutionTraceRecord struct {
	Entry       model.AgentTraceEntry `json:"entry"`
	KeyHash     string                `json:"keyHash"`
	ArgumentsID string                `json:"argumentsId,omitempty"`
	OutputID    string                `json:"outputId,omitempty"`
}

type solutionTraceKey struct {
	Sequence int `json:"sequence"`
}

type solutionTraceSource struct {
	workspaceID, executionID, solutionRunID string
	cfg                                     model.Config
	directory                               string
}

func validSolutionTraceID(id string) bool {
	return len(id) == 24 && strings.Trim(id, "0123456789abcdef") == ""
}

func validSolutionTraceHash(hash string) bool {
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

func traceRunExists(st model.State, executionID, solutionRunID string) bool {
	if batch := st.SolutionBatch; batch != nil && batch.ID == executionID {
		for _, run := range batch.Runs {
			if run.ID == solutionRunID && solutionExecutionID(&run) == executionID {
				return true
			}
		}
	}
	return st.SolutionRun != nil && st.SolutionRun.ID == solutionRunID && solutionExecutionID(st.SolutionRun) == executionID
}

// Bind every request to both the selected workspace and its execution/child.
// A caller-provided path is never used to locate a trace or an archive.
func (s *Service) solutionTraceSource(workspaceID, executionID, solutionRunID string) (solutionTraceSource, error) {
	s.mu.Lock()
	if s.closed || workspaceID == "" || workspaceID != s.state.ActiveWorkspaceID {
		s.mu.Unlock()
		return solutionTraceSource{}, fmt.Errorf("ワークスペースが変更されています")
	}
	if !validSolutionTraceID(executionID) || !validSolutionTraceID(solutionRunID) {
		s.mu.Unlock()
		return solutionTraceSource{}, fmt.Errorf("実行・ソリューション処理IDが不正です")
	}
	cfg := s.state.Config
	known := traceRunExists(s.state, executionID, solutionRunID)
	s.mu.Unlock()
	directory, err := executionDirectory(cfg.QueuePath, executionID)
	if err != nil {
		return solutionTraceSource{}, err
	}
	if !known {
		var record executionRecord
		if err := readSolutionTraceJSON(cfg, filepath.Join(directory, "snapshot.json"), &record); err != nil {
			return solutionTraceSource{}, fmt.Errorf("実行履歴を確認できません: %w", err)
		}
		if record.Version != 1 || record.Run.ID != executionID || !sameRoot(record.State.Config.QueuePath, cfg.QueuePath) || !sameRoot(record.State.Config.Root, cfg.Root) || !traceRunExists(record.State, executionID, solutionRunID) {
			return solutionTraceSource{}, fmt.Errorf("指定した実行にソリューションの処理履歴がありません")
		}
	}
	source := solutionTraceSource{workspaceID: workspaceID, executionID: executionID, solutionRunID: solutionRunID, cfg: cfg, directory: filepath.Join(directory, "traces", solutionRunID)}
	return source, s.checkSolutionTraceSource(source)
}

func (s *Service) checkSolutionTraceSource(source solutionTraceSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state.ActiveWorkspaceID != source.workspaceID || !sameRoot(s.state.Config.QueuePath, source.cfg.QueuePath) || !sameRoot(s.state.Config.Root, source.cfg.Root) {
		return fmt.Errorf("ワークスペースまたは実行履歴の保存先が変更されています")
	}
	return nil
}

// Read inside an opened queue parent, rejecting symlinks at every level before
// opening a file. os.Root confines a concurrent path replacement to that root.
func readSolutionTraceJSON(cfg model.Config, path string, value any) error {
	queue, err := outputAbsolute(cfg.QueuePath)
	if err != nil {
		return err
	}
	path, err = outputAbsolute(path)
	if err != nil {
		return err
	}
	base := filepath.Dir(queue)
	for directory := base; ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("操作履歴の保存先にシンボリックリンクは使用できません")
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("操作履歴のパスが不正です")
	}
	root, err := openTargetFileRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := targetFileInfo(root, filepath.ToSlash(relative))
	if err != nil {
		return err
	}
	file, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return fmt.Errorf("操作履歴ファイルが読み出し中に変更されました")
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("操作履歴のJSONを読み込めません: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("操作履歴のJSON末尾が不正です")
	}
	return nil
}

func readSolutionTraceIndex(source solutionTraceSource) (solutionTraceIndex, error) {
	index := solutionTraceIndex{Version: 1}
	err := readSolutionTraceJSON(source.cfg, filepath.Join(source.directory, "index.json"), &index)
	if os.IsNotExist(err) {
		return solutionTraceIndex{Version: 1}, nil // old executions have no trace
	}
	if err != nil {
		return index, err
	}
	if index.Version != 1 || index.Count < 0 {
		return index, fmt.Errorf("操作履歴の一覧情報が不正です")
	}
	return index, nil
}

func solutionTraceRecordPath(source solutionTraceSource, sequence int) string {
	return filepath.Join(source.directory, "entries", fmt.Sprintf("%08d.json", sequence))
}

func solutionTraceBodyPath(source solutionTraceSource, sequence int, section, hash string) string {
	return filepath.Join(source.directory, "details", fmt.Sprintf("%08d-%s-%s.json", sequence, section, hash))
}

func readSolutionTraceRecord(source solutionTraceSource, sequence int) (solutionTraceRecord, error) {
	var record solutionTraceRecord
	if err := readSolutionTraceJSON(source.cfg, solutionTraceRecordPath(source, sequence), &record); err != nil {
		return record, err
	}
	if record.Entry.Sequence != sequence || record.Entry.Iteration < 0 || !validSolutionTraceHash(record.KeyHash) || record.Entry.ArgumentsBytes < 0 || record.Entry.OutputBytes < 0 {
		return record, fmt.Errorf("操作履歴の行情報が不正です")
	}
	for _, body := range []struct {
		id   string
		size int
	}{{record.ArgumentsID, record.Entry.ArgumentsBytes}, {record.OutputID, record.Entry.OutputBytes}} {
		if (body.id == "") != (body.size == 0) || body.id != "" && !validSolutionTraceHash(body.id) {
			return record, fmt.Errorf("操作履歴の本文参照が不正です")
		}
	}
	return record, nil
}

func (s *Service) newSolutionTraceWriter(cfg model.Config, executionID, solutionRunID string) (func(int, model.AgentTraceEvent) error, error) {
	s.mu.Lock()
	workspaceID := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	source, err := s.solutionTraceSource(workspaceID, executionID, solutionRunID)
	if err != nil {
		return nil, err
	}
	if !sameRoot(source.cfg.QueuePath, cfg.QueuePath) || !sameRoot(source.cfg.Root, cfg.Root) {
		return nil, fmt.Errorf("操作履歴と実行設定の保存先が一致しません")
	}
	return func(iteration int, event model.AgentTraceEvent) error {
		if iteration < 0 || event.Key == "" || event.Session < 0 || event.Turn < 0 {
			return fmt.Errorf("AI操作イベントの識別情報が不正です")
		}
		for _, text := range []string{event.Key, event.Phase, event.Kind, event.Tool, event.CallID, event.Status, event.Summary, event.Arguments, event.Output, event.StartedAt, event.FinishedAt} {
			if !utf8.ValidString(text) {
				return fmt.Errorf("AI操作イベントがUTF-8ではありません")
			}
		}
		// Share the archive mutex: separate writer handles cannot allocate the
		// same sequence, and trace readers see one complete metadata revision.
		s.executionMu.Lock()
		defer s.executionMu.Unlock()
		if err := s.checkSolutionTraceSource(source); err != nil {
			return err
		}
		index, err := readSolutionTraceIndex(source)
		if err != nil {
			return err
		}
		// A crash after the row was published but before its count/key was
		// committed must not let another event reuse that sequence.
		for {
			pending, err := readSolutionTraceRecord(source, index.Count+1)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			pendingKey := filepath.Join(source.directory, "keys", pending.KeyHash+".json")
			if err := writeOutputJSON(source.cfg, pendingKey, solutionTraceKey{Sequence: pending.Entry.Sequence}); err != nil {
				return err
			}
			index.Count = pending.Entry.Sequence
			if err := writeOutputJSON(source.cfg, filepath.Join(source.directory, "index.json"), index); err != nil {
				return err
			}
		}
		keyHash := digest([]byte(strconv.Itoa(iteration) + "\x00" + event.Key))
		keyPath := filepath.Join(source.directory, "keys", keyHash+".json")
		var key solutionTraceKey
		err = readSolutionTraceJSON(source.cfg, keyPath, &key)
		fresh := os.IsNotExist(err)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			key.Sequence = index.Count + 1
		}
		if key.Sequence < 1 || key.Sequence > index.Count+1 {
			return fmt.Errorf("AI操作イベントの順序が不正です")
		}
		record := solutionTraceRecord{Entry: model.AgentTraceEntry{Sequence: key.Sequence, Iteration: iteration}, KeyHash: keyHash}
		if !fresh {
			record, err = readSolutionTraceRecord(source, key.Sequence)
			if err != nil {
				return err
			}
			if record.KeyHash != keyHash || record.Entry.Iteration != iteration {
				return fmt.Errorf("AI操作イベントの更新先が一致しません")
			}
		}
		s.mu.Lock()
		for _, field := range []*string{&event.Phase, &event.Kind, &event.Tool, &event.CallID, &event.Status, &event.Summary, &event.StartedAt, &event.FinishedAt} {
			*field = s.redactSecretsLocked(*field)
		}
		event.Arguments = redactSolutionTraceText(event.Arguments, s.redactSecretsLocked, 0)
		event.Output = redactSolutionTraceText(event.Output, s.redactSecretsLocked, 0)
		s.mu.Unlock()
		entry := &record.Entry
		entry.Session, entry.Turn = event.Session, event.Turn
		for _, field := range []struct {
			next   string
			target *string
		}{
			{event.Phase, &entry.Phase}, {event.Kind, &entry.Kind}, {event.Tool, &entry.Tool},
			{event.CallID, &entry.CallID}, {event.Status, &entry.Status}, {event.Summary, &entry.Summary},
			{event.StartedAt, &entry.StartedAt}, {event.FinishedAt, &entry.FinishedAt},
		} {
			if field.next != "" {
				*field.target = field.next
			}
		}
		for _, field := range []struct {
			target *string
			limit  int
		}{
			{&entry.Summary, 1024}, {&entry.Tool, 128}, {&entry.CallID, 1024},
			{&entry.Phase, 64}, {&entry.Kind, 64}, {&entry.Status, 64},
			{&entry.StartedAt, 128}, {&entry.FinishedAt, 128},
		} {
			*field.target = boundedSolutionTraceString(*field.target, field.limit)
		}
		if event.Usage != (model.Usage{}) {
			entry.Usage = event.Usage
		}
		for _, body := range []struct {
			section, content string
			id               *string
			size             *int
		}{{"arguments", event.Arguments, &record.ArgumentsID, &entry.ArgumentsBytes}, {"output", event.Output, &record.OutputID, &entry.OutputBytes}} {
			// Completion events may omit arguments already sent at tool start.
			if body.content == "" {
				continue
			}
			hash := digest([]byte(body.content))
			if hash != *body.id {
				if err := writeOutputJSON(source.cfg, solutionTraceBodyPath(source, key.Sequence, body.section, hash), body.content); err != nil {
					return err
				}
			}
			*body.id, *body.size = hash, len(body.content)
		}
		// Publish the row only after both detailed bodies exist. Content-addressed
		// generations retain concurrent readers' references without copying a
		// repeated argument body on every start/end notification.
		if err := writeOutputJSON(source.cfg, solutionTraceRecordPath(source, key.Sequence), record); err != nil {
			return err
		}
		if fresh {
			if err := writeOutputJSON(source.cfg, keyPath, key); err != nil {
				return err
			}
		}
		if key.Sequence > index.Count {
			index.Count = key.Sequence
			if err := writeOutputJSON(source.cfg, filepath.Join(source.directory, "index.json"), index); err != nil {
				return err
			}
		}
		return s.checkSolutionTraceSource(source)
	}, nil
}

func boundedSolutionTraceString(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}

// Redact decoded JSON string tokens rather than replacing escaped bytes. This
// preserves keys, numbers, whitespace and even repeated keys, and also masks
// JSON text embedded inside a tool result's content string.
func redactSolutionTraceText(text string, redact func(string) string, depth int) string {
	if depth >= 64 || !json.Valid([]byte(text)) {
		return redact(text)
	}
	var result strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '"' {
			result.WriteByte(text[i])
			i++
			continue
		}
		start := i
		i++
		for i < len(text) {
			if text[i] == '\\' {
				i += 2
				continue
			}
			if text[i] == '"' {
				i++
				break
			}
			i++
		}
		var decoded string
		_ = json.Unmarshal([]byte(text[start:i]), &decoded)
		redacted := redactSolutionTraceText(decoded, redact, depth+1)
		if redacted == decoded {
			result.WriteString(text[start:i])
			continue
		}
		encoded, _ := json.Marshal(redacted)
		result.Write(encoded)
	}
	return result.String()
}

func (s *Service) ListSolutionTrace(workspaceID, executionID, solutionRunID string, offset int) (model.AgentTracePage, error) {
	page := model.AgentTracePage{Entries: []model.AgentTraceEntry{}}
	if offset < 0 {
		return page, fmt.Errorf("操作履歴のオフセットが不正です")
	}
	source, err := s.solutionTraceSource(workspaceID, executionID, solutionRunID)
	if err != nil {
		return page, err
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	index, err := readSolutionTraceIndex(source)
	if err != nil {
		return page, err
	}
	if offset > index.Count {
		return page, fmt.Errorf("操作履歴のオフセットが範囲外です")
	}
	end := offset + min(solutionTracePageSize, index.Count-offset)
	for i := offset; i < end; i++ {
		record, err := readSolutionTraceRecord(source, i+1)
		if err != nil {
			return model.AgentTracePage{}, err
		}
		page.Entries = append(page.Entries, record.Entry)
	}
	page.NextOffset, page.Total, page.Complete = end, index.Count, end == index.Count
	if err := s.checkSolutionTraceSource(source); err != nil {
		return model.AgentTracePage{}, err
	}
	return page, nil
}

func (s *Service) ReadSolutionTrace(workspaceID, executionID, solutionRunID string, sequence int, section string, offset int) (model.AgentTraceContent, error) {
	var result model.AgentTraceContent
	if sequence < 1 || offset < 0 || (section != "arguments" && section != "output") {
		return result, fmt.Errorf("操作履歴の本文指定が不正です")
	}
	source, err := s.solutionTraceSource(workspaceID, executionID, solutionRunID)
	if err != nil {
		return result, err
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	index, err := readSolutionTraceIndex(source)
	if err != nil {
		return result, err
	}
	if sequence > index.Count {
		return result, fmt.Errorf("操作履歴の行がありません")
	}
	record, err := readSolutionTraceRecord(source, sequence)
	if err != nil {
		return result, err
	}
	id, size := record.ArgumentsID, record.Entry.ArgumentsBytes
	if section == "output" {
		id, size = record.OutputID, record.Entry.OutputBytes
	}
	content := ""
	if id != "" {
		if err := readSolutionTraceJSON(source.cfg, solutionTraceBodyPath(source, sequence, section, id), &content); err != nil {
			return result, err
		}
		if !utf8.ValidString(content) || len(content) != size || digest([]byte(content)) != id {
			return result, fmt.Errorf("操作履歴の本文が保存時の内容と一致しません")
		}
	}
	if offset > len(content) || offset < len(content) && !utf8.RuneStart(content[offset]) {
		return result, fmt.Errorf("本文のオフセットが文字境界または範囲に一致しません")
	}
	end := offset + min(solutionTraceContentSize, len(content)-offset)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	result = model.AgentTraceContent{Content: content[offset:end], Offset: offset, NextOffset: end, TotalBytes: len(content), Complete: end == len(content)}
	if err := s.checkSolutionTraceSource(source); err != nil {
		return model.AgentTraceContent{}, err
	}
	return result, nil
}
