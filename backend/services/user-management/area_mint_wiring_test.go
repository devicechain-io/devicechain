// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The area-bound mint as main.go wires it: on the internal listener and not the public one, reading
// the credential from the mounted file, judging with the PRODUCTION ceiling table. None of these
// requests reaches the issuer (the routes test's Manager is a zero value), which is the point: every
// one of them must be refused before it.
func TestAreaMintIsWiredOnTheInternalListenerWithTheProductionCeiling(t *testing.T) {
	ms := newUserManagementForRoutes(t, "https://iot.example.com/api/user-management")
	ms.InstanceConfiguration.Infrastructure.ServiceAuth.Secret = "right-secret"

	dir := t.TempDir()
	prevDir := areaMintCredentialDir
	t.Cleanup(func() { areaMintCredentialDir = prevDir })
	areaMintCredentialDir = dir
	const credential = "automation-credential-0123456789abcdefghijklmnopq"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "automation"), 0o700))
	// A trailing newline, as `kubectl create secret --from-file` leaves one, still matches.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "automation", auth.AreaMintCredentialKey), []byte(credential+"\n"), 0o600))

	registerKeyHandlers()
	registerServiceTokenHandler()
	registerOAuthHandlersIfEnabled()
	registerSsoHandlersIfEnabled()
	registerAdminHandler()
	registerSettingsHandler()
	ms.RegisterProbes(nil)

	public := httptest.NewServer(ms.Mux())
	defer public.Close()
	internal := httptest.NewServer(internalAuthMux())
	defer internal.Close()

	post := func(t *testing.T, base, header, value, body string) int {
		req, err := http.NewRequest(http.MethodPost, base+auth.AreaServiceTokenPath, strings.NewReader(body))
		require.NoError(t, err)
		if header != "" {
			req.Header.Set(header, value)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	require.Equal(t, http.StatusNotFound, post(t, public.URL, auth.AreaCredentialHeader, credential, `{"authorities":["device:read"]}`),
		"the area-bound mint is on the PUBLIC mux")

	for _, tc := range []struct {
		name, header, value, body string
		want                      int
	}{
		{"device:write", auth.AreaCredentialHeader, credential, `{"authorities":["device:write"]}`, http.StatusForbidden},
		{"the super-authority", auth.AreaCredentialHeader, credential, `{"authorities":["*"]}`, http.StatusForbidden},
		{"a named subject", auth.AreaCredentialHeader, credential, `{"subject":"lwm2m-ingest","authorities":["device:read"]}`, http.StatusBadRequest},
		// automation has no ceiling entry until its mint site lands: an empty ceiling.
		{"device:read before the ceiling entry exists", auth.AreaCredentialHeader, credential, `{"authorities":["device:read"]}`, http.StatusForbidden},
		{"a wrong credential", auth.AreaCredentialHeader, credential + "x", `{"authorities":["device:read"]}`, http.StatusUnauthorized},
		{"the shared secret instead", auth.ServiceSecretHeader, "right-secret", `{"authorities":["device:read"]}`, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, post(t, internal.URL, tc.header, tc.value, tc.body))
		})
	}

	rec := httptest.NewRecorder()
	ms.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	require.Contains(t, body, `_area_mint_requests_total{area="automation",result="over_ceiling"} 2`)
	require.Contains(t, body, `_area_mint_requests_total{area="automation",result="wildcard"} 1`)
	require.Contains(t, body, `_area_mint_requests_total{area="automation",result="invalid_request"} 1`)
	require.Contains(t, body, `_area_mint_requests_total{area="other",result="bad_credential"} 2`)
	require.NotContains(t, body, credential)
}

// With no credential mounted (the area is not enabled), the endpoint is off: 503, not a token.
func TestAreaMintWithNoMountedCredentialIsOff(t *testing.T) {
	newUserManagementForRoutes(t, "")
	prevDir := areaMintCredentialDir
	t.Cleanup(func() { areaMintCredentialDir = prevDir })
	areaMintCredentialDir = t.TempDir()
	registerServiceTokenHandler()

	internal := httptest.NewServer(internalAuthMux())
	defer internal.Close()
	req, err := http.NewRequest(http.MethodPost, internal.URL+auth.AreaServiceTokenPath, strings.NewReader(`{"authorities":["device:read"]}`))
	require.NoError(t, err)
	req.Header.Set(auth.AreaCredentialHeader, strings.Repeat("a", 64))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
