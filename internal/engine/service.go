package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/azureauth"
	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
	"onebyone/internal/store"
)

type manifest struct {
	RulePackagePath string       `json:"rulePackagePath,omitempty"`
	Version         int          `json:"version"`
	Root            string       `json:"root"`
	RepoRoot        string       `json:"repoRoot"`
	SourceRelative  string       `json:"sourceRelative"`
	BaseCommit      string       `json:"baseCommit"`
	Worktree        string       `json:"worktree"`
	Branch          string       `json:"branch"`
	RuleHash        string       `json:"ruleHash"`
	Config          model.Config `json:"config"`
	Scanned         int          `json:"scanned"`
	Excluded        int          `json:"excluded"`
}

type Service struct {
	ruleLease            *store.WorkspaceLease
	ruleLeaseWorkspaceID string
	ruleLeaseID          string
	mu                   sync.Mutex
	executionMu          sync.Mutex
	activeExecution      *executionRecord
	op                   sync.Mutex
	state                model.State
	meta                 manifest
	cat                  *catalog.Catalog
	configPath           string
	cancel               context.CancelFunc
	done                 chan struct{}
	propose              func(context.Context, agent.Input) (model.Proposal, error)
	workspaces           []workspaceRecord
	workspaceIssues      map[string]map[string]model.WorkspaceIssue
	personal             *privateconfig.Store
	workspaceLease       *store.WorkspaceLease
	leaseID              string
	leaseRoot            string
	closed               bool
	oauthCancel          context.CancelFunc
	oauthTokenMu         sync.Mutex
	oauthSignIn          func(context.Context, azureauth.Settings) (azureauth.Session, error)
	oauthAcquire         func(context.Context, azureauth.Settings, azureauth.Session) (string, azureauth.Session, error)
}

func DefaultConfig() model.Config {
	return model.Config{Provider: "azure", AuthMode: "api_key", IncludeGlobs: []string{}, ExcludeGlobs: []string{}, CheckCommands: []model.Command{}}
}

func New(configPath string) *Service {
	s := &Service{configPath: configPath, propose: agent.Run, state: emptyState(DefaultConfig())}
	dir, err := personalDirectory()
	if err == nil {
		s.personal, err = privateconfig.Open(dir)
	}
	if err != nil {
		s.state.LastError = "個人用LLM設定の保存先を開けません: " + err.Error()
		return s
	}
	if e := s.refreshConnections(); e != nil {
		s.state.LastError = e.Error()
		return s
	}
	if e := s.initializeWorkspaces(); e != nil {
		s.state.LastError = e.Error()
	}
	return s
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func uid() string { b := make([]byte, 12); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func safeConfig(c model.Config) model.Config {
	c.AcquireToken = nil
	if c.AuthMode == "oauth" {
		c.Credential = ""
		return c
	}
	env := "AZURE_OPENAI_API_KEY"
	switch c.Provider {
	case "openai":
		env = "OPENAI_API_KEY"
	case "claude":
		env = "ANTHROPIC_API_KEY"
	default:
		if c.AuthMode == "bearer" {
			env = "AZURE_OPENAI_AUTH_TOKEN"
		}
	}
	c.CredentialSet = c.Credential != "" || os.Getenv(env) != ""
	c.Credential = ""
	return c
}

func (s *Service) Snapshot() model.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.state
	x.Tasks = copyTasks(s.state.Tasks)
	for i := range x.Tasks {
		x.Tasks[i].CanDiscardChanges = canDiscardChanges(x.Tasks[i])
	}
	x.Config = safeConfig(x.Config)
	if x.SelectedLLMConnectionID == "" {
		x.Config.CredentialSet = false
	}
	x.LLMConnections = append([]model.LLMConnection(nil), x.LLMConnections...)
	for i := range x.LLMConnections {
		c := &x.LLMConnections[i]
		if c.AuthMode == "oauth" {
			c.CredentialSet = c.OAuthSignedIn
		} else {
			c.CredentialSet = safeConfig(model.Config{Provider: c.Provider, AuthMode: c.AuthMode, Credential: c.Credential}).CredentialSet
		}
		c.Credential = ""
	}
	data, _ := json.Marshal(x)
	var out model.State
	_ = json.Unmarshal(data, &out)
	if out.Tasks == nil {
		out.Tasks = []model.Task{}
	}
	if out.Rules == nil {
		out.Rules = []model.Rule{}
	}
	if out.Logs == nil {
		out.Logs = []model.LogEntry{}
	}
	if out.LLMConnections == nil {
		out.LLMConnections = []model.LLMConnection{}
	}
	if out.Workspaces == nil {
		out.Workspaces = []model.Workspace{}
	}
	if out.ExecutionRuns == nil {
		out.ExecutionRuns = []model.ExecutionRun{}
	}
	return out
}

func (s *Service) log(level, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg = s.redactLocked(msg)
	s.state.Logs = append(s.state.Logs, model.LogEntry{Time: now(), Level: level, Message: msg})
	if len(s.state.Logs) > 300 {
		s.state.Logs = s.state.Logs[len(s.state.Logs)-300:]
	}
}

func (s *Service) idle() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("このアプリのセッションは終了しています")
	}
	if s.state.Running {
		s.mu.Unlock()
		return fmt.Errorf("処理中です。停止後に操作してください")
	}
	s.mu.Unlock()
	return s.flushFinishedExecution()
}

