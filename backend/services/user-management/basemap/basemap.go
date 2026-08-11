// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package basemap is the tenant basemap shape and its validation (ADR-079). Like
// the branding package beside it, it is a leaf — it imports neither iam nor
// settings — so the cascade resolver, the tenant mutation validator and the
// instance-default setting validator all reference one shape and one rule set.
//
// A Basemap is an OVERRIDE: every field is optional, and a nil field inherits the
// next tier down (a tenant's override, then the operator's `basemap.default`
// system setting). That bottom tier ships a real value — the OpenStreetMap
// standard tile layer, see settings.DefaultBasemapJSON — so an instance nobody
// has configured still draws a map.
//
// A basemap unset at EVERY tier is still reachable, and is what the plain-panel
// path exists for: an operator gets there by storing `{}` as the instance default.
// 🔴 Not by CLEARING it — settings.Clear drops the override and reverts to the code
// default above, which now means "Reset to default" turns maps back ON.
package basemap

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Basemap is a tenant/instance basemap override. Every field is optional.
//
// The fields fall into two groups, and the split governs both Merge and Validate:
//
//   - the TILE SOURCE (TileURL + Attribution) — atomic, because a tile URL and the
//     credit line its licence requires are one indivisible thing;
//   - the CAMERA FALLBACK (CenterLat + CenterLon + Zoom) — a starting view used only
//     when a surface has nothing of its own to fit to.
type Basemap struct {
	TileURL     *string  `json:"tileUrl,omitempty"`
	Attribution *string  `json:"attribution,omitempty"`
	CenterLat   *float64 `json:"centerLat,omitempty"`
	CenterLon   *float64 `json:"centerLon,omitempty"`
	Zoom        *float64 `json:"zoom,omitempty"`
}

// Validation bounds (ADR-079 §8).
const (
	// MaxTileURLLen bounds the tile template. Matches the branding logo URL cap:
	// both are operator-supplied URLs stored on the control-plane row.
	MaxTileURLLen = 2048
	// MaxAttributionLen bounds the credit line. Generous enough for the multi-source
	// attributions a composited basemap carries ("© A contributors | © B"), small
	// enough that it stays a credit line rather than a payload.
	MaxAttributionLen = 512
	// MinZoom / MaxZoom bound the fallback zoom. MapLibre's own usable range; zoom is
	// fractional, so this is a float bound rather than an integer one.
	MinZoom = 0
	MaxZoom = 24
)

// anchorRe matches the ONE markup construct an attribution may contain: an opening
// anchor with an https href and no other attribute. Deliberately strict — exactly
// one space, double quotes, no target/rel/class, no single quotes, no whitespace
// inside the tag beyond that one separator. See validateAttribution for why the
// allow-list exists at all, and why it is not simply "strip all markup".
//
// The href bound is the attribution cap rather than a URL cap: the href lives INSIDE
// the attribution, so it can never legitimately exceed it, and bounding the repetition
// keeps the expression linear on adversarial input.
var anchorRe = regexp.MustCompile(`^<a href="https://[^"<>\s]{1,` + strconv.Itoa(MaxAttributionLen) + `}">`)

// tilePlaceholders are the substitution sets that make a URL a tile TEMPLATE rather
// than a single image. MapLibre substitutes {z}/{x}/{y} for an XYZ scheme, or one of
// the two alternatives for the schemes that do not use a tile triple. A URL carrying
// none of them requests the same image for every tile on the map, which is exactly
// the shape of the two common paste errors: a single tile's URL, and a style JSON URL.
//
// {ratio} is NOT listed: it is a modifier on an XYZ template, never a scheme of its
// own, so a URL carrying only {ratio} is still not a template.
var tilePlaceholders = [][]string{
	{"{z}", "{x}", "{y}"},
	{"{bbox-epsg-3857}"},
	{"{quadkey}"},
}

