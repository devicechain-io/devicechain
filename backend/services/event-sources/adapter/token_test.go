// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/devicechain-io/dc-microservice/core"
)

// TestDerivedTokenAlwaysSatisfiesTheGrammar is the check-that-cannot-fail: a raw source
// external id routinely carries '/', '.', spaces and unicode — every one of which the
// token grammar (ADR-042) rejects fail-closed at the storage layer. If DeriveDeviceToken
// ever returned one verbatim, createDevice would fail on the FIRST real message. So for a
// battery of adversarial ids the derived token MUST pass the exact same
// core.ValidateToken the rdb create guard applies, and stay within the length bound. A
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
		assert.NoErrorf(t, core.ValidateToken(tok), "derived token %q for raw id %q must satisfy the grammar", tok, raw)
		assert.LessOrEqualf(t, len(tok), core.MaxTokenLen, "derived token for %q exceeds MaxTokenLen", raw)
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
