// Package agent implements resumable, isolated provider API tool loops.
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
	"regexp"
	"strconv"
	"strings"
	"time"

	"onebyone/internal/azureauth"
	"onebyone/internal/model"
)

// Large tool results are paged, never rejected for their total size.
const toolPreviewBytes = 96 << 10

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
	endpoint      string
	credential    string
	authMode      string
	cfg           model.Config
	http          *http.Client
	outputTokens  int
	outputCeiling int
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
	for _, price := range []float64{cfg.InputPricePerMillion, cfg.CachedInputPricePerMillion, cfg.OutputPricePerMillion} {
		if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
			return cfg, errors.New("Token prices must be finite nonnegative numbers")
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
	return &client{endpoint: endpoint, credential: credential, authMode: auth, cfg: cfg, outputTokens: 8192, http: &http.Client{
		Transport:     transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type response struct {
	// Messages uses a native assistant content array for continuation. It is
	// never serialized as Responses input and never escapes this file's Run.
	RequestAttempts    int               `json:"-"`
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
	Uncertain    bool `json:"-"`
	Requests     int  `json:"-"`
	CacheWrite5m int  `json:"-"`
	CacheWrite1h int  `json:"-"`
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	InputDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// request retries transient failures until cancelled. An ambiguous POST remains
// pending in the durable journal; a later successful response retains the fact
// that usage from earlier attempts could not be measured.
func (c *client) request(ctx context.Context, body []byte, log func(string)) (result response, err error) {
	uncertain, requests := false, 0
	defer func() {
		result.RequestAttempts = requests
		if err != nil && uncertain && !IsUsageUnknown(err) {
			err = unknownUsage(err)
		}
	}()
	for retry := 0; ; retry++ {
		if err := ctx.Err(); err != nil {
			if uncertain {
				return response{}, unknownUsage(err)
			}
			return response{}, err
		}
		credential, err := c.requestCredential(ctx)
		if err != nil {
			return response{}, &fatalError{err}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return response{}, &fatalError{errors.New("Cannot construct LLM request")}
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
		requests++
		res, sendErr := c.http.Do(req)
		if sendErr != nil {
			uncertain = true
			if err := waitProviderRetry(ctx, "LLM transport interrupted; usage may have been billed", "", retry, log); err != nil {
				return response{}, unknownUsage(err)
			}
			continue
		}
		data, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			uncertain = true
			if err := waitProviderRetry(ctx, "LLM response interrupted; usage may have been billed", res.Header.Get("Retry-After"), retry, log); err != nil {
				return response{}, unknownUsage(err)
			}
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500 {
			if res.StatusCode != http.StatusTooManyRequests {
				uncertain = true
			}
			if err := waitProviderRetry(ctx, fmt.Sprintf("LLM HTTP %d", res.StatusCode), res.Header.Get("Retry-After"), retry, log); err != nil {
				if uncertain {
					return response{}, unknownUsage(err)
				}
				return response{}, err
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			var envelope struct {
				Error struct {
					Code    json.RawMessage `json:"code"`
					Type    string          `json:"type"`
					Message string          `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &envelope)
			code := safeIdentifier(strings.Trim(string(envelope.Error.Code), "\""))
			if code == "" && c.cfg.Provider == "claude" {
				code = safeIdentifier(envelope.Error.Type)
			}
			if credential != "" && strings.Contains(code, credential) {
				code = ""
			}
			message := strings.ToLower(envelope.Error.Message)
			if res.StatusCode == 413 || strings.Contains(code, "context_length") || strings.Contains(code, "context_window") || strings.Contains(message, "prompt is too long") || strings.Contains(message, "maximum context length") {
				return response{}, &ContextLimitError{Reason: "LLM input exceeds the provider context; reduce the next request"}
			}
			if c.cfg.Provider == "claude" && res.StatusCode == 400 && c.learnOutputCeiling(message) {
				var payload map[string]any
				if json.Unmarshal(body, &payload) == nil {
					payload["max_tokens"] = c.outputLimit()
					body, _ = json.Marshal(payload)
					continue
				}
			}
			httpErr := fmt.Errorf("LLM HTTP %d", res.StatusCode)
			if code != "" {
				httpErr = fmt.Errorf("LLM HTTP %d (%s)", res.StatusCode, code)
			}
			return response{}, &fatalError{httpErr}
		}
		out, decodeErr := c.decodeResponse(data)
		if decodeErr != nil {
			uncertain = true
			if err := waitProviderRetry(ctx, "LLM returned invalid response JSON; usage may have been billed", "", retry, log); err != nil {
				return response{}, unknownUsage(err)
			}
			continue
		}
		if out.Usage == nil {
			out.Usage = &responseUsage{Uncertain: true}
		}
		out.Usage.Uncertain = out.Usage.Uncertain || uncertain
		out.Usage.Requests = requests
		return out, nil
	}
}

func waitProviderRetry(ctx context.Context, reason, header string, retry int, log func(string)) error {
	delay, _ := retryDelay(header, retry)
	if log != nil {
		log(fmt.Sprintf("%s; retry %d after %s", reason, retry+1, delay))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// These are protocol constraints, not workspace budgets. Responses supports
// omission of max_output_tokens; Messages requires an explicit max_tokens.
func (c *client) outputLimit() int {
	if c.outputTokens <= 0 {
		c.outputTokens = 8192
	}
	return c.outputTokens
}
func (c *client) growOutputLimit() bool {
	if c.cfg.Provider != "claude" {
		return false
	}
	if c.outputLimit() > int(^uint(0)>>1)/2 {
		return false
	}
	next := c.outputLimit() * 2
	if c.outputCeiling > 0 && next > c.outputCeiling {
		next = c.outputCeiling
	}
	if next <= c.outputLimit() {
		return false
	}
	c.outputTokens = next
	return true
}

var outputCeilingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`max_tokens\s*[:=]?\s*\d+\s*>\s*(\d+)`),
	regexp.MustCompile(`max_tokens[^.\n]*?(?:at most|maximum(?: of)?|less than or equal to|<=)\s*(\d+)`),
	regexp.MustCompile(`max_tokens[^.\n]*?between\s+1\s+and\s+(\d+)`),
}

func (c *client) learnOutputCeiling(message string) bool {
	var match []string
	for _, pattern := range outputCeilingPatterns {
		if match = pattern.FindStringSubmatch(message); len(match) == 2 {
			break
		}
	}
	if len(match) != 2 {
		return false
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 1 || n >= c.outputLimit() {
		return false
	}
	c.outputCeiling, c.outputTokens = n, n
	return true
}

type OutputLimitError struct{ Reason string }

func (e *OutputLimitError) Error() string { return e.Reason }
func IsOutputLimit(err error) bool        { var target *OutputLimitError; return errors.As(err, &target) }

type ContextLimitError struct{ Reason string }

func (e *ContextLimitError) Error() string { return e.Reason }
func IsContextLimit(err error) bool        { var target *ContextLimitError; return errors.As(err, &target) }

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
	delay := time.Duration(min(retry+1, 30)) * time.Second
	if header != "" {
		if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
			if seconds < 0 {
				seconds = 0
			}
			maxSeconds := int64((1<<63 - 1) / int64(time.Second))
			delay = time.Duration(min(seconds, maxSeconds)) * time.Second
		} else if when, err := http.ParseTime(header); err == nil {
			delay = max(0, time.Until(when))
		}
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

// Accounting is observational; it never prevents another repair request.
func (c *client) reserve(_ []byte, _ model.Usage) error { return nil }

func (c *client) addUsage(usage *model.Usage, u *responseUsage) error {
	usage.Turns++
	if u == nil {
		usage.Uncertain = true
		return nil
	}
	if u.Requests > 1 {
		usage.Turns += u.Requests - 1
	}
	usage.Uncertain = usage.Uncertain || u.Uncertain
	if u.InputTokens == nil || u.OutputTokens == nil {
		usage.Uncertain = true
		return nil
	}
	input, output, cached := *u.InputTokens, *u.OutputTokens, u.InputDetails.CachedTokens
	if input < 0 || output < 0 || cached < 0 || cached > input || u.CacheWrite5m < 0 || u.CacheWrite1h < 0 || u.CacheWrite5m > input-cached || u.CacheWrite1h > input-cached-u.CacheWrite5m {
		usage.Uncertain = true
		return nil
	}
	usage.InputTokens += input
	usage.CachedTokens += cached
	usage.OutputTokens += output
	usage.CostUSD += (float64(input-cached)*c.cfg.InputPricePerMillion + float64(cached)*c.cfg.CachedInputPricePerMillion + float64(output)*c.cfg.OutputPricePerMillion) / 1e6
	usage.CostUSD += (float64(u.CacheWrite5m)*.25 + float64(u.CacheWrite1h)) * c.cfg.InputPricePerMillion / 1e6
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
				return &OutputLimitError{Reason: "LLM response reached max_output_tokens before completing the proposal"}
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
	c, err := newClient(cfg)
	if err != nil {
		return "", err
	}
	defer c.http.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
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
