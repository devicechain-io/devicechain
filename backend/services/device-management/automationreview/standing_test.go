// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package automationreview

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
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

func TestPrincipalStanding(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		bad, transport bool
	}{
		{"happy", `{"data":{"principalStanding":[{"principal":"a@x","qualifies":true}]}}`, false, false},
		{"graphql", `{"errors":[{"message":"unknown field"}]}`, true, false},
		{"missing", `{"data":{"principalStanding":[]}}`, true, false},
		{"duplicate", `{"data":{"principalStanding":[{"principal":"a@x","qualifies":true},{"principal":"a@x","qualifies":false}]}}`, true, false},
		{"null qualifies", `{"data":{"principalStanding":[{"principal":"a@x","qualifies":null}]}}`, true, false},
		{"transport", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request auth.ServiceTokenRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, []string{string(auth.TenantRead)}, request.Authorities)
				_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service-token", ExpiresAt: 1 << 40})
			}))
			defer mint.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "acme", r.Header.Get(auth.ServiceTenantHeader))
				var request struct {
					Query     string
					Variables struct{ Principals, Authorities []string }
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, []string{"a@x"}, request.Variables.Principals)
				require.Equal(t, []string{"device:write", "event:read"}, request.Variables.Authorities)
				require.Contains(t, request.Query, "principalStanding")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer endpoint.Close()
			if tc.transport {
				endpoint.Close()
			}
			host, portText, err := net.SplitHostPort(mint.Listener.Addr().String())
			require.NoError(t, err)
			port, err := strconv.Atoi(portText)
			require.NoError(t, err)
			client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "secret", "device-management", []string{string(auth.TenantRead)})
			result, err := NewStandingClient(client, endpoint.URL).PrincipalStanding(core.WithTenant(context.Background(), "acme"), []string{"a@x"}, []string{"device:write", "event:read"})
			if tc.bad {
				require.Error(t, err)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, map[string]bool{"a@x": true}, result)
			}
		})
	}
}