func normalizeConfig(c model.Config) (model.Config, error) {
	if c.RulePackageName != "" {
		c.RulePackageName = filepath.Base(strings.ReplaceAll(c.RulePackageName, "\\", "/"))
		if len(c.RulePackageName) > 255 || strings.ContainsAny(c.RulePackageName, "\r\n\x00") {
			return c, fmt.Errorf("ルールパッケージ名が不正です")
		}
	}
	if c.Provider == "" {
		c.Provider = "azure"
	}
	if c.AuthMode == "" {
		c.AuthMode = "api_key"
	}
	if c.Provider != "azure" && c.Provider != "openai" && c.Provider != "claude" {
		return c, fmt.Errorf("LLMプロバイダーが不正です")
	}
	for _, p := range []*string{&c.Root, &c.RulesPath, &c.LegacyPath, &c.QueuePath} {
		if *p != "" {
			a, e := filepath.Abs(*p)
			if e != nil {
				return c, e
			}
			*p = canonicalAlias(a)
		}
	}
	if c.Root != "" && c.QueuePath != "" {
		if isAtOrWithin(c.Root, c.QueuePath) {
			return c, fmt.Errorf("キューの保存先は対象フォルダの外に指定してください（作業データをソースに混ぜないため）")
		}
	}
	if c.MaxAttempts < 0 || c.MaxAttempts > 3 {
		return c, fmt.Errorf("候補の検証回数は未設定（無制限）、または1〜3です")
	}
	if c.MaxTurns < 0 || c.MaxTurns > 32 || (c.MaxOutputTokens != 0 && c.MaxOutputTokens < 256) || c.MaxOutputTokens > 32768 {
		return c, fmt.Errorf("ターン数は未設定（無制限）、または1〜32です。最大出力トークンは未設定、または256〜32768です")
	}
	if (c.MaxFileBytes != 0 && c.MaxFileBytes < 1024) || c.MaxFileBytes > 1<<20 {
		return c, fmt.Errorf("ファイル上限は未設定、または1KB〜1MBです")
	}
	if (c.TimeoutSeconds != 0 && c.TimeoutSeconds < 10) || c.TimeoutSeconds > 3600 {
		return c, fmt.Errorf("タイムアウトは未設定（無制限）、または10〜3600秒です")
	}
	if c.MaxCostUSD < 0 || c.InputPricePerMillion < 0 || c.CachedInputPricePerMillion < 0 || c.OutputPricePerMillion < 0 {
		return c, fmt.Errorf("費用・単価は0以上です")
	}
	if c.MaxCostUSD > 0 && (c.InputPricePerMillion <= 0 || c.OutputPricePerMillion <= 0) {
		return c, fmt.Errorf("費用上限を使う場合は入力・出力単価を指定してください")
	}
	if c.AuthMode != "api_key" && c.AuthMode != "bearer" && !(c.Provider == "azure" && c.AuthMode == "oauth") {
		return c, fmt.Errorf("認証方式が不正です")
	}
	for _, cmd := range c.CheckCommands {
		if strings.TrimSpace(cmd.Executable) == "" {
			return c, fmt.Errorf("検証コマンドの実行ファイルが空です")
		}
	}
	if c.IncludeGlobs == nil {
		c.IncludeGlobs = []string{}
	}
	if c.ExcludeGlobs == nil {
		c.ExcludeGlobs = []string{}
	}
	if c.CheckCommands == nil {
		c.CheckCommands = []model.Command{}
	}
	return c, nil
}

