// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The request shape is what "OpenAI-compatible" means, so it is asserted rather than
// assumed: the chat-completions path, a bearer Authorization header, and the system
// prompt as a leading message (not a top-level field — the local servers implement the
// message list and little else).
func TestOpenAICompatibleInferHappyPath(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotContentType = r.Header.Get("content-type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen","choices":[{"message":{"content":"hello world"}}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "qwen", "sk-secret", 1234)
	require.NoError(t, err)

	out, err := p.Infer(context.Background(), Input{System: "be terse", Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "hello world", out.Candidate)
	assert.Equal(t, "qwen", out.Model)
	// Usage is the only way inference spend is observable at all, so a dropped block
	// would make the spend metrics read zero forever.
	assert.Equal(t, 11, out.InputTokens)
	assert.Equal(t, 7, out.OutputTokens)

	assert.Equal(t, openAIChatCompletionsPath, gotPath)
	assert.Equal(t, "Bearer sk-secret", gotAuth)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "qwen", gotBody.Model)
	assert.Equal(t, 1234, gotBody.MaxTokens)
	require.Len(t, gotBody.Messages, 2)
	assert.Equal(t, "system", gotBody.Messages[0].Role)
	assert.Equal(t, "be terse", gotBody.Messages[0].Content)
	assert.Equal(t, "user", gotBody.Messages[1].Role)
	assert.Equal(t, "hi", gotBody.Messages[1].Content)
}

// With no system prompt there is no system message — an empty one is not the same as
// none, and some local servers reject a blank system turn.
func TestOpenAICompatibleOmitsAnEmptySystemMessage(t *testing.T) {
	var gotBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "m", "k", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.NoError(t, err)
	require.Len(t, gotBody.Messages, 1)
	assert.Equal(t, "user", gotBody.Messages[0].Role)
}

// 🔴 A KEY IS ON THE WIRE, SO THE RESPONSE BODY NEVER REACHES THE CALLER. The endpoint is
// one an operator typed and its error page is not ours; a reflecting gateway can echo the
// request headers straight back. The caller learns the status and nothing else.
func TestOpenAICompatibleNon2xxNeverSurfacesTheBodyOrTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// A deliberately hostile body: it reflects the credential it was sent.
		_, _ = w.Write([]byte(`{"error":"bad token Bearer sk-secret"}`))
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "m", "sk-secret", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-secret")
	assert.NotContains(t, err.Error(), "bad token")
	assert.Contains(t, err.Error(), "401")
}

// A candidate is returned verbatim to the caller, so a purpose-built reflecting endpoint
// must not be able to smuggle the key back through it.
func TestOpenAICompatibleRedactsTheKeyFromACandidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"your key is sk-secret"}}]}`))
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "m", "sk-secret", 16)
	require.NoError(t, err)
	out, err := p.Infer(context.Background(), Input{Prompt: "hi"})
	require.NoError(t, err)
	assert.NotContains(t, out.Candidate, "sk-secret")
}

// 🔴 THE SSRF POSTURE, REUSED RATHER THAN REWRITTEN: a 3xx is returned as the response
// instead of being followed, so an endpoint cannot bounce the request — with a real
// credential attached — onto a different target.
func TestOpenAICompatibleRedirectNotFollowed(t *testing.T) {
	var innerHit bool
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerHit = true
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"leaked"}}]}`))
	}))
	defer inner.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL+openAIChatCompletionsPath, http.StatusFound)
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "m", "k", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
	assert.False(t, innerHit, "a redirect must not be followed onto another target")
}

// 🔴 THE EMPTY ENDPOINT IS AN ERROR, NOT A DEFAULT. This kind has no built-in base URL,
// so an empty one would splice a bare path onto nothing and send a real credential and a
// tenant's prompt to whatever that resolves to. This is the second of the two checks; the
// write path carries the first.
func TestOpenAICompatibleRefusesAnEmptyEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "   "} {
		_, err := newOpenAICompatibleProvider(nil, endpoint, "m", "k", 16)
		require.ErrorIs(t, err, ErrUnavailable, "endpoint %q", endpoint)
	}
}

// The shared URL hardening rejects a non-http(s) scheme and an endpoint carrying
// userinfo credentials, which belong in the secret handle.
func TestOpenAICompatibleRejectsAMalformedEndpoint(t *testing.T) {
	for _, endpoint := range []string{"ftp://example.com", "http://user:pass@example.com"} {
		_, err := newOpenAICompatibleProvider(nil, endpoint, "m", "k", 16)
		require.ErrorIs(t, err, ErrUnavailable, "endpoint %q", endpoint)
	}
}

// An in-cluster private address is ACCEPTED, deliberately: the endpoint is operator-
// supplied under ai:admin and a self-hosted model on a ClusterIP is the case this kind
// exists to serve. Blocking private addresses here — as the tenant-facing egress
// boundary does, where the destination is tenant-chosen — would refuse the intended
// deployment while protecting nobody from a caller who already administers the instance.
func TestOpenAICompatibleAcceptsAnInClusterEndpoint(t *testing.T) {
	p, err := newOpenAICompatibleProvider(nil, "http://vllm.ai.svc.cluster.local:8000", "m", "k", 16)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(p.url, openAIChatCompletionsPath))
}

// A trailing slash on the base must not produce a doubled path separator.
func TestOpenAICompatibleTrimsATrailingSlash(t *testing.T) {
	p, err := newOpenAICompatibleProvider(nil, "https://api.deepseek.com/", "m", "k", 16)
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com"+openAIChatCompletionsPath, p.url)
}

// An endpoint that answers 200 with no text is an error, not an empty candidate handed
// to the compiler downstream.
func TestOpenAICompatibleEmptyCompletionIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p, err := newOpenAICompatibleProvider(srv.Client(), srv.URL, "m", "k", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
}
