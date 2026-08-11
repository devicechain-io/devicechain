// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package settingsdefs declares the instance system settings the platform knows
// about (ADR-042 P2): the key, the code default, the operator-facing description,
// and — the part that did not exist before — the validator that decides whether a
// written value is legal.
//
// It sits between the generic settings store and the packages that own the value
// shapes, and that position is the whole reason it exists. The store stays
// shape-agnostic so it can be extracted to its own service later; branding and
// basemap keep owning their own rules; this package is the one place that knows
// which key means which shape. Nothing else needs to.
//
// 🔴 To add a setting, add a settings.Define call below. There is no other step
// and no way to skip the validator — it is a positional argument, so a definition
// without one does not compile. If the value genuinely has no server-checkable
// shape, pass settings.OpaqueJSON and say why in the comment; that makes it a
// decision on the record instead of a blank.
package settingsdefs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-user-management/basemap"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/devicechain-io/dc-user-management/tokenmask"
)

// KeyTokenMasks is the first setting (ADR-042): per-entity-type token mask
// templates the console uses to generate and normalize tokens. The backend does
// not APPLY them (masks are advisory at mint time; the backend enforces only the
// global token grammar — see core.ValidateToken) but it does validate them, since
// a mask that cannot mint a usable token fails silently in the console.
const KeyTokenMasks = "entity.token_masks"

// KeyBrandingDefault is the instance-wide white-labeling default (ADR-038 Phase 2):
// the JSON branding shape (title/logo/logoMaxHeight/primary/background/foreground/
// accent) an operator can override platform-wide, sitting below any per-tenant
// override and above the console's built-in look. The value shape is owned by the
// branding package. The code default below only sets a title + logo height —
// colors stay absent so an un-rebranded tenant keeps the shipped palette rather
// than a re-derived one.
const KeyBrandingDefault = "branding.default"

// KeyBasemapDefault is the instance-wide basemap default (ADR-079): the JSON
// basemap shape (tileUrl/attribution/centerLat/centerLon/zoom) an operator can set
// platform-wide, sitting below any per-tenant override. Same cascade shape as the
// branding default above; the value shape and its rules are owned by the basemap
// package.
//
// 🔴 The code default is OpenStreetMap, and that REVERSES an earlier decision to ship
// nothing. The original reasoning — that the platform must not silently adopt a tile
// provider's terms on an operator's behalf — was right, but the conclusion drawn from
// it was not: shipping an empty canvas made a working install look broken on first
// contact, which is a worse failure than the one it avoided. A named, visible,
// one-click-replaceable default adopts nothing silently.
//
// It is also the parity bar: ThingsBoard ships OpenStreetMap with no credentials.
// The OSMF tile policy sets meetable requirements rather than a commercial ban — a
// browser fetching only the tiles a user is looking at, showing the credit line, is
// exactly the usage it describes. Two obligations ride along and are easy to break
// silently later, so both are guarded rather than remembered:
//
//   - never pre-seed, bulk-fetch or archive tiles (no code does; an "offline area
//     download" feature must never target these servers);
//   - never set a restrictive Referrer-Policy, which would strip the Referer the
//     policy requires from a browser client — hack/check-osm-tile-policy.sh.
const KeyBasemapDefault = "basemap.default"

// DefaultBasemapJSON is the shipped basemap default: the OpenStreetMap standard tile
// layer with the attribution its licence requires.
//
// 🔴 The two halves are ONE value and must move together. Editing the tile URL to
// point somewhere else while leaving this credit line in place would ship a licence
// violation to every instance — the precise defect basemap.Merge exists to prevent,
// reintroduced at the one tier that reaches everybody. TestShippedBasemapDefault
// pins the host and the copyright link against each other for that reason.
const DefaultBasemapJSON = `{"tileUrl":"https://tile.openstreetmap.org/{z}/{x}/{y}.png","attribution":"© <a href=\"https://www.openstreetmap.org/copyright\">OpenStreetMap</a> contributors"}`

