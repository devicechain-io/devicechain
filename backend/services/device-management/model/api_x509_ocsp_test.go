// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/require"
)

func ocspMode(v string) OptionalOcspMode { return OptionalOcspMode{Set: true, Value: &v} }
func ocspURL(v string) dcgraphql.OptionalString {
	return dcgraphql.OptionalString{Set: true, Value: &v}
}

func TestOcspModeOfReadsNullAsOffAndRefusesUnknown(t *testing.T) {
	a := &DeviceTrustAnchor{}
	m, err := a.OcspModeOf()
	require.NoError(t, err)
	require.Equal(t, x509trust.OCSPOff, m)

	for _, v := range []string{"OFF", "ENFORCE_KNOWN", "REQUIRE_CURRENT"} {
		a.OcspMode = sql.NullString{String: v, Valid: true}
		m, err := a.OcspModeOf()
		require.NoError(t, err)
		require.Equal(t, x509trust.OCSPMode(v), m)
	}
	// An unknown stored mode is an error, never OFF: it would silently stop checking.
	for _, v := range []string{"", "off", "ENFORCE", "BOGUS"} {
		a.OcspMode = sql.NullString{String: v, Valid: true}
		_, err := a.OcspModeOf()
		require.Error(t, err, "%q", v)
	}
}

func TestApplyOcspSettings(t *testing.T) {
	on := OcspPolicy{Enabled: true}
	onAll := OcspPolicy{Enabled: true, AllowRequireCurrent: true}
	enforcing := sql.NullString{String: "ENFORCE_KNOWN", Valid: true}
	for _, tc := range []struct {
		name    string
		policy  OcspPolicy
		start   DeviceTrustAnchor
		req     DeviceTrustAnchorUpdateRequest
		wantErr string
		mode    string // stored mode afterwards, "" = not checked
		url     string
	}{
		{name: "nothing set is a no-op", policy: on},
		{name: "ENFORCE_KNOWN", policy: on, req: DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("ENFORCE_KNOWN")}, mode: "ENFORCE_KNOWN"},
		{name: "ENFORCE_KNOWN with the switch off is refused", policy: OcspPolicy{},
			req: DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("ENFORCE_KNOWN")}, wantErr: "switched off"},
		{name: "OFF is always allowed, switch or not", policy: OcspPolicy{}, req: DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("OFF")}, mode: "OFF"},
		{name: "REQUIRE_CURRENT needs the instance to allow it", policy: on,
			req:     DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("REQUIRE_CURRENT"), OcspResponderUrl: ocspURL("https://ocsp.example.com/")},
			wantErr: "does not allow"},
		{name: "REQUIRE_CURRENT needs a responder url", policy: onAll,
			req: DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("REQUIRE_CURRENT")}, wantErr: "needs ocspResponderUrl"},
		{name: "REQUIRE_CURRENT with a url", policy: onAll,
			req:  DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("REQUIRE_CURRENT"), OcspResponderUrl: ocspURL("https://ocsp.example.com/")},
			mode: "REQUIRE_CURRENT", url: "https://ocsp.example.com/"},
		{name: "clearing the url under REQUIRE_CURRENT is refused", policy: onAll,
			start: DeviceTrustAnchor{OcspMode: sql.NullString{String: "REQUIRE_CURRENT", Valid: true},
				OcspResponderUrl: sql.NullString{String: "https://o.example/", Valid: true}},
			req:     DeviceTrustAnchorUpdateRequest{OcspResponderUrl: dcgraphql.OptionalString{Set: true}},
			wantErr: "needs ocspResponderUrl"},
		{name: "a null mode is refused", policy: on, req: DeviceTrustAnchorUpdateRequest{OcspMode: OptionalOcspMode{Set: true}}, wantErr: "cannot be cleared"},
		{name: "an unknown mode is refused", policy: on, req: DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("MAYBE")}, wantErr: "must be OFF"},
		{name: "an anchor already enforcing can be edited with the switch off", policy: OcspPolicy{},
			start: DeviceTrustAnchor{OcspMode: enforcing},
			req:   DeviceTrustAnchorUpdateRequest{OcspMode: ocspMode("ENFORCE_KNOWN")}, mode: "ENFORCE_KNOWN"},
		{name: "a stored unknown mode blocks an edit", policy: on,
			start:   DeviceTrustAnchor{OcspMode: sql.NullString{String: "BOGUS", Valid: true}},
			req:     DeviceTrustAnchorUpdateRequest{OcspResponderUrl: ocspURL("https://o.example/")},
			wantErr: "unknown OCSP mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &Api{}
			api.SetOcspPolicy(tc.policy)
			locked := tc.start
			locked.Token = "maker"
			err := api.applyOcspSettings(&locked, &tc.req)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.True(t, strings.Contains(err.Error(), tc.wantErr), "%v does not mention %q", err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			if tc.mode != "" {
				require.Equal(t, tc.mode, locked.OcspMode.String)
			}
			if tc.url != "" {
				require.Equal(t, tc.url, locked.OcspResponderUrl.String)
			}
		})
	}
}

func TestValidateOcspResponderUrl(t *testing.T) {
	for _, ok := range []string{"http://ocsp.example.com", "https://ocsp.example.com:8080/path?x=1", "http://10.0.0.5/ocsp"} {
		require.NoError(t, validateOcspResponderUrl(ok), ok)
	}
	for _, bad := range []string{"", " http://x", "ftp://x", "ldap://x/y", "http://", "https:///path", "http://user:pw@x/", "http://x/#frag", "//x", "ocsp.example.com",
		"http://x/" + strings.Repeat("a", MaxOcspResponderUrlLength)} {
		require.Error(t, validateOcspResponderUrl(bad), bad)
	}
}
