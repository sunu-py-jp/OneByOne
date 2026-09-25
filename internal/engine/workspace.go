package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

type workspaceRecord struct {
	model.Workspace
	QueuePath string
}

type workspaceSetting struct {
	Version int          `json:"version"`
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Config  model.Config `json:"config"`
}

// Version 2 uses stable editing leases outside the workspace directory.
// Older applications only accept version 1 and must not edit migrated data.
const workspaceSettingVersion = 2

type appSettings struct {
	Version           int    `json:"version"`
	ActiveWorkspaceID string `json:"activeWorkspaceId"`
}

func emptyState(c model.Config) model.State {
	return model.State{LLMConnections: []model.LLMConnection{}, Config: c, Workspaces: []model.Workspace{}, Tasks: []model.Task{}, Rules: []model.Rule{}, Logs: []model.LogEntry{}, Phase: "idle"}
}

// Workspace settings and reports contain no personal LLM credentials.
func storedConfig(c model.Config) model.Config {
	c.AcquireToken = nil
	c.Provider, c.Endpoint, c.Deployment, c.AuthMode = "", "", "", ""
	c.Credential, c.CredentialSet = "", false
	c.LLMConnectionID = ""
	return c
}

func withoutCredential(c model.Config) model.Config {
	c.AcquireToken = nil
	c.Credential, c.CredentialSet = "", false
	return c
}

func cleanWorkspaceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "新しいワークスペース"
	}
	if len([]rune(name)) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("ワークスペース名は改行を含まない80文字以内にしてください")
	}
	return name, nil
}

func canonicalAlias(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" {
		for _, prefix := range []string{"/var", "/tmp"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return "/private" + path
			}
		}
	}
	return path
}

func sameRoot(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	// Treat casing conservatively on the desktop platforms, including macOS
	// volumes that commonly use case-insensitive file names.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(canonicalAlias(a), canonicalAlias(b))
	}
	return canonicalAlias(a) == canonicalAlias(b)
}

func isAtOrWithin(root, path string) bool {
	for path = filepath.Clean(path); ; path = filepath.Dir(path) {
		if sameRoot(root, path) {
			return true
		}
		if path == filepath.Dir(path) {
			return false
		}
	}
}

// settingsRoot resolves standard macOS aliases and rejects symlink targets.
func settingsRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "darwin" {
		for _, prefix := range []string{"/var", "/tmp"} {
			if abs == prefix || strings.HasPrefix(abs, prefix+"/") {
				abs = "/private" + abs
				break
			}
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("対象フォルダを開けません: %w", err)
	}
	if !sameRoot(abs, resolved) {
		return "", fmt.Errorf("対象フォルダにシンボリックリンクは指定できません: %s", abs)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("対象フォルダがありません: %s", abs)
	}
	return abs, nil
}

var workspaceIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

// Workspaces are owned by the app, never by the source folder.
func (s *Service) localDirectory() (string, error) {
	if s.configPath != "" {
		return filepath.Abs(filepath.Dir(s.configPath))
	}
	dir, err := personalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Dir(dir), nil
}

func (s *Service) workspacePath(id, name string) (string, error) {
	return s.localWorkspacePath("workspaces", id, name)
}

// Leases must stay outside the workspace directory. Windows cannot rename a
// directory containing open files, even if their handles share delete access.
// Keeping these paths stable lets deletion retain exclusion through archive and
// rollback without moving, replacing, or temporarily releasing any lock file.
func (s *Service) workspaceLockPath(id, name string) (string, error) {
	return s.localWorkspacePath("workspace-locks", id, name)
}

func (s *Service) localWorkspacePath(directory, id, name string) (string, error) {
	if !workspaceIDPattern.MatchString(id) || !filepath.IsLocal(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("ワークスペースの保存パスが不正です")
	}
	base, err := s.localDirectory()
	if err != nil {
		return "", err
	}
	base = canonicalAlias(base)
	path := filepath.Join(base, directory, id, name)
	// Validate all existing ancestors; only app-owned directories are created later.
	for current := path; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e == nil && (info.Mode()&os.ModeSymlink != 0 || (current != path && !info.IsDir())) {
			return "", fmt.Errorf("ローカル設定の保存先に実体のあるフォルダを指定してください")
		}
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return path, nil
}

