package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

func normalizeProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "azure"
	}
	switch provider {
	case "azure", "openai", "claude":
		return provider, nil
	default:
		return "", errors.New("Provider must be azure, openai or claude")
	}
}

func (c *client) providerLabel() string {
	switch c.cfg.Provider {
	case "openai":
		return "OpenAI"
	case "claude":
		return "Claude"
	default:
		return "Azure"
	}
}

// NormalizeEndpoint preserves the original Azure endpoint interface.
func NormalizeEndpoint(endpoint string) (string, error) {
	return NormalizeProviderEndpoint("azure", endpoint)
}

// NormalizeProviderEndpoint accepts a resource/base URL or a full API endpoint.
// Only OpenAI and Claude have public defaults; Azure requires a resource URL.
// Custom HTTPS gateways are supported, but redirects never carry credentials.
func NormalizeProviderEndpoint(provider, endpoint string) (string, error) {
	provider, err := normalizeProvider(provider)
	if err != nil {
		return "", err
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		switch provider {
		case "openai":
			endpoint = "https://api.openai.com/v1/responses"
		case "claude":
			endpoint = "https://api.anthropic.com/v1/messages"
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", errors.New("LLM endpoint must be an absolute HTTPS URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("LLM endpoint cannot contain credentials, query parameters, or a fragment")
	}
	// A literal loopback IP is permitted for local providers and httptest.
	// Hostnames such as localhost are excluded to avoid DNS rebinding.
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return "", errors.New("LLM endpoint must use HTTPS (HTTP is allowed only for a literal loopback IP)")
	}
	if strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "\\") || strings.Contains(u.Path, "//") {
		return "", errors.New("LLM endpoint path must not contain encoded or ambiguous segments")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", errors.New("LLM endpoint path cannot contain dot segments")
		}
	}
	p := strings.TrimSuffix(u.Path, "/")
	switch provider {
	case "azure":
		switch {
		case p == "":
			u.Path = "/openai/v1/responses"
		case strings.HasSuffix(p, "/responses"):
			u.Path = p
		case strings.HasSuffix(p, "/openai/v1"):
			u.Path = p + "/responses"
		default:
			return "", errors.New("Use the Azure resource URL, a URL ending in /openai/v1, or the full /responses endpoint")
		}
	case "openai", "claude":
		method := "responses"
		if provider == "claude" {
			method = "messages"
		}
		switch {
		case p == "":
			u.Path = "/v1/" + method
		case strings.HasSuffix(p, "/"+method):
			u.Path = p
		case strings.HasSuffix(p, "/v1"):
			u.Path = p + "/" + method
		default:
			return "", fmt.Errorf("Use the provider base URL, a URL ending in /v1, or the full /%s endpoint", method)
		}
	}
	u.RawPath = ""
	return u.String(), nil
}

// Request construction and history encoding are the only protocol differences
// in the runner. Tool permissions, budgets, parsing and retry limits stay shared.
func (c *client) migrationRequest(history []json.RawMessage, prompt string) ([]byte, error) {
	if c.cfg.Provider == "claude" {
		tools := []map[string]any{}
		for _, tool := range toolDefinitions() {
			tools = append(tools, map[string]any{"name": tool["name"], "description": tool["description"], "strict": true, "input_schema": tool["parameters"]})
		}
		format := proposalFormat()["format"].(map[string]any)
		return json.Marshal(map[string]any{
			"model": c.cfg.Deployment, "max_tokens": c.cfg.MaxOutputTokens,
			"system":   []any{map[string]any{"type": "text", "text": prompt, "cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"}}},
			"messages": history, "tools": tools,
			"tool_choice":   map[string]any{"type": "auto", "disable_parallel_tool_use": true},
			"output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": format["schema"]}},
		})
	}
	return json.Marshal(map[string]any{
		"model": c.cfg.Deployment, "store": false, "instructions": prompt,
		"input": history, "tools": toolDefinitions(), "parallel_tool_calls": false,
		"max_output_tokens": c.cfg.MaxOutputTokens, "text": proposalFormat(), "include": []string{"reasoning.encrypted_content"},
	})
}

func (c *client) connectionRequest() ([]byte, error) {
	const prompt = "This is a connection check. Reply with OK only."
	if c.cfg.Provider == "claude" {
		return json.Marshal(map[string]any{"model": c.cfg.Deployment, "max_tokens": c.cfg.MaxOutputTokens, "messages": []any{map[string]any{"role": "user", "content": prompt}}})
	}
	return json.Marshal(map[string]any{"model": c.cfg.Deployment, "store": false, "max_output_tokens": c.cfg.MaxOutputTokens, "input": prompt})
}

type toolResult struct {
	CallID  string
	Content string
	IsError bool
}

