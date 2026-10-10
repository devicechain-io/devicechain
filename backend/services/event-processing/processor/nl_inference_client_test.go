// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/nldraft"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiInferenceTimedOutText is ai-inference's ErrTimedOut message as it crosses the wire. It is
// written out here, not imported, because it IS the wire contract: the two services are separate
// modules and this is what event-processing actually receives.
const aiInferenceTimedOutText = "inference timed out before the model answered; retry, or shorten the request"

// fakeAiInference stands in for both peers the inference client talks to: user-management's
// service-token mint and ai-inference's GraphQL endpoint, whose behaviour is answer.
func fakeAiInference(t *testing.T, answer http.HandlerFunc) (config.UserManagementConfiguration, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "tok", ExpiresAt: 1 << 40})
			return
		}
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, srv.URL + "/graphql"
}

func candidateAfter(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

// The caller must outwait ai-inference. A model that answers after svcclient's default 10s
// bound — well inside ai-inference's 60s default inference timeout — is a SUCCESS, not an error
// the author reads as an outage. Built through NewInferenceClient, the constructor main wires,
// so this exercises the caller's real timeout rather than a client the test assembled.
func TestInferenceClientOutwaitsTheDefaultBound(t *testing.T) {
	um, url := fakeAiInference(t, candidateAfter(11*time.Second))
	c := NewInferenceClient(um, "secret", url)

	out, err := c.Infer(context.Background(), "acme", "prompt", "")
	require.NoError(t, err, "a model slower than 10s but inside ai-inference's own timeout was cut off by the caller")
	assert.Equal(t, "{}", out.Candidate)
}

// ai-inference's own "timed out" answer is classified as a timeout, not left opaque (which the
// drafter would report as "unavailable").
func TestInferenceClientClassifiesTheCalleeTimeout(t *testing.T) {
	um, url := fakeAiInference(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": aiInferenceTimedOutText}}})
	})
	_, err := NewInferenceClient(um, "secret", url).Infer(context.Background(), "acme", "prompt", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, nldraft.ErrTimedOut)
	assert.False(t, errors.Is(err, nldraft.ErrRateLimited))
}

// A deadline on the caller's side is a timeout too.
func TestInferenceClientClassifiesTheCallerDeadline(t *testing.T) {
	um, url := fakeAiInference(t, candidateAfter(5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewInferenceClient(um, "secret", url).Infer(ctx, "acme", "prompt", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, nldraft.ErrTimedOut)
}

// The counterweight: a peer that is down or failing is NOT a timeout — it stays opaque, and the
// drafter reports it as unavailable.
func TestInferenceClientLeavesOtherFailuresOpaque(t *testing.T) {
	um, url := fakeAiInference(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := NewInferenceClient(um, "secret", url).Infer(context.Background(), "acme", "prompt", "")
	require.Error(t, err)
	assert.False(t, errors.Is(err, nldraft.ErrTimedOut), "a 500 misread as a timeout: %v", err)
	assert.False(t, errors.Is(err, nldraft.ErrRateLimited))
}
