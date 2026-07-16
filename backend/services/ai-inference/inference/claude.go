// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/httpsink"
)

const (
	// anthropicDefaultBase is the public Anthropic API base URL, used when a provider
	// row carries no Endpoint override.
	anthropicDefaultBase = "https://api.anthropic.com"
	// anthropicMessagesPath is the Messages API path appended to the base URL.
	anthropicMessagesPath = "/v1/messages"
	// anthropicVersion is the required Anthropic API version header.
	anthropicVersion = "2023-06-01"
	// maxInferenceResponseBytes caps the response read so a misbehaving endpoint cannot
	// exhaust memory. A rule candidate is small; 1 MiB is generous headroom.
	maxInferenceResponseBytes = 1 << 20
	// errSnippetBytes bounds the provider error body echoed for diagnosis.
	errSnippetBytes = 512
)

// noFollowRedirect returns a 3xx as the response rather than following it, so an
// endpoint cannot 302 the request onto a different (internal) target — the same SSRF
// posture core/httpsink enforces for tenant webhooks. The resulting non-2xx is
// reported as a provider error.
func noFollowRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// defaultInferenceClient is the shared outbound client for inference calls. Each call
// is bounded by a context deadline (from the configured inference timeout), so no
// client-level Timeout is set; only the no-redirect policy is pinned.
var defaultInferenceClient = &http.Client{CheckRedirect: noFollowRedirect}

// claudeProvider calls the Anthropic Messages API. It is constructed by the resolver
// from a provider row (endpoint + model), the resolved API key, and the configured
// output-token cap. The key lives only in this struct and the request header — it is
// never placed in the URL, the request/response body I format, or any error text.
type claudeProvider struct {
	client    *http.Client
	url       string
	apiKey    string
	model     string
	maxTokens int
}

// newClaudeProvider builds a Claude provider. endpoint overrides the API base URL
// (empty ⇒ the public Anthropic API); the Messages path is appended and the resulting
// URL is validated http/https with a host and no embedded credentials (httpsink's
// SSRF-lite check — the operator sets the endpoint under ai:admin, so a typo is caught
// here). A nil client uses the package default.
func newClaudeProvider(client *http.Client, endpoint, model, apiKey string, maxTokens int) (*claudeProvider, error) {
	base := strings.TrimSpace(endpoint)
	if base == "" {
		base = anthropicDefaultBase
	}
	full := strings.TrimRight(base, "/") + anthropicMessagesPath
	if _, err := httpsink.ValidateURL(full); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if client == nil {
		client = defaultInferenceClient
	} else {
		// Force the no-redirect SSRF policy onto a caller-supplied client too, so the
		// guarantee never depends on the caller having set CheckRedirect (mirrors
		// httpsink.Send). Clone (shallow) to override only the redirect policy; the
		// Transport/Jar are shared, which is safe.
		clone := *client
		clone.CheckRedirect = noFollowRedirect
		client = &clone
	}
	return &claudeProvider{client: client, url: full, apiKey: apiKey, model: model, maxTokens: maxTokens}, nil
}

// claudeRequest is the Messages API request body.
type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// claudeResponse is the subset of the Messages API response we read.
type claudeResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Infer runs one prompt through the Messages API and returns the concatenated text
// content. The call is bounded by ctx. A transport error, a non-2xx, or an empty
// completion is an error; none of them can carry the API key (it is only ever set as a
// request header, never formatted into a URL/body/error).
func (p *claudeProvider) Infer(ctx context.Context, in Input) (Output, error) {
	body, err := json.Marshal(claudeRequest{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		System:    in.System,
		Messages:  []claudeMessage{{Role: "user", Content: in.Prompt}},
	})
	if err != nil {
		return Output{}, fmt.Errorf("marshal inference request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Output{}, fmt.Errorf("build inference request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("x-api-key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		// The URL carries no credential (validated userinfo-free at build), so it is safe
		// to include; the key is a header, never echoed here.
		return Output{}, fmt.Errorf("call inference provider %s: %w", p.url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxInferenceResponseBytes))
	if err != nil {
		return Output{}, fmt.Errorf("read inference response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Endpoint + key are both operator-owned at the same trust tier (unlike a
		// tenant-configured webhook), and the key is never in the body, so a bounded
		// snippet is safe to surface and aids diagnosis (e.g. a bad model id or an
		// expired key returns a structured Anthropic error).
		return Output{}, fmt.Errorf("inference provider returned %d: %s", resp.StatusCode, snippet(raw))
	}

	var parsed claudeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Output{}, fmt.Errorf("decode inference response: %w", err)
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	candidate := text.String()
	if strings.TrimSpace(candidate) == "" {
		return Output{}, fmt.Errorf("inference provider returned no text content")
	}
	return Output{Candidate: candidate, Model: parsed.Model}, nil
}

// snippet returns a whitespace-trimmed, length-bounded view of a provider error body
// for diagnostics.
func snippet(raw []byte) string {
	if len(raw) > errSnippetBytes {
		raw = raw[:errSnippetBytes]
	}
	return strings.TrimSpace(string(raw))
}
