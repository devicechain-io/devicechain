// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"github.com/devicechain-io/dc-microservice/presence"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The golden dedup-id strings pinned here are the CONTRACT the L0.5 extraction preserved
// byte-for-byte: a dedup id is JetStream's idempotency key (a drift duplicates presence /
// measurement events across a retry or failover) and has no schema to catch a change, so
// these literals ARE the schema. (The derived-token goldens moved with core.DeriveDeviceToken,
// to core/core/devicetoken_test.go.)
//
// These literals were computed from the PRE-EXTRACTION implementation (the old
// host.dedupID) and re-asserted here with the "sp" prefix passed explicitly. Their survival
// is the proof the parameterization kept the Sparkplug prefix producing byte-identical
// output — carry the SAME literals when adding an LwM2M golden with "lw".

func TestGoldenDedupIDs(t *testing.T) {
	samples := []Sample{
		{Name: "temp", Value: 21.5, Time: 1_700_000_000_000},
		{Name: "rpm", Value: 12345678, Time: 1_700_000_000_500},
	}
	assert.Equal(t, "sp3qb3wd59o09vv",
		measurementDedupID("sp", "acme", "sp-dev-abc", 1_700_000_000_500, samples),
		"measurement dedup id must not drift")

	ev := PresenceEvent{
		ExternalId: "plant-a/n1", Connected: true, Reason: "birth",
		SessionId: 1_700_000_000_123456789, OccurredAt: time.Unix(1_700_000_000, 0),
	}
	assert.Equal(t, "sp1gip9ahddziq7", stateChangeDedupID("sp", "acme", "sp-dev-abc", presenceStateChange(ev)),
		"connected presence dedup id must not drift")
	ev.Connected = false
	assert.Equal(t, "sp1gifu7otvqvza", stateChangeDedupID("sp", "acme", "sp-dev-abc", presenceStateChange(ev)),
		"disconnected presence dedup id must not drift")

	// A demotion of the SAME device and the SAME session must key distinctly from both
	// connectivity claims. Sharing a key would let a demotion be silently swallowed as a
	// duplicate of the connect or disconnect it follows — reported as success, for the
	// length of the duplicate window, on the one emission that repairs a frozen row.
	dem := DemotionEvent{
		Reason: "tap disabled", SessionId: 1_700_000_000_123456789, OccurredAt: time.Unix(1_700_000_000, 0),
	}
	demID := stateChangeDedupID("sp", "acme", "sp-dev-abc", demotionStateChange(dem))
	assert.NotEqual(t, "sp1gip9ahddziq7", demID, "a demotion must not collide with the connect it follows")
	assert.NotEqual(t, "sp1gifu7otvqvza", demID, "a demotion must not collide with the disconnect it follows")
	assert.Equal(t, "sp1ghx023qv9mhg", demID, "demotion dedup id must not drift")

	// dedupStateToken's collision-freedom is a CLAIM about the whole vocabulary, not just
	// the three values in it today, so it is asserted rather than left in a comment: every
	// declared state must produce a token distinct from every other. The one way the
	// default arm could collide is a state literally named "0", "1" or "2".
	seen := map[string]presence.State{}
	for _, st := range []presence.State{
		presence.StateConnected, presence.StateDisconnected, presence.StateDemoted,
	} {
		tok := dedupStateToken(st)
		if other, dup := seen[tok]; dup {
			t.Fatalf("presence states %q and %q share dedup token %q; one of them will be silently "+
				"swallowed as a duplicate of the other", st, other, tok)
		}
		seen[tok] = st
	}

	// Two demotions releasing DIFFERENT sessions are different events and must key apart.
	dem.SessionId++
	assert.NotEqual(t, demID, stateChangeDedupID("sp", "acme", "sp-dev-abc", demotionStateChange(dem)),
		"demotions of different sessions must key distinctly")
}
