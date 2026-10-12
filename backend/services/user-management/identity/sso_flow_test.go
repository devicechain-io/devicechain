// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestSsoHappyPath(t *testing.T) {
	e := newSsoEnv(t)
	e.ssoIdentity(t, "pat@"+ssoTestDomain, "acme-idp", "sub-pat", "acme")
	require.Equal(t, int32(1), e.op.discoveryHits.Load(), "discovery is fetched once, when the connection is created")

	got, _ := e.signInOK(t, "acme-idp", "sub-pat", "pat@"+ssoTestDomain)
	require.False(t, got.Superuser)
	require.Equal(t, []MembershipInfo{{Tenant: "acme", Roles: []string{}}}, got.Memberships)
	require.Equal(t, int32(1), e.op.discoveryHits.Load(), "sign-in never fetches discovery: it uses the approved snapshot")
	require.Equal(t, 1, e.metrics.count("acme-idp", SsoOutcomeSuccess))

	pair, err := e.m.SelectTenant(context.Background(), got.IdentityToken, "acme")
	require.NoError(t, err)
	claims, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, "acme", claims.Tenant)
}

var _ = auth.TokenTypeIdentity
