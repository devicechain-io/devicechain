// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// graphQLPeer is a stand-in service answering the service-token mint and ONE GraphQL field,
// recording which fields it was asked for.
func graphQLPeer(t *testing.T, field, data string, asked *[]string) (string, uint32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), field) {
			*asked = append(*asked, "unexpected: "+string(body))
			http.Error(w, "wrong service", http.StatusBadRequest)
			return
		}
		*asked = append(*asked, field)
		_, _ = w.Write([]byte(`{"data":` + data + `}`))
	}))
	t.Cleanup(srv.Close)
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, uint32(port)
}

// The seam main builds reads the tenant list from user-management and the roster from
// device-management — each from the right one. A swapped pair of URLs compiles, passes every
// processor test (they build the client themselves) and reconciles nothing in production.
func TestTheFactReconcileSeamAsksEachServiceItsOwnQuestion(t *testing.T) {
	var umAsked, dmAsked []string
	umHost, umPort := graphQLPeer(t, "tenantTokens", `{"tenantTokens":["acme"]}`, &umAsked)
	dmHost, dmPort := graphQLPeer(t, "deviceRosterPage",
		`{"deviceRosterPage":{"entries":[{"deviceToken":"d1","profileToken":"p","expectedSince":"2026-01-01T00:00:00Z"}],"nextCursor":null}}`,
		&dmAsked)

	saved := Microservice
	t.Cleanup(func() { Microservice = saved })
	Microservice = &core.Microservice{InstanceId: "inst", FunctionalArea: "event-processing"}
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.ServiceAuth.Secret = "shh"
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: umHost, Port: umPort}
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: dmHost, Port: dmPort}

	seam := buildFactReconcileSeam()
	require.NotNil(t, seam)
	tenants, err := seam.Tenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"acme"}, tenants)
	roster, err := seam.Roster(context.Background(), "acme")
	require.NoError(t, err)
	require.Len(t, roster, 1)
	require.Equal(t, "d1", roster[0].DeviceToken)
	require.Equal(t, []string{"tenantTokens"}, umAsked)
	require.Equal(t, []string{"deviceRosterPage"}, dmAsked)

	// And with either coordinate or the secret missing, the reconcile is OFF — nil, never a
	// client that fails on every sweep.
	infra.UserManagement.Port = 0
	require.Nil(t, buildFactReconcileSeam())
	infra.UserManagement.Port = umPort
	infra.DeviceManagement.Hostname = ""
	require.Nil(t, buildFactReconcileSeam())
	infra.DeviceManagement.Hostname = dmHost
	infra.ServiceAuth.Secret = ""
	require.Nil(t, buildFactReconcileSeam())
}
