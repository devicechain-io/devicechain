// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func proposalAnswer() map[string]any {
	return map[string]any{
		"devicesByToken":          []any{map[string]any{"token": "device", "id": "42"}},
		"deviceCommandVocabulary": map[string]any{"constrained": true, "commands": []any{map[string]any{"commandKey": "reboot", "parameterSchema": `[{"name":"x","type":"number"}]`, "encoder": nil, "impact": "NORMAL"}}},
		"validateCommandEnqueue":  map[string]any{"allowed": true, "code": nil, "reason": nil},
		"commandDispatchSchedule": map[string]any{"holds": []any{}, "refusals": []any{}},
		"deviceScopeAnchors":      []any{map[string]any{"deviceToken": "device", "anchors": []string{"customer:c"}}},
	}
}

func proposalReaderAgainst(t *testing.T, data map[string]any) *ProposalTargetReader {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(s.Close)
	return NewProposalTargetReader(s.URL)
}

func TestProposalTargetForwardsOnlyCallerBearer(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer caller.exact", r.Header.Get("Authorization"))
		var body struct {
			Query     string
			Variables map[string]any
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, proposalTargetQuery, body.Query)
		require.Equal(t, "null", body.Variables["p"])
		require.Equal(t, "2026-11-21T12:00:00Z", body.Variables["e"])
		_ = json.NewEncoder(w).Encode(map[string]any{"data": proposalAnswer()})
	}))
	defer s.Close()
	r := NewProposalTargetReader(s.URL)
	cutoff := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	out, err := r.ReadProposalTarget(auth.WithBearer(context.Background(), "caller.exact"), "device", "reboot", []byte("null"), cutoff)
	require.NoError(t, err)
	require.True(t, out.Exists && out.Constrained && out.Found && out.Allowed)
	require.EqualValues(t, 42, out.Incarnation)
	require.Equal(t, []string{"customer:c"}, out.Anchors)
	require.True(t, strings.HasPrefix(out.DefinitionFingerprint, "sha256:"))
	for _, ctx := range []context.Context{context.Background(), auth.WithClaims(auth.WithBearer(context.Background(), "service"), &auth.Claims{TokenType: auth.TokenTypeService})} {
		_, err := r.ReadProposalTarget(ctx, "device", "reboot", nil, cutoff)
		require.Error(t, err)
	}
	require.EqualValues(t, 1, calls.Load(), "no fallback request without a caller's own access bearer")
	require.Equal(t, 10*time.Second, r.HTTP.Timeout)
	transport := r.HTTP.Transport.(*http.Transport)
	require.Nil(t, transport.Proxy)
}

func TestProposalTargetNeverFollowsRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		t.Error("redirect target received a request and possibly the caller bearer")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := NewProposalTargetReader(source.URL).ReadProposalTarget(auth.WithBearer(context.Background(), "caller"), "device", "reboot", nil, time.Now())
	require.Error(t, err)
	require.Zero(t, targetCalls.Load())
}

func TestProposalTargetMissingFieldsFailClosed(t *testing.T) {
	for _, field := range []string{"devicesByToken", "deviceCommandVocabulary", "validateCommandEnqueue", "commandDispatchSchedule", "deviceScopeAnchors"} {
		t.Run(field, func(t *testing.T) {
			data := proposalAnswer()
			delete(data, field)
			_, err := proposalReaderAgainst(t, data).ReadProposalTarget(auth.WithBearer(context.Background(), "caller"), "device", "reboot", nil, time.Now())
			require.Error(t, err)
		})
	}
	for _, field := range []string{"constrained", "commands", "commandKey", "parameterSchema", "encoder", "impact", "allowed", "code", "reason", "holds", "refusals", "id", "anchors"} {
		t.Run(field, func(t *testing.T) {
			data := proposalAnswer()
			vocab := data["deviceCommandVocabulary"].(map[string]any)
			switch field {
			case "constrained", "commands":
				delete(vocab, field)
			case "commandKey", "parameterSchema", "encoder", "impact":
				delete(vocab["commands"].([]any)[0].(map[string]any), field)
			case "allowed", "code", "reason":
				delete(data["validateCommandEnqueue"].(map[string]any), field)
			case "holds", "refusals":
				delete(data["commandDispatchSchedule"].(map[string]any), field)
			case "id":
				delete(data["devicesByToken"].([]any)[0].(map[string]any), field)
			case "anchors":
				delete(data["deviceScopeAnchors"].([]any)[0].(map[string]any), field)
			}
			_, err := proposalReaderAgainst(t, data).ReadProposalTarget(auth.WithBearer(context.Background(), "caller"), "device", "reboot", nil, time.Now())
			require.Error(t, err)
		})
	}
}

func TestProposalTargetFingerprintCanonicalAndComplete(t *testing.T) {
	fingerprint := func(schema, encoder, impact string) string {
		data := proposalAnswer()
		entry := data["deviceCommandVocabulary"].(map[string]any)["commands"].([]any)[0].(map[string]any)
		entry["parameterSchema"], entry["encoder"], entry["impact"] = schema, encoder, impact
		out, err := proposalReaderAgainst(t, data).ReadProposalTarget(auth.WithBearer(context.Background(), "caller"), "device", "reboot", nil, time.Now())
		require.NoError(t, err)
		return out.DefinitionFingerprint
	}
	base := fingerprint(`[{"name":"x","type":"number"}]`, `{"b":2,"a":1}`, "NORMAL")
	require.Equal(t, base, fingerprint(`[{"type":"number","name":"x"}]`, `{"a":1,"b":2}`, "NORMAL"))
	require.NotEqual(t, base, fingerprint(`[{"name":"x","type":"string"}]`, `{"a":1,"b":2}`, "NORMAL"))
	require.NotEqual(t, base, fingerprint(`[{"name":"x","type":"number"}]`, `{"a":2,"b":2}`, "NORMAL"))
	require.NotEqual(t, base, fingerprint(`[{"name":"x","type":"number"}]`, `{"a":1,"b":2}`, "DESTRUCTIVE"))
}

func TestProposalTargetDocumentMatchesDeviceManagement(t *testing.T) {
	raw, err := os.ReadFile("../../device-management/graphql/schema.graphql")
	require.NoError(t, err)
	schema, err := gql.ParseSchema(string(raw), nil, gql.UseFieldResolvers())
	require.NoError(t, err)
	for _, e := range schema.Validate(proposalTargetQuery) {
		if e.Rule != "VariablesOfCorrectType" {
			t.Error(e)
		}
	}
}

func TestProposalTargetFailureIsNotAVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		forbidden  bool
	}{
		{"HTTP", `{}`, http.StatusServiceUnavailable, false},
		{"GraphQL", `{"errors":[{"message":"unavailable"}]}`, 200, false},
		{"forbidden after another error", `{"errors":[{"message":"other"},{"extensions":{"code":"FORBIDDEN"}}]}`, 200, true},
		{"oversize", strings.Repeat(" ", (1<<20)+1), 200, false},
		{"malformed", `{`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			out, err := NewProposalTargetReader(s.URL).ReadProposalTarget(auth.WithBearer(context.Background(), "caller"), "device", "reboot", nil, time.Now())
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, tc.forbidden, errors.Is(err, auth.ErrForbidden))
		})
	}
}
