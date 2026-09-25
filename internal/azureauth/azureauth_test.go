package azureauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

func testSettings() Settings {
	return Settings{
		TenantID: "12345678-1234-1234-1234-123456789abc",
		ClientID: "98765432-4321-4321-4321-abcdef123456",
		Endpoint: "https://sample.openai.azure.com/openai/v1/",
	}
}

func TestValidateEndpointRestrictsOAuthTokenDestination(t *testing.T) {
	for _, endpoint := range []string{
		"https://sample.openai.azure.com/openai/v1/",
		"https://sample.services.ai.azure.com/openai/v1/responses",
		"https://sample.cognitiveservices.azure.com:443/",
		"https://SAMPLE.openai.azure.com/",
	} {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Errorf("valid endpoint %q rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"", "http://sample.openai.azure.com", "https://openai.azure.com",
		"https://sample.openai.azure.com.attacker.test", "https://attacker.test/openai.azure.com",
		"https://sample.openai.azure.com@attacker.test", "https://user:secret@sample.openai.azure.com",
		"https://sample.openai.azure.com:8443", "https://sample.openai.azure.com:bad",
		"https://sample.openai.azure.com?secret=abc", "https://sample.openai.azure.com?",
		"https://sample.openai.azure.com#secret", "https://.openai.azure.com",
		"https://-sample.openai.azure.com", "https://sample_.openai.azure.com",
		"https://sample..openai.azure.com", "https://sample.openai.azure.com.",
		"https://127.0.0.1", "https://[::1]", "https://sample.openai.azure.cn",
	} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Errorf("unsafe endpoint %q accepted", endpoint)
		}
	}
}

func TestValidateRequiresConcreteTenantAndClientGUID(t *testing.T) {
	if err := Validate(testSettings()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "common", "organizations", "not-a-guid", "00000000-0000-0000-0000-000000000000"} {
		settings := testSettings()
		settings.TenantID = id
		if err := Validate(settings); err == nil {
			t.Errorf("invalid tenant %q accepted", id)
		}
		settings = testSettings()
		settings.ClientID = id
		if err := Validate(settings); err == nil {
			t.Errorf("invalid client %q accepted", id)
		}
	}
}

type fakeClient struct {
	listed          []public.Account
	accountsErr     error
	interactiveFunc func(context.Context) (public.AuthResult, error)
	silentFunc      func(context.Context, public.Account) (public.AuthResult, error)
}

func (f *fakeClient) interactive(ctx context.Context) (public.AuthResult, error) {
	if f.interactiveFunc == nil {
		panic("unexpected interactive login")
	}
	return f.interactiveFunc(ctx)
}

func (f *fakeClient) accounts(context.Context) ([]public.Account, error) {
	return f.listed, f.accountsErr
}

func (f *fakeClient) silent(ctx context.Context, account public.Account) (public.AuthResult, error) {
	if f.silentFunc == nil {
		panic("unexpected token acquisition")
	}
	return f.silentFunc(ctx, account)
}

func TestAccessTokenSelectsSavedAccountAndReturnsRefreshedCache(t *testing.T) {
	settings := testSettings()
	selected := public.Account{HomeAccountID: "selected-id", Realm: settings.TenantID, PreferredUsername: "selected@example.test"}
	session := Session{Cache: []byte("initial cache"), AccountID: selected.HomeAccountID}
	factory := func(_ Settings, tokens *memoryCache) (identityClient, error) {
		return &fakeClient{
			listed: []public.Account{
				{HomeAccountID: "another-id", Realm: settings.TenantID},
				{HomeAccountID: selected.HomeAccountID, Realm: "another-tenant"},
				selected,
			},
			silentFunc: func(ctx context.Context, account public.Account) (public.AuthResult, error) {
				if account.HomeAccountID != selected.HomeAccountID || account.Realm != settings.TenantID {
					t.Fatalf("wrong account used: %#v", account)
				}
				if err := tokens.Export(ctx, testSerializer{data: []byte("refreshed cache")}, cache.ExportHints{}); err != nil {
					t.Fatal(err)
				}
				return public.AuthResult{AccessToken: "new-token", Account: account}, nil
			},
		}, nil
	}
	token, refreshed, err := accessToken(context.Background(), settings, session, factory)
	if err != nil || token != "new-token" || string(refreshed.Cache) != "refreshed cache" || refreshed.Username != selected.PreferredUsername {
		t.Fatalf("unexpected refresh result: tokenPresent=%t, session=%#v, err=%v", token != "", refreshed, err)
	}
	if string(session.Cache) != "initial cache" {
		t.Fatal("input session was mutated")
	}
}

