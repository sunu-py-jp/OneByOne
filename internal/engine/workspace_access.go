package engine

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

func currentWorkspaceOwner() store.WorkspaceOwner {
	name := os.Getenv("USERNAME")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = "別のユーザー"
	}
	host, _ := os.Hostname()
	return store.WorkspaceOwner{Owner: name, Host: host, OpenedAt: now()}
}
func ownerModel(owner *store.WorkspaceOwner) *model.WorkspaceLock {
	if owner == nil {
		return nil
	}
	return &model.WorkspaceLock{Owner: owner.Owner, Host: owner.Host, OpenedAt: owner.OpenedAt}
}
func workspaceInUse(owner *store.WorkspaceOwner) error {
	if owner == nil {
		return fmt.Errorf("このワークスペースは閲覧専用です")
	}
	return fmt.Errorf("他のユーザーがこのワークスペースを開いています（%s / %s）。閲覧専用です", owner.Owner, owner.Host)
}

func (s *Service) obtainWorkspaceLease(w model.Workspace) (*store.WorkspaceLease, *store.WorkspaceOwner, bool, error) {
	if s.workspaceLease != nil && s.leaseID == w.ID && sameRoot(s.leaseRoot, w.Root) {
		return s.workspaceLease, nil, false, nil
	}
	path, err := s.workspaceLockPath(w.ID, ".edit.lock")
	if err != nil {
		return nil, nil, false, err
	}
	if err = ensureSharedOutputDirectory(filepath.Dir(path), 0700); err != nil {
		return nil, nil, false, err
	}
	lease, owner, err := store.AcquireWorkspace(path, currentWorkspaceOwner())
	if err != nil || owner != nil {
		return lease, owner, lease != nil, err
	}
	return lease, nil, true, nil
}

func (s *Service) adoptWorkspaceLease(w model.Workspace, lease *store.WorkspaceLease) {
	if s.leaseID != w.ID || !sameRoot(s.leaseRoot, w.Root) {
		s.releaseRuleLease()
	}
	if s.workspaceLease != nil && s.workspaceLease != lease {
		_ = s.workspaceLease.Release()
	}
	s.workspaceLease = lease
	s.leaseRoot, s.leaseID = "", ""
	if lease != nil {
		s.leaseRoot, s.leaseID = w.Root, w.ID
	}
}
func (s *Service) editable() error {
	if err := s.idle(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ReadOnly {
		if s.state.WorkspaceLock != nil {
			return fmt.Errorf("他のユーザーが開いているため、閲覧専用です。相手が閉じた後に更新してください")
		}
		return fmt.Errorf("このワークスペースは閲覧専用です")
	}
	if s.state.ActiveWorkspaceID != "" && (s.workspaceLease == nil || s.leaseID != s.state.ActiveWorkspaceID || !sameRoot(s.leaseRoot, s.state.Config.Root)) {
		return fmt.Errorf("ワークスペースの編集権限を確認できません。ワークスペースを開き直してください")
	}
	return nil
}

// Close releases the selected workspace only after all in-flight edits stop.
func (s *Service) Close() {
	// Mark closed before waiting for op so no new mutation may begin. Cancel
	// again after serialization to cover a Start already past its idle check.
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.CancelLLMSignIn()
	s.op.Lock()
	defer s.op.Unlock()
	s.Stop()
	s.Wait()
	s.releaseRuleLease()
	if s.workspaceLease != nil {
		_ = s.workspaceLease.Release()
		s.workspaceLease = nil
	}
}
