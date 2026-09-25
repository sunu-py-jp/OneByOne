package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func editDefinition(edit model.RuleEdit) model.RuleDefinition {
	return model.RuleDefinition{Version: 1, Name: edit.Name, Overview: edit.Overview, Before: edit.Before, After: edit.After, Notes: edit.Notes, HoldConditions: edit.HoldConditions, Pattern: edit.Pattern}
}

func ruleRevision(rule model.Rule) string {
	return ruleformat.Revision(model.RuleDefinition{Version: 1, Name: rule.Title, Overview: rule.Overview, Before: rule.Before, After: rule.After, Notes: rule.Notes, HoldConditions: rule.HoldConditions, Pattern: rule.Pattern})
}

var editableRuleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Caller owns op. Lock files remain stable and are outside exported rule assets.
func (s *Service) releaseRuleLease() {
	if s.ruleLease != nil {
		_ = s.ruleLease.Release()
	}
	s.ruleLease, s.ruleLeaseWorkspaceID, s.ruleLeaseID = nil, "", ""
}

func (s *Service) CloseRule() error {
	s.op.Lock()
	defer s.op.Unlock()
	s.releaseRuleLease()
	return nil
}

func (s *Service) ruleLockPath(workspaceID, ruleID string) (string, error) {
	if !editableRuleID.MatchString(ruleID) {
		return "", fmt.Errorf("ルールIDは英数字で始まる64文字以内の英数字・ハイフン・アンダースコアにしてください")
	}
	return s.workspaceLockPath(workspaceID, "rule-locks/"+strings.ToLower(ruleID)+".lock")
}

func (s *Service) acquireRuleLease(workspaceID, ruleID string) (*store.WorkspaceOwner, error) {
	if s.ruleLease != nil && s.ruleLeaseWorkspaceID == workspaceID && s.ruleLeaseID == ruleID {
		return nil, nil
	}
	s.releaseRuleLease()
	path, err := s.ruleLockPath(workspaceID, ruleID)
	if err != nil {
		return nil, err
	}
	if err = ensureSharedOutputDirectory(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lease, owner, err := store.AcquireWorkspace(path, currentWorkspaceOwner())
	if err != nil {
		return nil, err
	}
	if owner != nil {
		return owner, nil
	}
	s.ruleLease, s.ruleLeaseWorkspaceID, s.ruleLeaseID = lease, workspaceID, ruleID
	return nil, nil
}

func ruleFromPackage(pkg *rulepack.Package, id string) (model.Rule, error) {
	data, found := pkg.Files["rules/"+id+"/rule.json"]
	if !found {
		return model.Rule{}, fmt.Errorf("ルールが見つかりません: %s", id)
	}
	definition, err := ruleformat.Decode(data)
	if err != nil {
		return model.Rule{}, fmt.Errorf("%s/rule.json: %w", id, err)
	}
	return ruleformat.ToRule(id, definition), nil
}

func (s *Service) ruleFromCurrentPackage(workspaceID, id string) (model.Rule, error) {
	_, cfg, err := s.readWorkspaceSetting(workspaceID)
	if err != nil {
		return model.Rule{}, err
	}
	if cfg.RulesPath == "" {
		return model.Rule{}, fmt.Errorf("ルールが見つかりません: %s", id)
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
	if err != nil {
		return model.Rule{}, err
	}
	rule, err := ruleFromPackage(pkg, id)
	if err != nil {
		return model.Rule{}, err
	}
	s.mu.Lock()
	for _, previous := range s.state.Rules {
		if previous.ID == id {
			rule.CandidateCount, rule.AppliedCount = previous.CandidateCount, previous.AppliedCount
			break
		}
	}
	s.mu.Unlock()
	return rule, nil
}

// OpenRule acquires a stable rule-ID lease, independent of the package version.
// The workspace lease remains the outer write guard; a later app instance may
// inspect the latest rule and owner without modifying the filesystem.
func (s *Service) OpenRule(id string) (model.RuleEditor, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return model.RuleEditor{}, err
	}
	if !editableRuleID.MatchString(id) {
		return model.RuleEditor{}, fmt.Errorf("ルールIDが不正です")
	}
	s.mu.Lock()
	workspaceID, readOnly, workspaceOwner := s.state.ActiveWorkspaceID, s.state.ReadOnly, s.state.WorkspaceLock
	s.mu.Unlock()
	if workspaceID == "" {
		return model.RuleEditor{}, fmt.Errorf("先にワークスペースを選択してください")
	}
	rule, err := s.ruleFromCurrentPackage(workspaceID, id)
	if err != nil {
		return model.RuleEditor{}, err
	}
	result := model.RuleEditor{Rule: rule, Revision: ruleRevision(rule)}
	if readOnly {
		s.releaseRuleLease()
		result.ReadOnly = true
		if workspaceOwner != nil {
			result.LockOwner, result.LockHost = workspaceOwner.Owner, workspaceOwner.Host
		}
		if path, pathErr := s.ruleLockPath(workspaceID, id); pathErr == nil {
			owner, peekErr := store.PeekWorkspace(path)
			if peekErr != nil && !os.IsNotExist(peekErr) {
				return model.RuleEditor{}, peekErr
			}
			if owner != nil {
				result.LockOwner, result.LockHost = owner.Owner, owner.Host
			}
		}
		return result, nil
	}
	if err := s.editable(); err != nil {
		return model.RuleEditor{}, err
	}
	owner, err := s.acquireRuleLease(workspaceID, id)
	if err != nil {
		return model.RuleEditor{}, err
	}
	if owner != nil {
		result.ReadOnly, result.LockOwner, result.LockHost = true, owner.Owner, owner.Host
	}
	return result, nil
}

func validateRuleEdit(edit model.RuleEdit) error {
	if !editableRuleID.MatchString(edit.ID) {
		return fmt.Errorf("ルールIDは英数字で始まる64文字以内の英数字・ハイフン・アンダースコアにしてください")
	}
	return ruleformat.Validate(editDefinition(edit))
}

func (s *Service) SaveRule(edit model.RuleEdit) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	return s.writeRule(edit, false)
}