// knownPlaceholders is every token the RENDERER substitutes. Anything else in braces
// is passed through to the provider verbatim.
//
// 🔴 THIS LIST IS READ FROM MAPLIBRE'S SOURCE, NOT REMEMBERED. It is the exact set in
// the `.replace()` chain of Tile.prototype.getTileURL — {prefix}, {z}, {x}, {y},
// {ratio}, {quadkey}, {bbox-epsg-3857}. The first draft of this list omitted {prefix}
// (a sharding token substituted with the low nibbles of x and y) purely because the
// author did not know it existed, which turned a fail-closed guard into a refusal of
// a URL that renders perfectly — and shipped that false enumeration into the error
// message and the published documentation. When MapLibre is upgraded, re-read the
// chain rather than assuming this list is still complete; TestKnownPlaceholdersMatchesTheRenderer
// in the console suite is what actually holds the two in step.
//
// 🔴 The renderer is MapLibre, and MapLibre is not Leaflet. Leaflet substitutes an
// {s} subdomain token, and because Leaflet is what most tile documentation on the
// internet is written for, the URL an operator copies very often carries one:
// `{s}.tile.opentopomap.org`, `{s}.basemaps.cartocdn.com`, `{a|b|c}.tile…`. MapLibre
// requests that host LITERALLY, DNS fails, and every tile 404s — on a value that
// passed validation, was stored, and looks correct in the form.
//
// That is the same defect the template rule below already exists to catch ("a URL
// with no placeholder requests the same image for every tile"): a stored tile source
// that cannot draw. So the rule is fail-closed on the token set rather than a
// blocklist of the one Leaflet-ism we happen to have met — an unknown token is far
// likelier to be a placeholder from some other library's docs than a brace a provider
// genuinely wants in its query string.
//
// {apiKey} is deliberately ABSENT, and that absence is load-bearing. It is the
// console catalog's own token for composing a provider key into a template, never
// MapLibre's; a URL still carrying one means the key was never substituted, which
// would otherwise be stored and send every tile request the literal text "{apiKey}".
var knownPlaceholders = map[string]bool{
	"prefix": true,
	"z":      true, "x": true, "y": true,
	"ratio":          true,
	"bbox-epsg-3857": true,
	"quadkey":        true,
}

// placeholderRe finds each {token} in a tile template. Non-greedy by construction —
// `[^{}]*` cannot span a brace — so `{a}/{b}` is two tokens rather than one.
var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// Merge overlays a higher-priority override onto a lower one (ADR-079 §3).
//
// 🔴 It is NOT a uniform per-field merge, and that is the whole point of this
// function existing rather than being written inline.
//
// The TILE SOURCE resolves as a unit, from whichever tier sets TileURL. Merging
// TileURL and Attribution independently would let a tenant that sets only a tile URL
// inherit the OPERATOR's attribution — pairing one provider's tiles with another
// provider's credit line. That is not a cosmetic mismatch, it is a licence violation
// manufactured by the merge — in the one system whose whole premise is that tiles
// carry terms, and that getting the credit right is the platform's job rather than
// the operator's to remember.
//
// The CAMERA FALLBACK carries no licence weight and merges per field, so an operator
// can set a sensible instance-wide starting view that a tenant refines without having
// to re-declare a provider to keep it.
//
// 🔴 Both inputs are NORMALIZED first, and that is load-bearing rather than tidiness.
// A pointer to "" (or to "   ") is what a non-console client sends when it means
// "clear this": the console maps an emptied field to null, but dcctl, the SDKs and a
// raw GraphQL caller do not have to. Without normalizing, `high.TileURL != nil` reads
// that blank as SET, the tenant tier wins with an empty tile source, and the
// operator's instance default is MASKED — the tenant gets a blank map on an instance
// that has one configured, which is the exact inheritance this cascade exists to
// provide. Normalizing here also covers any blank already stored by an earlier write.
func Merge(high, low Basemap) Basemap {
	var out Basemap
	high, low = Normalize(high), Normalize(low)

	if high.TileURL != nil {
		out.TileURL = high.TileURL
		out.Attribution = high.Attribution
	} else {
		out.TileURL = low.TileURL
		out.Attribution = low.Attribution
	}

	out.CenterLat, out.CenterLon = low.CenterLat, low.CenterLon
	if high.CenterLat != nil || high.CenterLon != nil {
		// A coordinate is a pair, so the two halves move together for the same reason
		// the tile source does: a tenant latitude silently paired with an operator
		// longitude is a point neither tier chose.
		out.CenterLat, out.CenterLon = high.CenterLat, high.CenterLon
	}

	out.Zoom = low.Zoom
	if high.Zoom != nil {
		out.Zoom = high.Zoom
	}
	return out
}

