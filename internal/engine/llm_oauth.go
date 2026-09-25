package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onebyone/internal/azureauth"
	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
)

var errOAuthSignInRequired = errors.New("Microsoftへのサインインが必要です。LLM接続からサインインしてください")
var errOAuthConnectionChanged = errors.New("LLM接続またはサインイン状態が変更されました。接続を確認してやり直してください")

func oauthSignedIn(c privateconfig.Connection) bool {
	return c.Provider == "azure" && c.AuthMode == "oauth" && c.OAuthAccountID != "" && len(c.OAuthCache) != 0 && c.OAuthGeneration != ""
}

func oauthSettings(c privateconfig.Connection) azureauth.Settings {
	return azureauth.Settings{TenantID: c.OAuthTenantID, ClientID: c.OAuthClientID, Endpoint: c.Endpoint}
}

func sameConnectionIdentity(a, b privateconfig.Connection) bool {
	return a.ID == b.ID && a.Provider == b.Provider && a.AuthMode == b.AuthMode && a.Endpoint == b.Endpoint &&
		a.OAuthTenantID == b.OAuthTenantID && a.OAuthClientID == b.OAuthClientID
}

func connectionByID(r privateconfig.Registry, id string) (privateconfig.Connection, error) {
	for _, c := range r.Connections {
		if c.ID == id {
			return c, nil
		}
	}
	return privateconfig.Connection{}, fmt.Errorf("LLM接続が見つかりません。接続一覧を更新してください")
}

func validOAuthSession(session azureauth.Session) bool {
	return session.AccountID != "" && len(session.Cache) > 0 && len(session.Cache) <= 512<<10 && json.Valid(session.Cache)
}

// SignInLLMConnection is the only operation that opens a browser. The desktop
// bridge receives account display data, never an access/refresh token or cache.
func (s *Service) SignInLLMConnection(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return s.Snapshot(), err
	}
	if s.personal == nil {
		return s.Snapshot(), fmt.Errorf("個人用LLM設定を開けません")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return s.Snapshot(), fmt.Errorf("このアプリのセッションは終了しています")
	}
	s.oauthCancel = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.oauthCancel = nil; s.mu.Unlock() }()
	r, err := s.personal.LoadConnections()
	if ctx.Err() != nil {
		return s.Snapshot(), fmt.Errorf("Microsoftのサインインを中止しました: %w", ctx.Err())
	}
	if err != nil {
		return s.Snapshot(), err
	}
	connection, err := connectionByID(r, id)
	if err != nil {
		return s.Snapshot(), err
	}
	if connection.Provider != "azure" || connection.AuthMode != "oauth" {
		return s.Snapshot(), fmt.Errorf("Azureの認証方式をMicrosoft Entra ID（OAuth）にしてください")
	}
	if err := azureauth.Validate(oauthSettings(connection)); err != nil {
		return s.Snapshot(), err
	}
	signIn := s.oauthSignIn
	if signIn == nil {
		signIn = azureauth.SignIn
	}
	session, err := signIn(ctx, oauthSettings(connection))
	if ctx.Err() != nil {
		return s.Snapshot(), fmt.Errorf("Microsoftのサインインを中止しました: %w", ctx.Err())
	}
	if err != nil {
		// SDK diagnostics can contain token endpoint responses. Do not put them
		// into a toast, log, workspace issue, or a report.
		return s.Snapshot(), errors.New("Microsoftにサインインできませんでした。テナントID・クライアントID・アプリ登録の http://localhost とAPIアクセス許可を確認してください")
	}
	if !validOAuthSession(session) {
		return s.Snapshot(), errors.New("Microsoftの認証情報を確認できませんでした。もう一度サインインしてください")
	}
	r, err = s.personal.UpdateConnections(func(r *privateconfig.Registry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return context.Canceled
		}
		for i := range r.Connections {
			c := &r.Connections[i]
			if c.ID != id {
				continue
			}
			if !sameConnectionIdentity(*c, connection) || c.OAuthGeneration != connection.OAuthGeneration {
				return errOAuthConnectionChanged
			}
			c.Credential = ""
			c.OAuthCache = append([]byte(nil), session.Cache...)
			c.OAuthAccountID, c.OAuthUsername = session.AccountID, session.Username
			c.OAuthGeneration = uid()
			return nil
		}
		return errOAuthConnectionChanged
	})
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.publishConnectionsLocked(r)
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// Cancel never takes op: a pending interactive login holds that lock.
func (s *Service) CancelLLMSignIn() {
	s.mu.Lock()
	cancel := s.oauthCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) SignOutLLMConnection(id string) (model.State, error) {
	return s.ClearLLMConnectionCredential(id)
}

// A frozen run keeps the chosen identity. Each request still loads the latest
// encrypted cache so silent refresh survives restarts and explicit logout wins
// over an in-flight refresh in this or another app process.
func (s *Service) oauthTokenProvider(connection privateconfig.Connection) func(context.Context) (string, error) {
	return func(parent context.Context) (string, error) {
		s.oauthTokenMu.Lock()
		defer s.oauthTokenMu.Unlock()
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, err := s.personal.LoadConnections()
		if err != nil {
			return "", errors.New("保存済みのMicrosoft認証情報を読み込めません")
		}
		current, err := connectionByID(r, connection.ID)
		if err != nil || !sameConnectionIdentity(current, connection) || current.OAuthGeneration != connection.OAuthGeneration || !oauthSignedIn(current) {
			return "", errOAuthSignInRequired
		}
		if err = azureauth.Validate(oauthSettings(current)); err != nil {
			return "", err
		}
		acquire := s.oauthAcquire
		if acquire == nil {
			acquire = azureauth.AccessToken
		}
		token, session, err := acquire(ctx, oauthSettings(current), azureauth.Session{Cache: current.OAuthCache, AccountID: current.OAuthAccountID, Username: current.OAuthUsername})
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", errors.New("Microsoftの認証情報を更新できません。接続環境を確認し、LLM接続から再サインインしてください")
		}
		if token == "" || strings.ContainsAny(token, "\r\n") || !validOAuthSession(session) || session.AccountID != current.OAuthAccountID {
			return "", errOAuthSignInRequired
		}
		_, err = s.personal.UpdateConnections(func(r *privateconfig.Registry) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for i := range r.Connections {
				c := &r.Connections[i]
				if c.ID == current.ID {
					if !sameConnectionIdentity(*c, current) || c.OAuthGeneration != current.OAuthGeneration || c.OAuthAccountID != current.OAuthAccountID {
						return errOAuthConnectionChanged
					}
					c.OAuthCache = append([]byte(nil), session.Cache...)
					return nil
				}
			}
			return errOAuthConnectionChanged
		})
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

func (s *Service) oauthRunConfig(ctx context.Context, cfg model.Config) (model.Config, error) {
	if s.personal == nil {
		return cfg, errOAuthSignInRequired
	}
	r, err := s.personal.LoadConnections()
	if err != nil {
		return cfg, err
	}
	c, err := connectionByID(r, cfg.LLMConnectionID)
	if err != nil || c.Provider != cfg.Provider || c.AuthMode != cfg.AuthMode || c.Endpoint != cfg.Endpoint || !oauthSignedIn(c) {
		return cfg, errOAuthSignInRequired
	}
	cfg.Credential = ""
	cfg.AcquireToken = s.oauthTokenProvider(c)
	// Establish valid auth before creating a worktree or invoking check commands.
	if _, err := cfg.AcquireToken(ctx); err != nil {
		return cfg, err
	}
	return cfg, nil
}
