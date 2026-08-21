// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"os"
	"regexp"
	"testing"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/presence"
)

// TestClaimMapsEveryNamedState is the totality table. Claim replaced a string compare
// precisely because a compare is total onto a bool and therefore answers "is this device
// up?" even for inputs that are not about the device being up.
func TestClaimMapsEveryNamedState(t *testing.T) {
	cases := []struct {
		state string
		want  presence.Claim
		ok    bool
	}{
		{string(esmodel.PresenceConnected), presence.ClaimConnected, true},
		{string(esmodel.PresenceDisconnected), presence.ClaimDisconnected, true},
		{string(esmodel.PresenceDemoted), presence.ClaimDemoted, true},
		{"", presence.ClaimUnset, false},
		{"connected", presence.ClaimUnset, false}, // the wire enum is not case-folded
		{"RETIRED", presence.ClaimUnset, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.state), func(t *testing.T) {
			got, ok := (&ResolvedStateChangePayload{State: tc.state}).Claim()
			if ok != tc.ok {
				t.Fatalf("Claim() ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("Claim() = %v, want %v", got, tc.want)
			}
			// An unmapped state must never leak a claim presence.Decide would honour.
			if !ok && got.Valid() {
				t.Fatalf("an unmapped state produced the usable claim %v", got)
			}
		})
	}
}

// TestClaimCoversTheDeclaredVocabulary is the guard the table above cannot be: a table
// enumerates what its author remembered. The failure this exists for is adding a fourth
// PresenceState and not teaching the mapper — everything still compiles, every test still
// passes, and the new state is read as a malformed payload and dropped in production.
//
// So the vocabulary is read from the file that DECLARES it rather than restated here. That
// file is in another module, which Go's test cache does not track, so this test is only
// honest when run with -count=1 — which CI and the workspace sweep both pass.
func TestClaimCoversTheDeclaredVocabulary(t *testing.T) {
	const decl = "../../event-sources/model/events.go"
	src, err := os.ReadFile(decl)
	if err != nil {
		t.Fatalf("cannot read the presence-state declaration at %s: %v", decl, err)
	}
	matches := regexp.MustCompile(`(?m)^\s*Presence\w+\s+PresenceState\s*=\s*"([A-Z_]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(matches) < 3 {
		t.Fatalf("found %d declared presence states in %s; the scan is broken, not the vocabulary", len(matches), decl)
	}
	for _, m := range matches {
		state := m[1]
		if _, ok := (&ResolvedStateChangePayload{State: state}).Claim(); !ok {
			t.Errorf("presence state %q is declared on the wire but Claim() cannot map it; "+
				"every consumer will treat it as a malformed payload", state)
		}
	}
}
