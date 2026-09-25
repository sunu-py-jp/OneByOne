package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"onebyone/internal/demopreset"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

// CreateDemoProject creates an independent, committed example in a new child
// directory. It does not create a workspace or change the current selection.
func (s *Service) CreateDemoProject(parent string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return "", err
	}
	if strings.TrimSpace(parent) == "" {
		return "", fmt.Errorf("デモ保存先のディレクトリを選択してください")
	}
	parent, err := settingsRoot(parent)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if installation := CheckGitInstallation(ctx); !installation.Available {
		return "", fmt.Errorf("%s", installation.Message)
	}
	files, err := demopreset.ProjectFiles()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("このアプリのセッションは終了しています")
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
	// Never add a nested repository or untracked example files to user source.
	for current := parent; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return "", fmt.Errorf("既存のGitリポジトリの外にデモ保存先を選択してください")
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	if _, err := readSourceGit(ctx, parent, "rev-parse", "--absolute-git-dir"); err == nil {
		return "", fmt.Errorf("既存のGitリポジトリの外にデモ保存先を選択してください")
	} else if ctx.Err() != nil {
		return "", err
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return "", err
	}
	defer parentRoot.Close()
	name, root := "", ""
	for attempt := 0; attempt < 16; attempt++ {
		name = demopreset.ProjectName + "-" + uid()
		root = filepath.Join(parent, name)
		if err := s.validateDemoLocation(root); err != nil {
			return "", err
		}
		err = parentRoot.Mkdir(name, 0755)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	if err != nil {
		return "", fmt.Errorf("デモ用の新しいフォルダを作成できませんでした: %w", err)
	}
	created, err := parentRoot.Lstat(name)
	if err != nil || !created.IsDir() || created.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("デモ作成先のフォルダが変更されました")
	}
	completed := false
	defer func() {
		if !completed {
			// Remove only the directory created by this call, anchored to its
			// original parent. Earlier examples and replaced paths remain intact.
			if current, e := parentRoot.Lstat(name); e == nil && os.SameFile(created, current) {
				_ = parentRoot.RemoveAll(name)
			}
		}
	}()
	projectRoot, err := parentRoot.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer projectRoot.Close()
	verifyDirectory := func() error {
		opened, openErr := projectRoot.Stat(".")
		current, statErr := os.Lstat(root)
		if openErr != nil || statErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(created, opened) || !os.SameFile(created, current) {
			return fmt.Errorf("デモ作成先のフォルダが変更されました")
		}
		return nil
	}
	if err = verifyDirectory(); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		local := filepath.FromSlash(path)
		if !filepath.IsLocal(local) || local == "." || strings.Contains(path, "\\") {
			return "", fmt.Errorf("同梱デモのファイルパスが不正です")
		}
		for _, part := range strings.Split(path, "/") {
			if strings.EqualFold(part, ".git") {
				return "", fmt.Errorf("同梱デモにGit管理情報は含められません")
			}
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		local := filepath.FromSlash(path)
		if err = projectRoot.MkdirAll(filepath.Dir(local), 0755); err != nil {
			return "", err
		}
		file, err := projectRoot.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(files[path])
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	// Empty templates prevent inherited hooks or metadata. Force-add is scoped
	// to this new directory so user-global ignore patterns cannot omit a preset.
	for _, args := range [][]string{
		{"init", "--quiet", "--template=", "--initial-branch=main"},
		{"add", "--force", "--", "."},
		{"commit", "--quiet", "-m", "Initial OneByOne demo"},
	} {
		if err = verifyDirectory(); err != nil {
			return "", err
		}
		if _, err = git(ctx, root, args...); err != nil {
			return "", fmt.Errorf("デモのGitリポジトリを作成できません: %w", err)
		}
	}
	if err = verifyDirectory(); err != nil {
		return "", err
	}
	if _, _, err = readyGitSource(ctx, root); err != nil {
		return "", err
	}
	completed = true
	return root, nil
}

func (s *Service) validateDemoLocation(root string) error {
	if err := s.validateTargetLocation(root); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if targetOverlaps(root, s.state.Config.Root) {
		return fmt.Errorf("既存の対象フォルダの外にデモ保存先を選択してください")
	}
	for _, w := range s.workspaces {
		if targetOverlaps(root, w.Root) {
			return fmt.Errorf("既存の対象フォルダの外にデモ保存先を選択してください")
		}
	}
	return nil
}

