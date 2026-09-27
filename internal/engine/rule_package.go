package engine

import (
	"context"
	"fmt"
	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (s *Service) ImportRulePackage(path, mode string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if mode != "replace" && mode != "merge" {
		return s.Snapshot(), fmt.Errorf("取り込み方法は上書きまたはマージを指定してください")
	}
	s.mu.Lock()
	cfg := s.state.Config
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: cfg.Root}
	for _, v := range s.workspaces {
		if v.ID == w.ID {
			w.Name = v.Name
			break
		}
	}
	s.mu.Unlock()
	if w.ID == "" || cfg.Root == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	incoming, err := rulepack.Read(path)
	if err != nil {
		return s.Snapshot(), err
	}
	pkg := incoming
	if mode == "replace" {
		cfg.ExcludedRuleIDs = nil
	}
	var lockIDs []string
	if mode == "merge" {
		current := &rulepack.Package{Rules: []rulepack.Entry{}}
		if cfg.RulesPath != "" {
			if current, err = rulepack.Snapshot(cfg.RulesPath); err != nil {
				return s.Snapshot(), err
			}
		}
		renamed := mergedRuleIDs(packageRuleIDs(current), packageRuleIDs(incoming))
		pkg = &rulepack.Package{Rules: append([]rulepack.Entry{}, current.Rules...)}
		for _, entry := range incoming.Rules {
			if id := renamed[entry.ID]; id != entry.ID {
				if entry, err = rulepack.WithID(entry, id); err != nil {
					return s.Snapshot(), err
				}
			}
			pkg.Rules = append(pkg.Rules, entry)
		}
		lockIDs = packageRuleIDs(pkg)
	} else {
		// A valid replacement can repair a corrupt local JSON document.
		if lockIDs, err = s.existingRuleIDs(w.ID, cfg.RulesPath); err != nil {
			return s.Snapshot(), err
		}
		lockIDs = append(lockIDs, packageRuleIDs(incoming)...)
	}
	release, err := s.lockImportedRules(w.ID, lockIDs)
	if err != nil {
		return s.Snapshot(), err
	}
	defer release()
	state, err := s.installRulePackage(w, cfg, pkg)
	if err == nil {
		s.releaseRuleLease()
	}
	return state, err
}
func packageRuleIDs(pkg *rulepack.Package) []string {
	ids := []string{}
	for _, r := range pkg.Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

// existingRuleIDs names every rule a replacement must lock. A corrupt local
// document still cannot bypass a lease held on one of its rules.
func (s *Service) existingRuleIDs(workspaceID, rulesPath string) ([]string, error) {
	if rulesPath == "" {
		return nil, nil
	}
	if current, err := rulepack.Snapshot(rulesPath); err == nil {
		return packageRuleIDs(current), nil
	}
	probe, err := s.ruleLockPath(workspaceID, "probe")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(probe))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if id := strings.TrimSuffix(entry.Name(), ".lock"); !entry.IsDir() && id != entry.Name() && ruleformat.ValidID(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Reserve all incoming original IDs before allocating suffixes. 1 and 1_2 in
// one import must not steal each other's names on case-insensitive Windows or
// macOS filesystems, where rule lock files are named after IDs.
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
	return s.installRuleVersion(w, cfg, pkg, false)
}

func (s *Service) installRuleVersion(w model.Workspace, cfg model.Config, pkg *rulepack.Package, allowInvalid bool) (model.State, error) {
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
	if info, err := workspaceRoot.Lstat("rule-versions"); os.IsNotExist(err) {
		if err = workspaceRoot.Mkdir("rule-versions", 0700); err != nil {
			return s.Snapshot(), err
		}
		parentWasMissing = true
	} else if err != nil {
		return s.Snapshot(), err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return s.Snapshot(), fmt.Errorf("ルールの保存先が通常のフォルダではありません")
	}
	packageRoot, err := workspaceRoot.OpenRoot("rule-versions")
	if err != nil {
		if parentWasMissing {
			_ = workspaceRoot.Remove("rule-versions")
		}
		return s.Snapshot(), err
	}
	defer packageRoot.Close()
	stageName := uid()
	staging := filepath.Join(workspaceDir, "rule-versions", stageName)
	if err = packageRoot.Mkdir(stageName, 0700); err != nil {
		if parentWasMissing {
			_ = workspaceRoot.Remove("rule-versions")
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
				_ = workspaceRoot.Remove("rule-versions")
			}
		}
	}()
	if err = rulepack.Extract(pkg, staging); err != nil {
		return s.Snapshot(), err
	}
	cfg.RulesPath = filepath.Join(staging, "rules.json")
	cfg.ExcludedRuleIDs = normalizedWorkspaceRuleSelection(cfg)
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
	var cat *catalog.Catalog
	var catalogErr error
	if allowInvalid && len(pkg.Rules) == 0 {
		cfg.RulesPath = ""
		cfg.ExcludedRuleIDs = nil
	} else {
		cat, err = catalog.Load(ctx, validation)
		catalogErr = err
	}
	if err != nil && !allowInvalid {
		return s.Snapshot(), fmt.Errorf("ルールを検証できません: %w", err)
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
	s.state.Rules = []model.Rule{}
	if cat != nil {
		s.state.Rules = cat.Rules
	} else {
		for _, entry := range pkg.Rules {
			d, _ := ruleformat.Decode([]byte(entry.Markdown))
			rule := ruleformat.ToRule(entry.ID, d)
			if rule.Title == "" {
				rule.Title = "名称未入力"
			}
			s.state.Rules = append(s.state.Rules, rule)
		}
	}
	s.recountLocked()
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", catalogErr, "rules", "")
	// A run may have stopped on the same invalid catalog before this repair.
	if issue := s.workspaceIssues[s.state.ActiveWorkspaceID]["run"]; issue.Page == "rules" && catalogErr == nil {
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "run", nil, "", "")
	}
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func (s *Service) ExportRulePackage(path string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return "", err
	}
	s.mu.Lock()
	cfg := s.state.Config
	id := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	if cfg.Root == "" || cfg.RulesPath == "" {
		return "", fmt.Errorf("先にルールを読み込んでください")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	settingPath, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return "", err
	}
	if isAtOrWithin(filepath.Dir(settingPath), absolute) {
		return "", fmt.Errorf("書き出し先はワークスペース設定の外に指定してください")
	}
	p, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		return "", err
	}
	if err = rulepack.Write(absolute, p); err != nil {
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
	pkg, err := rulepack.Snapshot(c.RulesPath)
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
	if err = workspaceRoot.Mkdir("rule-versions", 0700); err != nil && !os.IsExist(err) {
		return c, err
	}
	info, err := workspaceRoot.Lstat("rule-versions")
	if err != nil {
		return c, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return c, fmt.Errorf("ルールパッケージの保存先が通常のフォルダではありません")
	}
	root, err := workspaceRoot.OpenRoot("rule-versions")
	if err != nil {
		return c, err
	}
	defer root.Close()
	name := uid()
	if err = root.Mkdir(name, 0700); err != nil {
		return c, err
	}
	stage := filepath.Join(filepath.Dir(settingPath), "rule-versions", name)
	if err = rulepack.Extract(pkg, stage); err != nil {
		_ = root.RemoveAll(name)
		return c, err
	}
	c.RulesPath = filepath.Join(stage, "rules.json")
	return c, nil
}
