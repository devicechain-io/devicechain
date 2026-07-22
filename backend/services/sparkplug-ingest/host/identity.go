// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

const (
	// tokenPrefix namespaces every auto-derived token so a Sparkplug-origin device is
	// recognizable and the token is guaranteed to start with a letter (the grammar
	// requires an alphanumeric first character).
	tokenPrefix = "sp-"
	// tokenHashLen is how many hex chars of the raw-id hash the derived token carries.
	// 12 hex = 48 bits: enough that two DISTINCT external ids that sanitize to the
	// same slug still get distinct tokens (the whole reason the hash is present), with
	// collision odds negligible at any realistic fleet size.
	tokenHashLen = 12
)

// SparkplugExternalId is the ADR-049 external id for the device a Sparkplug message
// identifies: "{group}/{node}" for a node-level message, or "{group}/{node}/{device}"
// for a device-level one. This raw, slash-joined form is the customer-owned foreign
// identity — it is stored ONLY in a device's externalId (which carries no grammar),
// never as a token. It is meaningful only for node/device messages, not STATE.
func SparkplugExternalId(t Topic) string {
	if t.IsDevice() {
		return t.GroupID + "/" + t.EdgeNodeID + "/" + t.DeviceID
	}
	return t.GroupID + "/" + t.EdgeNodeID
}

// DeriveDeviceToken maps a raw Sparkplug external id to a DeviceChain token that
// satisfies the global token grammar (ADR-042: letters, digits, '-' and '_',
// alphanumeric first). A Sparkplug id routinely contains '/', '.', spaces, or
// unicode — all rejected fail-closed by the storage layer — so it can NEVER be a
// token directly; only the externalId field holds it verbatim.
//
// The token is "sp-{slug}-{hash}" (or "sp-{hash}" when nothing survives slugging),
// where slug is the id with every unsafe character mapped to '-', and hash is a
// prefix of sha256(rawId). Two properties matter:
//   - Deterministic: the same external id always derives the same token, so a
//     create/create race between two concurrent DATA bursts collides on the token
//     unique index (which §5's conflict-swallow relies on) rather than making two
//     devices.
//   - Hash-disambiguated: two DISTINCT ids that slug to the same string still get
//     distinct tokens, so unrelated devices can never silently merge.
func DeriveDeviceToken(externalId string) string {
	sum := sha256.Sum256([]byte(externalId))
	hash := hex.EncodeToString(sum[:])[:tokenHashLen]

	slug := slugify(externalId)
	if slug == "" {
		return tokenPrefix + hash
	}
	// Bound the whole token to MaxTokenLen: reserve the prefix, the hash, and the two
	// joining hyphens, then trim any hyphen/underscore the cut left dangling (legal,
	// but tidier not to).
	max := core.MaxTokenLen - len(tokenPrefix) - len(hash) - 1
	if len(slug) > max {
		slug = strings.TrimRight(slug[:max], "-_")
	}
	return tokenPrefix + slug + "-" + hash
}

// slugify maps a raw id to the token grammar's safe alphabet: alphanumerics, '-'
// and '_' pass through, every other rune (including '/', '.', whitespace and any
// multibyte unicode) becomes a single '-'. Leading '-'/'_' are trimmed so the
// caller can prepend a prefix and keep an alphanumeric-adjacent boundary; an empty
// result means nothing survived (the caller falls back to the hash alone).
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
