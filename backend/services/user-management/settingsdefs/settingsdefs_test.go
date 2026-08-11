// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package settingsdefs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// def returns a shipped definition by key.
func def(t *testing.T, key string) settings.Definition {
	t.Helper()
	d, ok := Registry().Lookup(key)
	require.Truef(t, ok, "%s must be a registered setting", key)
	return d
}

// 🔴 The registry rejects a code default its own validator refuses, so simply
// building it is a real assertion — but only if something builds it. Nothing in
// this package's other tests would fail if Registry() panicked on an unrelated
// path, so it gets its own test.
func TestRegistryBuildsAndEveryShippedDefaultPassesItsOwnValidator(t *testing.T) {
	r := Registry()
	require.Len(t, r.All(), 3)
	for _, d := range r.All() {
		assert.NoErrorf(t, d.Validate(d.Default), "the shipped default for %s is refused by its own validator", d.Key)
	}
}

// Every setting must describe itself: the description is the only thing the
// settings UI can show an operator about a key it has no bespoke editor for.
func TestEveryDefinitionIsDescribed(t *testing.T) {
	for _, d := range Registry().All() {
		assert.NotEmptyf(t, strings.TrimSpace(d.Description), "%s has no description", d.Key)
		assert.NotEmptyf(t, d.Default, "%s has no code default", d.Key)
	}
}

// ---- entity.token_masks -----------------------------------------------------
//
// 🔴 This key had NO validator at all until now — it accepted any JSON, which is
// how a mask that mints nothing could be stored and served to every console user.

func TestValidateTokenMasks(t *testing.T) {
	d := def(t, KeyTokenMasks)

	valid := []string{
		`{}`,
		`{"default":"{slug}"}`,
		`{"device":"device-{alphanumeric-4}","area":"area-{slug}","default":"{slug}"}`,
		`{"device":"{uuid}"}`,
	}
	for _, v := range valid {
		assert.NoErrorf(t, d.Validate(json.RawMessage(v)), "valid token masks rejected: %s", v)
	}

	invalid := map[string]string{
		"not an object":            `["{slug}"]`,
		"null":                     `null`,
		"a non-string mask":        `{"device":42}`,
		"an empty entity type":     `{"":"{slug}"}`,
		"an empty mask":            `{"device":""}`,
		"an unknown placeholder":   `{"device":"dev-{sulg}"}`,
		"no placeholder at all":    `{"device":"device"}`,
		"a space in the literal":   `{"device":"my device-{slug}"}`,
		"a zero-width placeholder": `{"device":"{alphanumeric-0}"}`,
	}
	for name, v := range invalid {
		assert.Errorf(t, d.Validate(json.RawMessage(v)), "%s must be refused: %s", name, v)
	}
}

// The error has to identify WHICH entity type's mask is wrong — the value is a
// map, and "invalid mask" against a twenty-key map is not actionable.
func TestTokenMaskErrorNamesTheEntityType(t *testing.T) {
	err := def(t, KeyTokenMasks).Validate(json.RawMessage(`{"device-type":"dt-{sulg}"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"device-type"`)
}

// 🔴 An unrecognised entity-type KEY is deliberately accepted: the console gains
// entity types as it gains screens, and a server that refused keys it did not
// know would reject a mask written for a newer console against an older backend.
// Stated as a test so a future "tighten this up" change has to argue with it.
func TestTokenMasksAcceptsAnUnknownEntityTypeKey(t *testing.T) {
	assert.NoError(t, def(t, KeyTokenMasks).Validate(json.RawMessage(`{"some-future-type":"{slug}"}`)))
}

// ---- branding.default -------------------------------------------------------

// A value that would be rejected on the tenant path must not be storable here
// either (ADR-038), because this one is served to every non-overriding tenant.
func TestValidateBrandingDefault(t *testing.T) {
	d := def(t, KeyBrandingDefault)

	valid := []string{
		`{}`,
		`{"title":"Acme","logoMaxHeight":40}`,
		`{"primary":"#1f9fb7","accent":"#223344"}`,
		`{"logo":"https://cdn.example.com/logo.svg"}`,
	}
	for _, v := range valid {
		assert.NoErrorf(t, d.Validate(json.RawMessage(v)), "valid branding.default %q rejected", v)
	}

	invalid := []string{
		`{"primary":"blue"}`, // non-hex
		`{"logo":"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="}`, // inline SVG XSS carrier
		`{"logo":"http://example.com/l.png"}`,                   // http scheme
		`{"logoMaxHeight":5000}`,                                // out of range
		`{"unknownField":"x"}`,                                  // unknown key (DisallowUnknownFields)
		`not json`,
	}
	for _, v := range invalid {
		assert.Errorf(t, d.Validate(json.RawMessage(v)), "invalid branding.default %q accepted", v)
	}
}

// ---- basemap.default --------------------------------------------------------

func TestValidateBasemapDefault(t *testing.T) {
	d := def(t, KeyBasemapDefault)

	valid := []string{
		`{}`,
		`{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© Example"}`,
		`{"centerLat":33.75,"centerLon":-84.39,"zoom":10}`,
		`{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"<a href=\"https://e.invalid\">Example</a>"}`,
	}
	for _, v := range valid {
		assert.NoErrorf(t, d.Validate(json.RawMessage(v)), "valid basemap.default rejected: %s", v)
	}

	invalid := map[string]string{
		"a tile URL with no credit line": `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png"}`,
		"a credit line with no tiles":    `{"attribution":"© Example"}`,
		"http":                           `{"tileUrl":"http://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`,
		"not a template":                 `{"tileUrl":"https://t.example.invalid/style.json","attribution":"© E"}`,
		"script in the attribution":      `{"tileUrl":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"<script>alert(1)</script>"}`,
		"out-of-range latitude":          `{"centerLat":91,"centerLon":0}`,
		"half a coordinate":              `{"centerLat":33.75}`,
		// 🔴 The typo case is the reason DisallowUnknownFields is set: without it an
		// operator stores `tile_url`, setSetting reports success, and every map in the
		// instance stays blank behind a stored value that looks correct.
		"an unknown key": `{"tile_url":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`,
		"not json":       `nope`,
	}
	for name, v := range invalid {
		assert.Errorf(t, d.Validate(json.RawMessage(v)), "%s must be refused: %s", name, v)
	}
}

// 🔴 MEASURED, not assumed: encoding/json matches field names CASE-INSENSITIVELY, so
// DisallowUnknownFields does NOT reject `tileURL` — it binds to TileURL and works.
// Pinned here because the natural reading of "unknown fields are rejected" says the
// opposite, and someone will eventually write a validator that depends on the strict
// reading. Only a genuinely different key (see "an unknown key" above) is refused.
func TestKeyCasingIsAcceptedBecauseTheJsonDecoderIsCaseInsensitive(t *testing.T) {
	v := `{"tileURL":"https://t.example.invalid/{z}/{x}/{y}.png","attribution":"© E"}`
	assert.NoError(t, def(t, KeyBasemapDefault).Validate(json.RawMessage(v)),
		"case variation binds to the right field, so it is accepted rather than silently ignored")
}