// Validate rejects a malformed override (ADR-079 §8). All fields are optional; the
// value is rejected at the mint point, fail-closed, and never stored — so a
// consumer reading a stored basemap never has to defend against one.
func Validate(b Basemap) error {
	if err := validateTileSource(b); err != nil {
		return err
	}
	return validateCamera(b)
}

// IsSet reports whether a resolved basemap actually names a tile source. A basemap
// with only a camera fallback is legitimate (an operator can set a starting view
// before choosing a provider) but has nothing to render.
//
// Whitespace is not a tile source: a pointer to "   " reports false here, matching
// what Normalize and the renderers do with it.
func IsSet(b Basemap) bool {
	_, ok := trimmedValue(b.TileURL)
	return ok
}

// Normalize collapses "written but empty" to "not set": every string field is
// trimmed, and a field left blank becomes nil.
//
// 🔴 This is what makes "clear this override" expressible over the wire. A nil field
// and a pointer to "" mean the same thing to a human filling in a form, but they are
// different rows in the database and — before this existed — different answers from
// Merge. Applied at the write mint point so nothing blank is ever STORED, and again
// inside Merge so nothing blank stored earlier can still mask a lower tier.
//
// It also means a stored value is byte-identical to the value that was validated:
// without the trim, "  https://…  " passed Validate and was written back with its
// padding, leaving the renderers to trim it independently and forever.
func Normalize(b Basemap) Basemap {
	out := b
	if v, ok := trimmedValue(b.TileURL); ok {
		out.TileURL = &v
	} else {
		out.TileURL = nil
	}
	if v, ok := trimmedValue(b.Attribution); ok {
		out.Attribution = &v
	} else {
		out.Attribution = nil
	}
	return out
}

// validateTileSource enforces the atomic pair: either both halves are present and
// valid, or both are absent. An attribution alone is refused rather than ignored —
// storing one would leave a credit line attached to whatever tile source is
// inherited from below, which is the same licence defect Merge exists to prevent.
func validateTileSource(b Basemap) error {
	url, hasURL := trimmedValue(b.TileURL)
	attr, hasAttr := trimmedValue(b.Attribution)

	switch {
	case !hasURL && !hasAttr:
		return nil
	case hasURL && !hasAttr:
		return fmt.Errorf("basemap.attribution is required when basemap.tileUrl is set: showing a provider's tiles without the credit line their licence requires is a licence violation, so the two are stored as one value or not at all")
	case !hasURL && hasAttr:
		return fmt.Errorf("basemap.attribution requires basemap.tileUrl: the tile source and its credit line are one value, and an attribution alone would be applied to a tile source it does not describe")
	}

	if err := validateTileURL(url); err != nil {
		return err
	}
	return validateAttribution(attr)
}

func validateTileURL(url string) error {
	if len(url) > MaxTileURLLen {
		return fmt.Errorf("basemap.tileUrl must be at most %d characters", MaxTileURLLen)
	}
	// https only. In any TLS deployment the browser blocks an http tile source as
	// mixed content, so accepting one stores a value that cannot render; and the URL
	// commonly carries a provider key, which should not travel in cleartext. 🔴 This
	// does refuse a plain-http tile server on an internal network behind an http
	// console — a known, accepted trade-off (ADR-079 §8), not an oversight.
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("basemap.tileUrl must be an https URL (an http tile source is blocked as mixed content by any console served over TLS)")
	}
	if strings.ContainsAny(url, " \t\r\n<>\"") {
		return fmt.Errorf("basemap.tileUrl must not contain whitespace or markup characters")
	}
	// 🔴 Unknown tokens are rejected BEFORE the required-set check, and the order is
	// the point. `{s}.tile.example.com/{z}/{x}/{y}.png` already contains {z}, {x} and
	// {y}, so the loop below would return nil and the {s} would never be looked at —
	// the exact URL shape this rule exists to catch would pass through the rule.
	for _, m := range placeholderRe.FindAllStringSubmatch(url, -1) {
		if !knownPlaceholders[m[1]] {
			return fmt.Errorf(`basemap.tileUrl contains %s, which the map renderer does not substitute — it would be requested literally and every tile would fail. Only {prefix}, {z}, {x}, {y}, {ratio}, {bbox-epsg-3857} and {quadkey} are replaced; a subdomain placeholder like {s} comes from Leaflet documentation and should be replaced with a single subdomain (for example a.tile.example.com)`, m[0])
		}
	}
	for _, set := range tilePlaceholders {
		if containsAll(url, set) {
			return nil
		}
	}
	return fmt.Errorf(`basemap.tileUrl must be a tile TEMPLATE containing {z}, {x} and {y} (or {bbox-epsg-3857}, or {quadkey}) — a URL with no placeholder requests the same image for every tile`)
}