func (s *Service) writeWorkspaceSetting(w model.Workspace, c model.Config) error {
	path, err := s.workspacePath(w.ID, "setting.json")
	if err != nil {
		return err
	}
	if !sameRoot(w.Root, c.Root) {
		return fmt.Errorf("対象フォルダが一致しません")
	}
	if err = ensureSharedOutputDirectory(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("設定ファイルが不正です")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	savedConfig := storedConfig(c)
	savedConfig.LLMConnectionID = c.LLMConnectionID
	saved := workspaceSetting{Version: workspaceSettingVersion, ID: w.ID, Name: w.Name, Config: savedConfig}
	return store.WriteJSON(path, saved)
}

func decodeLocalJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("ローカル設定ファイルの形式を確認してください")
	}
	data, err := store.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("ローカル設定を読み込めません: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("ローカル設定にはJSONを1つだけ指定してください")
	}
	return nil
}

func (s *Service) readWorkspaceSetting(id string) (model.Workspace, model.Config, error) {
	c := DefaultConfig()
	path, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return model.Workspace{}, c, err
	}
	saved := workspaceSetting{}
	if err = decodeLocalJSON(path, &saved); err != nil {
		return model.Workspace{}, c, err
	}
	if (saved.Version != 1 && saved.Version != workspaceSettingVersion) || saved.ID != id || saved.Config.Root == "" {
		return model.Workspace{}, c, fmt.Errorf("setting.json の形式を確認してください")
	}
	if saved.Config.Provider != "" || saved.Config.Endpoint != "" || saved.Config.Deployment != "" || saved.Config.AuthMode != "" || saved.Config.Credential != "" || saved.Config.CredentialSet {
		return model.Workspace{}, c, fmt.Errorf("LLM接続はワークスペース設定に含めず、LLM接続メニューで管理してください")
	}
	name, err := cleanWorkspaceName(saved.Name)
	if err != nil {
		return model.Workspace{}, c, err
	}
	saved.Config.Provider, saved.Config.AuthMode = "azure", "api_key"
	c, err = normalizeConfig(saved.Config)
	return model.Workspace{ID: id, Name: name, Root: c.Root}, c, err
}

func (s *Service) listLocalWorkspaces() ([]workspaceRecord, error) {
	base, err := s.localDirectory()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "workspaces")
	if info, e := os.Lstat(dir); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("workspaces は実体のあるフォルダにしてください")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []workspaceRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := []workspaceRecord{}
	for _, entry := range entries {
		if !workspaceIDPattern.MatchString(entry.Name()) {
			continue
		}
		w, c, e := s.readWorkspaceSetting(entry.Name())
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		items = append(items, workspaceRecord{Workspace: w, QueuePath: c.QueuePath})
	}
	return items, nil
}

func (s *Service) publishWorkspacesLocked() {
	s.state.Workspaces = make([]model.Workspace, len(s.workspaces))
	for i, w := range s.workspaces {
		s.state.Workspaces[i] = w.Workspace
		s.state.Workspaces[i].Issues = s.cachedWorkspaceIssuesLocked(w.ID)
	}
}

