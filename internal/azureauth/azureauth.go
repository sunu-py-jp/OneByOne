// Package azureauth signs in Azure public-cloud users through MSAL. Token caches
// remain in memory here; callers must encrypt Session before persisting it.
package azureauth

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

const azureScope = "https://ai.azure.com/.default"

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Settings struct {
	TenantID string
	ClientID string
	Endpoint string
}

// Session includes refresh credentials. It must never be sent to the UI or logs,
// or persisted without encryption. AccountID selects the exact signed-in user.
type Session struct {
	Cache     []byte
	AccountID string
	Username  string
}

func Validate(settings Settings) error {
	if !validGUID(settings.TenantID) {
		return errors.New("テナントIDには Microsoft Entra ID のディレクトリ（テナント）IDを入力してください（GUID形式）")
	}
	if !validGUID(settings.ClientID) {
		return errors.New("クライアントIDには Microsoft Entra ID のアプリケーション（クライアント）IDを入力してください（GUID形式）")
	}
	return ValidateEndpoint(settings.Endpoint)
}

func validGUID(value string) bool {
	value = strings.TrimSpace(value)
	return guidPattern.MatchString(value) && value != "00000000-0000-0000-0000-000000000000"
}

// ValidateEndpoint prevents OAuth access tokens being routed to an arbitrary
// proxy or another cloud. This initial implementation supports Azure public
// cloud resource endpoints only.
func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		(u.Port() != "" && u.Port() != "443") {
		return errors.New("OAuth のエンドポイントには Azure の HTTPS リソースURLを指定してください（認証情報・クエリ・フラグメント・独自ポートは使用できません）")
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".openai.azure.com", ".services.ai.azure.com", ".cognitiveservices.azure.com"} {
		if strings.HasSuffix(host, suffix) && validDNSPrefix(strings.TrimSuffix(host, suffix)) {
			return nil
		}
	}
	return errors.New("OAuth は Azure のリソースURL（*.openai.azure.com / *.services.ai.azure.com / *.cognitiveservices.azure.com）に対応しています")
}

func validDNSPrefix(prefix string) bool {
	if prefix == "" {
		return false
	}
	for _, label := range strings.Split(prefix, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

// SignIn opens the system browser. MSAL supplies the authorization code flow's
// S256 PKCE, state check, and ephemeral localhost redirect listener. Configure
// http://localhost as a desktop/public-client redirect URI in Entra ID.
func SignIn(ctx context.Context, settings Settings) (Session, error) {
	return signIn(ctx, settings, newMSALClient)
}

// AccessToken uses only the saved account's cache or refresh token. It never
// starts an interactive login during execution. The returned Session contains
// MSAL's updated cache and must replace the prior encrypted session.
func AccessToken(ctx context.Context, settings Settings, session Session) (string, Session, error) {
	return accessToken(ctx, settings, session, newMSALClient)
}

type identityClient interface {
	interactive(context.Context) (public.AuthResult, error)
	accounts(context.Context) ([]public.Account, error)
	silent(context.Context, public.Account) (public.AuthResult, error)
}

type clientFactory func(Settings, *memoryCache) (identityClient, error)

type msalClient struct {
	client public.Client
}

func newMSALClient(settings Settings, tokens *memoryCache) (identityClient, error) {
	client, err := public.New(strings.TrimSpace(settings.ClientID),
		public.WithAuthority("https://login.microsoftonline.com/"+strings.TrimSpace(settings.TenantID)),
		public.WithCache(tokens))
	if err != nil {
		return nil, err
	}
	return msalClient{client: client}, nil
}

func (c msalClient) interactive(ctx context.Context) (public.AuthResult, error) {
	return c.client.AcquireTokenInteractive(ctx, []string{azureScope}, public.WithRedirectURI("http://localhost"))
}

func (c msalClient) accounts(ctx context.Context) ([]public.Account, error) {
	return c.client.Accounts(ctx)
}

func (c msalClient) silent(ctx context.Context, account public.Account) (public.AuthResult, error) {
	return c.client.AcquireTokenSilent(ctx, []string{azureScope}, public.WithSilentAccount(account))
}

func signIn(ctx context.Context, settings Settings, factory clientFactory) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if err := Validate(settings); err != nil {
		return Session{}, err
	}
	tokens := newMemoryCache(nil)
	client, err := factory(settings, tokens)
	if err != nil {
		return Session{}, authError(ctx, err, "Microsoft サインインを準備できませんでした。テナントIDとクライアントIDを確認してください")
	}
	result, err := client.interactive(ctx)
	if err != nil {
		return Session{}, authError(ctx, err, "Microsoft サインインに失敗しました。ネットワーク、アプリ登録の localhost リダイレクトURI、APIアクセス許可・同意を確認し、もう一度サインインしてください")
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if result.Account.HomeAccountID == "" || result.AccessToken == "" || len(tokens.snapshot()) == 0 {
		return Session{}, errors.New("Microsoft サインインのアカウントまたはトークンを保存できませんでした。もう一度サインインしてください")
	}
	return Session{Cache: tokens.snapshot(), AccountID: result.Account.HomeAccountID, Username: result.Account.PreferredUsername}, nil
}

func accessToken(ctx context.Context, settings Settings, session Session, factory clientFactory) (string, Session, error) {
	if err := ctx.Err(); err != nil {
		return "", Session{}, err
	}
	if err := Validate(settings); err != nil {
		return "", Session{}, err
	}
	if session.AccountID == "" || len(session.Cache) == 0 {
		return "", Session{}, errors.New("Microsoft にサインインしていません。LLM接続の設定からサインインしてください")
	}
	tokens := newMemoryCache(session.Cache)
	client, err := factory(settings, tokens)
	if err != nil {
		return "", Session{}, authError(ctx, err, "Microsoft 認証を準備できませんでした。LLM接続の設定を確認してください")
	}
	accounts, err := client.accounts(ctx)
	if err != nil {
		return "", Session{}, authError(ctx, err, "保存された Microsoft 認証情報を読み込めません。LLM接続の設定から再度サインインしてください")
	}
	var selected public.Account
	for _, account := range accounts {
		if account.HomeAccountID == session.AccountID && strings.EqualFold(account.Realm, strings.TrimSpace(settings.TenantID)) {
			selected = account
			break
		}
	}
	if selected.HomeAccountID == "" {
		return "", Session{}, errors.New("保存された Microsoft アカウントが見つかりません。LLM接続の設定から再度サインインしてください")
	}
	result, err := client.silent(ctx, selected)
	if err != nil {
		return "", Session{}, authError(ctx, err, "Microsoft 認証を更新できませんでした。ネットワークを確認し、LLM接続の設定から再度サインインしてください")
	}
	if err := ctx.Err(); err != nil {
		return "", Session{}, err
	}
	if result.AccessToken == "" || result.Account.HomeAccountID != session.AccountID {
		return "", Session{}, errors.New("Microsoft 認証のアカウントを確認できませんでした。LLM接続の設定から再度サインインしてください")
	}
	return result.AccessToken, Session{Cache: tokens.snapshot(), AccountID: session.AccountID, Username: result.Account.PreferredUsername}, nil
}

// SDK errors can contain token responses or request bodies. Only cancellation
// identity is preserved; all other errors become fixed, actionable messages.
func authError(ctx context.Context, err error, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(message)
}
