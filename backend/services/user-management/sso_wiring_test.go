// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-user-management/identity"
)

// 🔴 THIS TESTS THE CALLER, NOT THE FUNCTION. identity.FetchDiscovery refusing a private
// address is proven in the identity package against a client the test built itself; that
// proves the function behaves, and says nothing about whether the Manager the service
// RUNS was handed such a client. These tests build the Manager through newIdentityManager —
// the constructor main.go calls — and ask the Manager.

func privateIdp(t *testing.T) (url string, hits *int) {
	t.Helper()
	n := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/keys",
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// TestTheManagerBuiltLikeMainRefusesAPrivateDiscoveryURL: the IdP below is on 127.0.0.1,
// a destination the default guard denies.
func TestTheManagerBuiltLikeMainRefusesAPrivateDiscoveryURL(t *testing.T) {
	url, hits := privateIdp(t)

	m, err := newIdentityManager(nil, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
		mscfg.InfrastructureConfiguration{})
	require.NoError(t, err)

	_, err = m.FetchIdpDiscovery(context.Background(), url)
	require.ErrorIs(t, err, egress.ErrBlocked,
		"the Manager main builds must dial through the egress guard; it fetched a loopback address")
	require.Zero(t, *hits, "the request must be refused at the dial, before the IdP is reached")
}

// TestTheEgressAllowanceReachesTheManager is the control, and the half that makes the
// refusal above mean something: the same Manager construction, with the operator's allow
// list opening that address, succeeds. So the refusal was the guard — configured the way
// the operator's infrastructure config configures it — and not a malformed URL, and the
// allowance an operator sets is the one this Manager enforces.
func TestTheEgressAllowanceReachesTheManager(t *testing.T) {
	url, hits := privateIdp(t)

	m, err := newIdentityManager(nil, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
		mscfg.InfrastructureConfiguration{Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"127.0.0.0/8"}}})
	require.NoError(t, err)

	snap, err := m.FetchIdpDiscovery(context.Background(), url)
	require.NoError(t, err)
	require.Equal(t, url, snap.Issuer)
	require.Equal(t, 1, *hits)
}

// TestAMalformedEgressAllowanceFailsTheManagerConstruction: a silently-dropped allowance
// would look configured and behave as though it were not.
func TestAMalformedEgressAllowanceFailsTheManagerConstruction(t *testing.T) {
	_, err := newIdentityManager(nil, nil, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil,
		mscfg.InfrastructureConfiguration{Egress: mscfg.EgressConfiguration{AllowedDestinations: []string{"10.0.0.1/8"}}})
	require.Error(t, err)
}