func TestMissingSavedAccountNeverFallsBackOrOpensBrowser(t *testing.T) {
	factory := func(Settings, *memoryCache) (identityClient, error) {
		return &fakeClient{listed: []public.Account{{HomeAccountID: "another-id", Realm: testSettings().TenantID}}}, nil
	}
	token, session, err := accessToken(context.Background(), testSettings(), Session{AccountID: "missing-id", Cache: []byte("cache")}, factory)
	if err == nil || token != "" || len(session.Cache) != 0 {
		t.Fatal("missing account must require sign-in without returning tokens")
	}
}

func TestSignInReturnsAccountAndOpaqueCache(t *testing.T) {
	factory := func(settings Settings, tokens *memoryCache) (identityClient, error) {
		return &fakeClient{interactiveFunc: func(ctx context.Context) (public.AuthResult, error) {
			if err := tokens.Export(ctx, testSerializer{data: []byte("opaque credentials")}, cache.ExportHints{}); err != nil {
				t.Fatal(err)
			}
			return public.AuthResult{AccessToken: "access token", Account: public.Account{HomeAccountID: "account-id", Realm: settings.TenantID, PreferredUsername: "user@example.test"}}, nil
		}}, nil
	}
	session, err := signIn(context.Background(), testSettings(), factory)
	if err != nil || session.AccountID != "account-id" || session.Username != "user@example.test" || string(session.Cache) != "opaque credentials" {
		t.Fatalf("unexpected session: %#v, %v", session, err)
	}
}

func TestSDKErrorsCannotExposeCredentials(t *testing.T) {
	secret := "refresh_token=never-display-this"
	for _, phase := range []string{"constructor", "interactive", "accounts", "silent"} {
		t.Run(phase, func(t *testing.T) {
			factory := func(settings Settings, _ *memoryCache) (identityClient, error) {
				if phase == "constructor" {
					return nil, errors.New(secret)
				}
				client := &fakeClient{
					listed:          []public.Account{{HomeAccountID: "account-id", Realm: settings.TenantID}},
					interactiveFunc: func(context.Context) (public.AuthResult, error) { return public.AuthResult{}, errors.New(secret) },
					silentFunc: func(context.Context, public.Account) (public.AuthResult, error) {
						return public.AuthResult{}, errors.New(secret)
					},
				}
				if phase == "accounts" {
					client.accountsErr = errors.New(secret)
				}
				return client, nil
			}
			var err error
			if phase == "interactive" {
				_, err = signIn(context.Background(), testSettings(), factory)
			} else {
				_, _, err = accessToken(context.Background(), testSettings(), Session{AccountID: "account-id", Cache: []byte("cache")}, factory)
			}
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("SDK error not sanitized: %v", err)
			}
		})
	}
}

func TestMSALMalformedCacheFailsWithoutNetwork(t *testing.T) {
	_, _, err := AccessToken(context.Background(), testSettings(), Session{AccountID: "account-id", Cache: []byte("malformed secret cache")})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("malformed cache must fail safely: %v", err)
	}
}

