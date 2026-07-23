// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// tokenHashLen is how many hex chars of the raw-id hash a derived token carries. 12
// hex = 48 bits: enough that two DISTINCT external ids that sanitize to the same slug
// still get distinct tokens (the whole reason the hash is present), with collision
// odds negligible at any realistic fleet size.
const tokenHashLen = 12

// DeriveDeviceToken maps a raw source external id to a DeviceChain token that
// satisfies the global token grammar (ADR-042: letters, digits, '-' and '_',
// alphanumeric first). A source external id routinely contains '/', '.', spaces, or
// unicode — all rejected fail-closed by the storage layer — so it can NEVER be a token
// directly; only the externalId field holds it verbatim.
//
// The token is "{prefix}{slug}-{hash}" (or "{prefix}{hash}" when nothing survives
// slugging), where prefix namespaces the token to its origin protocol (so an operator
// reading "sp-…" vs "lw-…" knows where a device came from) AND guarantees an
// alphanumeric-adjacent start; slug is the id with every unsafe character mapped to
// '-'; and hash is a prefix of sha256(rawId). The prefix MUST itself satisfy the
// grammar's alphanumeric-first rule (callers pass "sp-" / "lw-"). Two properties
// matter:
//   - Deterministic: the same (external id, prefix) always derives the same token, so a
//     create/create race between two concurrent bursts collides on the token unique
//     index (which the registrar's conflict-swallow relies on) rather than making two
//     devices.
//   - Hash-disambiguated: two DISTINCT ids that slug to the same string still get
//     distinct tokens, so unrelated devices can never silently merge.
func DeriveDeviceToken(externalId, prefix string) string {
	sum := sha256.Sum256([]byte(externalId))
	hash := hex.EncodeToString(sum[:])[:tokenHashLen]

	slug := slugify(externalId)
	if slug == "" {
		return prefix + hash
	}
	// Bound the whole token to MaxTokenLen: reserve the prefix, the hash, and the one
	// joining hyphen, then trim any hyphen/underscore the cut left dangling (legal, but
	// tidier not to).
	max := core.MaxTokenLen - len(prefix) - len(hash) - 1
	if len(slug) > max {
		slug = strings.TrimRight(slug[:max], "-_")
	}
	return prefix + slug + "-" + hash
}

// slugify maps a raw id to the token grammar's safe alphabet: alphanumerics, '-' and
// '_' pass through, every other rune (including '/', '.', whitespace and any multibyte
// unicode) becomes a single '-'. Leading '-'/'_' are trimmed so the caller can prepend
// a prefix and keep an alphanumeric-adjacent boundary; an empty result means nothing
// survived (the caller falls back to the hash alone).
func slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}
