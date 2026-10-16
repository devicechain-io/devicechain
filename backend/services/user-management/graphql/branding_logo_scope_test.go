// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
)

// 🔴 The logo upload is a plain HTTP endpoint, not a resolver, and it used to gate on
// claims.HasAuthority — which answers only "does the token name branding:write?". A
// SCOPED member (ADR-081) whose role named it would have been admitted to a tenant-wide
// write that every resolver refuses them. The gate now goes through auth.Authorize.
func TestAScopedMemberCannotUploadTheTenantLogo(t *testing.T) {
	ts, iss := logoServerAndIssuer(t)
	scope, err := auth.NewScope([]string{"customer:acme-corp"})
	require.NoError(t, err)

	upload := func(token string) int {
		png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
		req, err := http.NewRequest(http.MethodPost, ts.URL+brandingLogoPath, bytes.NewReader(png))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	scoped, err := iss.IssueScopedTenantAccess("acme", "c@acme-corp.example", nil,
		[]string{string(auth.BrandingWrite)}, scope, "j-scoped")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, upload(scoped.Token),
		"a scoped member holding branding:write replaced the tenant's logo")

	// Negative control: the same authority on an UNSCOPED token is admitted, so the
	// refusal above is the scope and not the fixture.
	unscoped, err := iss.IssueTenantAccess("acme", "a@b.c", nil, []string{string(auth.BrandingWrite)}, false, "j-unscoped")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, upload(unscoped.Token))
}