// Registry returns the registry of every known system setting.
//
// It is a function rather than a package-level var so that a construction failure
// — a duplicate key, or a code default its own validator rejects — surfaces at
// service startup with a stack, rather than during package initialization of
// anything that happens to import this.
func Registry() *settings.Registry {
	return settings.NewRegistry(
		settings.Define(
			KeyTokenMasks,
			json.RawMessage(`{"default":"{slug}"}`),
			`Per-entity-type token mask templates the console uses to generate and normalize tokens. Keys are entity types (or "default"); values are mask templates built from literal text and placeholders: {slug}, {uuid}, {alphanumeric-N} and {numeric-N}.`,
			validateTokenMasks,
		),
		settings.Define(
			KeyBrandingDefault,
			json.RawMessage(`{"title":"DeviceChain","logoMaxHeight":28}`),
			`Instance-wide white-labeling default: title, logo, logoMaxHeight, and hex colors (primary/background/foreground/accent). Sits below any per-tenant override. Omitted colors keep the console's built-in palette.`,
			validateBrandingDefault,
		),
		settings.Define(
			KeyBasemapDefault,
			json.RawMessage(DefaultBasemapJSON),
			`Instance-wide basemap default: tileUrl, attribution, and a fallback view (centerLat/centerLon/zoom). Sits below any per-tenant override. tileUrl and attribution resolve TOGETHER — a tenant that sets its own tile URL never inherits this attribution — and neither may be set without the other. Defaults to the OpenStreetMap standard tile layer, which needs no credentials; change it to point at your own provider.`,
			validateBasemapDefault,
		),
	)
}

// validateTokenMasks rejects a token-masks value that is not a flat map of entity
// type to usable mask (ADR-042 P3).
//
// 🔴 The KEYS are deliberately NOT checked against a vocabulary of entity types,
// even though the console has one. The console adds entity types as it grows
// screens, and a server that refused an unrecognised key would reject a mask
// written for a type this build has not heard of — a version-skew failure with a
// confusing message, in exchange for catching a typo that costs the operator one
// unstyled token. The VALUES are where silent failure lives, and those are
// checked strictly.
func validateTokenMasks(value json.RawMessage) error {
	var masks map[string]string
	dec := json.NewDecoder(strings.NewReader(string(value)))
	if err := dec.Decode(&masks); err != nil {
		return fmt.Errorf("%s must be a JSON object mapping entity type to mask template: %w", KeyTokenMasks, err)
	}
	if masks == nil {
		// `null` decodes into a nil map without error, and would then store a value
		// the console reads as "no masks at all".
		return fmt.Errorf("%s must be a JSON object, not null", KeyTokenMasks)
	}
	for entityType, mask := range masks {
		if strings.TrimSpace(entityType) == "" {
			return fmt.Errorf("%s has an empty entity type key", KeyTokenMasks)
		}
		if err := tokenmask.Validate(mask); err != nil {
			return fmt.Errorf("%s[%q]: %w", KeyTokenMasks, entityType, err)
		}
	}
	return nil
}

// validateBrandingDefault rejects a branding.default value that is not a
// shape-valid, rule-valid branding override (ADR-038). DisallowUnknownFields so a
// typo'd key surfaces instead of silently no-op'ing.
func validateBrandingDefault(value json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(value)))
	dec.DisallowUnknownFields()
	var b branding.Branding
	if err := dec.Decode(&b); err != nil {
		return fmt.Errorf("%s is not a valid branding object: %w", KeyBrandingDefault, err)
	}
	return branding.Validate(b)
}

// validateBasemapDefault rejects a basemap.default value that is not a shape-valid,
// rule-valid basemap override (ADR-079). DisallowUnknownFields so a typo'd key
// surfaces instead of silently no-op'ing — the same gate the branding default gets,
// and worth more here because a mistyped key would leave the operator staring at a
// stored value that renders nothing.
//
// 🔴 Note what that does NOT cover: encoding/json matches names case-insensitively,
// so `tileURL` is not an unknown field — it binds to TileURL and works. Only a
// genuinely different key (`tile_url`) is refused. Pinned by
// TestKeyCasingIsAcceptedBecauseTheJsonDecoderIsCaseInsensitive.
func validateBasemapDefault(value json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(value)))
	dec.DisallowUnknownFields()
	var b basemap.Basemap
	if err := dec.Decode(&b); err != nil {
		return fmt.Errorf("%s is not a valid basemap object: %w", KeyBasemapDefault, err)
	}
	return basemap.Validate(b)
}
