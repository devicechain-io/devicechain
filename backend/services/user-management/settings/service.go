// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxValueBytes bounds a stored setting value. Settings are small admin-edited
// config; the cap stops a single write from persisting a payload every settings
// read then hauls back.
const MaxValueBytes = 64 * 1024

// Resolver-facing errors.
var (
	// ErrUnknownSetting is returned when a write or read targets a key with no code
	// definition — the store's vocabulary is closed (fail-closed).
	ErrUnknownSetting = errors.New("unknown setting key")
	// ErrInvalidValue is returned when a written value is not valid JSON.
	ErrInvalidValue = errors.New("setting value must be valid JSON")
	// ErrNoRegistry is returned when a Service was built without one. A nil
	// registry would otherwise make every key unknown, which reads like a data
	// problem rather than the wiring mistake it is.
	ErrNoRegistry = errors.New("settings service has no registry")
	// ErrValueTooLarge is returned when a written value exceeds MaxValueBytes.
	ErrValueTooLarge = fmt.Errorf("setting value exceeds the maximum size of %d bytes", MaxValueBytes)
)

// Effective is a setting resolved for presentation: its definition, the effective
// value (override when present, else the code default), whether it is overridden,
// and the override's audit metadata (nil/empty when using the default).
type Effective struct {
	Key         string
	Description string
	Value       json.RawMessage
	Overridden  bool
	UpdatedAt   *time.Time
	UpdatedBy   string
}

// Service resolves system settings by merging code defaults with stored overrides
// (ADR-042 P2). It owns the cross-cutting rules the raw store should not: the
// closed key vocabulary and value validation, both of which come from the
// Registry it is built with rather than from anything this package knows about
// the values themselves.
type Service struct {
	store    *Store
	registry *Registry
}

// NewService builds the settings Service over the override store and the registry
// of known settings.
func NewService(store *Store, registry *Registry) *Service {
	return &Service{store: store, registry: registry}
}

// definition looks up a setting definition by key.
func (s *Service) definition(key string) (Definition, error) {
	if s.registry == nil {
		return Definition{}, ErrNoRegistry
	}
	d, ok := s.registry.Lookup(key)
	if !ok {
		return Definition{}, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	return d, nil
}

// List returns every known setting with its effective value, in definition order.
func (s *Service) List(ctx context.Context) ([]Effective, error) {
	if s.registry == nil {
		return nil, ErrNoRegistry
	}
	rows, err := s.store.overrides(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]SystemSetting, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}
	defs := s.registry.All()
	out := make([]Effective, 0, len(defs))
	for _, d := range defs {
		out = append(out, merge(d, byKey[d.Key], hasKey(byKey, d.Key)))
	}
	return out, nil
}

// Get returns one known setting's effective value; ErrUnknownSetting for an
// undefined key.
func (s *Service) Get(ctx context.Context, key string) (*Effective, error) {
	def, err := s.definition(key)
	if err != nil {
		return nil, err
	}
	row, err := s.store.get(ctx, key)
	if err != nil {
		return nil, err
	}
	var eff Effective
	if row != nil {
		eff = merge(def, *row, true)
	} else {
		eff = merge(def, SystemSetting{}, false)
	}
	return &eff, nil
}

// Set overrides a known setting with a value and returns the new effective
// setting. updatedBy is the acting identity.
//
// The value must be valid JSON, within the size bound, AND legal for its key —
// the three checks run in that order, because the key's validator is entitled to
// assume it is being handed parseable JSON of a sane size.
//
// 🔴 This is the only write path there is — enforced by construction, not by
// convention: Store's methods are unexported, so no caller can reach the table
// without coming through here. The two keys that had rules were checked in the
// GraphQL mutation, so anything reaching the service another way — a future admin
// API, dcctl, a migration, a test — bypassed them silently.
func (s *Service) Set(ctx context.Context, key string, value []byte, updatedBy string) (*Effective, error) {
	def, err := s.definition(key)
	if err != nil {
		return nil, err
	}
	if len(value) > MaxValueBytes {
		return nil, ErrValueTooLarge
	}
	if !json.Valid(value) {
		return nil, ErrInvalidValue
	}
	if err := def.Validate(value); err != nil {
		return nil, err
	}
	if err := s.store.set(ctx, key, value, updatedBy); err != nil {
		return nil, err
	}
	return s.Get(ctx, key)
}

// Clear removes a known setting's override, reverting it to the code default, and
// returns the resulting effective (default) setting.
func (s *Service) Clear(ctx context.Context, key string) (*Effective, error) {
	if _, err := s.definition(key); err != nil {
		return nil, err
	}
	if err := s.store.clear(ctx, key); err != nil {
		return nil, err
	}
	return s.Get(ctx, key)
}

// merge combines a definition with an optional override into an Effective value.
func merge(d Definition, row SystemSetting, overridden bool) Effective {
	eff := Effective{Key: d.Key, Description: d.Description, Value: d.Default, Overridden: overridden}
	if overridden {
		eff.Value = json.RawMessage(row.Value)
		t := row.UpdatedAt
		eff.UpdatedAt = &t
		eff.UpdatedBy = row.UpdatedBy
	}
	return eff
}

func hasKey(m map[string]SystemSetting, key string) bool {
	_, ok := m[key]
	return ok
}