// validateAttribution bounds the credit line and allow-lists its markup.
//
// 🔴 The allow-list exists because MapLibre renders attribution as HTML. It does run
// a sanitizer over it first, so this is defense in depth rather than a claim that the
// library leaves the hole open — but that sanitizer's own source comment says it
// "might not be enough to prevent all XSS attacks", and this value is stored,
// cross-user, and at the instance-default tier cross-TENANT. That combination is
// worth a second gate that we own.
//
// Links are permitted rather than stripped because several providers' licences
// require the credit to LINK to their copyright page, so a plain-text-only rule would
// make compliance impossible rather than merely inconvenient.
func validateAttribution(attr string) error {
	if utf8.RuneCountInString(attr) > MaxAttributionLen {
		return fmt.Errorf("basemap.attribution must be at most %d characters", MaxAttributionLen)
	}
	for _, r := range attr {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("basemap.attribution must not contain control characters")
		}
	}

	// Walk the string tag by tag. Anything that opens a tag and is not one of the two
	// permitted forms is refused, so the default for an unrecognised construct is
	// rejection rather than pass-through.
	open := 0
	for i := 0; i < len(attr); {
		next := strings.IndexByte(attr[i:], '<')
		if next < 0 {
			break
		}
		i += next
		rest := attr[i:]

		if strings.HasPrefix(rest, "</a>") {
			if open == 0 {
				return fmt.Errorf("basemap.attribution has a closing </a> with no matching link")
			}
			open--
			i += len("</a>")
			continue
		}
		m := anchorRe.FindString(rest)
		if m == "" {
			return fmt.Errorf(`basemap.attribution may contain only links written exactly as <a href="https://…">text</a> — no other markup, attributes or quoting`)
		}
		if open > 0 {
			return fmt.Errorf("basemap.attribution must not nest links")
		}
		open++
		i += len(m)
	}
	if open != 0 {
		return fmt.Errorf("basemap.attribution has an unclosed link (every <a …> needs its </a>)")
	}
	return nil
}

// validateCamera bounds the fallback view. Latitude and longitude are validated as a
// pair: half a coordinate names no point, so accepting one would store a value whose
// only possible use is to be silently discarded.
func validateCamera(b Basemap) error {
	switch {
	case b.CenterLat != nil && b.CenterLon == nil:
		return fmt.Errorf("basemap.centerLon is required when basemap.centerLat is set")
	case b.CenterLat == nil && b.CenterLon != nil:
		return fmt.Errorf("basemap.centerLat is required when basemap.centerLon is set")
	}
	if b.CenterLat != nil {
		if !isFinite(*b.CenterLat) || *b.CenterLat < -90 || *b.CenterLat > 90 {
			return fmt.Errorf("basemap.centerLat must be between -90 and 90 (got %v)", *b.CenterLat)
		}
		if !isFinite(*b.CenterLon) || *b.CenterLon < -180 || *b.CenterLon > 180 {
			return fmt.Errorf("basemap.centerLon must be between -180 and 180 (got %v)", *b.CenterLon)
		}
	}
	if b.Zoom != nil {
		if !isFinite(*b.Zoom) || *b.Zoom < MinZoom || *b.Zoom > MaxZoom {
			return fmt.Errorf("basemap.zoom must be between %d and %d (got %v)", MinZoom, MaxZoom, *b.Zoom)
		}
	}
	return nil
}

// trimmedValue reports a pointer's trimmed value and whether it carries content: a
// pointer to "" and a pointer to "   " both report "not set".
//
// It only ANSWERS the question. Turning that answer into a cleared override is
// Normalize's job, and callers that skip Normalize get the raw pointer semantics —
// which is precisely the trap that made a blank tile URL mask the operator default.
func trimmedValue(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	v := strings.TrimSpace(*p)
	return v, v != ""
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
