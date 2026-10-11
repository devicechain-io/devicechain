// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-microservice/aiwire"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goodMint(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "tok", ExpiresAt: 1 << 40})
}

// fakePeers stands in for both peers the inference client talks to: user-management's
// service-token mint and ai-inference's GraphQL endpoint.
func fakePeers(t *testing.T, mint, answer http.HandlerFunc) (config.UserManagementConfiguration, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			mint(w, r)
			return
		}
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, srv.URL + "/graphql"
}

func fakeAiInference(t *testing.T, answer http.HandlerFunc) (config.UserManagementConfiguration, string) {
	return fakePeers(t, goodMint, answer)
}

func candidateAfter(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Drained first: the server notices a client that went away only once the body is
		// read, and the test server's Close waits for this handler.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"inferRuleCandidate": map[string]any{"candidate": "{}", "model": "m", "provider": "p"},
		}})
	}
}

// refusal answers as ai-inference does: a GraphQL error with a message and, when code is
// set, an extensions.code.
func refusal(message, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		e := map[string]any{"message": message}
		if code != "" {
			e["extensions"] = map[string]any{"code": code}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{e}})
	}
}

// The caller must outwait ai-inference. A model that answers after svcclient's default 10s
// bound — well inside ai-inference's own inference timeout — is a SUCCESS, not an error the
// author reads as an outage. Built through NewInferenceClient, the constructor main wires, so
// this exercises the caller's real timeout rather than a client the test assembled.
func TestInferenceClientOutwaitsTheDefaultBound(t *testing.T) {
	um, url := fakeAiInference(t, candidateAfter(11*time.Second))
	c := NewInferenceClient(um, "secret", url)

	out, err := c.Infer(context.Background(), "acme", "prompt", "")
	require.NoError(t, err, "a model slower than 10s but inside ai-inference's own timeout was cut off by the caller")
	assert.Equal(t, "{}", out.Candidate)
}

// ai-inference's refusals are classified on their extensions.code — and ONLY on it: the same
// words without the code are not a timeout or a rate limit, which is what proves the match is
// not on message text.
func TestInferenceClientClassifiesOnTheCode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		answer        http.HandlerFunc
		timedOut, rlt bool
	}{
		{"timed-out code", refusal("anything at all", aiwire.CodeTimedOut), true, false},
		{"rate-limited code", refusal("anything at all", aiwire.CodeRateLimited), false, true},
		{"timed-out words, no code", refusal("inference timed out before the model answered", ""), false, false},
		{"rate-limit words, no code", refusal("inference rate limit exceeded for this tenant", ""), false, false},
		{"a 500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			um, url := fakeAiInference(t, tc.answer)
			_, err := NewInferenceClient(um, "secret", url).Infer(context.Background(), "acme", "prompt", "")
			require.Error(t, err)
			assert.Equal(t, tc.timedOut, errors.Is(err, nldraft.ErrTimedOut), "timed out? %v", err)
			assert.Equal(t, tc.rlt, errors.Is(err, nldraft.ErrRateLimited), "rate limited? %v", err)
		})
	}
}

// A deadline on the caller's side while ai-inference is being asked is a timeout too.
func TestInferenceClientClassifiesTheCallerDeadline(t *testing.T) {
	um, url := fakeAiInference(t, candidateAfter(5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewInferenceClient(um, "secret", url).Infer(ctx, "acme", "prompt", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, nldraft.ErrTimedOut)
}

// A slow or failing user-management MINT is not the model being slow: ai-inference was never
// asked. It stays opaque (the drafter reports unavailable), even when the deadline that ended
// the wait was the caller's.
func TestInferenceClientMintFailureIsNotATimeout(t *testing.T) {
	t.Run("mint refuses", func(t *testing.T) {
		um, url := fakePeers(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusInternalServerError)
		}, candidateAfter(0))
		_, err := NewInferenceClient(um, "secret", url).Infer(context.Background(), "acme", "prompt", "")
		require.Error(t, err)
		assert.False(t, errors.Is(err, nldraft.ErrTimedOut), "%v", err)
	})
	t.Run("mint outlives the caller's deadline", func(t *testing.T) {
		release := make(chan struct{})
		um, url := fakePeers(t, func(w http.ResponseWriter, _ *http.Request) {
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}, candidateAfter(0))
		t.Cleanup(func() { close(release) }) // runs before the server's Close (LIFO)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := NewInferenceClient(um, "secret", url).Infer(ctx, "acme", "prompt", "")
		require.Error(t, err)
		assert.False(t, errors.Is(err, nldraft.ErrTimedOut), "a slow mint was reported as a slow model: %v", err)
	})
}

// timeoutNetError is a net.Error that times out WITHOUT wrapping context.DeadlineExceeded —
// a dial or read timeout as the network layer reports one.
type timeoutNetError struct{ timeout bool }

func (e timeoutNetError) Error() string   { return fmt.Sprintf("i/o timeout=%v", e.timeout) }
func (e timeoutNetError) Timeout() bool   { return e.timeout }
func (e timeoutNetError) Temporary() bool { return false }

// isTimeout's net.Error arm, on its own: a network timeout that is not a context deadline is
// still a timeout, and a network error that is not a timeout is not.
func TestIsTimeoutReadsNetErrors(t *testing.T) {
	assert.True(t, isTimeout(&net.OpError{Op: "read", Err: timeoutNetError{timeout: true}}))
	assert.False(t, isTimeout(&net.OpError{Op: "dial", Err: timeoutNetError{timeout: false}}))
	assert.True(t, isTimeout(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)))
	assert.False(t, isTimeout(context.Canceled), "the author going away is not a timeout")
	assert.False(t, isTimeout(errors.New("connection refused")))
}
