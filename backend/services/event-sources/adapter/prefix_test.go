// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two per-protocol prefixes (token prefix, dedup-id namespace) are the ONLY
// Sparkplug-specific identifiers L0.5 lifted into the shared adapter and turned into
// construction-time parameters. This file is what L0.5 actually ADDS: it proves the
// parameterization is load-bearing — two protocols never collide in the shared device
// namespace or the shared JetStream dedup window — where the moved suite only proves
// nothing regressed for Sparkplug.

// TestTokenPrefixNamespacesDevicesByProtocol proves two protocols deriving a token for
// the SAME external id produce distinct, origin-labelled tokens — so an LwM2M device and
// a Sparkplug device sharing an operator's naming scheme can never collide on the token
// unique index (which would merge two unrelated devices), while each stays deterministic
// within its protocol (the auto-register conflict-swallow still works).
func TestTokenPrefixNamespacesDevicesByProtocol(t *testing.T) {
	const id = "plant-a/node-3"
	sp := DeriveDeviceToken(id, "sp-")
	lw := DeriveDeviceToken(id, "lw-")

	assert.True(t, strings.HasPrefix(sp, "sp-"), "a Sparkplug token is labelled sp-")
	assert.True(t, strings.HasPrefix(lw, "lw-"), "an LwM2M token is labelled lw-")
	assert.NotEqual(t, sp, lw, "the same external id under two protocols must not share a token")
	// Determinism within a protocol is what the create/create conflict-swallow relies on.
	assert.Equal(t, sp, DeriveDeviceToken(id, "sp-"), "derivation stays deterministic per protocol")
	// The hash disambiguator is shared, so the tokens differ ONLY by prefix — dropping
	// the prefix (an unparameterized regression) would collapse them.
	assert.Equal(t, strings.TrimPrefix(sp, "sp-"), strings.TrimPrefix(lw, "lw-"),
		"only the prefix distinguishes them — so an unparameterized prefix WOULD collide")
}

// TestDedupPrefixNamespacesIdsByProtocol proves two protocols emitting the byte-identical
// logical event carry distinct dedup ids, so they can never dedup each other out of the
// shared InboundEvents window — while each protocol's own retry still dedups (the id is
// stable within a prefix).
func TestDedupPrefixNamespacesIdsByProtocol(t *testing.T) {
	samples := []Sample{{Name: "t", Value: 21.5, Time: 1000}}
	spM := measurementDedupID("sp", "acme", "dev-1", 1000, samples)
	lwM := measurementDedupID("lw", "acme", "dev-1", 1000, samples)
	assert.True(t, strings.HasPrefix(spM, "sp"))
	assert.True(t, strings.HasPrefix(lwM, "lw"))
	assert.NotEqual(t, spM, lwM, "the same measurement under two protocols must carry distinct dedup ids")
	require.Equal(t, spM, measurementDedupID("sp", "acme", "dev-1", 1000, samples), "stable within a protocol (a retry dedups)")

	ev := PresenceEvent{ExternalId: "g/n", Connected: true, SessionId: 5, OccurredAt: time.Unix(1, 0)}
	spP := presenceDedupID("sp", "acme", "dev-1", ev)
	lwP := presenceDedupID("lw", "acme", "dev-1", ev)
	assert.NotEqual(t, spP, lwP, "the same presence transition under two protocols must carry distinct dedup ids")
}
