package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

const maxCLIInputBytes = 4 << 20

type contextInput struct {
	context.Context
	io.Reader
}

type cliArgumentValidationKey struct{}

func validatingCLIArguments(ctx context.Context) bool {
	value, _ := ctx.Value(cliArgumentValidationKey{}).(bool)
	return value
}

func dispatchCLIGroup(ctx context.Context, s *engine.Service, o options, in io.Reader) (any, error) {
	switch o.command {
	case "workspace":
		return executeWorkspaceCommand(ctx, s, o.commandArgs, in)
	case "rules":
		return executeRulesCommand(ctx, s, o.commandArgs, in)
	case "llm":
		return executeLLMCommand(ctx, s, o.commandArgs, in)
	case "settings":
		return executeSettingsCommand(ctx, s, o.commandArgs, in)
	case "files":
		return executeFilesCommand(ctx, s, o.commandArgs)
	case "runs":
		return executeRunsCommand(ctx, s, o.commandArgs)
	case "publish":
		return executePublishCommand(ctx, s, o.commandArgs, in)
	}
	return nil, errors.New("不明なコマンドです")
}

// Read credentials from a bounded file/stdin, never from argv. Decoder errors
// deliberately omit the input because malformed JSON may contain API secrets.
func readCLIJSON(input io.Reader, path string, dest any) error {
	if path == "" {
		return errors.New("--input にJSONファイルまたは -（標準入力）を指定してください")
	}
	reader := input
	ctx := context.Background()
	if source, ok := input.(contextInput); ok {
		ctx, reader = source.Context, source.Reader
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return errors.New("入力JSONファイルを開けません")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("入力は通常のJSONファイルを指定してください")
		}
		reader = file
	}
	if reader == nil {
		return errors.New("標準入力からJSONを読み込めません")
	}
	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, maxCLIInputBytes+1))
		done <- readResult{data, err}
	}()
	var data []byte
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-done:
		if result.err != nil {
			return errors.New("入力JSONを読み込めません")
		}
		data = result.data
	}
	if len(data) > maxCLIInputBytes {
		return errors.New("入力JSONは4 MiB以下にしてください")
	}
	// Windows PowerShell's UTF-8 files may start with a BOM.
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if len(data) == 0 || data[0] != '{' {
		return errors.New("入力JSONは設定項目を持つオブジェクトにしてください")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return errors.New("入力JSONの形式・項目名・値の型を確認してください")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("入力JSONには1つのオブジェクトだけを指定してください")
	}
	return ctx.Err()
}

func commandFlags(name string, args []string, configure func(*flag.FlagSet)) error {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configure(f)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("余分な位置引数があります。--help を参照してください")
	}
	return nil
}

var editableSettings = []string{
	"includeGlobs", "excludeGlobs", "checkCommands", "maxAttempts", "maxTurns",
	"maxOutputTokens", "maxFileBytes", "timeoutSeconds", "maxCostUSD",
	"inputPricePerMillion", "cachedInputPricePerMillion", "outputPricePerMillion",
}

func publicSettings(cfg model.Config) map[string]json.RawMessage {
	data, _ := json.Marshal(cfg)
	var all map[string]json.RawMessage
	_ = json.Unmarshal(data, &all)
	result := make(map[string]json.RawMessage, len(editableSettings))
	for _, key := range editableSettings {
		result[key] = all[key]
	}
	return result
}

func executeSettingsCommand(ctx context.Context, s *engine.Service, args []string, in io.Reader) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("settings show または update を指定してください")
	}
	var input string
	switch args[0] {
	case "show":
		if err := commandFlags("settings show", args[1:], func(*flag.FlagSet) {}); err != nil {
			return nil, err
		}
		if validatingCLIArguments(ctx) {
			return nil, nil
		}
		return publicSettings(s.Snapshot().Config), nil
	case "update":
		if err := commandFlags("settings update", args[1:], func(f *flag.FlagSet) { f.StringVar(&input, "input", "", "JSONファイルまたは -") }); err != nil {
			return nil, err
		}
		if input == "" {
			return nil, errors.New("--input を指定してください")
		}
		if validatingCLIArguments(ctx) {
			return nil, nil
		}
		var patch map[string]json.RawMessage
		if err := readCLIJSON(in, input, &patch); err != nil {
			return nil, err
		}
		if len(patch) == 0 {
			return nil, errors.New("変更する設定を1つ以上指定してください")
		}
		cfg := s.Snapshot().Config
		if s.Snapshot().ActiveWorkspaceID == "" {
			return nil, errors.New("先にワークスペースを作成・選択してください")
		}
		permitted := publicSettings(cfg)
		for key, raw := range patch {
			if _, ok := permitted[key]; !ok {
				return nil, errors.New("変更できない設定項目があります。settings show の項目だけを指定してください")
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, errors.New("設定の null は使用できません。上限解除は0、配列の解除は [] を指定してください")
			}
		}
		data, _ := json.Marshal(patch)
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return nil, errors.New("設定値の型が正しくありません")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.SaveConfig(cfg)
	default:
		return nil, errors.New("不明なsettings操作です。--help を参照してください")
	}
}

