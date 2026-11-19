// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

func suppressionWire(t *testing.T, handler http.HandlerFunc) (*svcclient.Client, string) {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service-token", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}))
	t.Cleanup(mint.Close)
	host, port, err := net.SplitHostPort(mint.Listener.Addr().String())
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	peer := httptest.NewServer(handler)
	t.Cleanup(peer.Close)
	return svcclient.New(config.UserManagementConfiguration{Hostname: host, InternalPort: uint32(number)}, "secret", "notification-management", []string{"alarm:read"}), peer.URL
}

func TestSuppressionLookupOldSchemaKeepsBackstop(t *testing.T) {
	n, rec, api, ctx := pagedThenDue(t, namedAlarm())
	var documents []string
	client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Query string }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		documents = append(documents, req.Query)
		if strings.Contains(req.Query, "alarmSuppressionStatus") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field alarmSuppressionStatus on type Query"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"alarmLifecycles":[{"token":"a1","state":"CLEARED"}]}}`))
		}
	})
	n.AlarmLookup = NewAlarmLookup(client, url)
	n.SuppressionLookup = NewSuppressionLookup(client, url)
	statuses := n.lookupSuppressions(ctx, []string{"a1"}, n.clock(), 5*time.Second)
	require.Empty(t, statuses)
	states, err := api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.NoError(t, n.Escalate(ctx, states[0], enabledPolicies(t, api, ctx), n.clock(), 5))
	require.Len(t, rec.sent, 1, "the lifecycle backstop must still prevent a page about a cleared alarm")
	require.Equal(t, []string{alarmSuppressionStatusDocument, `query($tokens: [String!]!) { alarmLifecycles(tokens: $tokens) { token state } }`}, documents)
	states, err = api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.Empty(t, states)
}

func TestSuppressionLookupDeduplicatesAndChunks(t *testing.T) {
	now := time.Now().UTC()
	var batches []int
	seen := map[string]bool{}
	client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables struct{ Tokens []string }
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, alarmSuppressionStatusDocument, req.Query)
		require.LessOrEqual(t, len(req.Variables.Tokens), 200)
		batches = append(batches, len(req.Variables.Tokens))
		var rows []map[string]any
		for _, token := range req.Variables.Tokens {
			require.False(t, seen[token], "tokens must be unique across requests")
			seen[token] = true
			rows = append(rows, map[string]any{"alarmToken": token, "suppression": map[string]string{"windowToken": "w", "until": now.Add(time.Hour).Format(time.RFC3339Nano)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"alarmSuppressionStatus": rows}})
	})
	var tokens []string
	for i := 0; i < 450; i++ {
		tokens = append(tokens, fmt.Sprintf("a%d", i), fmt.Sprintf("a%d", i))
	}
	lookup := NewSuppressionLookup(client, url)
	found, err := lookup.LookupSuppressions(tenantScoped(), tokens)
	require.NoError(t, err)
	require.Len(t, found, 450)
	require.Equal(t, []int{200, 200, 50}, batches)
}

func TestSuppressionLookupMalformedUntilPages(t *testing.T) {
	for _, mode := range []string{"malformed", "empty-window", "past", "equal-now", "missing"} {
		t.Run(mode, func(t *testing.T) {
			n, rec, api, ctx := pagedThenDue(t, namedAlarm())
			now := n.clock()
			client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
				until, window := now.Add(time.Hour).Format(time.RFC3339Nano), "w"
				switch mode {
				case "malformed":
					until = "bad-time"
				case "empty-window":
					window = ""
				case "past":
					until = now.Add(-time.Second).Format(time.RFC3339Nano)
				case "equal-now":
					until = now.Format(time.RFC3339Nano)
				}
				rows := []map[string]any{}
				if mode != "missing" {
					rows = append(rows, map[string]any{"alarmToken": "a1", "suppression": map[string]string{"windowToken": window, "until": until}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"alarmSuppressionStatus": rows}})
			})
			lookup := NewSuppressionLookup(client, url).(*suppressionLookup)
			lookup.now = func() time.Time { return now }
			n.SuppressionLookup = lookup
			states, err := api.OpenNotificationStates(ctx)
			require.NoError(t, err)
			require.NoError(t, n.Escalate(ctx, states[0], enabledPolicies(t, api, ctx), now, 5))
			require.Len(t, rec.sent, 2, "invalid optional suppression must page")
		})
	}
}

func TestSuppressionLookupTimeoutAndNilWiring(t *testing.T) {
	n := &PolicyNotifier{}
	require.Empty(t, n.lookupSuppressions(context.Background(), []string{"a"}, time.Now(), 5*time.Second))
	n.SuppressionLookup = suppressionFunc(func(ctx context.Context, _ []string) (map[string]SuppressionStatus, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		return nil, context.DeadlineExceeded
	})
	require.Empty(t, n.lookupSuppressions(context.Background(), []string{"a"}, time.Now(), 5*time.Second))
}
