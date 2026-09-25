package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onebyone/internal/model"
)

const oauthTestEndpoint = "https://onebyone-test.openai.azure.com/openai/v1/"

type oauthRoundTrip func(*http.Request) (*http.Response, error)

func (f oauthRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func oauthConfig(acquire func(context.Context) (string, error)) model.Config {
	cfg := testInput(oauthTestEndpoint).Config
	cfg.Provider, cfg.AuthMode = "azure", "oauth"
	cfg.AcquireToken = acquire
	return cfg
}

func oauthResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestOAuthAcquiresFreshTokenForEveryRequestAndRateLimitRetry(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "ignored-api-key")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "ignored-env-token")
	acquisitions, requests := 0, 0
	c, err := newClient(oauthConfig(func(context.Context) (string, error) {
		acquisitions++
		return fmt.Sprintf("oauth-token-%d", acquisitions), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.credential != "" || c.cfg.Credential != "" {
		t.Fatal("OAuth retained an unrelated API key")
	}
	c.http.Transport = oauthRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if got := r.Header.Get("Authorization"); got != fmt.Sprintf("Bearer oauth-token-%d", requests) {
			t.Errorf("request %d used stale authentication: %q", requests, got)
		}
		if r.Header.Get("api-key") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("OAuth request carried an API key")
		}
		if requests == 1 {
			res := oauthResponse(http.StatusTooManyRequests, `{}`)
			res.Header.Set("Retry-After", "0")
			return res, nil
		}
		return oauthResponse(http.StatusOK, `{"status":"completed","output":[]}`), nil
	})
	for i := 0; i < 2; i++ {
		if _, err := c.request(context.Background(), []byte(`{}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if acquisitions != 3 || requests != 3 {
		t.Fatalf("token calls=%d HTTP requests=%d, want 3 each", acquisitions, requests)
	}
}

func TestOAuthRequiresProviderAndNeverFallsBackToSecrets(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "ignored-api-key")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "ignored-env-token")
	if _, err := newClient(oauthConfig(nil)); err == nil {
		t.Fatal("OAuth accepted a saved or environment credential without a token provider")
	}
	for _, provider := range []string{"openai", "claude"} {
		cfg := oauthConfig(func(context.Context) (string, error) { return "token", nil })
		cfg.Provider = provider
		cfg.Endpoint = "https://example.com/v1/"
		if _, err := newClient(cfg); err == nil {
			t.Errorf("OAuth was enabled for unsupported provider %s", provider)
		}
	}
	for _, tc := range []struct {
		name, token string
		err         error
	}{
		{name: "refresh failure", err: errors.New("refresh-token-secret must never be displayed")},
		{name: "empty token"},
		{name: "whitespace token", token: " \t"},
		{name: "newline token", token: "token\nrefresh-token-secret"},
		{name: "carriage return token", token: "token\rrefresh-token-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := newClient(oauthConfig(func(context.Context) (string, error) { return tc.token, tc.err }))
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = oauthRoundTrip(func(*http.Request) (*http.Response, error) {
				t.Fatal("request was sent after OAuth acquisition failed")
				return nil, nil
			})
			_, err = c.request(context.Background(), []byte(`{}`), nil)
			if err == nil || strings.Contains(err.Error(), "refresh-token-secret") || IsUsageUnknown(err) {
				t.Fatalf("unsafe or ambiguous credential error: %v", err)
			}
		})
	}
}

func TestOAuthPreservesCancellationWithoutLeakingProviderError(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		c, err := newClient(oauthConfig(func(context.Context) (string, error) {
			return "", fmt.Errorf("refresh-token-secret: %w", cause)
		}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.request(context.Background(), nil, nil)
		if !errors.Is(err, cause) || strings.Contains(err.Error(), "refresh-token-secret") || IsUsageUnknown(err) {
			t.Fatalf("unsafe cancellation error: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err := newClient(oauthConfig(func(context.Context) (string, error) {
		t.Fatal("token provider called for already canceled request")
		return "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.request(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context lost: %v", err)
	}
}

func TestOAuthRejectsUntrustedEndpointsBeforeAcquiringToken(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.example.com/openai/v1/", "http://127.0.0.1:8000/openai/v1/",
		"https://onebyone.openai.azure.com.attacker.example/openai/v1/",
		"https://openai.azure.com/openai/v1/", "https://onebyone.openai.azure.com:8443/openai/v1/",
		"https://user:secret@onebyone.openai.azure.com/openai/v1/",
		"https://onebyone.openai.azure.com/openai/v1/?token=secret",
	} {
		t.Run(endpoint, func(t *testing.T) {
			cfg := oauthConfig(func(context.Context) (string, error) {
				t.Fatal("token acquired before endpoint validation")
				return "", nil
			})
			cfg.Endpoint = endpoint
			if _, err := newClient(cfg); err == nil {
				t.Fatal("OAuth accepted untrusted endpoint")
			}
		})
	}
}

func TestOAuthNeverRetriesUnauthorizedOrFollowsRedirect(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		requests, acquisitions := 0, 0
		c, err := newClient(oauthConfig(func(context.Context) (string, error) {
			acquisitions++
			return "token-secret", nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		c.http.Transport = oauthRoundTrip(func(r *http.Request) (*http.Response, error) {
			requests++
			if r.URL.Hostname() != "onebyone-test.openai.azure.com" {
				t.Fatal("OAuth token followed an off-host redirect")
			}
			res := oauthResponse(status, `{"error":{"code":"token-secret","message":"token-secret"}}`)
			res.Header.Set("Location", "https://attacker.example/responses")
			return res, nil
		})
		_, err = c.request(context.Background(), []byte(`{}`), nil)
		if err == nil || strings.Contains(err.Error(), "token-secret") {
			t.Fatalf("HTTP error exposed token: %v", err)
		}
		if requests != 1 || acquisitions != 1 {
			t.Fatalf("status %d was retried: requests=%d tokens=%d", status, requests, acquisitions)
		}
	}
}

func TestOAuthEditorIndependentReviewAndConnectionUseSameTokenProvider(t *testing.T) {
	var acquisitions, requests, phase atomic.Int32
	var reviewIn ReviewInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if got := r.Header.Get("Authorization"); got != fmt.Sprintf("Bearer oauth-token-%d", requestNumber) {
			t.Errorf("entry point used wrong authentication: %q", got)
		}
		if r.Header.Get("api-key") != "" {
			t.Error("OAuth entry point sent API key")
		}
		switch phase.Load() {
		case 0:
			respond(w, standardTurn(int(requestNumber)-1))
		case 1:
			respond(w, reviewResponseItem(reviewWire(reviewIn, "passed")))
		default:
			respond(w, map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "OK"}}})
		}
	}))
	defer server.Close()
	// Route only this test's HTTPS requests to a local HTTP handler. Keeping the
	// Azure URL intact exercises the production endpoint allowlist without any
	// external API calls or changes to the application's normal TLS behavior.
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	cfg := oauthConfig(func(context.Context) (string, error) {
		return fmt.Sprintf("oauth-token-%d", acquisitions.Add(1)), nil
	})
	in := testInput(oauthTestEndpoint)
	in.Config = cfg
	if out, err := Run(context.Background(), in); err != nil || out.Outcome != "modified" {
		t.Fatalf("OAuth editor failed: %v, %+v", err, out)
	}
	reviewIn = reviewTestInput(oauthTestEndpoint)
	reviewIn.Config = cfg
	phase.Store(1)
	if out, err := Review(context.Background(), reviewIn); err != nil || out.Verdict != "passed" {
		t.Fatalf("OAuth independent review failed: %v, %+v", err, out)
	}
	phase.Store(2)
	if _, err := TestConnection(context.Background(), cfg); err != nil {
		t.Fatalf("OAuth connection check failed: %v", err)
	}
	if requests.Load() != 6 || acquisitions.Load() != 6 {
		t.Fatalf("requests=%d token acquisitions=%d; want 6", requests.Load(), acquisitions.Load())
	}
}
