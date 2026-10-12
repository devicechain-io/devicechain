// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The summary says single sign-on is off exactly when the chart will not derive an issuer
// for the instance's origin: plain http on a host that is not loopback. The chart's own
// side of this rule is rendered, in every combination, by hack/check-oauth-discovery-routing.sh.
func TestSsoUnavailableNoteFollowsTheChartsDerivationRule(t *testing.T) {
	for _, tc := range []struct {
		scheme, host string
		off          bool
	}{
		{"https", "dc.local", false},
		{"https", "iot.example.com", false},
		{"http", "localhost", false},
		{"http", "127.0.0.1", false},
		{"http", "dc.local", true},
		{"http", "iot.example.com", true},
	} {
		note := ssoUnavailableNote(tc.scheme, tc.host)
		if tc.off {
			require.Contains(t, note, "off", "%s://%s", tc.scheme, tc.host)
			require.Contains(t, note, "--no-tls", "the note names the flag that caused it")
		} else {
			require.Empty(t, note, "%s://%s", tc.scheme, tc.host)
		}
	}
}
