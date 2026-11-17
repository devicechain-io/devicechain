// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

func TestRulesCeilingWireAndFailure(t *testing.T) {
	value := "7"
	failed := false
	validation := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service", ExpiresAt: 1 << 40})
			return
		}
		require.Equal(t, "tenant-a", r.Header.Get(auth.ServiceTenantHeader))
		var q struct {
			Query string `json:"query"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&q))
		require.Equal(t, "query { tenantGovernance { rulesPerTenant } }", q.Query)
		if validation != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": validation}}})
			return
		}
		if failed {
			http.Error(w, "down", 503)
			return
		}
		if value == "" {
			_, _ = w.Write([]byte(`{"data":{"tenantGovernance":{}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"tenantGovernance":{"rulesPerTenant":` + value + `}}}`))
	}))
	defer srv.Close()
	host, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	c := svcclient.New(config.UserManagementConfiguration{Hostname: host, InternalPort: uint32(port)}, "secret", "device-management", []string{"tenant:read"})
	r := NewRulesCeilingResolver(c, srv.URL)
	for _, tc := range []struct {
		wire string
		want int
	}{{"7", 7}, {"0", 0}, {"null", -1}, {"", -1}} {
		value = tc.wire
		got, err := r.Resolve(context.Background(), "tenant-a")
		require.NoError(t, err)
		if tc.want < 0 {
			require.Nil(t, got)
		} else {
			require.NotNil(t, got)
			require.Equal(t, tc.want, *got)
		}
	}
	for _, message := range []string{
		`Cannot query field "rulesPerTenant" on type "TenantGovernance".`,
		`Unknown field "tenantGovernance" on type "Query".`,
	} {
		validation = message
		got, err := r.Resolve(context.Background(), "tenant-a")
		require.NoError(t, err)
		require.Nil(t, got)
	}
	validation = "permission denied"
	_, err = r.Resolve(context.Background(), "tenant-a")
	require.Error(t, err)
	validation = ""
	value = "-1"
	_, err = r.Resolve(context.Background(), "tenant-a")
	require.Error(t, err)
	failed = true
	_, err = r.Resolve(context.Background(), "tenant-a")
	require.Error(t, err)
	_, err = r.Resolve(context.Background(), "")
	require.Error(t, err)
}

func TestUnsupportedRulesCeilingErrors(t *testing.T) {
	require.False(t, unsupportedRulesCeiling(nil))
	require.False(t, unsupportedRulesCeiling([]string{`Cannot query field "unrelated" on type "Query".`}))
	require.False(t, unsupportedRulesCeiling([]string{`Cannot query field "rulesPerTenant" on type "TenantGovernance".`, "permission denied"}))
}
