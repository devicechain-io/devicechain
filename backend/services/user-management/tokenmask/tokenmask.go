// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tokenmask is the server-side authority for the entity token-mask
// template language (ADR-042 P3). A mask is literal text plus typed placeholders
// — "device-{alphanumeric-4}", "area-{slug}" — that the console fills in to mint
// an entity token.
//
// The masks themselves are advisory at MINT time: the console applies them, the
// backend enforces only the security grammar (core.ValidateToken). But the masks
// are AUTHORED here, stored in the system-settings key entity.token_masks, and
// served to every console user — and until this package existed that key accepted
// any JSON at all. The two failures it lets through are both silent:
//
//   - an unknown placeholder ({sulg} for {slug}) contributes NOTHING to a
//     generated token, so the console quietly mints "area-" instead of
//     "area-north-yard" and the operator sees a plausible-looking prefix;
//   - a mask with no placeholder at all generates the SAME token for every
//     entity, so the first create succeeds and every one after it collides.
//
// Neither is visible in the JSON. Both are refused here.
//
// 🔴 This mirrors frontend/packages/client/src/tokens.ts, which is the console's
// copy of the same grammar. The two are kept in step by hand — the placeholder
// regex, the known type names, and the default widths are duplicated deliberately
// rather than shipped over the wire, because the console must be able to mint a
// token before it has talked to this service. When you change one, change the
// other, and note the asymmetry that makes the duplication safe: this package is
// STRICTER (it refuses masks), the console is more permissive (it renders
// whatever it is given). A rule added here and forgotten there costs a confusing
// server rejection; the reverse — a rule only the console knows — is the one that
// would let a bad mask through, so new rules go here first.
package tokenmask

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/devicechain-io/dc-microservice/core"
)

// placeholderPattern mirrors PLACEHOLDER in tokens.ts: a brace-delimited type
// name with an optional numeric width. The name is matched case-insensitively
// (the console lowercases before its lookup), the width only in decimal.
var placeholderPattern = regexp.MustCompile(`\{([a-zA-Z]+)(?:-(\d+))?\}`)

// The known placeholder types, mirroring KNOWN in tokens.ts. A brace expression
// naming anything else is not a placeholder the console can fill.
const (
	typeAlphanumeric = "alphanumeric"
	typeNumeric      = "numeric"
	typeSlug         = "slug"
	typeUUID         = "uuid"
)

// The widths the console uses when a placeholder carries none.
const (
	defaultAlphanumericWidth = 8
	defaultNumericWidth      = 4
)

// sampleAlphabet is the console's READABLE_ALPHABET: lowercase letters and digits
// minus the ambiguous 0 o 1 l i. Sample generation walks it in order rather than
// at random so a mask always produces the same sample — this package validates by
// generating, and a validator that sometimes passes is worse than none.
const sampleAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// sampleSeed fills {slug} placeholders when generating a sample. Any seed that
// normalizes to a non-empty slug would do; a fixed one keeps Sample deterministic.
const sampleSeed = "Sample Name"

// sampleUUID fills {uuid} placeholders — a real UUID's shape, fixed so the sample
// is stable.
const sampleUUID = "3f2b8c14-9d7e-4a51-b6c2-0e8d5a1f7b93"

// segment is one piece of a parsed mask: a literal run, or a placeholder.
//
// 🔴 hasWidth is separate from width on purpose. "{alphanumeric-0}" and
// "{alphanumeric}" are NOT the same mask: the console reads the width with
// `seg.n ?? 8`, and because "0" is a truthy string in JavaScript its parsed 0
// survives the nullish coalesce — so an explicit 0 mints nothing while an absent
// width mints eight characters. Collapsing the two here would make Sample
// disagree with the generator it exists to predict, and Validate would pass a
// mask that mints an empty token.
type segment struct {
	literal     string
	placeholder string // "" for a literal; the lowercased type name otherwise
	known       bool
	width       int
	hasWidth    bool
	raw         string // the placeholder as written, for error messages
}

