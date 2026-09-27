package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func editDefinition(edit model.RuleEdit) model.RuleDefinition {
	return model.RuleDefinition{ID: edit.ID, Name: edit.Name, Description: edit.Description, PathPattern: edit.PathPattern, ContentPattern: edit.ContentPattern, Body: edit.Body}
}

func ruleRevision(rule model.Rule) string {
	return ruleformat.Revision(model.RuleDefinition{ID: rule.ID, Name: rule.Title, Description: rule.Summary, PathPattern: rule.PathPattern, ContentPattern: rule.ContentPattern, Body: rule.Body})
}

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
	if !ruleformat.ValidID(ruleID) {
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
	for _, entry := range pkg.Rules {
		if entry.ID == id {
			d, err := ruleformat.Decode([]byte(entry.Markdown))
			if err != nil {
				return model.Rule{}, fmt.Errorf("rule %s: %w", id, err)
			}
			return ruleformat.ToRule(id, d), nil
		}
	}
	return model.Rule{}, fmt.Errorf("ルールが見つかりません: %s", id)
}

func (s *Service) ruleFromCurrentPackage(workspaceID, id string) (model.Rule, error) {
	_, cfg, err := s.readWorkspaceSetting(workspaceID)
	if err != nil {
		return model.Rule{}, err
	}
	if cfg.RulesPath == "" {
		return model.Rule{}, fmt.Errorf("ルールが見つかりません: %s", id)
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath)
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
	if !ruleformat.ValidID(id) {
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
	return ruleformat.Validate(editDefinition(edit))
}

func (s *Service) SaveRule(edit model.RuleEdit) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	workspaceID := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	if s.ruleLease == nil || s.ruleLeaseWorkspaceID != workspaceID || s.ruleLeaseID != edit.ID {
		return s.Snapshot(), fmt.Errorf("ルールの編集権限がありません。ルールを開き直してください")
	}
	if edit.ExpectedRevision == "" {
		return s.Snapshot(), fmt.Errorf("保存にはexpectedRevisionが必要です")
	}
	return s.saveRules([]model.RuleEdit{edit})
}
func (s *Service) CreateRule(edit model.RuleEdit) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	// An omitted ID receives the next number in saveRules.
	if edit.ExpectedRevision != "" {
		return s.Snapshot(), fmt.Errorf("新規ルールにexpectedRevisionは指定できません")
	}
	return s.saveRules([]model.RuleEdit{edit})
}
