package engine

import (
	"context"
	"fmt"
	"strings"

	"onebyone/internal/agent"
	"onebyone/internal/azureauth"
	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
)

func connectionConfig(c model.Config, connection privateconfig.Connection) model.Config {
	c.Provider, c.Endpoint, c.Deployment = connection.Provider, connection.Endpoint, connection.Deployment
	c.AuthMode, c.Credential, c.CredentialSet = connection.AuthMode, connection.Credential, false
	c.AcquireToken = nil
	if c.AuthMode == "oauth" {
		c.Credential, c.CredentialSet = "", oauthSignedIn(connection)
	}
	return c
}

func selectedConnection(c model.Config, r privateconfig.Registry) (model.Config, string) {
	// Empty or deleted selections never inherit a previous workspace's endpoint
	// or secret. Provider defaults keep unrelated rule settings valid to edit.
	c.Provider, c.Endpoint, c.Deployment, c.AuthMode, c.Credential, c.CredentialSet = "azure", "", "", "api_key", "", false
	c.AcquireToken = nil
	id := c.LLMConnectionID
	for _, connection := range r.Connections {
		if connection.ID == id && id != "" {
			return connectionConfig(c, connection), id
		}
	}
	c.LLMConnectionID = ""
	return c, ""
}

// Caller holds mu; registry data is already authenticated and decrypted.
func (s *Service) publishConnectionsLocked(r privateconfig.Registry) {
	s.state.LLMConnections = make([]model.LLMConnection, 0, len(r.Connections))
	for _, c := range r.Connections {
		s.state.LLMConnections = append(s.state.LLMConnections, model.LLMConnection{
			ID: c.ID, Name: c.Name, Provider: c.Provider, Endpoint: c.Endpoint,
			Deployment: c.Deployment, AuthMode: c.AuthMode, Credential: c.Credential,
			OAuthTenantID: c.OAuthTenantID, OAuthClientID: c.OAuthClientID,
			OAuthUsername: c.OAuthUsername, OAuthSignedIn: oauthSignedIn(c),
		})
	}
	s.state.Config, s.state.SelectedLLMConnectionID = selectedConnection(s.state.Config, r)
	s.state.LLMSettingsPath = s.personal.ConnectionsDirectory()
}

func (s *Service) refreshConnections() error {
	if s.personal == nil {
		return fmt.Errorf("個人用LLM設定を開けません")
	}
	s.mu.Lock()
	active := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	selection := ""
	if active != "" {
		_, saved, err := s.readWorkspaceSetting(active)
		if err != nil {
			return err
		}
		selection = saved.LLMConnectionID
	}
	r, err := s.personal.LoadConnections()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.state.Config.LLMConnectionID = selection
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return nil
}

// GetState refreshes personal connections changed by another app instance when
// idle. Polling never waits behind an API request or changes a running run's
// frozen connection. Snapshot remains the side-effect-free internal view.
func (s *Service) GetState() (model.State, error) {
	if !s.op.TryLock() {
		return s.Snapshot(), nil
	}
	defer s.op.Unlock()
	s.mu.Lock()
	running, closed := s.state.Running, s.closed
	s.mu.Unlock()
	if closed {
		return s.Snapshot(), fmt.Errorf("このアプリのセッションは終了しています")
	}
	if running {
		return s.Snapshot(), nil
	}
	err := s.refreshConnections()
	if err == nil {
		s.refreshActiveWorkspaceDiagnostics()
	}
	return s.Snapshot(), err
}

func normalizeConnection(c model.LLMConnection) (model.LLMConnection, error) {
	c.Name, c.Provider, c.Endpoint, c.Deployment, c.AuthMode = strings.TrimSpace(c.Name), strings.TrimSpace(c.Provider), strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Deployment), strings.TrimSpace(c.AuthMode)
	if c.Name == "" || len([]rune(c.Name)) > 80 || strings.ContainsAny(c.Name, "\r\n\x00") {
		return c, fmt.Errorf("LLM接続名は改行を含まない1〜80文字で指定してください")
	}
	if c.Provider == "" {
		c.Provider = "azure"
	}
	if c.AuthMode == "" {
		c.AuthMode = "api_key"
	}
	cfg := DefaultConfig()
	cfg.Provider, cfg.AuthMode = c.Provider, c.AuthMode
	if _, err := normalizeConfig(cfg); err != nil {
		return c, err
	}
	if c.Provider != "azure" && c.AuthMode != "api_key" {
		return c, fmt.Errorf("このプロバイダーはAPIキー認証を使用してください")
	}
	c.CredentialSet = false
	c.OAuthSignedIn, c.OAuthUsername = false, ""
	if c.AuthMode == "oauth" {
		c.OAuthTenantID = strings.ToLower(strings.TrimSpace(c.OAuthTenantID))
		c.OAuthClientID = strings.ToLower(strings.TrimSpace(c.OAuthClientID))
		if err := azureauth.Validate(azureauth.Settings{TenantID: c.OAuthTenantID, ClientID: c.OAuthClientID, Endpoint: c.Endpoint}); err != nil {
			return c, err
		}
		c.Credential = ""
	} else {
		c.OAuthTenantID, c.OAuthClientID = "", ""
	}
	return c, nil
}