func (s *Service) SaveConfig(c model.Config) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if e := s.editable(); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	old := s.state.Config
	workspaceID := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	// Workspace settings cannot change a personal named connection. Resolve the
	// latest selection independently, including changes made by another app.
	if e := s.refreshConnections(); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	connection := s.state.Config
	s.mu.Unlock()
	c.Provider, c.Endpoint, c.Deployment, c.AuthMode = connection.Provider, connection.Endpoint, connection.Deployment, connection.AuthMode
	c.Credential, c.CredentialSet = connection.Credential, connection.CredentialSet
	c.AcquireToken = nil
	c.LLMConnectionID = connection.LLMConnectionID
	if c.Root != "" {
		root, err := settingsRoot(c.Root)
		if err != nil {
			return s.Snapshot(), err
		}
		c.Root = root
	}
	if old.Root != "" && !sameRoot(c.Root, old.Root) {
		return s.Snapshot(), fmt.Errorf("対象フォルダの変更操作から変更してください")
	}
	// The workspace owns its queue, history, and worktree location. Ordinary
	// settings saves must not redirect them or detach an existing session.
	// Initial standalone configuration still supports a caller-specified path.
	if workspaceID != "" {
		c.QueuePath = old.QueuePath
	} else if c.Root != old.Root && c.QueuePath == old.QueuePath {
		c.QueuePath = ""
	}
	c, e := normalizeConfig(c)
	if e != nil {
		return s.Snapshot(), e
	}
	if e = s.saveWorkspaceConfig(&c); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	s.state.Config = c
	if c.Root != old.Root || c.QueuePath != old.QueuePath {
		s.state.ExecutionRuns = []model.ExecutionRun{}
		s.state.Tasks = []model.Task{}
		s.state.Rules = []model.Rule{}
		s.state.Worktree = ""
		s.state.Branch = ""
		s.state.Usage = model.Usage{}
		s.state.ScannedCount = 0
		s.state.ExcludedCount = 0
		s.state.CurrentFile = ""
		s.state.LastError = ""
		s.state.Logs = []model.LogEntry{}
		s.meta = manifest{Version: 1}
		s.cat = nil
	}
	s.mu.Unlock()
	s.refreshActiveWorkspaceDiagnostics()
	return s.Snapshot(), nil
}

func (s *Service) load(path string) error {
	tasks, e := store.LoadQueue(path)
	if e != nil {
		return e
	}
	m := manifest{Version: 1}
	if b, err := store.ReadFile(path + ".session.json"); err == nil {
		if e = json.Unmarshal(b, &m); e != nil {
			return fmt.Errorf("セッション情報: %w", e)
		}
	}
	c := s.state.Config
	if m.Root != "" {
		connection := c
		if connection.Root != "" && !sameRoot(connection.Root, m.Root) {
			return fmt.Errorf("キューは別の対象フォルダの履歴です")
		}
		c = m.Config
		// Credentials are scoped to the user's current connection, never to an
		// endpoint supplied by an imported session file.
		c.Credential = ""
		{
			c.Provider = connection.Provider
			c.Endpoint = connection.Endpoint
			c.Deployment = connection.Deployment
			c.AuthMode = connection.AuthMode
			c.Credential = connection.Credential
			c.LLMConnectionID = connection.LLMConnectionID
			c.MaxAttempts = connection.MaxAttempts
			c.MaxTurns = connection.MaxTurns
			c.MaxOutputTokens = connection.MaxOutputTokens
			c.MaxFileBytes = connection.MaxFileBytes
			c.TimeoutSeconds = connection.TimeoutSeconds
			c.MaxCostUSD = connection.MaxCostUSD
			c.InputPricePerMillion = connection.InputPricePerMillion
			c.CachedInputPricePerMillion = connection.CachedInputPricePerMillion
			c.OutputPricePerMillion = connection.OutputPricePerMillion
		}
		c.CredentialSet = c.Credential != "" || (c.AuthMode == "oauth" && connection.CredentialSet)
	}
	c.QueuePath = path
	if c.Root == "" {
		return fmt.Errorf("先に対象フォルダを設定してください")
	}
	for _, t := range tasks {
		if _, e = catalog.PathWithin(c.Root, t.File); e != nil {
			return fmt.Errorf("キュー %s: %w", t.File, e)
		}
	}
	s.mu.Lock()
	s.state.Config = c
	s.state.Tasks = tasks
	s.meta = m
	s.state.Worktree = m.Worktree
	s.state.Branch = m.Branch
	s.state.ScannedCount = m.Scanned
	s.state.ExcludedCount = m.Excluded
	s.recountLocked()
	s.mu.Unlock()
	return s.loadExecutionRuns(path)
}

