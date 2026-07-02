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

// Resolver-facing errors.
var (
	// ErrUnknownSetting is returned when a write or read targets a key with no code
	// definition — the store's vocabulary is closed (fail-closed).
	ErrUnknownSetting = errors.New("unknown setting key")
	// ErrInvalidValue is returned when a written value is not valid JSON.
	ErrInvalidValue = errors.New("setting value must be valid JSON")
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
// closed key vocabulary and value validation.
type Service struct {
	store *Store
}

// NewService builds the settings Service over the override store.
func NewService(store *Store) *Service { return &Service{store: store} }

// List returns every known setting with its effective value, in definition order.
func (s *Service) List(ctx context.Context) ([]Effective, error) {
	rows, err := s.store.Overrides(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]SystemSetting, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}
	defs := Definitions()
	out := make([]Effective, 0, len(defs))
	for _, d := range defs {
		out = append(out, merge(d, byKey[d.Key], hasKey(byKey, d.Key)))
	}
	return out, nil
}

// Get returns one known setting's effective value; ErrUnknownSetting for an
// undefined key.
func (s *Service) Get(ctx context.Context, key string) (*Effective, error) {
	def, ok := definition(key)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	row, err := s.store.Get(ctx, key)
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

// Set overrides a known setting with a value (opaque, but must be valid JSON) and
// returns the new effective setting. updatedBy is the acting identity.
func (s *Service) Set(ctx context.Context, key string, value []byte, updatedBy string) (*Effective, error) {
	if _, ok := definition(key); !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	if !json.Valid(value) {
		return nil, ErrInvalidValue
	}
	if err := s.store.Set(ctx, key, value, updatedBy); err != nil {
		return nil, err
	}
	return s.Get(ctx, key)
}

// Clear removes a known setting's override, reverting it to the code default, and
// returns the resulting effective (default) setting.
func (s *Service) Clear(ctx context.Context, key string) (*Effective, error) {
	if _, ok := definition(key); !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	if err := s.store.Clear(ctx, key); err != nil {
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