func TestCancellationIsPreserved(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if err := authError(context.Background(), fmt.Errorf("SDK error with secret: %w", cause), "safe message"); !errors.Is(err, cause) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SignIn(ctx, testSettings()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sign-in proceeded: %v", err)
	}
	if _, _, err := AccessToken(ctx, testSettings(), Session{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh proceeded: %v", err)
	}
}

type testSerializer struct {
	data []byte
	err  error
}

func (s testSerializer) Marshal() ([]byte, error) { return s.data, s.err }

type testUnmarshaler struct{ data []byte }

func (u *testUnmarshaler) Unmarshal(data []byte) error {
	u.data = data
	return nil
}

func TestMemoryCacheCopiesAndHonorsCancellation(t *testing.T) {
	ctx := context.Background()
	input := []byte("original")
	tokens := newMemoryCache(input)
	input[0] = 'x'
	copy := tokens.snapshot()
	copy[0] = 'y'
	if string(tokens.snapshot()) != "original" {
		t.Fatal("cache exposes shared storage")
	}
	reader := &testUnmarshaler{}
	if err := tokens.Replace(ctx, reader, cache.ReplaceHints{}); err != nil {
		t.Fatal(err)
	}
	reader.data[0] = 'z'
	if string(tokens.snapshot()) != "original" {
		t.Fatal("Replace exposed shared storage")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := tokens.Export(canceled, testSerializer{data: []byte("replacement")}, cache.ExportHints{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Export succeeded")
	}
	if err := tokens.Replace(canceled, reader, cache.ReplaceHints{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Replace succeeded")
	}
	if string(tokens.snapshot()) != "original" {
		t.Fatal("canceled export mutated credentials")
	}
}

type mockHTTPClient func(*http.Request) (*http.Response, error)

func (f mockHTTPClient) Do(request *http.Request) (*http.Response, error) { return f(request) }

func (f mockHTTPClient) CloseIdleConnections() {}

// Exercise the real MSAL serializer, account lookup and refresh-token grant. All
// HTTP requests are handled in memory; this test never contacts Microsoft.
func TestMSALRefreshPersistsRotationAndSurvivesClientRestart(t *testing.T) {
	ctx := context.Background()
	settings := testSettings()
	authority := "https://login.microsoftonline.com/" + settings.TenantID
	grantCalls := 0
	transport := mockHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "login.microsoftonline.com" {
			t.Fatalf("unexpected token authority: %s", request.URL.Host)
		}
		var body string
		switch {
		case strings.Contains(request.URL.Path, "/discovery/instance"):
			body = fmt.Sprintf(`{"tenant_discovery_endpoint":%q,"metadata":[{"preferred_network":"login.microsoftonline.com","preferred_cache":"login.microsoftonline.com","aliases":["login.microsoftonline.com"]}]}`, authority+"/v2.0/.well-known/openid-configuration")
		case strings.HasSuffix(request.URL.Path, "/.well-known/openid-configuration"):
			body = fmt.Sprintf(`{"token_endpoint":%q,"authorization_endpoint":%q,"issuer":%q}`, authority+"/oauth2/v2.0/token", authority+"/oauth2/v2.0/authorize", authority+"/v2.0")
		case strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token"):
			grantCalls++
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("client_id") != settings.ClientID || !strings.Contains(request.Form.Get("scope"), azureScope) {
				t.Fatal("unexpected client or scope in token request")
			}
			if grantCalls == 1 {
				if request.Form.Get("grant_type") != "authorization_code" {
					t.Fatal("fixture initialization must use authorization_code")
				}
			} else if grantCalls == 2 {
				if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "mock-refresh-1" {
					t.Fatal("MSAL did not refresh using the saved refresh token")
				}
			} else {
				t.Fatal("fresh cached token was not reused after client restart")
			}
			claims := fmt.Sprintf(`{"aud":%q,"tid":%q,"oid":"user-id","preferred_username":"user@example.test","iss":%q,"iat":%d,"exp":%d}`, settings.ClientID, settings.TenantID, authority+"/v2.0", time.Now().Unix(), time.Now().Add(time.Hour).Unix())
			idToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
			clientInfo := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"uid":"user-id","utid":%q}`, settings.TenantID)))
			body = fmt.Sprintf(`{"token_type":"Bearer","access_token":"mock-access-%d","refresh_token":"mock-refresh-%d","expires_in":3600,"id_token":%q,"client_info":%q,"scope":%q}`, grantCalls, grantCalls, idToken, clientInfo, azureScope)
		default:
			t.Fatalf("unexpected HTTP request: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	newClient := func(tokens *memoryCache) (public.Client, error) {
		return public.New(settings.ClientID, public.WithAuthority(authority), public.WithCache(tokens), public.WithHTTPClient(transport))
	}
	tokens := newMemoryCache(nil)
	client, err := newClient(tokens)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := client.AcquireTokenByAuthCode(ctx, "mock-code", "http://localhost", []string{azureScope})
	if err != nil {
		t.Fatal(err)
	}
	var expired map[string]any
	if err := json.Unmarshal(tokens.snapshot(), &expired); err != nil {
		t.Fatal(err)
	}
	accessTokens, ok := expired["AccessToken"].(map[string]any)
	if !ok || len(accessTokens) == 0 {
		t.Fatal("MSAL did not export an access-token cache")
	}
	for _, item := range accessTokens {
		item.(map[string]any)["expires_on"] = "1"
		item.(map[string]any)["extended_expires_on"] = "1"
	}
	expiredJSON, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	factory := func(_ Settings, tokens *memoryCache) (identityClient, error) {
		client, err := newClient(tokens)
		return msalClient{client: client}, err
	}
	access, refreshed, err := accessToken(ctx, settings, Session{Cache: expiredJSON, AccountID: initial.Account.HomeAccountID}, factory)
	if err != nil {
		t.Fatal(err)
	}
	if access != "mock-access-2" || !strings.Contains(string(refreshed.Cache), "mock-refresh-2") || grantCalls != 2 {
		t.Fatal("MSAL refresh result or rotated token was not persisted")
	}
	access, _, err = accessToken(ctx, settings, refreshed, factory)
	if err != nil || access != "mock-access-2" || grantCalls != 2 {
		t.Fatalf("cached token was not reused: %v", err)
	}
}
