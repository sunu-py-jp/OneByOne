package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

// ImportRulePackage validates an owned local copy before one atomic setting.json
// replacement publishes it. No configured program, check, source scan, or LLM
// call is run while importing a package.
func (s *Service) ImportRulePackage(path, mode string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if mode != "replace" && mode != "merge" {
		return s.Snapshot(), fmt.Errorf("ルールの取り込み方法は上書きまたはマージを指定してください")
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
	if !strings.EqualFold(filepath.Ext(path), ".oborules") {
		return s.Snapshot(), fmt.Errorf(".oborules ファイルを選択してください")
	}
	pkg, err := rulepack.Read(path)
	if err != nil {
		return s.Snapshot(), err
	}
	existingIDs, err := storedRuleIDs(cfg.RulesPath)
	if err != nil {
		return s.Snapshot(), err
	}
	incomingIDs := packageRuleIDs(pkg)
	renamed := map[string]string{}
	if mode == "merge" {
		renamed = mergedRuleIDs(existingIDs, incomingIDs)
	}
	lockIDs := append([]string{}, existingIDs...)
	for _, id := range incomingIDs {
		if replacement, found := renamed[id]; found {
			id = replacement
		}
		lockIDs = append(lockIDs, id)
	}
	release, err := s.lockImportedRules(w.ID, lockIDs)
	if err != nil {
		return s.Snapshot(), err
	}
	defer release()
	if mode == "merge" {
		current := &rulepack.Package{Settings: rulepack.FromConfig(cfg), Files: map[string][]byte{}}
		if cfg.RulesPath != "" {
			var snapshotErr error
			current, snapshotErr = rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
			if snapshotErr != nil {
				return s.Snapshot(), snapshotErr
			}
		}
		pkg = mergeRulePackages(current, pkg, renamed)
		if cfg.RulePackageName == "" {
			cfg.RulePackageName = "rules.oborules"
		}
	} else {
		cfg = pkg.Settings.Apply(cfg)
		cfg.RulePackageName = filepath.Base(path)
	}
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

func packageRuleIDs(pkg *rulepack.Package) []string {
	seen := map[string]bool{}
	for path := range pkg.Files {
		parts := strings.Split(path, "/")
		if len(parts) >= 3 && parts[0] == "rules" {
			seen[parts[1]] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Reading names alone allows a valid package to replace a broken rule.json.
func storedRuleIDs(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && editableRuleID.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

// Reserve all incoming original IDs before allocating suffixes. R019 and
// R019_2 in one import must not steal each other's names on case-insensitive
// Windows or macOS filesystems. Titles are separate from directory-based IDs.
func mergedRuleIDs(existing, incoming []string) map[string]string {
	used, occupied := map[string]bool{}, map[string]bool{}
	for _, id := range existing {
		used[strings.ToLower(id)], occupied[strings.ToLower(id)] = true, true
	}
	for _, id := range incoming {
		used[strings.ToLower(id)] = true
	}
	result := map[string]string{}
	for _, id := range incoming {
		if !occupied[strings.ToLower(id)] {
			result[id] = id
			continue
		}
		for number := 2; ; number++ {
			suffix := "_" + strconv.Itoa(number)
			base := id
			if len(base)+len(suffix) > 64 {
				base = base[:64-len(suffix)]
			}
			candidate := base + suffix
			if !used[strings.ToLower(candidate)] {
				result[id], used[strings.ToLower(candidate)] = candidate, true
				break
			}
		}
	}
	return result
}

func mergeRulePackages(current, incoming *rulepack.Package, renamed map[string]string) *rulepack.Package {
	merged := &rulepack.Package{Settings: current.Settings, Files: map[string][]byte{}}
	for name, data := range current.Files {
		merged.Files[name] = data
	}
	for name, data := range incoming.Files {
		parts := strings.Split(name, "/")
		if len(parts) >= 3 && parts[0] == "rules" {
			parts[1] = renamed[parts[1]]
			merged.Files[strings.Join(parts, "/")] = data
		}
	}
	const legacy = "patterns/legacy-symbols.txt"
	_, hasCurrent := current.Files[legacy]
	_, hasIncoming := incoming.Files[legacy]
	if hasCurrent || hasIncoming {
		seen := map[string]bool{}
		var lines []string
		for _, data := range [][]byte{current.Files[legacy], incoming.Files[legacy]} {
			for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !seen[line] {
					seen[line] = true
					lines = append(lines, line)
				}
			}
		}
		merged.Files[legacy] = []byte(strings.Join(lines, "\n") + "\n")
	}
	return merged
}

// Keep stable per-ID locks until the new package revision has been published.
// An already-open editor in this process retains its lease on a failed import.
func (s *Service) lockImportedRules(workspaceID string, ids []string) (func(), error) {
	var leases []*store.WorkspaceLease
	release := func() {
		for _, lease := range leases {
			_ = lease.Release()
		}
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	for _, id := range ids {
		key := strings.ToLower(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		if s.ruleLease != nil && s.ruleLeaseWorkspaceID == workspaceID && strings.EqualFold(s.ruleLeaseID, id) {
			continue
		}
		path, err := s.ruleLockPath(workspaceID, id)
		if err == nil {
			err = ensureSharedOutputDirectory(filepath.Dir(path), 0700)
		}
		if err != nil {
			release()
			return nil, err
		}
		lease, owner, err := store.AcquireWorkspace(path, currentWorkspaceOwner())
		if err != nil {
			release()
			return nil, err
		}
		if owner != nil {
			release()
			return nil, fmt.Errorf("ルール %s を他のユーザーが編集中です（%s / %s）", id, owner.Owner, owner.Host)
		}
		leases = append(leases, lease)
	}
	return release, nil
}

// installRulePackage publishes a validated copy while the caller owns op and
// the workspace lease. Rule edits keep their rule lease across this operation.
func (s *Service) installRulePackage(w model.Workspace, cfg model.Config, pkg *rulepack.Package) (model.State, error) {
	settings, err := s.workspacePath(w.ID, "setting.json")
	if err != nil {
		return s.Snapshot(), err
	}
	workspaceDir := filepath.Dir(settings)
	workspaceRoot, err := os.OpenRoot(workspaceDir)
	if err != nil {
		return s.Snapshot(), err
	}
	defer workspaceRoot.Close()
	parentWasMissing := false
	if info, err := workspaceRoot.Lstat("rule-packages"); os.IsNotExist(err) {
		if err = workspaceRoot.Mkdir("rule-packages", 0700); err != nil {
			return s.Snapshot(), err
		}
		parentWasMissing = true
	} else if err != nil {
		return s.Snapshot(), err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return s.Snapshot(), fmt.Errorf("ルールパッケージの保存先が通常のフォルダではありません")
	}
	packageRoot, err := workspaceRoot.OpenRoot("rule-packages")
	if err != nil {
		if parentWasMissing {
			_ = workspaceRoot.Remove("rule-packages")
		}
		return s.Snapshot(), err
	}
	defer packageRoot.Close()
	stageName := uid()
	staging := filepath.Join(workspaceDir, "rule-packages", stageName)
	if err = packageRoot.Mkdir(stageName, 0700); err != nil {
		if parentWasMissing {
			_ = workspaceRoot.Remove("rule-packages")
		}
		return s.Snapshot(), err
	}
	adopted := false
	defer func() {
		if !adopted {
			// Cleanup is anchored to the local application directory. Only this
			// newly-created random subtree is removed, never source or older packs.
			_ = packageRoot.RemoveAll(stageName)
			_ = packageRoot.Close()
			if parentWasMissing {
				_ = workspaceRoot.Remove("rule-packages")
			}
		}
	}()
	if err = rulepack.Extract(pkg, staging); err != nil {
		return s.Snapshot(), err
	}
	cfg.RulesPath = filepath.Join(staging, "rules")
	cfg.LegacyPath = ""
	if _, present := pkg.Files["patterns/legacy-symbols.txt"]; present {
		cfg.LegacyPath = filepath.Join(staging, "patterns", "legacy-symbols.txt")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return s.Snapshot(), fmt.Errorf("このアプリのセッションは終了しています")
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
	validation := cfg
	validation.RGPath = "" // A package-provided executable is data, never an import hook.
	cat, err := catalog.Load(ctx, validation)
	if err != nil {
		return s.Snapshot(), fmt.Errorf("ルールパッケージを検証できません: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return s.Snapshot(), err
	}
	// This is the only commit point. All import-owned data is ready; failure
	// leaves the old settings and every earlier package in place.
	if err = s.writeWorkspaceSetting(w, cfg); err != nil {
		return s.Snapshot(), err
	}
	adopted = true
	s.mu.Lock()
	s.state.Config = cfg
	s.cat = cat
	s.state.Rules = cat.Rules
	s.recountLocked()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", nil, "", "")
	// A run may have stopped on the same invalid catalog before this repair.
	if issue := s.workspaceIssues[s.state.ActiveWorkspaceID]["run"]; issue.Page == "rules" {
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "run", nil, "", "")
	}
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// ExportRulePackage writes a new archive from the current saved settings and
// rule assets. It does not edit the selected archive or the workspace settings.
func (s *Service) ExportRulePackage(path string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return "", err
	}
	s.mu.Lock()
	cfg := s.state.Config
	s.mu.Unlock()
	if cfg.Root == "" || cfg.RulesPath == "" {
		return "", fmt.Errorf("先にルールパッケージを読み込んでください")
	}
	if !strings.EqualFold(filepath.Ext(path), ".oborules") {
		return "", fmt.Errorf("保存先は .oborules ファイルを指定してください")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	id := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	settingPath, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return "", err
	}
	// An export is a separate artifact, never a rewrite of managed workspace
	// files or of a file that will be included as a rule auxiliary asset.
	for _, reserved := range []string{filepath.Dir(settingPath), cfg.RulesPath} {
		for current := absolute; ; current = filepath.Dir(current) {
			if sameRoot(current, reserved) {
				return "", fmt.Errorf("パッケージの保存先はワークスペース設定とルールフォルダの外に指定してください")
			}
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
	if err != nil {
		return "", err
	}
	if err = rulepack.Write(absolute, pkg); err != nil {
		return "", err
	}
	return absolute, nil
}

// copyWorkspaceRules makes a duplicate's auxiliary resources independent. The
// caller owns the new workspace lease and publishes its setting.json afterward.
// It performs no command execution, LLM requests, or source writes.
func (s *Service) copyWorkspaceRules(c model.Config, id string) (model.Config, error) {
	if c.RulesPath == "" {
		return c, nil
	}
	pkg, err := rulepack.Snapshot(c.RulesPath, c.LegacyPath, rulepack.FromConfig(c))
	if err != nil {
		return c, err
	}
	settingPath, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return c, err
	}
	workspaceRoot, err := os.OpenRoot(filepath.Dir(settingPath))
	if err != nil {
		return c, err
	}
	defer workspaceRoot.Close()
	if err = workspaceRoot.Mkdir("rule-packages", 0700); err != nil && !os.IsExist(err) {
		return c, err
	}
	info, err := workspaceRoot.Lstat("rule-packages")
	if err != nil {
		return c, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return c, fmt.Errorf("ルールパッケージの保存先が通常のフォルダではありません")
	}
	root, err := workspaceRoot.OpenRoot("rule-packages")
	if err != nil {
		return c, err
	}
	defer root.Close()
	name := uid()
	if err = root.Mkdir(name, 0700); err != nil {
		return c, err
	}
	stage := filepath.Join(filepath.Dir(settingPath), "rule-packages", name)
	if err = rulepack.Extract(pkg, stage); err != nil {
		_ = root.RemoveAll(name)
		return c, err
	}
	c.RulesPath, c.LegacyPath = filepath.Join(stage, "rules"), ""
	if _, present := pkg.Files["patterns/legacy-symbols.txt"]; present {
		c.LegacyPath = filepath.Join(stage, "patterns", "legacy-symbols.txt")
	}
	if c.RulePackageName == "" {
		c.RulePackageName = "rules.oborules"
	}
	return c, nil
}
