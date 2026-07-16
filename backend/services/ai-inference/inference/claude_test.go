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

// TestClaudeInferHappyPath verifies the Messages API request shape (path, version +
// key headers, model/max_tokens/system/messages body) and that the concatenated text
// content is returned with the echoed model.
func TestClaudeInferHappyPath(t *testing.T) {
	var gotPath, gotVersion, gotKey, gotContentType string
	var gotBody claudeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		gotContentType = r.Header.Get("content-type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-opus-4-8","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}`))
	}))
	defer srv.Close()

	p, err := newClaudeProvider(srv.Client(), srv.URL, "claude-opus-4-8", "sk-secret", 1234)
	require.NoError(t, err)

	out, err := p.Infer(context.Background(), Input{System: "be terse", Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "hello world", out.Candidate)
	assert.Equal(t, "claude-opus-4-8", out.Model)

	assert.Equal(t, anthropicMessagesPath, gotPath)
	assert.Equal(t, anthropicVersion, gotVersion)
	assert.Equal(t, "sk-secret", gotKey)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "claude-opus-4-8", gotBody.Model)
	assert.Equal(t, 1234, gotBody.MaxTokens)
	assert.Equal(t, "be terse", gotBody.System)
	require.Len(t, gotBody.Messages, 1)
	assert.Equal(t, "user", gotBody.Messages[0].Role)
	assert.Equal(t, "hi", gotBody.Messages[0].Content)
}

// TestClaudeInferNon2xx surfaces a bounded provider error snippet (and never the key).
func TestClaudeInferNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	p, err := newClaudeProvider(srv.Client(), srv.URL, "claude-opus-4-8", "sk-bad", 16)
	require.NoError(t, err)

	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.NotContains(t, err.Error(), "sk-bad", "the API key must never appear in an error")
}

// TestClaudeInferEmptyContent rejects a 2xx response with no text blocks.
func TestClaudeInferEmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","content":[]}`))
	}))
	defer srv.Close()

	p, err := newClaudeProvider(srv.Client(), srv.URL, "m", "k", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no text content")
}

// TestClaudeRedirectNotFollowed proves a 3xx from the endpoint is NOT followed onto
// another target (the SSRF posture) — it surfaces as a non-2xx error instead.
func TestClaudeRedirectNotFollowed(t *testing.T) {
	var innerHit bool
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerHit = true
		_, _ = w.Write([]byte(`{"model":"m","content":[{"type":"text","text":"leaked"}]}`))
	}))
	defer inner.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL+anthropicMessagesPath, http.StatusFound)
	}))
	defer srv.Close()

	p, err := newClaudeProvider(srv.Client(), srv.URL, "m", "k", 16)
	require.NoError(t, err)
	_, err = p.Infer(context.Background(), Input{Prompt: "hi"})
	require.Error(t, err)
	assert.False(t, innerHit, "a redirect must not be followed onto another target")
}

// TestClaudeBadEndpoint rejects a non-http(s) endpoint at construction.
func TestClaudeBadEndpoint(t *testing.T) {
	_, err := newClaudeProvider(nil, "ftp://example.com", "m", "k", 16)
	require.ErrorIs(t, err, ErrUnavailable)
}

// TestClaudeDefaultEndpoint uses the public Anthropic base when no endpoint is set.
func TestClaudeDefaultEndpoint(t *testing.T) {
	p, err := newClaudeProvider(nil, "", "m", "k", 16)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(p.url, anthropicDefaultBase+anthropicMessagesPath))
}