func executeFilesCommand(ctx context.Context, s *engine.Service, args []string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("files list または show を指定してください")
	}
	var file, source string
	if args[0] != "list" && args[0] != "show" {
		return nil, errors.New("不明なfiles操作です")
	}
	if err := commandFlags("files "+args[0], args[1:], func(f *flag.FlagSet) {
		if args[0] == "show" {
			f.StringVar(&file, "file", "", "相対パス")
			f.StringVar(&source, "source", "target", "target / execution")
		}
	}); err != nil {
		return nil, err
	}
	if args[0] == "show" {
		file = filepath.ToSlash(file)
		if file == "" {
			return nil, errors.New("--file を指定してください")
		}
		if source != "target" && source != "execution" {
			return nil, errors.New("--source は target または execution を指定してください")
		}
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	st := s.Snapshot()
	if st.ActiveWorkspaceID == "" {
		return nil, errors.New("先にワークスペースを作成・選択してください")
	}
	if args[0] == "list" {
		return s.ListTargetFiles(st.ActiveWorkspaceID, st.Config.Root)
	}
	if file == "" {
		return nil, errors.New("--file を指定してください")
	}
	switch source {
	case "target":
		return s.ReadTargetFile(st.ActiveWorkspaceID, st.Config.Root, file)
	case "execution":
		return s.ReadExecutionFile(st.ActiveWorkspaceID, st.Config.Root, file)
	default:
		return nil, errors.New("--source は target または execution を指定してください")
	}
}

func executeRunsCommand(ctx context.Context, s *engine.Service, args []string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("runs list または show を指定してください")
	}
	var id string
	if args[0] != "list" && args[0] != "show" {
		return nil, errors.New("不明なruns操作です")
	}
	if err := commandFlags("runs "+args[0], args[1:], func(f *flag.FlagSet) {
		if args[0] == "show" {
			f.StringVar(&id, "id", "", "実行ID")
		}
	}); err != nil {
		return nil, err
	}
	if args[0] == "show" && id == "" {
		return nil, errors.New("--id を指定してください")
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	if args[0] == "list" {
		return s.Snapshot().ExecutionRuns, nil
	}
	if id == "" {
		return nil, errors.New("--id を指定してください")
	}
	return s.GetExecutionRun(id)
}

func printCLIResult(w io.Writer, value any, asJSON bool) error {
	if asJSON {
		return writeJSON(w, value)
	}
	switch v := value.(type) {
	case model.State:
		if v.ActiveWorkspaceID != "" {
			fmt.Fprintln(w, "ワークスペース:", v.ActiveWorkspaceID)
		}
		printSummary(w, v)
		if v.LastError != "" {
			fmt.Fprintln(w, "診断:", v.LastError)
		}
		return nil
	case map[string]string:
		if diff, ok := v["diff"]; ok {
			_, err := io.WriteString(w, diff)
			return err
		}
		if path, ok := v["path"]; ok {
			_, err := fmt.Fprintln(w, path)
			return err
		}
		if message, ok := v["message"]; ok {
			_, err := fmt.Fprintln(w, message)
			return err
		}
	case model.TargetFileContent:
		if v.UnavailableReason != "" {
			_, err := fmt.Fprintln(w, v.UnavailableReason)
			return err
		}
		_, err := io.WriteString(w, v.Content)
		return err
	case model.TargetFileList:
		for _, file := range v.Files {
			fmt.Fprintf(w, "%s\t%d bytes\n", file.File, file.Size)
		}
		if v.Truncated {
			fmt.Fprintf(w, "先頭%d件を表示しています。\n", v.Limit)
		}
		return nil
	case model.FileDetail:
		fmt.Fprintln(w, v.Task.File, "—", displayStatus(v.Task.Status))
		if v.Task.Note != "" {
			fmt.Fprintln(w, v.Task.Note)
		}
		for _, item := range v.Changes {
			if item.Change == "" && item.Risk == "" && item.Expected == "" {
				continue
			}
			fmt.Fprintf(w, "%s · %s · %s: %s\n", item.RuleID, item.Location, item.Status, item.Change)
		}
		if v.Diff != "" {
			_, err := io.WriteString(w, v.Diff)
			return err
		}
		_, err := io.WriteString(w, v.Before)
		return err
	case []model.LLMConnection:
		for _, c := range v {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Provider, c.AuthMode)
		}
		return nil
	case []model.Rule:
		for _, r := range v {
			kind := "個別"
			if r.Always {
				kind = "共通"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.ID, kind, r.Title)
		}
		return nil
	case []model.ExecutionRun:
		for _, r := range v {
			fmt.Fprintf(w, "%s\t%s\t%s\t%dファイル\n", r.ID, r.StartedAt, r.Status, r.TargetCount)
		}
		return nil
	}
	return writeJSON(w, value)
}

// Keep canonical statuses in JSON; friendly labels are only presentation.
func displayStatus(status string) string {
	labels := map[string]string{"pending": "未修正", "done": "完了", "skipped": "変更不要", "failed": "失敗", "needs_human": "要確認", "running": "処理中"}
	if label, ok := labels[status]; ok {
		return label
	}
	return strings.TrimSpace(status)
}
