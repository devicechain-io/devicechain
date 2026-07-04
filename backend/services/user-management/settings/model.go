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
			Description: `Per-entity-type token mask templates the console uses to generate and normalize tokens (ADR-042). Keys are entity types (or "default"); values are mask templates.`,
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
