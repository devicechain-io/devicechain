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
	"github.com/rs/zerolog/log"
)

// openAIChatCompletionsPath is the chat-completions path appended to the configured base
// URL. It is the one endpoint this provider uses; every server in the family serves it at
// this path, which is what "OpenAI-compatible" names.
const openAIChatCompletionsPath = "/v1/chat/completions"

// openAICompatibleProvider calls any endpoint speaking the OpenAI chat-completions API —
// vLLM, Ollama, DeepSeek, llama.cpp's server, a gateway in front of several.
//
// 🔑 IT IS A SECOND CASE IN build(), NOT A SECOND SEAM. Everything above Provider is
// unchanged: the resolution cascade, the consent gate, the rate ceiling, the key
// resolution and the bounds all run exactly as they do for the shipped Claude provider,
// because they are decided before a Provider is constructed at all. What is different is
// below this line — the request shape, the auth header, and the response fields.
//
// 🔴 THE KEY LIVES ONLY IN THIS STRUCT AND THE Authorization HEADER. It is never in the
// URL, the body, an error, or a log line — the same discipline the Claude provider keeps,
// for the same reason: this provider's endpoint is one an operator typed, so the response
// body it returns is not ours and may reflect what we sent it.
type openAICompatibleProvider struct {
	client    *http.Client
	url       string
	apiKey    string
	model     string
	maxTokens int
}

// newOpenAICompatibleProvider builds the provider. endpoint is REQUIRED and is the API
// base URL; the chat-completions path is appended and the result validated http/https,
// with a host and no embedded credentials.
//
// 🔴 THERE IS NO DEFAULT BASE URL TO FALL BACK ON, AND THAT IS WHY THE EMPTY CASE IS AN
// ERROR RATHER THAN A DEFAULT. This kind is defined by its address: an empty endpoint
// would splice a bare path onto nothing and produce a relative URL, which is a request to
// whatever the process happens to resolve — with the tenant's prompt and a real
// credential attached. The write path refuses such a row (validateEndpointForKind); this
// is the seam that catches one that got in any other way.
//
// 🔑 THE SSRF POSTURE IS THE SHARED ONE, NOT A SECOND HAND-ROLLED COPY. core/httpsink is
// where this platform's outbound-URL hardening lives — ValidateURL (http/https, a host,
// no userinfo) plus the no-redirect policy that stops an endpoint 302-ing the request onto
// a different target. The Claude provider uses exactly these, and so does the connectors
// service's webhook sink; reusing them is what keeps the guarantee from drifting into
// three versions.
//
// 🔴 WHAT IS DELIBERATELY *NOT* APPLIED IS ADDRESS BLOCKING, and the reason is the whole
// point of this kind. The connectors service additionally refuses destinations that
// resolve to private or link-local addresses, because there the destination is chosen by
// a TENANT and reaching inside the cluster is the attack. Here the endpoint is chosen by
// an OPERATOR under ai:admin against their own instance config, and an in-cluster address
// is the intended, most common deployment — a vLLM service on a private ClusterIP. A
// private-address block would refuse the self-hosted case this kind exists to serve while
// protecting nobody from a caller who already administers the instance.
func newOpenAICompatibleProvider(client *http.Client, endpoint, model, apiKey string, maxTokens int) (*openAICompatibleProvider, error) {
	base := strings.TrimSpace(endpoint)
	if base == "" {
		return nil, fmt.Errorf("%w: an openai-compatible provider has no default API base URL, "+
			"so its endpoint must be configured", ErrUnavailable)
	}
	full := strings.TrimRight(base, "/") + openAIChatCompletionsPath
	if _, err := httpsink.ValidateURL(full); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if client == nil {
		client = defaultInferenceClient
	} else {
		// Force the no-redirect policy onto a caller-supplied client, so the guarantee
		// never depends on the caller having set CheckRedirect (mirrors the Claude
		// provider and httpsink.Send). Shallow clone: only the redirect policy is
		// overridden, the Transport/Jar are shared, which is safe.
		clone := *client
		clone.CheckRedirect = noFollowRedirect
		client = &clone
	}
	return &openAICompatibleProvider{
		client: client, url: full, apiKey: apiKey, model: model, maxTokens: maxTokens,
	}, nil
}

// openAIRequest is the chat-completions request body.
//
// The system prompt is carried as a leading message with role "system" rather than as a
// top-level field, because that is the shape every server in this family accepts —
// including the local ones, which mostly implement the message list and little else.
type openAIRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse is the subset of the chat-completions response we read.
type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	// Usage is what the call cost. Local servers report it inconsistently, so an absent
	// block reads as zero — which Output documents as UNKNOWN, never as free.
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Infer runs one prompt through the chat-completions API and returns the first choice's
// text. The call is bounded by ctx. A transport error, a non-2xx, or an empty completion
// is an error, and none of them can carry the API key.
func (p *openAICompatibleProvider) Infer(ctx context.Context, in Input) (Output, error) {
	messages := make([]openAIMessage, 0, 2)
	if in.System != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: in.System})
	}
	messages = append(messages, openAIMessage{Role: "user", Content: in.Prompt})

	body, err := json.Marshal(openAIRequest{
		Model: p.model, MaxTokens: p.maxTokens, Messages: messages,
	})
	if err != nil {
		return Output{}, fmt.Errorf("marshal inference request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Output{}, fmt.Errorf("build inference request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+p.apiKey)

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
		// 🔴 NEVER SURFACE THE RESPONSE BODY. A key is on the wire, the body is the
		// ENDPOINT's rather than ours, and a reflecting proxy or gateway error page can
		// echo the request headers straight into a tenant-facing error. A redacted
		// snippet is logged for the operator; the caller learns only the status.
		log.Warn().Int("status", resp.StatusCode).Str("body", redact(snippet(raw), p.apiKey)).
			Msg("Inference provider returned a non-success status")
		return Output{}, fmt.Errorf("inference provider returned %d", resp.StatusCode)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Output{}, fmt.Errorf("decode inference response: %w", err)
	}
	var text strings.Builder
	for _, choice := range parsed.Choices {
		text.WriteString(choice.Message.Content)
	}
	// Scrub any occurrence of the key from the candidate before it is returned verbatim,
	// against a purpose-built reflecting endpoint.
	candidate := redact(text.String(), p.apiKey)
	if strings.TrimSpace(candidate) == "" {
		return Output{}, fmt.Errorf("inference provider returned no text content")
	}
	return Output{
		Candidate:    candidate,
		Model:        parsed.Model,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
}