func (s *Service) LoadQueue(path string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if e := s.editable(); e != nil {
		return s.Snapshot(), e
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	previous := s.state
	previous.Rules = append([]model.Rule(nil), s.state.Rules...)
	previousMeta, previousCatalog := s.meta, s.cat
	s.mu.Unlock()
	e = func() error {
		if err := s.load(abs); err != nil {
			return err
		}
		s.mu.Lock()
		cfg := s.state.Config
		s.mu.Unlock()
		cat, err := catalog.Load(context.Background(), cfg)
		if err != nil {
			return err
		}
		if err = s.saveWorkspaceConfig(&cfg); err != nil {
			return err
		}
		s.mu.Lock()
		s.state.Config = cfg
		s.cat = cat
		s.state.Rules = cat.Rules
		s.recountLocked()
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "queue", nil, "", "")
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", nil, "", "")
		s.publishWorkspacesLocked()
		s.mu.Unlock()
		return nil
	}()
	if e != nil {
		s.mu.Lock()
		s.state, s.meta, s.cat = previous, previousMeta, previousCatalog
		s.mu.Unlock()
	}
	return s.Snapshot(), e
}

func (s *Service) persist() error {
	s.mu.Lock()
	tasks := copyTasks(s.state.Tasks)
	m := s.meta
	m.Config = storedConfig(s.state.Config)
	m.Root = s.state.Config.Root
	m.Worktree = s.state.Worktree
	m.Branch = s.state.Branch
	m.Scanned = s.state.ScannedCount
	m.Excluded = s.state.ExcludedCount
	cfg := s.state.Config
	path := cfg.QueuePath
	s.mu.Unlock()
	if path == "" {
		return fmt.Errorf("キュー保存先が未設定です")
	}
	if e := writeOutputQueue(cfg, tasks); e != nil {
		return e
	}
	if err := writeOutputJSON(cfg, path+".session.json", m); err != nil {
		return err
	}
	return s.saveExecutionSnapshot(false)
}

