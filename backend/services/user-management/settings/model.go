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
// nor any of the value shapes it stores, and it treats every value as opaque JSON.
// It lives inside user-management for now because that service is the instance
// control-plane authority, but the seam is pre-cut so it can be extracted to its
// own service later (ADR-042).
//
// # Shape validation without knowing the shapes
//
// "Opaque JSON" used to mean UNVALIDATED JSON, and that was a hole: the store
// accepted anything for any key, and the two keys that did have rules were
// validated by a pair of `if key == …` branches sitting in the GraphQL resolver.
// A third key had none at all, which is how entity.token_masks came to accept
// arbitrary JSON — a list of exceptions is not a contract, and nothing made the
// omission visible.
//
// The fix keeps this package shape-agnostic and moves the obligation to the key's
// owner: a Definition carries a Validator alongside its default, the Registry is
// built by whoever knows the shapes (see the settingsdefs package), and Set runs
// it on every write. The store never learns what a basemap is; it only knows that
// every key can answer "is this value legal for you?".
//
// 🔴 The validator is a POSITIONAL argument to Define and the field behind it is
// unexported, so a new setting cannot be added without answering that question —
// omitting it does not compile. A key that genuinely has no shape passes the
// OpaqueJSON validator, which makes "anything goes" a decision someone wrote down
// rather than a blank nobody noticed.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
)

// Validator reports whether a value is legal for one setting key. It runs on the
// write path, before anything is persisted, and it is the only thing standing
// between an operator's JSON and every consumer of that key — for the two keys
// that cascade to tenants, a bad value would reach every tenant at once.
//
// The value is already known to be syntactically valid JSON within the size
// bound; a Validator's job is the SHAPE and the rules on top of it.
type Validator func(value json.RawMessage) error

// OpaqueJSON is the Validator for a key whose value genuinely has no shape the
// server can check — its meaning lives entirely in a consumer. Passing it is an
// explicit statement, and the only way to get "any valid JSON is fine" past
// Define, which is the difference between a considered choice and a forgotten
// one.
//
// Reach for it rarely. A key with no server-side rules can be written to a value
// the console then cannot read, and the operator finds out from a broken screen
// rather than a rejected save.
func OpaqueJSON(json.RawMessage) error { return nil }

// ErrNoValidator is returned when a Definition reaches the write path without a
// validator. Define makes that unreachable for definitions built the intended
// way; this covers a Definition assembled as a bare struct literal, where the
// zero value would otherwise mean "accept anything" — the failure mode this whole
// arrangement exists to remove. Fail closed instead.
var ErrNoValidator = errors.New("setting definition has no validator")

// Definition is a known system setting: its key, its code default value, a human
// description for the settings UI, and the validator that decides whether a
// written value is legal. The set of Definitions in the Registry is the whole
// vocabulary — a write to an unknown key is rejected (fail-closed, like typed
// config), so the store never accumulates junk keys.
type Definition struct {
	Key         string
	Default     json.RawMessage
	Description string
	validate    Validator
}

// Define builds a Definition. Every argument is required and the validator is
// positional, so a new setting that forgets its rules is a compile error rather
// than a silently permissive key.
//
// It panics on a missing key or validator: this runs at startup over constant
// data, so a failure here is a programming error that must not become a running
// instance with an unguarded setting.
func Define(key string, def json.RawMessage, description string, validate Validator) Definition {
	if key == "" {
		panic("settings.Define: key must not be empty")
	}
	if validate == nil {
		panic(fmt.Sprintf("settings.Define: %q needs a validator; pass settings.OpaqueJSON to state that it has no shape", key))
	}
	return Definition{Key: key, Default: def, Description: description, validate: validate}
}

// Validate applies the key's shape rules to a value.
func (d Definition) Validate(value json.RawMessage) error {
	if d.validate == nil {
		return fmt.Errorf("%w: %q", ErrNoValidator, d.Key)
	}
	return d.validate(value)
}

// Registry is the closed vocabulary of known settings, in definition order (the
// order the console renders them in).
type Registry struct {
	defs  []Definition
	byKey map[string]Definition
}

// NewRegistry builds the registry from a definition list, rejecting a duplicate
// key and — the check worth having — a code default its OWN validator refuses.
// A shipped default that cannot be written back is a real bug and an odd one to
// debug: the settings page shows it, an operator edits one field, and the save is
// rejected for a reason that was already there before they touched it.
//
// Like Define it panics, for the same reason: this is startup-time validation of
// constant data, and the alternative to a loud failure is an instance running
// with a broken default.
func NewRegistry(defs ...Definition) *Registry {
	byKey := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if _, dup := byKey[d.Key]; dup {
			panic(fmt.Sprintf("settings.NewRegistry: duplicate key %q", d.Key))
		}
		if err := d.Validate(d.Default); err != nil {
			panic(fmt.Sprintf("settings.NewRegistry: the code default for %q is rejected by its own validator: %v", d.Key, err))
		}
		byKey[d.Key] = d
	}
	return &Registry{defs: defs, byKey: byKey}
}

// All returns every definition in order.
func (r *Registry) All() []Definition { return r.defs }

// Lookup finds a definition by key.
func (r *Registry) Lookup(key string) (Definition, bool) {
	d, ok := r.byKey[key]
	return d, ok
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
