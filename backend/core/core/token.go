// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"regexp"
)

// MaxTokenLen bounds a token's length, matching the size:128 storage column on
// rdb.TokenReference.Token.
const MaxTokenLen = 128

// tokenGrammar is the single, type-independent grammar every entity token and
// every tenant id must satisfy (ADR-042 P2). It is a *security* grammar, not a
// house-style one: its only job is to keep a token safe everywhere tokens are
// spliced into infrastructure namespaces. A tenant token becomes the middle
// segment of a NATS subject (messaging.ScopedSubject → "inst.<tenant>.suffix",
// recovered by splitting on "."), so a "." shifts subject segments and "*"/">"
// inject NATS wildcards that match across tenants; "/", "+" and "#" are likewise
// hazardous on MQTT topics; whitespace and other punctuation break subject/URL/log
// handling.
//
// It therefore allows letters (either case), digits, hyphen and underscore, and
// nothing else — which admits machine-supplied identifiers like uppercase device
// serials and VINs (the platform's own sample data), while still rejecting every
// metacharacter above. Case-folding a token to a lowercase-kebab house style is a
// *presentation* concern owned by the console/masks (ADR-042 P3), not enforced
// here: the backend rejects an unsafe token rather than silently rewriting an
// identifier a device or client chose.
//
// It lives in the leaf `core` package (importing nothing of DeviceChain's) so both
// the storage layer (rdb's create/update GORM guard, ADR-042) and the messaging
// layer (the WriteMessages fail-closed tenant guard, ADR-025) enforce the SAME
// grammar from one source, rather than duplicating a drift-prone regexp.
var tokenGrammar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidateToken reports whether a token (an entity token or a tenant id) conforms
// to the global security grammar. It is the fail-closed guard applied wherever a
// caller-supplied token is spliced into a storage key or a messaging subject.
func ValidateToken(token string) error {
	if token == "" {
		return fmt.Errorf("token must not be empty")
	}
	if len(token) > MaxTokenLen {
		return fmt.Errorf("token %q exceeds the maximum length of %d", token, MaxTokenLen)
	}
	if !matchesTokenGrammar(token) {
		return fmt.Errorf("token %q is invalid: must be letters, digits, hyphens and underscores, starting with a letter or digit (%s)", token, tokenGrammar.String())
	}
	return nil
}

// matchesTokenGrammar is tokenGrammar.MatchString without the regular-expression engine:
// a byte loop over the same two character classes. Every published message checks its
// tenant against the grammar (messaging's tenantSubject), so the engine was a per-event
// cost on every service that publishes; it was about 1% of device-management's CPU in a
// production-shaped profile. tokenGrammar stays the statement of the grammar, and the
// error message quotes it; TestTokenGrammarMatcherAgreesWithTheRegexp holds the two to
// the same answer.
//
// It works on bytes, not runes: every byte the grammar admits is ASCII, so a multi-byte
// UTF-8 sequence is refused at its first byte, as the regexp refuses its rune.
func matchesTokenGrammar(token string) bool {
	if token == "" {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}