func (s *Service) Scan() (state model.State, err error) {
	s.op.Lock()
	defer s.op.Unlock()
	if e := s.editable(); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	cfg := s.state.Config
	s.mu.Unlock()
	cfg, e := normalizeConfig(cfg)
	if e != nil {
		return s.Snapshot(), e
	}
	if cfg.Root == "" {
		return s.Snapshot(), fmt.Errorf("対象フォルダを選択してください")
	}
	if cfg.RulesPath == "" {
		return s.Snapshot(), fmt.Errorf("実行設定を開く前にルールを1件以上追加してください")
	}
	root, targetErr := s.ValidateTargetFolder(cfg.Root)
	s.mu.Lock()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "target", targetErr, "target", "")
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	if targetErr != nil {
		return s.Snapshot(), targetErr
	}
	cfg.Root = root
	unlock, e := lockSharedQueue(cfg)
	if e != nil {
		return s.Snapshot(), e
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.state.Config = cfg
	if s.closed {
		s.mu.Unlock()
		return s.Snapshot(), fmt.Errorf("このアプリのセッションは終了しています")
	}
	s.state.Running = true
	s.state.Phase = "scanning"
	s.state.LastError = ""
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.state.Running = false
		s.state.Phase = "idle"
		s.cancel = nil
		s.mu.Unlock()
		state = s.Snapshot()
	}()
	s.log("info", "ルールと正規表現を検査しています")
	cat, e := catalog.Load(ctx, cfg)
	if e == nil && len(cat.Rules) == 0 {
		e = fmt.Errorf("実行設定を開く前にルールを1件以上追加してください")
	}
	if e != nil {
		s.mu.Lock()
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", e, "rules", "")
		s.publishWorkspacesLocked()
		s.mu.Unlock()
		return s.Snapshot(), e
	}
	s.mu.Lock()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", nil, "", "")
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	tasks, scanned, excluded, e := cat.Scan(ctx, cfg)
	if e != nil {
		return s.Snapshot(), e
	}
	old := map[string]model.Task{}
	if previous, err := store.LoadQueue(cfg.QueuePath); err == nil {
		if data, readErr := store.ReadFile(cfg.QueuePath + ".session.json"); readErr == nil {
			var previousSession manifest
			if err := json.Unmarshal(data, &previousSession); err != nil {
				return s.Snapshot(), fmt.Errorf("既存セッション情報を確認できません: %w", err)
			}
			if previousSession.Root != "" && filepath.Clean(previousSession.Root) != filepath.Clean(cfg.Root) {
				return s.Snapshot(), fmt.Errorf("ワークスペースの対象フォルダと保存済みの実行履歴が一致していません")
			}
			s.mu.Lock()
			activeWorktree := s.state.Worktree
			s.mu.Unlock()
			if previousSession.Worktree != "" && (activeWorktree == "" || filepath.Clean(activeWorktree) != filepath.Clean(previousSession.Worktree)) {
				return s.Snapshot(), fmt.Errorf("保存済みの実行履歴と作業コピーを復元するため、ワークスペースを開き直してください")
			}
		} else if !os.IsNotExist(readErr) {
			return s.Snapshot(), readErr
		}
		for _, t := range previous {
			old[t.File] = t
		}
	} else if !os.IsNotExist(err) {
		return s.Snapshot(), fmt.Errorf("既存キューを読み込めないため上書きしません: %w", err)
	}
	s.mu.Lock()
	hasWorktree := s.state.Worktree != ""
	s.mu.Unlock()
	included := map[string]bool{}
	for i, t := range tasks {
		included[t.File] = true
		if p, ok := old[t.File]; ok {
			tasks[i].Excluded = p.Excluded
			if p.InputHash == t.InputHash || hasWorktree || len(p.History) > 0 || p.Status == "done" || p.Status == "skipped" {
				p.Rules = t.Rules
				tasks[i] = p
			}
		}
	}
	{
		for file, previous := range old {
			if included[file] || !hasWorktree && len(previous.History) == 0 && previous.Status != "done" && previous.Status != "skipped" {
				continue
			}
			if previous.Status == "running" {
				return s.Snapshot(), fmt.Errorf("%s は中断した処理の復旧前です。抽出範囲を変更せずに再開してから再抽出してください", file)
			}
			if previous.Status == "pending" || previous.Status == "failed" {
				previous.Status = "needs_human"
				previous.Note = "今回の抽出範囲から除外されました。履歴を保持し、自動実行の対象から外しています"
				previous.UpdatedAt = now()
			}
			previous.Rules = []string{}
			previous.Excluded = true
			tasks = append(tasks, previous)
		}
		sortTasks(tasks)
	}
	s.mu.Lock()
	if s.meta.Root != "" && s.meta.Root != cfg.Root {
		s.meta = manifest{Version: 1}
		s.state.Worktree = ""
		s.state.Branch = ""
	}
	s.cat = cat
	s.meta.Version = 1
	s.meta.RuleHash = cat.Hash
	s.meta.RulePackagePath = ""
	if cfg.RulePackageName != "" {
		s.meta.RulePackagePath = cfg.RulesPath
	}
	s.state.Tasks = tasks
	s.state.Rules = cat.Rules
	s.state.ScannedCount = scanned
	s.state.ExcludedCount = excluded
	s.recountLocked()
	s.mu.Unlock()
	if e = s.persist(); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "queue", nil, "", "")
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	s.log("info", fmt.Sprintf("%dファイルを列挙し、%dファイルをキューに保存しました", scanned, len(tasks)))
	return s.Snapshot(), nil
}

