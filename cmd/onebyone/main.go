// Command onebyone exposes the same processing service used by the desktop app.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("値を指定してください")
	}
	*s = append(*s, filepath.ToSlash(value))
	return nil
}

type options struct {
	provider, command, config, root, workspace       string
	endpoint, deployment, authMode, rule, connection string
	output, runID, directory, name                   string
	files                                            stringList
	limit, attempt                                   int
	json, all, none, yes                             bool
	commandArgs                                      []string
	set                                              map[string]bool
}

func execute(args []string, stdout, stderr io.Writer) int {
	return executeWithInput(args, os.Stdin, stdout, stderr)
}

func executeWithInput(args []string, input io.Reader, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return executeContext(ctx, args, input, stdout, stderr)
}

func executeContext(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) int {
	input = contextInput{Context: ctx, Reader: input}
	o, help, err := parseOptions(args, stdout, stderr)
	if help {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "エラー:", err)
		return 1
	}
	if ctx.Err() != nil {
		return 130
	}
	if groupedCommand(o.command) {
		_, err = dispatchCLIGroup(context.WithValue(ctx, cliArgumentValidationKey{}, true), nil, o, nil)
		if errors.Is(err, flag.ErrHelp) {
			printGroupHelp(stdout, o.command)
			return 0
		}
		if err != nil {
			fmt.Fprintln(stderr, "エラー:", err)
			return 1
		}
	}
	s := engine.New(o.config)
	defer s.Close()
	// Cancellation also applies to login and connection tests, whose Running
	// flag is false. Retry cancellation to cover operation registration races.
	done, watcherDone := make(chan struct{}), make(chan struct{})
	defer func() { close(done); <-watcherDone }()
	go watchCancellation(ctx, s, done, watcherDone)
	if o.workspace != "" {
		if _, err = s.SelectWorkspace(o.workspace); err != nil {
			fmt.Fprintln(stderr, "エラー:", err)
			return 1
		}
	}
	var value any
	code := 0
	if groupedCommand(o.command) {
		value, err = dispatchCLIGroup(ctx, s, o, input)
	} else {
		if err = prepareCLIWorkspace(s, o); err == nil && ctx.Err() == nil {
			switch o.command {
			case "scan":
				value, err = s.Scan()
			case "demo":
				if o.directory == "" {
					o.directory = os.TempDir()
				}
				var root string
				root, err = s.CreateDemoProject(o.directory)
				if err == nil {
					_, err = s.CreateDemoWorkspace(o.name, root)
				}
				if err == nil {
					value, err = s.Scan()
				}
			case "run":
				previous := map[string]bool{}
				for _, run := range s.Snapshot().ExecutionRuns {
					previous[run.ID] = true
				}
				if err = s.Start(o.limit); err == nil {
					waitForRun(s, stderr)
					state := s.Snapshot()
					if state.LastError != "" {
						err = errors.New(state.LastError)
					} else {
						code, err = executionExitCode(s, previous)
					}
				}
			case "retry":
				var files []string
				files, err = selectRetryFiles(s.Snapshot().Tasks, o.files, o.rule)
				if err == nil {
					value, err = s.RetryTasks(files)
				}
			case "selection":
				files := []string(o.files)
				if o.all {
					for _, task := range s.Snapshot().Tasks {
						files = append(files, task.File)
					}
				}
				value, err = s.SetTaskSelection(files)
			case "discard":
				value, err = s.DiscardFileChanges(o.files[0])
			case "detail":
				index := o.attempt - 1 // omitted (=0) means first-to-latest cumulative changes
				if o.runID == "" {
					value, err = s.GetFileDetail(o.files[0], index)
				} else {
					value, err = s.GetExecutionFileDetail(o.runID, o.files[0], index)
				}
			case "report":
				var path string
				if o.runID == "" {
					path, err = s.ExportReport(o.output)
				} else {
					path, err = s.ExportExecutionReport(o.runID, o.output)
				}
				value = map[string]string{"path": path}
			case "status":
				value = s.Snapshot()
			case "doctor":
				git := engine.CheckGitInstallation(ctx)
				value = git
				if !git.Available {
					code = 1
				}
			}
		}
	}
	if ctx.Err() != nil {
		s.Stop()
		s.CancelLLMSignIn()
		s.Wait()
		fmt.Fprintln(stderr, "停止しました。保存済みのワークスペースから再開できます。")
		code = 130
	} else if err != nil {
		fmt.Fprintln(stderr, "エラー:", err)
		code = 1
	}
	if value == nil || err != nil || code == 130 {
		value = s.Snapshot()
	}
	if o.command == "llm" && !o.json && code == 0 {
		if state, ok := value.(model.State); ok {
			value = state.LLMConnections
		}
	}
	if err = printCLIResult(stdout, value, o.json); err != nil {
		fmt.Fprintln(stderr, "出力エラー:", err)
		return 1
	}
	return code
}