func personalConnection(c model.LLMConnection) privateconfig.Connection {
	return privateconfig.Connection{ID: c.ID, Name: c.Name, LLMSettings: privateconfig.LLMSettings{
		Provider: c.Provider, Endpoint: c.Endpoint, Deployment: c.Deployment, AuthMode: c.AuthMode, Credential: c.Credential,
		OAuthTenantID: c.OAuthTenantID, OAuthClientID: c.OAuthClientID,
	}}
}

// SaveLLMConnection updates only this user's encrypted registry. It does not
// select a connection or require any workspace (nor a shared workspace lease).
func (s *Service) SaveLLMConnection(c model.LLMConnection) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	c, err := normalizeConnection(c)
	if err != nil {
		return s.Snapshot(), err
	}
	create := c.ID == ""
	if create {
		c.ID = uid()
	}
	r, err := s.personal.UpdateConnections(func(r *privateconfig.Registry) error {
		index := -1
		for i, old := range r.Connections {
			if old.ID == c.ID {
				index = i
			}
			if old.ID != c.ID && strings.EqualFold(old.Name, c.Name) {
				return fmt.Errorf("同じ名前のLLM接続が既にあります")
			}
		}
		if !create && index < 0 {
			return fmt.Errorf("LLM接続が見つかりません。接続一覧を更新してください")
		}
		next := personalConnection(c)
		if c.AuthMode == "oauth" {
			next.OAuthGeneration = uid()
		}
		if index >= 0 {
			old := r.Connections[index]
			if sameConnectionIdentity(next, old) {
				if c.AuthMode == "oauth" {
					next.OAuthCache, next.OAuthAccountID, next.OAuthUsername, next.OAuthGeneration = old.OAuthCache, old.OAuthAccountID, old.OAuthUsername, old.OAuthGeneration
				} else if c.Credential == "" {
					next.Credential = old.Credential
				}
			}
			r.Connections[index] = next
		} else {
			r.Connections = append(r.Connections, next)
		}
		return nil
	})
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func (s *Service) DeleteLLMConnection(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	r, err := s.personal.UpdateConnections(func(r *privateconfig.Registry) error {
		index := -1
		for i, c := range r.Connections {
			if c.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("LLM接続が見つかりません")
		}
		r.Connections = append(r.Connections[:index], r.Connections[index+1:]...)
		return nil
	})
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// SelectLLMConnection saves a nonsecret reference with this workspace's other
// settings. The global encrypted registry contains connection definitions only.
func (s *Service) SelectLLMConnection(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	c := s.state.Config
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: c.Root}
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
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	r, err := s.personal.LoadConnections()
	if err != nil {
		return s.Snapshot(), err
	}
	if id != "" {
		found := false
		for _, connection := range r.Connections {
			if connection.ID == id {
				found = true
				break
			}
		}
		if !found {
			return s.Snapshot(), fmt.Errorf("LLM接続が見つかりません。接続一覧を更新してください")
		}
	}
	c.LLMConnectionID = id
	c, _ = selectedConnection(c, r)
	if err = s.writeWorkspaceSetting(w, c); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.state.Config = c
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func (s *Service) ClearLLMConnectionCredential(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	r, err := s.personal.UpdateConnections(func(r *privateconfig.Registry) error {
		for i := range r.Connections {
			if r.Connections[i].ID == id {
				r.Connections[i].Credential = ""
				r.Connections[i].OAuthCache = nil
				r.Connections[i].OAuthAccountID, r.Connections[i].OAuthUsername = "", ""
				r.Connections[i].OAuthGeneration = uid()
				return nil
			}
		}
		return fmt.Errorf("LLM接続が見つかりません")
	})
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func (s *Service) TestLLMConnection(id string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return "", err
	}
	if s.personal == nil {
		return "", fmt.Errorf("個人用LLM設定を開けません")
	}
	r, err := s.personal.LoadConnections()
	if err != nil {
		return "", err
	}
	for _, connection := range r.Connections {
		if connection.ID == id {
			s.mu.Lock()
			cfg := s.state.Config
			s.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				cancel()
				return "", fmt.Errorf("このアプリのセッションは終了しています")
			}
			s.cancel = cancel
			s.mu.Unlock()
			defer func() { cancel(); s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
			cfg = connectionConfig(cfg, connection)
			if cfg.AuthMode == "oauth" {
				if !oauthSignedIn(connection) {
					return "", errOAuthSignInRequired
				}
				cfg.AcquireToken = s.oauthTokenProvider(connection)
			}
			return agent.TestConnection(ctx, cfg)
		}
	}
	return "", fmt.Errorf("LLM接続が見つかりません")
}
