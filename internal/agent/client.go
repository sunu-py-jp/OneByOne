// Package agent implements stateless, bounded provider API tool loops.
// It can propose edits, but has no filesystem mutation or command execution tools.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"onebyone/internal/azureauth"
	"onebyone/internal/model"
)

const (
	maxRequestBytes  = 2 << 20
	maxResponseBytes = 4 << 20
	maxToolBytes     = 96 << 10
	maxReadBytes     = 512 << 10
	maxToolCalls     = 32
)

type Input struct {
	Config               model.Config
	SystemPrompt         string
	File                 string
	Content              string
	CandidateRules       []string
	BaseHash             string
	Rules                []model.Rule
	RepairState          *model.RepairState
	BudgetBaseline       ExecutionBudgetBaseline
	SaveRepairState      func(model.RepairState) error
	ValidateCandidate    func(context.Context, model.CandidateRequest) (model.CandidateValidation, error)
	ReviewCandidate      func(context.Context, ReviewInput) (model.IndependentReview, error)
	AllowUncertainResume bool
	PreviousFailure      string
	ReadRule             func(id string) (string, error)
	// ReadContext must restrict paths to the project and reject symlinks, binary
	// files, and project/user instruction or credential files. Line numbers are 1-based.
	ReadContext func(path string, startLine, endLine int) (string, error)
	Log         func(string)
}

type client struct {
	endpoint   string
	credential string
	authMode   string
	cfg        model.Config
	http       *http.Client
}

func normalizeConfig(cfg model.Config) (model.Config, error) {
	var err error
	cfg.Provider, err = normalizeProvider(cfg.Provider)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.Deployment) == "" {
		return cfg, errors.New("Model or Azure deployment name is required")
	}
	cfg = model.EffectiveExecutionConfig(cfg)
	if cfg.MaxAttempts < 0 || cfg.MaxAttempts > 3 {
		return cfg, errors.New("MaxAttempts must be 0 (unlimited) or between 1 and 3")
	}
	if cfg.MaxTurns < 0 || cfg.MaxTurns > 32 {
		return cfg, errors.New("MaxTurns must be 0 (unlimited) or between 1 and 32")
	}
	if cfg.MaxOutputTokens < 64 || cfg.MaxOutputTokens > 32768 {
		return cfg, errors.New("MaxOutputTokens must be between 64 and 32768")
	}
	if cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > 1<<20 {
		return cfg, errors.New("MaxFileBytes must be between 1 and 1048576")
	}
	if cfg.TimeoutSeconds < 0 || cfg.TimeoutSeconds > 3600 {
		return cfg, errors.New("TimeoutSeconds must be 0 (unlimited) or between 1 and 3600")
	}
	for _, p := range []float64{cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.CachedInputPricePerMillion, cfg.OutputPricePerMillion} {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 {
			return cfg, errors.New("Cost limit and token prices must be finite nonnegative numbers")
		}
	}
	// A missing cache price must not turn cached tokens into free usage. Use the
	// uncached rate conservatively until an explicit discounted rate is supplied.
	if cfg.CachedInputPricePerMillion == 0 {
		cfg.CachedInputPricePerMillion = cfg.InputPricePerMillion
	}
	if cfg.CachedInputPricePerMillion > cfg.InputPricePerMillion {
		return cfg, errors.New("Cached input price cannot exceed uncached input price")
	}
	if cfg.MaxCostUSD > 0 && (cfg.InputPricePerMillion <= 0 || cfg.OutputPricePerMillion <= 0) {
		return cfg, errors.New("A cost limit requires positive input and output prices; enter the prices for your deployment")
	}
	return cfg, nil
}