func (s *Service) Start(limit int) error {
	s.op.Lock()
	defer s.op.Unlock()
	if e := s.editable(); e != nil {
		return e
	}
	if limit < 0 {
		return fmt.Errorf("件数は0以上です")
	}
	if err := s.refreshConnections(); err != nil {
		return err
	}
	s.mu.Lock()
	selected := s.state.SelectedLLMConnectionID
	s.mu.Unlock()
	if selected == "" {
		return fmt.Errorf("実行に使用するLLM接続を選択してください")
	}
	s.mu.Lock()
	if len(s.state.Tasks) == 0 {
		s.mu.Unlock()
		return fmt.Errorf("対象ファイルがありません。実行設定でファイルを抽出してください")
	}
	selectedFiles, runnable := 0, false
	for _, task := range s.state.Tasks {
		if !task.Excluded {
			selectedFiles++
			runnable = runnable || task.Status == "pending" || task.Status == "failed" || task.Status == "running"
		}
	}
	if selectedFiles == 0 {
		s.mu.Unlock()
		return fmt.Errorf("実行するファイルが選択されていません。実行設定で選択してください")
	}
	if !runnable {
		s.mu.Unlock()
		return fmt.Errorf("選択したファイルに未処理または再試行可能な対象がありません")
	}
	if s.state.Config.RulePackageName != "" && s.meta.RulePackagePath != s.state.Config.RulesPath {
		s.mu.Unlock()
		return fmt.Errorf("ルールパッケージが変更されています。対象抽出を再実行してください")
	}
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("このアプリのセッションは終了しています")
	}
	s.state.Running = true
	s.state.Phase = "preparing"
	s.state.LastError = ""
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "run", nil, "", "")
	s.publishWorkspacesLocked()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	if err := s.beginExecution(limit); err != nil {
		cancel()
		s.mu.Lock()
		s.state.Running, s.state.Phase, s.cancel = false, "idle", nil
		s.mu.Unlock()
		close(done)
		return err
	}
	go func() {
		defer close(done)
		err := s.run(ctx, limit)
		if err != nil {
			s.log("error", err.Error())
		}
		s.mu.Lock()
		file := s.state.CurrentFile
		source := "run"
		var diagnostic *catalog.DiagnosticError
		if errors.As(err, &diagnostic) {
			source = "catalog"
		}
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, source, err, "results", file)
		s.publishWorkspacesLocked()
		s.state.CurrentFile = ""
		s.state.Phase = "finalizing"
		s.cancel = nil
		if err != nil {
			s.state.LastError = s.redactLocked(err.Error())
		}
		s.mu.Unlock()
		if saveErr := s.finishExecution(ctx, err); saveErr != nil {
			s.log("error", saveErr.Error())
			s.mu.Lock()
			s.state.LastError = s.redactLocked(saveErr.Error())
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.state.Running, s.state.Phase = false, "idle"
		s.mu.Unlock()
	}()
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.log("info", "停止を要求しました。作業中の変更を回収しています")
	}
}
func (s *Service) Wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *Service) RetryTasks(files []string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if e := s.editable(); e != nil {
		return s.Snapshot(), e
	}
	s.mu.Lock()
	cfg := s.state.Config
	s.mu.Unlock()
	unlock, e := lockSharedQueue(cfg)
	if e != nil {
		return s.Snapshot(), e
	}
	defer unlock()
	// Reconcile a process interruption before changing a running journal to pending.
	if s.Snapshot().Worktree != "" {
		if e = s.recover(context.Background()); e != nil {
			return s.Snapshot(), e
		}
	}
	wanted := map[string]bool{}
	for _, f := range files {
		wanted[f] = true
	}
	s.mu.Lock()
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		if wanted[t.File] {
			t.Excluded = false
			t.Status = "pending"
			t.ResumeRequested = true
			t.Note = "再試行に追加しました（計画・履歴を引き継ぎ、次の実行はターン数0から開始します）"
			t.UpdatedAt = now()
			if s.state.Worktree != "" {
				if p, e := catalog.PathWithin(s.sourceDirLocked(), t.File); e == nil {
					if b, e := os.ReadFile(p); e == nil {
						t.InputHash = digest(b)
					}
				}
			}
		}
	}
	s.recountLocked()
	s.mu.Unlock()
	if e = s.persist(); e != nil {
		return s.Snapshot(), e
	}
	return s.Snapshot(), nil
}

func (s *Service) ReadRule(id string) (model.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.state.Rules {
		if r.ID == id {
			return r, nil
		}
	}
	return model.Rule{}, fmt.Errorf("ルールが見つかりません: %s", id)
}

func (s *Service) recountLocked() {
	s.state.Usage = model.Usage{}
	counts := map[string]int{}
	applied := map[string]int{}
	selected := 0
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		t.CanDiscardChanges = canDiscardChanges(*t)
		if !t.Excluded {
			selected++
			for _, id := range t.Rules {
				counts[id]++
			}
		}
		for _, id := range t.RulesApplied {
			applied[id]++
		}
		for _, h := range t.History {
			addUsage(&s.state.Usage, h.Usage)
		}
	}
	for i := range s.state.Rules {
		s.state.Rules[i].CandidateCount = counts[s.state.Rules[i].ID]
		if s.state.Rules[i].Always {
			s.state.Rules[i].CandidateCount = selected
		}
		s.state.Rules[i].AppliedCount = applied[s.state.Rules[i].ID]
	}
	s.updateTaskIssuesLocked(s.state.ActiveWorkspaceID, s.state.Tasks)
	s.publishWorkspacesLocked()
}

