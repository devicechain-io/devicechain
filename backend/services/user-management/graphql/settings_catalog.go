// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/settings"
)

// SettingResolver resolves the Setting type from a merged effective setting.
type SettingResolver struct {
	E settings.Effective
}

func (r *SettingResolver) Key() string         { return r.E.Key }
func (r *SettingResolver) Description() string { return r.E.Description }
func (r *SettingResolver) Value() string       { return string(r.E.Value) }
func (r *SettingResolver) Overridden() bool    { return r.E.Overridden }

func (r *SettingResolver) UpdatedAt() *string {
	if r.E.UpdatedAt == nil {
		return nil
	}
	return util.FormatTime(*r.E.UpdatedAt)
}

func (r *SettingResolver) UpdatedBy() *string {
	if r.E.UpdatedBy == "" {
		return nil
	}
	return &r.E.UpdatedBy
}

// Settings lists every known setting with its effective value (requires
// settings:read).
func (r *SettingsResolver) Settings(ctx context.Context) ([]*SettingResolver, error) {
	if err := auth.Authorize(ctx, auth.SettingsRead); err != nil {
		return nil, err
	}
	effs, err := r.getSettingsService(ctx).List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*SettingResolver, 0, len(effs))
	for i := range effs {
		out = append(out, &SettingResolver{E: effs[i]})
	}
	return out, nil
}

// TokenMasks returns the effective entity token-mask map as a JSON string. Unlike
// the other settings operations it requires only authentication (any signed-in
// identity), not settings:read: every console user needs the masks to generate
// tokens in the create forms, and the masks are non-sensitive UI templates
// (ADR-042 P3). It is read-only, so there is no write counterpart here.
func (r *SettingsResolver) TokenMasks(ctx context.Context) (string, error) {
	if _, ok := auth.ClaimsFromContext(ctx); !ok {
		return "", auth.ErrUnauthenticated
	}
	eff, err := r.getSettingsService(ctx).Get(ctx, settings.KeyTokenMasks)
	if err != nil {
		return "", err
	}
	return string(eff.Value), nil
}

// Setting resolves one known setting by key (requires settings:read).
func (r *SettingsResolver) Setting(ctx context.Context, args struct{ Key string }) (*SettingResolver, error) {
	if err := auth.Authorize(ctx, auth.SettingsRead); err != nil {
		return nil, err
	}
	eff, err := r.getSettingsService(ctx).Get(ctx, args.Key)
	if err != nil {
		return nil, err
	}
	return &SettingResolver{E: *eff}, nil
}

// SetSetting overrides a setting with a JSON value (requires settings:write). The
// acting identity is recorded as the override's updatedBy.
func (r *SettingsResolver) SetSetting(ctx context.Context, args struct {
	Key   string
	Value string
}) (*SettingResolver, error) {
	if err := auth.Authorize(ctx, auth.SettingsWrite); err != nil {
		return nil, err
	}
	// The generic settings store treats every value as opaque JSON, but the
	// branding.default value has a shape + rules (ADR-038): validate it at the mint
	// point so an operator can never store a value that would be rejected on the
	// tenant path (a non-hex color, an inline-SVG logo) and have it served to every
	// non-overriding tenant. Other keys stay opaque.
	if args.Key == settings.KeyBrandingDefault {
		if err := validateBrandingDefault(args.Value); err != nil {
			return nil, err
		}
	}
	eff, err := r.getSettingsService(ctx).Set(ctx, args.Key, []byte(args.Value), actingUser(ctx))
	if err != nil {
		return nil, err
	}
	return &SettingResolver{E: *eff}, nil
}

// ClearSetting removes a setting's override, reverting to the code default
// (requires settings:write).
func (r *SettingsResolver) ClearSetting(ctx context.Context, args struct{ Key string }) (*SettingResolver, error) {
	if err := auth.Authorize(ctx, auth.SettingsWrite); err != nil {
		return nil, err
	}
	eff, err := r.getSettingsService(ctx).Clear(ctx, args.Key)
	if err != nil {
		return nil, err
	}
	return &SettingResolver{E: *eff}, nil
}

// validateBrandingDefault rejects a branding.default value that is not a
// shape-valid, rule-valid branding override (ADR-038). DisallowUnknownFields so a
// typo'd key surfaces instead of silently no-op'ing.
func validateBrandingDefault(value string) error {
	dec := json.NewDecoder(strings.NewReader(value))
	dec.DisallowUnknownFields()
	var b branding.Branding
	if err := dec.Decode(&b); err != nil {
		return fmt.Errorf("branding.default is not a valid branding object: %w", err)
	}
	return branding.Validate(b)
}

// actingUser returns the authenticated caller's username for audit stamping, or
// "" when unauthenticated (the resolvers above require authority, so this is
// populated in practice).
func actingUser(ctx context.Context) string {
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		return claims.Username
	}
	return ""
}