// parse splits a mask into literal and placeholder segments, mirroring parseMask
// in tokens.ts.
func parse(mask string) []segment {
	var segments []segment
	last := 0
	for _, m := range placeholderPattern.FindAllStringSubmatchIndex(mask, -1) {
		start, end := m[0], m[1]
		if start > last {
			segments = append(segments, segment{literal: mask[last:start]})
		}
		name := strings.ToLower(mask[m[2]:m[3]])
		seg := segment{placeholder: name, raw: mask[start:end]}
		switch name {
		case typeAlphanumeric, typeNumeric, typeSlug, typeUUID:
			seg.known = true
		}
		if m[4] >= 0 {
			// The pattern only matches digits here, so the only failure Atoi can
			// report is overflow. A width that does not fit in an int is treated as
			// absent rather than clamped: it is nonsense either way, and the length
			// bound in Validate reports it against a sample the operator can read.
			if n, err := strconv.Atoi(mask[m[4]:m[5]]); err == nil {
				seg.width, seg.hasWidth = n, true
			}
		}
		segments = append(segments, seg)
		last = end
	}
	if last < len(mask) {
		segments = append(segments, segment{literal: mask[last:]})
	}
	return segments
}

// Sample generates the token a mask would mint, deterministically. It is the
// server-side twin of generateToken in tokens.ts and exists so validation can ask
// the real question — "does this mask mint a usable token?" — instead of
// approximating it with character classes.
//
// An unknown placeholder contributes nothing, exactly as the console's generator
// does. That is the defect, faithfully reproduced: Validate refuses such a mask
// precisely because Sample shows what the operator would get.
func Sample(mask string) string {
	var b strings.Builder
	for _, seg := range parse(mask) {
		if seg.placeholder == "" {
			b.WriteString(seg.literal)
			continue
		}
		switch seg.placeholder {
		case typeAlphanumeric:
			b.WriteString(fill(sampleAlphabet, seg.resolvedWidth(defaultAlphanumericWidth)))
		case typeNumeric:
			b.WriteString(fill(digits, seg.resolvedWidth(defaultNumericWidth)))
		case typeSlug:
			b.WriteString(Normalize(sampleSeed))
		case typeUUID:
			b.WriteString(sampleUUID)
		}
		// An unknown placeholder writes nothing — see the doc comment.
	}
	return b.String()
}

// digits fills {numeric-N}.
const digits = "0123456789"

// resolvedWidth returns the placeholder's declared width, or the console's
// default when it declared none. A width written as 0 is honoured as 0 — it mints
// nothing, which is a mask defect Validate reports rather than one this quietly
// repairs into something that works.
func (s segment) resolvedWidth(fallback int) int {
	if !s.hasWidth {
		return fallback
	}
	return s.width
}

// fill returns n characters walked from the alphabet in order.
func fill(alphabet string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[i%len(alphabet)])
	}
	return b.String()
}

// hyphenRuns collapses a run of hyphens to one.
var hyphenRuns = regexp.MustCompile(`-+`)

// Normalize kebab-cases a human string into a slug, mirroring normalizeToken in
// tokens.ts: lower-case, whitespace and underscores to hyphens, drop anything
// else, collapse and trim hyphens.
func Normalize(input string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(input)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r), r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(hyphenRuns.ReplaceAllString(b.String(), "-"), "-")
}

// Validate reports whether a mask can mint usable entity tokens. It refuses four
// things, each of which fails silently at mint time if it gets through:
//
//	empty            — nothing to mint from
//	unknown {…}      — contributes nothing; the operator gets a truncated token
//	no placeholder   — every entity mints the SAME token; the second create collides
//	ungrammatical    — the minted sample is not a legal token (core.ValidateToken)
//
// The last check is the general one and subsumes several specific ones by
// construction: a space or a slash in a literal, a leading hyphen, a width that
// pushes the token past the length bound. Asking core.ValidateToken about a real
// generated sample keeps this package from growing its own second opinion about
// the platform grammar.
func Validate(mask string) error {
	if mask == "" {
		return fmt.Errorf("mask must not be empty")
	}
	segments := parse(mask)
	placeholders := 0
	for _, seg := range segments {
		if seg.placeholder == "" {
			continue
		}
		if !seg.known {
			return fmt.Errorf("mask %q uses unknown placeholder %s — known placeholders are {alphanumeric}, {alphanumeric-N}, {numeric-N}, {slug} and {uuid}; an unknown one silently generates nothing", mask, seg.raw)
		}
		placeholders++
	}
	if placeholders == 0 {
		return fmt.Errorf("mask %q has no placeholder, so every entity would be given the identical token %q — add {slug}, {alphanumeric-N}, {numeric-N} or {uuid}", mask, mask)
	}
	sample := Sample(mask)
	if err := core.ValidateToken(sample); err != nil {
		return fmt.Errorf("mask %q generates %q, which is not a valid token: %w", mask, sample, err)
	}
	return nil
}
