// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package locale is the tenant default-locale shape and its validation (ADR-066
// sub-workstream d). Like the branding and basemap packages beside it, it is a leaf
// — it imports neither iam nor settings — so the cascade resolver, the tenant
// mutation validator and the instance-default setting validator all reference one
// shape and one rule set.
//
// A tenant locale is an OVERRIDE: nil means "inherit the tier below" (the operator's
// `locale.default` system setting, then the code default, then — in the console —
// the viewer's own browser languages). The value is a BCP-47 language tag such as
// "en", "es" or "pt-BR".
//
// # Why this does NOT check the tag against a list of shipped catalogs
//
// The obvious stricter rule is "refuse any locale the console does not ship", and it
// was rejected on purpose. The shipped set lives in the CONSOLE (SUPPORTED_LOCALES
// in src/i18n/config.ts) because that is where the catalogs live; mirroring it here
// would create a second copy that has to be kept in step by hand, and the failure it
// produces when it drifts is the bad direction — the backend refusing a locale the
// console has just started shipping, with a message that reads like the operator
// typed it wrong.
//
// The token-masks validator beside this one reasons the same way about entity types,
// and the trade lands in the same place: SHAPE is checked strictly here, MEMBERSHIP
// is checked where the vocabulary actually lives. The console's locale picker is a
// select over SUPPORTED_LOCALES, so a tenant admin cannot choose an unshipped locale
// in the first place, and applyTenantDefaultLocale ignores one that arrives anyway
// rather than rendering raw keys to a user.
//
// What that leaves is benign and forward-compatible: a locale stored through a raw
// GraphQL call for a catalog this build does not have is inert until the catalog
// ships, at which point it starts working. What it does NOT leave is a stored value
// that is not a language tag at all — a sentence, an underscore-separated POSIX
// locale, or a kilobyte of text — which is what the rules below refuse.
package locale

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxLen bounds a stored language tag. A BCP-47 tag carrying a language, a script and
// a region subtag ("zh-Hans-CN") is 11 characters; this leaves generous room while
// keeping the column a tag rather than a payload.
const MaxLen = 35

// tagRe matches the subset of BCP-47 this platform stores: a 2- or 3-letter primary
// language subtag, an optional 4-letter script subtag, and an optional region subtag
// that is either 2 letters or 3 digits.
//
// Deliberately a SUBSET rather than the full grammar. Full BCP-47 admits variants,
// extensions and private-use sequences that no i18next catalog directory is ever
// named after, so admitting them would widen what can be STORED without widening what
// can WORK. What this rejects is what matters: "en_US" (the POSIX spelling, which
// i18next resolves to nothing), "English", and anything that is prose rather than a
// tag.
var tagRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z]{4})?(-([A-Za-z]{2}|[0-9]{3}))?$`)

// Normalize turns a submitted locale into the value to store: whitespace trimmed,
// blank collapsed to nil, and the tag put in BCP-47 canonical case.
//
// 🔴 The blank-to-nil collapse is load-bearing rather than tidiness, and it is the
// same trap basemap.Normalize documents. A pointer to "" is what a non-console client
// sends when it means "clear this" — the console maps an emptied field to null;
// dcctl, the SDKs and a raw GraphQL caller need not — and a stored blank is NOT the
// same as an absent one. It WINS the cascade and masks the operator's
// `locale.default`, so the tenant's users fall through to their browser language on
// an instance that has a default configured.
//
// The case canonicalization (lowercase language, Titlecase script, UPPERCASE region)
// makes the stored value deterministic, so "es-mx" and "es-MX" are one row value
// rather than two that compare unequal.
func Normalize(l *string) *string {
	if l == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*l)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "-")
	for i, p := range parts {
		switch {
		case i == 0:
			parts[i] = strings.ToLower(p)
		case len(p) == 4:
			// A 4-letter subtag in this grammar is a script: Titlecase.
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		default:
			parts[i] = strings.ToUpper(p)
		}
	}
	canonical := strings.Join(parts, "-")
	return &canonical
}

// Validate rejects a malformed locale override. A nil locale is legal — it is what
// "inherit" looks like — so only a present value is checked. Callers normalize first,
// so a blank never reaches here as a value; a caller that skips normalization gets a
// refusal rather than a silently stored blank.
func Validate(l *string) error {
	if l == nil {
		return nil
	}
	if len(*l) > MaxLen {
		return fmt.Errorf("locale must be at most %d characters, got %d", MaxLen, len(*l))
	}
	if !tagRe.MatchString(*l) {
		return fmt.Errorf("locale %q is not a language tag: expected a BCP-47 tag such as %q, %q or %q", *l, "en", "es", "pt-BR")
	}
	return nil
}

// Merge overlays a higher-priority override onto a lower one — the tenant's own
// locale over the operator's `locale.default`.
//
// 🔴 Both inputs are NORMALIZED first, for the reason Normalize states: without it a
// stored blank at the high tier reads as SET and masks the tier below. Normalizing
// here also covers any blank written before that gate existed.
func Merge(high, low *string) *string {
	high, low = Normalize(high), Normalize(low)
	if high != nil {
		return high
	}
	return low
}
