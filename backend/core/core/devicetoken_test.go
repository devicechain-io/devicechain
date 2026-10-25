// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDerivedTokenAlwaysSatisfiesTheGrammar is the check-that-cannot-fail: a raw source
// external id routinely carries '/', '.', spaces and unicode — every one of which the
// token grammar (ADR-042) rejects fail-closed at the storage layer. If DeriveDeviceToken
// ever returned one verbatim, createDevice would fail on the FIRST real message. So for a
// battery of adversarial ids the derived token MUST pass the exact same
// ValidateToken the rdb create guard applies, and stay within the length bound. A
// mutation that skips slugging or the length cap reddens here. The prefix is exercised as
// a parameter ("sp-"); the bound arithmetic subtracts len(prefix), so a longer prefix
// still fits.
func TestDerivedTokenAlwaysSatisfiesTheGrammar(t *testing.T) {
	adversarial := []string{
		"plant-a/node-3/dev-2",       // slashes (the common case)
		"line.7/pressure.gauge",      // dots
		"Aire Acondicionado/Célula3", // spaces + unicode
		"/leading/slash",             // leading separator → slug must not start with '-'
		"///",                        // nothing survives slugging → hash-only token
		"",                           // empty id
		strings.Repeat("x", 500),     // over-long → must be truncated to <= MaxTokenLen
		"OK-Already_valid",           // already valid
		"9starts-with-digit",         // digit-first is legal
		"  spaces  ",                 // surrounding whitespace
		"tab\tnl\nvalue",             // control characters
		"emoji-🚀-metric",             // multibyte rune
	}
	for _, raw := range adversarial {
		tok := DeriveDeviceToken(raw, "sp-")
		assert.NoErrorf(t, ValidateToken(tok), "derived token %q for raw id %q must satisfy the grammar", tok, raw)
		assert.LessOrEqualf(t, len(tok), MaxTokenLen, "derived token for %q exceeds MaxTokenLen", raw)
	}
}

// TestDerivedTokenIsDeterministic pins that the same external id always derives the same
// token — the property the create/create race relies on to collide on the token unique
// index instead of making two devices.
func TestDerivedTokenIsDeterministic(t *testing.T) {
	for _, raw := range []string{"plant-a/node-3/dev-2", "///", "", "already-valid"} {
		assert.Equal(t, DeriveDeviceToken(raw, "sp-"), DeriveDeviceToken(raw, "sp-"), "derivation must be deterministic for %q", raw)
	}
}

// TestDerivedTokenDisambiguatesCollidingSlugs pins that two DISTINCT ids that sanitize to
// the same slug still get DISTINCT tokens (via the raw-id hash), so unrelated devices can
// never silently merge onto one token.
func TestDerivedTokenDisambiguatesCollidingSlugs(t *testing.T) {
	// "a/b" and "a.b" both slug to "a-b"; the hash of the raw id must keep them apart.
	assert.NotEqual(t, DeriveDeviceToken("a/b", "sp-"), DeriveDeviceToken("a.b", "sp-"))
}

// The golden token strings are the CONTRACT the L0.5 extraction preserved byte-for-byte,
// and the move into core preserved again. A derived device token is the deterministic key
// an auto-register collides on (a drift mints a duplicate device row for every existing
// device) and the key a self-registration re-derives. Neither has a schema to catch a
// change, so these literals ARE the schema. They were computed from the pre-extraction
// implementation and are carried unchanged.
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

// The prefix namespaces devices by protocol: the same external id under two protocols
// never shares a token, and the two differ only by prefix.
func TestTokenPrefixNamespacesDevicesByProtocol(t *testing.T) {
	const id = "plant-a/node-3"
	sp, lw, pv := DeriveDeviceToken(id, "sp-"), DeriveDeviceToken(id, "lw-"), DeriveDeviceToken(id, "pv-")
	assert.True(t, strings.HasPrefix(pv, "pv-"), "a self-registered device is labelled pv-")
	assert.NotEqual(t, sp, lw)
	assert.NotEqual(t, sp, pv)
	assert.Equal(t, strings.TrimPrefix(sp, "sp-"), strings.TrimPrefix(pv, "pv-"))
}