func watchCancellation(ctx context.Context, s *engine.Service, done <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	select {
	case <-ctx.Done():
	case <-done:
		return
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.CancelLLMSignIn()
		s.Stop()
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func prepareCLIWorkspace(s *engine.Service, o options) error {
	if o.command != "scan" && o.command != "run" {
		return nil
	}
	if o.set["root"] {
		root, err := filepath.Abs(o.root)
		if err != nil {
			return err
		}
		st := s.Snapshot()
		if st.ActiveWorkspaceID == "" {
			if _, err = s.CreateWorkspace(filepath.Base(root), root); err != nil {
				return err
			}
		} else if filepath.Clean(st.Config.Root) != filepath.Clean(root) {
			if _, err = s.ChangeTargetFolder(root); err != nil {
				return err
			}
		}
	}
	if o.connection != "" {
		_, err := s.SelectLLMConnection(o.connection)
		return err
	}
	if o.set["provider"] || o.set["endpoint"] || o.set["deployment"] || o.set["auth-mode"] {
		cfg := s.Snapshot().Config
		applyOptions(&cfg, o)
		return applyCLIConnection(s, cfg)
	}
	return nil
}

func executionExitCode(s *engine.Service, previous map[string]bool) (int, error) {
	for _, run := range s.Snapshot().ExecutionRuns {
		if previous[run.ID] {
			continue
		}
		result, err := s.GetExecutionRun(run.ID)
		if err != nil {
			return 1, err
		}
		return executionResultExitCode(result), nil
	}
	return 1, errors.New("今回の実行結果が見つかりません")
}

func executionResultExitCode(result model.ExecutionRunResult) int {
	if result.Run.Error != "" || result.Run.Status == "failed" {
		return 1
	}
	targets := map[string]bool{}
	for _, file := range result.TargetFiles {
		targets[file] = true
	}
	for _, task := range result.State.Tasks {
		if targets[task.File] && (task.Status == "failed" || task.Status == "needs_human") {
			return 2
		}
	}
	return 0
}

// Explicit CLI overrides create a separate personal connection; they never edit
// a named connection that other workspaces may also be using.
func applyCLIConnection(s *engine.Service, cfg model.Config) error {
	before := s.Snapshot()
	for _, connection := range before.LLMConnections {
		if connection.ID == before.SelectedLLMConnectionID && connection.Provider == cfg.Provider && connection.Endpoint == cfg.Endpoint && connection.Deployment == cfg.Deployment && connection.AuthMode == cfg.AuthMode {
			return nil
		}
	}
	if cfg.AuthMode == "oauth" {
		return errors.New("OAuth接続の変更は llm save --input で行い、llm login でサインインしてください")
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, connection := range before.LLMConnections {
		ids[connection.ID] = true
		names[strings.ToLower(connection.Name)] = true
	}
	baseName := "CLI · " + filepath.Base(cfg.Root)
	if runes := []rune(baseName); len(runes) > 64 {
		baseName = string(runes[:64])
	}
	name := baseName
	for i := 2; names[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s (%d)", baseName, i)
	}
	saved, err := s.SaveLLMConnection(model.LLMConnection{
		Name: name, Provider: cfg.Provider,
		Endpoint: cfg.Endpoint, Deployment: cfg.Deployment, AuthMode: cfg.AuthMode,
	})
	if err != nil {
		return err
	}
	for _, connection := range saved.LLMConnections {
		if !ids[connection.ID] {
			_, err = s.SelectLLMConnection(connection.ID)
			return err
		}
	}
	return errors.New("CLIのLLM接続を登録できませんでした")
}

func groupedCommand(command string) bool {
	switch command {
	case "workspace", "rules", "llm", "settings", "files", "runs", "publish":
		return true
	}
	return false
}

func parseOptions(args []string, stdout, stderr io.Writer) (options, bool, error) {
	o := options{set: map[string]bool{}, name: "デモ"}
	o.config = os.Getenv("ONEBYONE_CONFIG")
	if o.config == "" {
		dir, err := engine.DefaultLocalDirectory()
		if err != nil {
			return o, false, err
		}
		o.config = filepath.Join(dir, "app-settings.json")
	}
	// Global switches are accepted on either side of any command/subcommand.
	var rest []string
	for i := 0; i < len(args); i++ {
		key, value, equals := strings.Cut(args[i], "=")
		if key == "--config" || key == "--workspace" {
			if !equals {
				i++
				if i == len(args) {
					return o, false, fmt.Errorf("%s に値を指定してください", key)
				}
				value = args[i]
			}
			if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "--") {
				return o, false, fmt.Errorf("%s に値を指定してください", key)
			}
			if key == "--config" {
				o.config = value
			} else {
				o.workspace = value
			}
		} else if key == "--json" {
			if equals && value != "true" && value != "false" {
				return o, false, errors.New("--json は true または false です")
			}
			o.json = !equals || value == "true"
		} else {
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 || rest[0] == "--help" || rest[0] == "-h" || rest[0] == "help" {
		printHelp(stdout)
		return o, true, nil
	}
	o.command = rest[0]
	if groupedCommand(o.command) {
		for _, arg := range rest[1:] {
			if arg == "--help" || arg == "-h" {
				printGroupHelp(stdout, o.command)
				return o, true, nil
			}
		}
		if len(rest) == 1 {
			printGroupHelp(stdout, o.command)
			return o, true, nil
		}
		o.commandArgs = rest[1:]
		return o, false, nil
	}
	switch o.command {
	case "scan", "run", "status", "retry", "report", "demo", "selection", "discard", "detail", "doctor":
	default:
		return o, false, fmt.Errorf("不明なコマンド %q。onebyone --help を参照してください", o.command)
	}
	f := flag.NewFlagSet("onebyone "+o.command, flag.ContinueOnError)
	f.SetOutput(stderr)
	switch o.command {
	case "scan":
		f.StringVar(&o.root, "root", "", "対象フォルダ（新規作成または対象変更）")
	case "run":
		f.IntVar(&o.limit, "limit", 0, "処理ファイル数（0で選択した全件）")
	case "retry":
		f.Var(&o.files, "file", "再試行対象の相対パス（複数指定可能）")
		f.StringVar(&o.rule, "rule", "", "適用済みルールIDで再試行対象を指定")
	case "report":
		f.StringVar(&o.output, "output", "", "結果JSONの出力先")
		f.StringVar(&o.runID, "run", "", "出力する実行ID（省略時は現在）")
	case "demo":
		f.StringVar(&o.directory, "directory", "", "デモプロジェクトの保存先フォルダ（省略時は一時フォルダ）")
		f.StringVar(&o.name, "name", "デモ", "ワークスペース名")
	case "selection":
		f.Var(&o.files, "file", "選択する相対パス（複数指定可能・選択全体を置換）")
		f.BoolVar(&o.all, "all", false, "全ファイルを選択")
		f.BoolVar(&o.none, "none", false, "全選択を解除")
	case "discard":
		f.Var(&o.files, "file", "変更を破棄する相対パス")
		f.BoolVar(&o.yes, "yes", false, "採用済み変更の破棄を確認")
	case "detail":
		f.Var(&o.files, "file", "表示する相対パス")
		f.StringVar(&o.runID, "run", "", "実行ID（省略時は現在）")
		f.IntVar(&o.attempt, "attempt", 0, "試行番号（1から。省略時は累積の差分・対応状況）")
	}
	if o.command == "scan" || o.command == "run" {
		f.StringVar(&o.connection, "connection", "", "登録済みLLM接続ID")
		f.StringVar(&o.provider, "provider", "", "簡易登録: openai / azure / claude")
		f.StringVar(&o.endpoint, "endpoint", "", "簡易登録: APIベースURL")
		f.StringVar(&o.deployment, "deployment", "", "簡易登録: モデル名 / デプロイ名")
		f.StringVar(&o.authMode, "auth-mode", "", "簡易登録: api_key / bearer（OAuthは llm save/login を使用）")
	}
	f.Usage = func() {
		fmt.Fprintf(stdout, "使い方: onebyone %s [--config PATH] [--workspace ID] [--json] [options]\n\n", o.command)
		f.SetOutput(stdout)
		f.PrintDefaults()
		f.SetOutput(stderr)
	}
	if err := f.Parse(rest[1:]); err != nil {
		return o, errors.Is(err, flag.ErrHelp), err
	}
	if f.NArg() != 0 {
		return o, false, errors.New("余分な位置引数があります。--help を参照してください")
	}
	f.Visit(func(v *flag.Flag) { o.set[v.Name] = true })
	for name, value := range map[string]string{"root": o.root, "connection": o.connection, "provider": o.provider, "deployment": o.deployment, "auth-mode": o.authMode, "directory": o.directory, "name": o.name, "output": o.output, "run": o.runID, "rule": o.rule} {
		if o.set[name] && strings.TrimSpace(value) == "" {
			return o, false, fmt.Errorf("--%s に値を指定してください", name)
		}
	}
	if o.limit < 0 {
		return o, false, errors.New("--limit は0以上で指定してください")
	}
	if o.set["root"] && strings.TrimSpace(o.root) == "" {
		return o, false, errors.New("--root が空です")
	}
	if o.command == "retry" && len(o.files) == 0 && o.rule == "" {
		return o, false, errors.New("retry には --file または --rule が必要です")
	}
	if o.command == "selection" {
		modes := 0
		if len(o.files) > 0 {
			modes++
		}
		if o.all {
			modes++
		}
		if o.none {
			modes++
		}
		if modes != 1 {
			return o, false, errors.New("selection には --file / --all / --none のいずれか1つを指定してください")
		}
	}
	if (o.command == "detail" || o.command == "discard") && len(o.files) != 1 {
		return o, false, errors.New("--file を1つ指定してください")
	}
	if o.command == "discard" && !o.yes {
		return o, false, errors.New("変更を破棄するには --yes を指定してください")
	}
	if o.set["attempt"] && o.attempt < 1 {
		return o, false, errors.New("--attempt は1以上で指定してください")
	}
	if o.authMode == "oauth" {
		return o, false, errors.New("OAuthは llm save --input と llm login --id で設定してください")
	}
	if o.connection != "" && (o.set["provider"] || o.set["endpoint"] || o.set["deployment"] || o.set["auth-mode"]) {
		return o, false, errors.New("--connection と簡易接続設定は同時に指定できません")
	}
	return o, false, nil
}

func applyOptions(cfg *model.Config, o options) {
	if o.set["provider"] {
		old := cfg.Provider
		if old == "" {
			old = "azure"
		}
		if old != o.provider {
			cfg.Endpoint, cfg.Deployment, cfg.Credential, cfg.CredentialSet, cfg.AcquireToken = "", "", "", false, nil
			cfg.AuthMode = "api_key"
			switch o.provider {
			case "openai":
				cfg.Endpoint = "https://api.openai.com/v1/"
			case "claude":
				cfg.Endpoint = "https://api.anthropic.com/"
			}
		}
		cfg.Provider = o.provider
	}
	if o.set["root"] {
		cfg.Root = o.root
	}
	if o.set["endpoint"] {
		cfg.Endpoint = o.endpoint
	}
	if o.set["deployment"] {
		cfg.Deployment = o.deployment
	}
	if o.set["auth-mode"] {
		cfg.AuthMode = o.authMode
	}
}

func waitForRun(s *engine.Service, stderr io.Writer) {
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	seen := map[string]bool{}
	flush := func() {
		for _, log := range s.Snapshot().Logs {
			key := log.Time + "\x00" + log.Message
			if !seen[key] {
				seen[key] = true
				fmt.Fprintf(stderr, "[%s] %s\n", log.Level, log.Message)
			}
		}
	}
	for {
		select {
		case <-done:
			flush()
			return
		case <-ticker.C:
			flush()
		}
	}
}

func selectRetryFiles(tasks []model.Task, requested []string, rule string) ([]string, error) {
	wanted := map[string]bool{}
	known := map[string]bool{}
	for _, task := range tasks {
		known[task.File] = true
		if rule != "" {
			for _, applied := range task.RulesApplied {
				if applied == rule {
					wanted[task.File] = true
				}
			}
		}
	}
	for _, file := range requested {
		if !known[file] {
			return nil, fmt.Errorf("キューに存在しないファイル: %s", file)
		}
		wanted[file] = true
	}
	if len(wanted) == 0 {
		return nil, errors.New("再実行の対象がありません。--rule は候補ではなく rulesApplied を照合します")
	}
	files := make([]string, 0, len(wanted))
	for file := range wanted {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printSummary(w io.Writer, state model.State) {
	counts := map[string]int{}
	for _, task := range state.Tasks {
		counts[task.Status]++
	}
	fmt.Fprintf(w, "対象 %d | 完了 %d | 変更不要 %d | 未修正 %d | 失敗 %d | 要確認 %d\n", len(state.Tasks), counts["done"], counts["skipped"], counts["pending"], counts["failed"], counts["needs_human"])
	if state.Config.QueuePath != "" {
		fmt.Fprintln(w, "キュー:", state.Config.QueuePath)
	}
	if state.Worktree != "" {
		fmt.Fprintln(w, "作業コピー:", state.Worktree)
		fmt.Fprintln(w, "ブランチ:", state.Branch)
	}
	if state.Usage.InputTokens+state.Usage.OutputTokens > 0 {
		fmt.Fprintf(w, "使用量: 入力 %d / 出力 %d tokens、推定 $%.6f（未報告の使用量は含みません）\n", state.Usage.InputTokens, state.Usage.OutputTokens, state.Usage.CostUSD)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `OneByOne — 1ファイルずつ独立したAIコンテキストで修正するCLI

使い方: onebyone [--config PATH] <command> [options] [--workspace ID] [--json]

  workspace  ワークスペースの作成・一覧・選択・変更・削除
  llm        LLM接続の登録・選択・テスト・OAuthサインイン
  rules      ルールの追加・編集・削除、.oborulesの取り込み・書き出し
  settings   実行上限・料金・絞り込み・検証コマンドの表示と変更
  files      対象フォルダまたは実行コピーのファイル一覧・内容
  scan       対象と候補ルールを抽出（選択・履歴を保持）
  selection  --file PATH / --all / --none で処理対象を確定
  run        選択した未修正・失敗を処理（--limit 20 でパイロット）
  status     現在の進捗
  runs       実行履歴の一覧・実行別の結果
  detail     --file PATH [--run ID] で差分・ルール別の対応状況
  retry      --file PATH / --rule R019 で再試行に追加
  discard    --file PATH --yes で採用済み変更を破棄
  report     [--run ID] --output PATH に結果JSONを出力
  publish    preview / diff / create で結果を確認・新規ブランチへ1コミットで反映
  demo       [--directory PATH] [--name NAME] でプロジェクト・ルールを作成
  doctor     同梱Gitの診断

設定: --config > ONEBYONE_CONFIG > アプリのローカル設定フォルダ
キュー・結果はワークスペースごとに自動管理。Git・rgは同梱版を使用。
APIキーは llm save --input - の標準入力、またはプロバイダーの環境変数で指定。
終了コード: 0=成功、1=エラー、2=今回の対象に失敗/要確認あり、130=中止
詳細: onebyone <command> --help / docs/cli.md`)
}

func printGroupHelp(w io.Writer, group string) {
	help := map[string]string{
		"workspace": "list | create --name NAME --root PATH | select --id ID | rename --name NAME | duplicate --name NAME | delete --id ID --yes | target --root PATH | validate --root PATH",
		"llm":       "list | save --input JSON_FILE|- | select --id ID | test --id ID | login --id ID | logout --id ID | clear --id ID --yes | delete --id ID --yes",
		"rules":     "list | show --id ID | create --input JSON_FILE|- | update --input JSON_FILE|- | delete --id ID --yes | import --input FILE.oborules [--mode replace|merge] | export --output FILE.oborules",
		"settings":  "show | update --input JSON_FILE|-（指定した項目だけ変更。0で上限解除）",
		"files":     "list | show --file PATH [--source target|execution]",
		"runs":      "list | show --id EXECUTION_ID",
		"publish":   "preview | diff --input JSON_FILE|-（workspaceId・revision・file必須） | create --input JSON_FILE|-（workspaceId・revision・branch・title必須）",
	}
	fmt.Fprintf(w, "使い方: onebyone %s <%s> [--config PATH] [--workspace ID] [--json]\n", group, help[group])
}