func (s *Service) writeActiveWorkspace(id string) error {
	if s.configPath == "" {
		return nil
	}
	path, err := filepath.Abs(s.configPath)
	if err != nil {
		return err
	}
	path = canonicalAlias(path)
	for current := path; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e == nil && (info.Mode()&os.ModeSymlink != 0 || (current == path && !info.Mode().IsRegular()) || (current != path && !info.IsDir())) {
			return fmt.Errorf("アプリ設定の保存先に実体のあるファイルとフォルダを指定してください")
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return store.WriteJSON(s.configPath, appSettings{Version: 1, ActiveWorkspaceID: id})
}

func (s *Service) initializeWorkspaces() error {
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return err
	}
	s.workspaces = items
	s.publishWorkspacesLocked()
	for _, item := range items {
		_, c, readErr := s.readWorkspaceSetting(item.ID)
		if readErr == nil {
			s.inspectWorkspaceDiagnostics(item.ID, c, false)
		}
	}
	active := ""
	if s.configPath != "" {
		var saved appSettings
		if e := decodeLocalJSON(s.configPath, &saved); e == nil {
			if saved.Version != 1 {
				return fmt.Errorf("アプリ設定の形式を確認してください")
			}
			active = saved.ActiveWorkspaceID
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	if active == "" && len(items) > 0 {
		active = items[0].ID
	}
	if active != "" {
		return s.activateWorkspace(active, false)
	}
	return nil
}

func (s *Service) saveWorkspaceConfig(cfg *model.Config) error {
	c := *cfg
	if c.Root == "" {
		return fmt.Errorf("最初に対象フォルダを指定してください")
	}
	s.mu.Lock()
	active := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	// Editing unrelated settings must remain possible after the source becomes
	// dirty. New targets require readiness; existing targets still require safe
	// paths and Git worktree membership, and publish readiness diagnostics.
	root, err := s.validateTargetFolder(c.Root, active == "")
	if err != nil {
		return err
	}
	c.Root = root
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return err
	}
	w := model.Workspace{ID: active, Name: filepath.Base(root), Root: root}
	index := -1
	for i, item := range items {
		if item.ID == active {
			index = i
			w = item.Workspace
		}
	}
	if active == "" {
		w.ID = uid()
		active = w.ID
	}
	if !sameRoot(w.Root, root) {
		return fmt.Errorf("対象フォルダの変更操作から変更してください")
	}
	if c.QueuePath == "" {
		path, e := s.workspacePath(active, "setting.json")
		if e != nil {
			return e
		}
		c.QueuePath = filepath.Join(filepath.Dir(path), "runs", "queue.jsonl")
	}
	if err = validateTargetQueue(c.Root, c.QueuePath); err != nil {
		return err
	}
	if err = validateWorkspaceQueue(items, active, c.QueuePath); err != nil {
		return err
	}
	lease, owner, fresh, err := s.obtainWorkspaceLease(w)
	if err != nil {
		return err
	}
	if owner != nil {
		return workspaceInUse(owner)
	}
	committed := false
	defer func() {
		if fresh && !committed {
			_ = lease.Release()
		}
	}()
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return err
	}
	c, _ = selectedConnection(c, registry)
	if err = s.writeWorkspaceSetting(w, c); err != nil {
		return err
	}
	if err = s.writeActiveWorkspace(active); err != nil {
		return err
	}
	record := workspaceRecord{Workspace: w, QueuePath: c.QueuePath}
	if index < 0 {
		items = append(items, record)
	} else {
		items[index] = record
	}
	s.adoptWorkspaceLease(w, lease)
	committed = true
	s.mu.Lock()
	s.workspaces = items
	s.state.ActiveWorkspaceID = active
	s.state.Config = c
	s.state.ReadOnly, s.state.WorkspaceLock = false, nil
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	*cfg = c
	return nil
}

func (s *Service) activateWorkspace(id string, persist bool) error {
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return err
	}
	w, c, err := s.readWorkspaceSetting(id)
	if err != nil {
		return fmt.Errorf("ワークスペースが見つからないか、設定を読み込めません: %w", err)
	}
	if err = validateWorkspaceQueue(items, id, c.QueuePath); err != nil {
		return err
	}
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return err
	}
	lease, owner, fresh, err := s.obtainWorkspaceLease(w)
	if err != nil {
		return err
	}
	adopted := false
	defer func() {
		if fresh && !adopted && lease != nil {
			_ = lease.Release()
		}
	}()
	if owner == nil && fresh {
		w, c, err = s.readWorkspaceSetting(id)
		if err != nil {
			return err
		}
		if err = validateWorkspaceQueue(items, id, c.QueuePath); err != nil {
			return err
		}
	}
	c, _ = selectedConnection(c, registry)
	if persist {
		if err = s.writeActiveWorkspace(id); err != nil {
			return err
		}
	}
	s.adoptWorkspaceLease(w, lease)
	adopted = true
	s.mu.Lock()
	s.workspaces = items
	s.state = emptyState(c)
	s.state.ActiveWorkspaceID = id
	s.state.ReadOnly = owner != nil
	s.state.WorkspaceLock = ownerModel(owner)
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
	s.meta = manifest{Version: 1}
	s.cat = nil
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if err = s.restoreWorkspace(c); err != nil {
		s.mu.Lock()
		s.state.LastError = s.redactLocked(err.Error())
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) CreateWorkspace(name, root string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	if strings.TrimSpace(root) == "" {
		return s.Snapshot(), fmt.Errorf("最初に対象フォルダを指定してください")
	}
	name, err := cleanWorkspaceName(name)
	if err != nil {
		return s.Snapshot(), err
	}
	c := DefaultConfig()
	c.Root = root
	return s.createWorkspace(name, c, "")
}

