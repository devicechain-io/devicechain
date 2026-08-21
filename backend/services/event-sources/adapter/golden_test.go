// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The golden token/dedup-id strings pinned here are the CONTRACT the L0.5 extraction
// preserved byte-for-byte. A derived device token is the deterministic key an
// auto-register collides on (a drift mints a duplicate device row for every existing
// device); a dedup id is JetStream's idempotency key (a drift duplicates presence /
// measurement events across a retry or failover). Neither has a schema to catch a
// change, so these literals ARE the schema.
//
// These literals were computed from the PRE-EXTRACTION implementation (the old
// host.dedupID/DeriveDeviceToken) and re-asserted here with the "sp"/"sp-" prefixes
// passed explicitly. Their survival is the proof the parameterization kept the Sparkplug
// prefixes producing byte-identical output — carry the SAME literals when adding an LwM2M
// golden with "lw"/"lw-".

func TestGoldenDerivedTokens(t *testing.T) {
	cases := map[string]string{
		"plant-a/node-3/dev-2": "sp-plant-a-node-3-dev-2-f059b7804d5d",
		"plant-a/n1":           "sp-plant-a-n1-efc075f9d7b9",
		"café/ñodo 1/dev.7":    "sp-caf---odo-1-dev-7-92df7f344f11", // unicode/space/dot all slug to '-'
		"////":                 "sp-0ea28b450f5e",                   // nothing survives slugging → hash only
	}
	for id, want := range cases {
		assert.Equal(t, want, DeriveDeviceToken(id, "sp-"), "derived token for %q must not drift", id)
	}
}

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

	// Two demotions releasing DIFFERENT sessions are different events and must key apart.
	dem.SessionId++
	assert.NotEqual(t, demID, stateChangeDedupID("sp", "acme", "sp-dev-abc", demotionStateChange(dem)),
		"demotions of different sessions must key distinctly")
}