func newClient(cfg model.Config) (*client, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	endpoint, err := NormalizeProviderEndpoint(cfg.Provider, cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	auth := cfg.AuthMode
	if auth == "" {
		auth = "api_key"
	}
	var env string
	if cfg.Provider == "azure" {
		switch auth {
		case "api_key":
			env = "AZURE_OPENAI_API_KEY"
		case "bearer":
			env = "AZURE_OPENAI_AUTH_TOKEN"
		case "oauth":
			if cfg.AcquireToken == nil {
				return nil, errors.New("Microsoft にサインインしてから接続してください")
			}
			if err := azureauth.ValidateEndpoint(endpoint); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("Azure AuthMode must be api_key, bearer or oauth")
		}
	} else {
		if auth != "api_key" {
			return nil, errors.New("OpenAI and Claude AuthMode must be api_key")
		}
		env = "OPENAI_API_KEY"
		if cfg.Provider == "claude" {
			env = "ANTHROPIC_API_KEY"
		}
	}
	credential := ""
	if auth == "oauth" {
		// Browser sign-in is the only authority for this mode. Never retain a
		// stale API key or fall back to process credentials if refresh fails.
		cfg.Credential = ""
	} else {
		credential = strings.TrimSpace(cfg.Credential)
		if credential == "" {
			credential = strings.TrimSpace(os.Getenv(env))
		}
		if credential == "" {
			return nil, fmt.Errorf("LLM credential is required (or set %s)", env)
		}
		if strings.ContainsAny(credential, "\r\n") {
			return nil, errors.New("LLM credential contains a newline")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	return &client{endpoint: endpoint, credential: credential, authMode: auth, cfg: cfg, http: &http.Client{
		Transport:     transport,
		Timeout:       time.Duration(cfg.TimeoutSeconds) * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func executionContext(parent context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds > 0 {
		return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	}
	return context.WithCancel(parent)
}

type response struct {
	// Messages uses a native assistant content array for continuation. It is
	// never serialized as Responses input and never escapes this file's Run.
	NativeContinuation json.RawMessage   `json:"-"`
	ProtocolError      error             `json:"-"`
	Status             string            `json:"status"`
	Output             []json.RawMessage `json:"output"`
	Usage              *responseUsage    `json:"usage"`
	Error              *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type responseUsage struct {
	CacheWrite5m int  `json:"-"`
	CacheWrite1h int  `json:"-"`
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	InputDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// request never retries an ambiguous network error or a server failure: the POST
// may already have generated billable tokens. Only explicit 429s are retried,
// twice at most, honoring Retry-After within the run deadline.
func (c *client) request(ctx context.Context, body []byte, log func(string)) (result response, err error) {
	defer func() {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = &fatalError{err}
		}
	}()
	for retry := 0; retry <= 2; retry++ {
		credential, err := c.requestCredential(ctx)
		if err != nil {
			return response{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return response{}, errors.New("Cannot construct LLM request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if c.cfg.Provider == "claude" {
			req.Header.Set("x-api-key", credential)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else if c.cfg.Provider == "openai" || c.authMode == "bearer" || c.authMode == "oauth" {
			req.Header.Set("Authorization", "Bearer "+credential)
		} else {
			req.Header.Set("api-key", credential)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return response{}, unknownUsage(fmt.Errorf("LLM request stopped: %w; in-flight usage may still be billed", ctx.Err()))
			}
			// Do not include transport errors that may quote headers or source data.
			return response{}, unknownUsage(errors.New("LLM request failed in transport; it was not retried because in-flight usage may have been billed"))
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
		res.Body.Close()
		if readErr != nil {
			return response{}, unknownUsage(errors.New("LLM response could not be read; usage may have been billed"))
		}
		if len(data) > maxResponseBytes {
			return response{}, unknownUsage(errors.New("LLM response exceeded the 4 MiB limit; usage may have been billed"))
		}
		if res.StatusCode == http.StatusTooManyRequests && retry < 2 {
			delay, err := retryDelay(res.Header.Get("Retry-After"), retry)
			if err != nil {
				return response{}, err
			}
			if log != nil {
				log(fmt.Sprintf("LLM rate limit: retry %d/2 after %s", retry+1, delay))
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return response{}, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			// Provider messages can echo source or credentials. Surface HTTP status
			// and a short identifier only, never the raw response body.
			var envelope struct {
				Error struct {
					Code json.RawMessage `json:"code"`
					Type string          `json:"type"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &envelope)
			code := safeIdentifier(strings.Trim(string(envelope.Error.Code), "\""))
			if code == "" && c.cfg.Provider == "claude" {
				code = safeIdentifier(envelope.Error.Type)
			}
			if strings.Contains(code, credential) {
				code = ""
			}
			httpErr := fmt.Errorf("LLM HTTP %d", res.StatusCode)
			if code != "" {
				httpErr = fmt.Errorf("LLM HTTP %d (%s)", res.StatusCode, code)
			}
			if res.StatusCode >= 500 || res.StatusCode == http.StatusRequestTimeout {
				return response{}, unknownUsage(httpErr)
			}
			return response{}, httpErr
		}
		out, err := c.decodeResponse(data)
		if err != nil {
			return response{}, unknownUsage(errors.New("LLM returned invalid JSON; usage may have been billed"))
		}
		return out, nil
	}
	return response{}, errors.New("LLM rate-limit retries exhausted")
}

// OAuth credentials are acquired for every POST, including explicit 429
// retries, so a long-running editor or independent reviewer never holds an
// expired access token. Authentication errors happen before any LLM request
// and must not be reported as potentially billable transport failures.
func (c *client) requestCredential(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.authMode != "oauth" {
		return c.credential, nil
	}
	if c.cfg.AcquireToken == nil {
		return "", errors.New("Microsoft にサインインしてから接続してください")
	}
	token, err := c.cfg.AcquireToken(ctx)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		// Providers can include token material in errors. Preserve only safe
		// cancellation semantics; never wrap or log the underlying error.
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}
		return "", errors.New("Microsoft の認証を更新できません。LLM 接続の設定を確認し、再サインインしてください")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("Microsoft の認証情報が無効です。再サインインしてください")
	}
	return strings.TrimSpace(token), nil
}

func retryDelay(header string, retry int) (time.Duration, error) {
	delay := time.Duration(retry+1) * time.Second
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			delay = time.Duration(seconds) * time.Second
		} else if when, err := http.ParseTime(header); err == nil {
			delay = time.Until(when)
			if delay < 0 {
				delay = 0
			}
		}
	}
	if delay < 0 || delay > 30*time.Second {
		return 0, errors.New("LLM requested a retry delay over 30 seconds; retry the file later")
	}
	return delay, nil
}

func safeIdentifier(s string) string {
	if len(s) > 80 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return s
}

func (c *client) reserve(body []byte, usage model.Usage) error {
	if len(body) > maxRequestBytes {
		return errors.New("Conversation exceeded the 2 MiB request limit")
	}
	if c.cfg.MaxCostUSD == 0 {
		return nil
	}
	// Every UTF-8 byte can be a token in the conservative bound. Count serialized
	// schema, tool metadata and escaped JSON as well; double it and reserve an
	// additional 4096 tokens for provider framing. Never assume a cache discount.
	inputBound := float64(2*len(body) + 4096)
	if c.cfg.Provider == "claude" {
		// The static system prefix uses a 5-minute cache breakpoint. Reserve
		// cache-write pricing for all input, without assuming a cache hit.
		inputBound *= 1.25
	}
	reservation := (inputBound*c.cfg.InputPricePerMillion + float64(c.cfg.MaxOutputTokens)*c.cfg.OutputPricePerMillion) / 1e6
	if usage.CostUSD+reservation > c.cfg.MaxCostUSD {
		return fmt.Errorf("Per-file cost limit: spent $%.6f plus conservative next-request reservation $%.6f exceeds $%.6f", usage.CostUSD, reservation, c.cfg.MaxCostUSD)
	}
	return nil
}

func (c *client) addUsage(usage *model.Usage, u *responseUsage) error {
	usage.Turns++
	if u == nil || u.InputTokens == nil || u.OutputTokens == nil {
		return unknownUsage(errors.New("LLM response omitted usage; cost cannot be verified"))
	}
	input, output, cached := *u.InputTokens, *u.OutputTokens, u.InputDetails.CachedTokens
	if input < 0 || input > 4*maxRequestBytes || output < 0 || output > 4*maxResponseBytes || cached < 0 || cached > input || u.CacheWrite5m < 0 || u.CacheWrite1h < 0 || u.CacheWrite5m > input-cached || u.CacheWrite1h > input-cached-u.CacheWrite5m {
		return unknownUsage(errors.New("LLM returned invalid token usage"))
	}
	usage.InputTokens += input
	usage.CachedTokens += cached
	usage.OutputTokens += output
	usage.CostUSD += (float64(input-cached)*c.cfg.InputPricePerMillion + float64(cached)*c.cfg.CachedInputPricePerMillion + float64(output)*c.cfg.OutputPricePerMillion) / 1e6
	// InputTokens includes cache writes. Add only their premium over the base
	// input rate already counted above (5-minute 1.25x, 1-hour 2x).
	usage.CostUSD += (float64(u.CacheWrite5m)*.25 + float64(u.CacheWrite1h)) * c.cfg.InputPricePerMillion / 1e6
	if output > c.cfg.MaxOutputTokens {
		return errors.New("LLM returned more output tokens than the request limit; no further request will be sent")
	}
	if c.cfg.MaxCostUSD > 0 && usage.CostUSD > c.cfg.MaxCostUSD {
		return errors.New("Reported LLM usage exceeds the configured cost limit; no further request will be sent")
	}
	return nil
}

func completed(res response) error {
	if res.ProtocolError != nil {
		return res.ProtocolError
	}
	if res.Error != nil {
		return errors.New("LLM response failed")
	}
	if res.Status != "completed" {
		if res.IncompleteDetails != nil {
			switch res.IncompleteDetails.Reason {
			case "max_output_tokens":
				return errors.New("LLM response reached max_output_tokens before completing the proposal")
			case "content_filter":
				return errors.New("LLM response was stopped by a content filter")
			}
		}
		return errors.New("LLM response was not completed")
	}
	return nil
}

// TestConnection sends only a fixed, harmless prompt. It does not send files,
// rules, previous migration history, or any saved conversation identifiers.
func TestConnection(ctx context.Context, cfg model.Config) (string, error) {
	cfg.MaxTurns = 1
	cfg.MaxOutputTokens = 512
	c, err := newClient(cfg)
	if err != nil {
		return "", err
	}
	defer c.http.CloseIdleConnections()
	ctx, cancel := executionContext(ctx, c.cfg.TimeoutSeconds)
	defer cancel()
	body, _ := c.connectionRequest()
	if err := c.reserve(body, model.Usage{}); err != nil {
		return "", err
	}
	res, err := c.request(ctx, body, nil)
	if err != nil {
		return "", err
	}
	if err := c.addUsage(&model.Usage{}, res.Usage); err != nil {
		return "", err
	}
	if err := completed(res); err != nil {
		return "", err
	}
	calls, text, err := parseOutput(res.Output)
	if err != nil {
		return "", err
	}
	if len(calls) != 0 || strings.TrimSpace(text) == "" {
		return "", errors.New("Connection succeeded but the deployment returned no text")
	}
	return c.providerLabel() + " API connection succeeded", nil
}