func (c *client) appendTurn(history []json.RawMessage, res response, results []toolResult) []json.RawMessage {
	if c.cfg.Provider == "claude" {
		blocks := []any{}
		for _, result := range results {
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": result.CallID, "content": result.Content, "is_error": result.IsError})
		}
		return append(history, res.NativeContinuation, raw(map[string]any{"role": "user", "content": blocks}))
	}
	history = append(history, res.Output...)
	for _, result := range results {
		history = append(history, raw(map[string]any{"type": "function_call_output", "call_id": result.CallID, "output": result.Content}))
	}
	return history
}

func (c *client) decodeResponse(data []byte) (response, error) {
	if c.cfg.Provider == "claude" {
		return decodeClaudeResponse(data)
	}
	var out response
	err := json.Unmarshal(data, &out)
	return out, err
}

type claudeUsage struct {
	InputTokens   *int `json:"input_tokens"`
	OutputTokens  *int `json:"output_tokens"`
	CacheRead     int  `json:"cache_read_input_tokens"`
	CacheCreation int  `json:"cache_creation_input_tokens"`
	CacheDetails  *struct {
		FiveMinutes int `json:"ephemeral_5m_input_tokens"`
		OneHour     int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func normalizeClaudeUsage(u *claudeUsage) *responseUsage {
	if u == nil || u.InputTokens == nil || u.OutputTokens == nil {
		return nil
	}
	// Claude's input_tokens excludes both cache writes and reads. Check each
	// term before adding so overflow or malformed usage cannot reduce the cost.
	for _, n := range []int{*u.InputTokens, u.CacheRead, u.CacheCreation} {
		if n < 0 || n > 4*maxRequestBytes {
			return nil
		}
	}
	total := *u.InputTokens + u.CacheRead + u.CacheCreation
	five, hour := u.CacheCreation, 0 // The request explicitly selects a 5m TTL.
	if u.CacheDetails != nil {
		five, hour = u.CacheDetails.FiveMinutes, u.CacheDetails.OneHour
		if five < 0 || hour < 0 || five > u.CacheCreation || hour != u.CacheCreation-five {
			return nil
		}
	}
	out := &responseUsage{InputTokens: &total, OutputTokens: u.OutputTokens, CacheWrite5m: five, CacheWrite1h: hour}
	out.InputDetails.CachedTokens = u.CacheRead
	return out
}

func decodeClaudeResponse(data []byte) (response, error) {
	var message struct {
		Type       string            `json:"type"`
		Role       string            `json:"role"`
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      *claudeUsage      `json:"usage"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return response{}, err
	}
	out := response{Usage: normalizeClaudeUsage(message.Usage)}
	if message.Type != "message" || message.Role != "assistant" {
		out.ProtocolError = errors.New("Claude returned an invalid assistant message")
		return out, nil
	}
	out.NativeContinuation = raw(map[string]any{"role": "assistant", "content": message.Content})
	if message.StopReason == "end_turn" || message.StopReason == "tool_use" {
		out.Status = "completed"
	} else {
		// A refusal or max_tokens response may still contain apparently valid
		// JSON. Its reported usage is counted, but no edits or tools are accepted.
		switch message.StopReason {
		case "max_tokens":
			out.ProtocolError = errors.New("Claude reached max_tokens before completing the proposal")
		case "refusal":
			out.ProtocolError = errors.New("Claude refused to generate a migration proposal")
		case "model_context_window_exceeded":
			out.ProtocolError = errors.New("Claude reached its model context window limit")
		default:
			out.ProtocolError = errors.New("Claude response was not completed")
		}
		return out, nil
	}
	toolCount := 0
	for _, block := range message.Content {
		var content struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Signature string          `json:"signature"`
			Data      string          `json:"data"`
		}
		if err := json.Unmarshal(block, &content); err != nil {
			out.ProtocolError = errors.New("Claude returned malformed content")
			return out, nil
		}
		switch content.Type {
		case "text":
			out.Output = append(out.Output, raw(map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": content.Text}}}))
		case "tool_use":
			toolCount++
			out.Output = append(out.Output, raw(functionCall{Type: "function_call", CallID: content.ID, Name: content.Name, Arguments: string(content.Input)}))
		case "thinking":
			if content.Signature == "" {
				out.ProtocolError = errors.New("Claude thinking content has no continuation signature")
				return out, nil
			}
		case "redacted_thinking":
			if content.Data == "" {
				out.ProtocolError = errors.New("Claude returned invalid redacted thinking content")
				return out, nil
			}
		default:
			out.ProtocolError = errors.New("Claude returned an unsupported content type")
			return out, nil
		}
	}
	if (message.StopReason == "tool_use") != (toolCount > 0) {
		out.ProtocolError = errors.New("Claude stop reason and tool calls do not agree")
	}
	return out, nil
}