func (s *Service) createWorkspace(name string, c model.Config, connectionID string) (model.State, error) {
	root, err := s.ValidateTargetFolder(c.Root)
	if err != nil {
		return s.Snapshot(), err
	}
	c.Root = root
	w := model.Workspace{ID: uid(), Name: name, Root: c.Root}
	path, err := s.workspacePath(w.ID, "setting.json")
	if err != nil {
		return s.Snapshot(), err
	}
	c.QueuePath = filepath.Join(filepath.Dir(path), "runs", "queue.jsonl")
	lease, owner, _, err := s.obtainWorkspaceLease(w)
	if err != nil {
		return s.Snapshot(), err
	}
	if owner != nil {
		return s.Snapshot(), workspaceInUse(owner)
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = lease.Release()
		}
	}()
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return s.Snapshot(), err
	}
	if connectionID != "" {
		found := false
		for _, connection := range registry.Connections {
			if connection.ID == connectionID {
				found = true
				break
			}
		}
		if !found {
			return s.Snapshot(), fmt.Errorf("複製元のLLM接続が見つかりません")
		}
	}
	c.LLMConnectionID = connectionID
	c, _ = selectedConnection(c, registry)
	// The editing lease lives outside this directory. Creation must explicitly
	// prepare its own destination before copying any package into the workspace.
	if err = ensureSharedOutputDirectory(filepath.Dir(path), 0700); err != nil {
		return s.Snapshot(), err
	}
	if c.RulesPath != "" {
		c, err = s.copyWorkspaceRules(c, w.ID)
		if err != nil {
			return s.Snapshot(), err
		}
	}
	if err = s.writeWorkspaceSetting(w, c); err != nil {
		return s.Snapshot(), err
	}
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return s.Snapshot(), err
	}
	if err = s.writeActiveWorkspace(w.ID); err != nil {
		return s.Snapshot(), err
	}
	s.adoptWorkspaceLease(w, lease)
	adopted = true
	s.mu.Lock()
	s.workspaces = items
	s.state = emptyState(c)
	s.state.ActiveWorkspaceID = w.ID
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
	s.meta = manifest{Version: 1}
	s.cat = nil
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if e := s.restoreWorkspace(c); e != nil {
		s.mu.Lock()
		s.state.LastError = s.redactLocked(e.Error())
		s.mu.Unlock()
	}
	return s.Snapshot(), nil
}

func (s *Service) SelectWorkspace(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	err := s.activateWorkspace(id, true)
	return s.Snapshot(), err
}

func (s *Service) restoreWorkspace(c model.Config) error {
	_, targetErr := s.ValidateTargetFolder(c.Root)
	queueErr := s.restoreWorkspaceQueue(c)
	if queueErr == nil {
		queueErr = s.restorePendingDiscards()
	}
	cat, catalogErr := passiveCatalog(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "target", targetErr, "target", "")
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "queue", queueErr, "results", "")
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", catalogErr, "rules", "")
	if cat != nil {
		s.cat = cat
		s.state.Rules = cat.Rules
	}
	s.recountLocked()
	s.publishWorkspacesLocked()
	if targetErr != nil {
		return targetErr
	}
	if queueErr != nil {
		return queueErr
	}
	return catalogErr
}

func (s *Service) restoreWorkspaceQueue(c model.Config) error {
	if c.QueuePath != "" {
		if _, err := os.Stat(c.QueuePath); err == nil {
			var m manifest
			if b, readErr := store.ReadFile(c.QueuePath + ".session.json"); readErr == nil {
				if err = json.Unmarshal(b, &m); err != nil {
					return &workspaceSessionError{err: err}
				}
				if m.Root != "" && !sameRoot(m.Root, c.Root) {
					return fmt.Errorf("キューは別の対象フォルダの履歴です。対象の設定を確認してください")
				}
			} else if !os.IsNotExist(readErr) {
				return &workspaceSessionError{err: readErr}
			}
			if err = s.load(c.QueuePath); err != nil {
				return err
			}
			// Session metadata restores results, while edited workspace settings
			// retain precedence over the configuration from a previous run.
			s.mu.Lock()
			s.state.Config = c
			s.mu.Unlock()
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Service) DuplicateWorkspace(name string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	c := s.state.Config
	connectionID := s.state.SelectedLLMConnectionID
	active := s.state.ActiveWorkspaceID
	original := ""
	for _, w := range s.workspaces {
		if w.ID == active {
			original = w.Name
		}
	}
	s.mu.Unlock()
	if original == "" || c.Root == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	if strings.TrimSpace(name) == "" {
		name = original + " のコピー"
	}
	name, err := cleanWorkspaceName(name)
	if err != nil {
		return s.Snapshot(), err
	}
	return s.createWorkspace(name, c, connectionID)
}

func (s *Service) RenameWorkspace(name string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	name, err := cleanWorkspaceName(name)
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	items := append([]workspaceRecord(nil), s.workspaces...)
	active, c := s.state.ActiveWorkspaceID, s.state.Config
	s.mu.Unlock()
	found := false
	for i := range items {
		if items[i].ID == active {
			found = true
			items[i].Name = name
			if err = s.writeWorkspaceSetting(items[i].Workspace, c); err != nil {
				return s.Snapshot(), err
			}
		}
	}
	if !found {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを作成してください")
	}
	if err = s.writeActiveWorkspace(active); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.workspaces = items
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}