// ImportDemoRules uses the same validated, atomic install as a rule archive.
// Source selection, queue history and the selected LLM connection are retained.
func (s *Service) ImportDemoRules() (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	cfg := s.state.Config
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: cfg.Root}
	for _, candidate := range s.workspaces {
		if candidate.ID == w.ID {
			w.Name = candidate.Name
			break
		}
	}
	s.mu.Unlock()
	if w.ID == "" || cfg.Root == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	pkg, err := demopreset.Rules()
	if err != nil {
		return s.Snapshot(), err
	}
	cfg = pkg.Settings.Apply(cfg)
	cfg.RulePackageName = demopreset.ProjectName + ".oborules"
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	state, err := s.installRulePackage(w, cfg, pkg)
	if err == nil {
		s.releaseRuleLease()
	}
	return state, err
}

// CreateDemoWorkspace prepares rules and settings in an unpublished, exclusively
// allocated workspace. Only a complete workspace becomes the active selection.
func (s *Service) CreateDemoWorkspace(name, root string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	root, err := s.ValidateTargetFolder(root)
	if err != nil {
		return s.Snapshot(), err
	}
	name, err = cleanWorkspaceName(name)
	if err != nil {
		return s.Snapshot(), err
	}
	pkg, err := demopreset.Rules()
	if err != nil {
		return s.Snapshot(), err
	}
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return s.Snapshot(), err
	}
	w := model.Workspace{ID: uid(), Name: name, Root: root}
	setting, err := s.workspacePath(w.ID, "setting.json")
	if err != nil {
		return s.Snapshot(), err
	}
	base := filepath.Dir(filepath.Dir(setting))
	if err = ensureSharedOutputDirectory(base, 0700); err != nil {
		return s.Snapshot(), err
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return s.Snapshot(), err
	}
	defer parent.Close()
	if err = parent.Mkdir(w.ID, 0700); err != nil {
		return s.Snapshot(), err
	}
	owned, err := parent.Lstat(w.ID)
	if err != nil || !owned.IsDir() || owned.Mode()&os.ModeSymlink != 0 {
		return s.Snapshot(), fmt.Errorf("デモのワークスペース保存先が変更されました")
	}
	var lease *store.WorkspaceLease
	committed := false
	defer func() {
		if !committed {
			if lease != nil {
				_ = lease.Release()
			}
			if current, e := parent.Lstat(w.ID); e == nil && os.SameFile(owned, current) {
				_ = parent.RemoveAll(w.ID)
			}
		}
	}()
	var owner *store.WorkspaceOwner
	lease, owner, _, err = s.obtainWorkspaceLease(w)
	if err != nil {
		return s.Snapshot(), err
	}
	if owner != nil {
		return s.Snapshot(), workspaceInUse(owner)
	}
	cfg := pkg.Settings.Apply(DefaultConfig())
	cfg.Root, cfg.QueuePath = root, filepath.Join(filepath.Dir(setting), "runs", "queue.jsonl")
	cfg.RulePackageName = demopreset.ProjectName + ".oborules"
	cfg, err = normalizeConfig(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	// Reuse the normal package installation and catalog validation in a private
	// staging state. Its settings are invisible to the active service until the
	// active-workspace pointer is committed; failures remove only this new ID.
	staging := &Service{configPath: s.configPath, personal: s.personal, state: emptyState(cfg), meta: manifest{Version: 1}}
	staging.state.ActiveWorkspaceID = w.ID
	staging.workspaces = []workspaceRecord{{Workspace: w, QueuePath: cfg.QueuePath}}
	if _, err = staging.installRulePackage(w, cfg, pkg); err != nil {
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
	committed = true
	s.mu.Lock()
	s.workspaces = items
	s.state = staging.state
	s.cat = staging.cat
	s.meta, s.cancel, s.done = manifest{Version: 1}, nil, nil
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// LoadDemo keeps the CLI convenience flow, using the same bundled project and
// rule import as the desktop actions. It only scans; no LLM request is made.
func (s *Service) LoadDemo() (model.State, error) {
	root, err := s.CreateDemoProject(os.TempDir())
	if err != nil {
		return s.Snapshot(), err
	}
	if _, err = s.CreateDemoWorkspace("デモ", root); err != nil {
		return s.Snapshot(), err
	}
	return s.Scan()
}