func (s *Service) CreateRule(edit model.RuleEdit) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	return s.writeRule(edit, true)
}

func (s *Service) writeRule(edit model.RuleEdit, create bool) (model.State, error) {
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if err := validateRuleEdit(edit); err != nil {
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
	if w.ID == "" || w.Root == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	pkg := &rulepack.Package{Settings: rulepack.FromConfig(cfg), Files: map[string][]byte{}}
	var err error
	if cfg.RulesPath != "" {
		pkg, err = rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
		if err != nil {
			return s.Snapshot(), err
		}
	}
	prefix := "rules/" + edit.ID + "/"
	_, exists := pkg.Files[prefix+"rule.json"]
	if create {
		for path := range pkg.Files {
			parts := strings.Split(path, "/")
			if len(parts) >= 3 && parts[0] == "rules" && strings.EqualFold(parts[1], edit.ID) {
				return s.Snapshot(), fmt.Errorf("同じIDのルールが既にあります: %s", edit.ID)
			}
		}
		owner, lockErr := s.acquireRuleLease(w.ID, edit.ID)
		if lockErr != nil {
			return s.Snapshot(), lockErr
		}
		if owner != nil {
			return s.Snapshot(), fmt.Errorf("他のユーザーがこのルールを編集中です（%s / %s）", owner.Owner, owner.Host)
		}
	} else {
		if !exists {
			return s.Snapshot(), fmt.Errorf("ルールが見つかりません: %s", edit.ID)
		}
		if s.ruleLease == nil || s.ruleLeaseWorkspaceID != w.ID || s.ruleLeaseID != edit.ID {
			return s.Snapshot(), fmt.Errorf("ルールの編集権限がありません。ルールを開き直してください")
		}
		current, readErr := ruleFromPackage(pkg, edit.ID)
		if readErr != nil {
			return s.Snapshot(), readErr
		}
		currentRevision := ruleRevision(current)
		if edit.ExpectedRevision == "" || edit.ExpectedRevision != currentRevision {
			return s.Snapshot(), fmt.Errorf("このルールは他の編集で更新されています。最新の内容を開き直し、変更を確認してから保存してください")
		}
	}
	data, err := ruleformat.Encode(editDefinition(edit))
	if err != nil {
		return s.Snapshot(), err
	}
	pkg.Files[prefix+"rule.json"] = data
	if cfg.RulePackageName == "" {
		cfg.RulePackageName = "rules.oborules"
	}
	return s.installRulePackage(w, cfg, pkg)
}