func addUsage(dst *model.Usage, u model.Usage) {
	dst.Uncertain = dst.Uncertain || u.Uncertain
	dst.InputTokens += u.InputTokens
	dst.CachedTokens += u.CachedTokens
	dst.OutputTokens += u.OutputTokens
	dst.CostUSD += u.CostUSD
	dst.Turns += u.Turns
}
func (s *Service) sourceDirLocked() string {
	if s.state.Worktree == "" {
		return s.state.Config.Root
	}
	return filepath.Join(s.state.Worktree, s.meta.SourceRelative)
}

func (s *Service) GetFileDetail(file string, index int) (model.FileDetail, error) {
	s.mu.Lock()
	var task model.Task
	found := false
	for _, t := range s.state.Tasks {
		if t.File == file {
			task = copyTask(t)
			found = true
			break
		}
	}
	cfg := s.state.Config
	m := s.meta
	s.mu.Unlock()
	if !found {
		return model.FileDetail{}, fmt.Errorf("キューに存在しないファイルです")
	}
	selectedTasks := []model.Task{task}
	backfillChangeReports(cfg, selectedTasks)
	task = selectedTasks[0]
	d := model.FileDetail{Task: task, Changes: []model.ChangeReportItem{}}
	if index < 0 {
		return cumulativeFileDetail(cfg, m, task)
	}
	if index >= len(task.History) {
		return d, fmt.Errorf("試行が見つかりません")
	}
	h := task.History[index]
	for _, recorded := range h.Changes {
		row := recorded
		row.SourceAttemptID = h.ID
		d.Changes = append(d.Changes, row)
	}
	base := filepath.Join(cfg.QueuePath+".artifacts", h.ID)
	if b, e := os.ReadFile(base + ".before"); e == nil {
		d.Before = string(b)
	}
	if b, e := os.ReadFile(base + ".after"); e == nil {
		d.After = string(b)
	} else {
		d.After = d.Before
	}
	if b, e := os.ReadFile(base + ".diff"); e == nil {
		d.Diff = string(b)
	}
	return d, nil
}

// ExportReport writes to the chosen file. An empty path uses the CLI default.
func (s *Service) ExportReport(path string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return "", err
	}
	st := s.Snapshot()
	backfillChangeReports(st.Config, st.Tasks)
	st.Config = storedConfig(st.Config)
	st.LLMSettingsPath = ""
	st.LLMConnections = nil
	st.SelectedLLMConnectionID = ""
	return s.exportReportValue(path, st, st)
}

func (s *Service) exportReportValue(path string, st model.State, value any) (string, error) {
	if path == "" {
		if st.Config.QueuePath == "" {
			return "", fmt.Errorf("保存先を設定してください")
		}
		path = filepath.Join(filepath.Dir(st.Config.QueuePath), "reports", "report-"+time.Now().Format("20060102-150405")+".json")
	}
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return "", fmt.Errorf("保存先は .json ファイルを指定してください")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	setting, err := s.workspacePath(st.ActiveWorkspaceID, "setting.json")
	if err != nil {
		return "", err
	}
	for _, reserved := range []string{s.configPath, setting, st.Config.QueuePath, st.Config.QueuePath + ".session.json"} {
		if reserved != "" && sameRoot(path, reserved) {
			return "", fmt.Errorf("設定ファイルやキューを出力先に指定できません")
		}
	}
	if isAtOrWithin(st.Config.QueuePath+".executions", path) || isAtOrWithin(st.Config.QueuePath+".artifacts", path) {
		return "", fmt.Errorf("実行履歴を出力先に指定できません")
	}
	if e := writeOutputJSON(st.Config, path, value); e != nil {
		return "", e
	}
	return path, nil
}

func (s *Service) TestConnection() (string, error) {
	s.mu.Lock()
	id := s.state.SelectedLLMConnectionID
	s.mu.Unlock()
	return s.TestLLMConnection(id)
}

func sortTasks(tasks []model.Task) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].File < tasks[j].File })
}
