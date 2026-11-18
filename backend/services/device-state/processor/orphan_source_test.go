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

func orphanLookupClient(t *testing.T) *svcclient.Client {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "sweep-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)
	host, portString, err := net.SplitHostPort(mint.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	return svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "secret", "device-state", []string{string(auth.DeviceRead), string(auth.TenantRead)})
}

func TestDeviceLookupRejectsErrorsAndMissingAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"missing", `{ "data": {} }`, true},
		{"null", `{ "data": { "devicesByToken": null } }`, true},
		{"old server error", `{ "errors": [{ "message": "unknown field" }] }`, true},
		{"partial data with error", `{ "data": { "devicesByToken": [] }, "errors": [{ "message": "unavailable" }] }`, true},
		{"bad id", `{ "data": { "devicesByToken": [{ "token": "truck-7", "id": "bad", "createdAt": "2026-11-01T00:00:00Z" }] } }`, true},
		{"missing creation time", `{ "data": { "devicesByToken": [{ "token": "truck-7", "id": "23" }] } }`, true},
		{"authoritative empty", `{ "data": { "devicesByToken": [] } }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			source := &DeviceLookup{Client: orphanLookupClient(t), DMURL: server.URL}
			live, err := source.Devices(context.Background(), "a", []string{"truck-7"})
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, live)
			} else {
				require.NoError(t, err)
				require.NotNil(t, live)
				require.Empty(t, live)
			}
		})
	}
}

func TestDeviceLookupChunksAndReadsIdentityAndTenantList(t *testing.T) {
	client := orphanLookupClient(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var chunkSizes []int
	var violations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string
			Variables struct{ Tokens []string }
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		tenant := r.Header.Get(auth.ServiceTenantHeader)
		if strings.Contains(in.Query, "tenantTokens") {
			if tenant != "instance" {
				violations = append(violations, "wrong list scope")
			}
			_, _ = w.Write([]byte(`{"data":{"tenantTokens":["a","b"]}}`))
			return
		}
		if tenant != "a" {
			violations = append(violations, "wrong device tenant")
		}
		if !strings.Contains(in.Query, "token id createdAt") {
			violations = append(violations, "missing identity fields")
		}
		chunkSizes = append(chunkSizes, len(in.Variables.Tokens))
		devices := make([]map[string]any, len(in.Variables.Tokens))
		for i, token := range in.Variables.Tokens {
			devices[i] = map[string]any{"token": token, "id": "23", "createdAt": at.Format(time.RFC3339)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devicesByToken": devices}})
	}))
	defer server.Close()
	source := &DeviceLookup{Client: client, DMURL: server.URL, UMURL: server.URL, ListScope: "instance"}
	tenants, err := source.Tenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, tenants)
	tokens := make([]string, 1001)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("truck-%d", i)
	}
	live, err := source.Devices(context.Background(), "a", tokens)
	require.NoError(t, err)
	require.Len(t, live, 1001)
	require.Equal(t, []int{1000, 1}, chunkSizes)
	require.Equal(t, LiveDevice{Token: "truck-1000", ID: 23, CreatedAt: at}, live["truck-1000"])
	require.Empty(t, violations)
}
