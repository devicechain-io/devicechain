// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package settings is the instance-scoped system-settings store for the platform
// (ADR-042 P2): a general key/JSON override store with code-defined defaults.
//
// The shape follows the ADR-038 branding cascade — defaults live in code, the
// table stores ONLY overrides, and a merge yields the effective value — so there
// is no seed migration and a default can never drift from the code that reads it.
//
// It is deliberately self-contained: this package imports neither iam nor identity
// and treats every value as opaque JSON (all interpretation lives in the consumer,
// e.g. the console reads the token-mask setting). It lives inside user-management
// for now because that service is the instance control-plane authority, but the
// seam is pre-cut so it can be extracted to its own service later (ADR-042).
package settings

import (
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// KeyTokenMasks is the first setting (ADR-042): per-entity-type token mask
// templates the console uses to generate and normalize tokens. The backend never
// interprets it (masks are advisory client-side; the backend enforces only the
// global token grammar — see core.ValidateToken).
const KeyTokenMasks = "entity.token_masks"

// KeyBrandingDefault is the instance-wide white-labeling default (ADR-038 Phase 2):
// the JSON branding shape (title/logo/logoMaxHeight/primary/background/foreground/
// accent) an operator can override platform-wide, sitting below any per-tenant
// override and above the console's built-in look. The value shape is owned by the
// branding package; this store treats it as opaque JSON (like the token masks). The
// code default below only sets a title + logo height — colors stay absent so an
// un-rebranded tenant keeps the shipped palette rather than a re-derived one.
const KeyBrandingDefault = "branding.default"

// KeyBasemapDefault is the instance-wide basemap default (ADR-079): the JSON
// basemap shape (tileUrl/attribution/centerLat/centerLon/zoom) an operator can set
// platform-wide, sitting below any per-tenant override. Same cascade shape as the
// branding default above, and treated as opaque JSON here — the value shape and its
// rules are owned by the basemap package.
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

// Definition is a known system setting: its key, its code default value, and a
// human description for the settings UI. The set of Definitions is the whole
// vocabulary — a write to an unknown key is rejected (fail-closed, like typed
// config), so the store never accumulates junk keys.
type Definition struct {
	Key         string
	Default     json.RawMessage
	Description string
}

// Definitions returns the registry of every known system setting. Extend this as
// new settings are introduced; the DB stores only overrides against these keys.
func Definitions() []Definition {
	return []Definition{
		{
			Key:         KeyTokenMasks,
			Default:     json.RawMessage(`{"default":"{slug}"}`),
			Description: `Per-entity-type token mask templates the console uses to generate and normalize tokens. Keys are entity types (or "default"); values are mask templates.`,
		},
		{
			Key:         KeyBrandingDefault,
			Default:     json.RawMessage(`{"title":"DeviceChain","logoMaxHeight":28}`),
			Description: `Instance-wide white-labeling default: title, logo, logoMaxHeight, and hex colors (primary/background/foreground/accent). Sits below any per-tenant override. Omitted colors keep the console's built-in palette.`,
		},
		{
			Key:         KeyBasemapDefault,
			Default:     json.RawMessage(DefaultBasemapJSON),
			Description: `Instance-wide basemap default: tileUrl, attribution, and a fallback view (centerLat/centerLon/zoom). Sits below any per-tenant override. tileUrl and attribution resolve TOGETHER — a tenant that sets its own tile URL never inherits this attribution — and neither may be set without the other. Defaults to the OpenStreetMap standard tile layer, which needs no credentials; change it to point at your own provider.`,
		},
	}
}

// definition looks up a setting definition by key.
func definition(key string) (Definition, bool) {
	for _, d := range Definitions() {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

// SystemSetting is a persisted override row. It is instance-global — no
// TenantScoped, no soft-delete, no TokenReference — so the tenant-scope and
// token-grammar callbacks pass it through; mutations are still audited by the
// core journal. UpdatedBy records the acting identity as plain text (an audit
// value that must survive identity deletion), not a foreign key.
type SystemSetting struct {
	Key       string         `gorm:"primaryKey;size:190"`
	Value     datatypes.JSON `gorm:"not null"`
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:190"`
}

// TableName pins the table name independent of struct-name pluralization.
func (SystemSetting) TableName() string { return "system_settings" }
