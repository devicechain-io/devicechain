// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
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

// TokenMasks returns the effective entity token-mask map as a JSON string on the
// IDENTITY lane, for the admin console's create forms — which are guaranteed an
// identity session by their route guard and are NOT guaranteed a tenant one (an
// operator creating the instance's first tenant holds no tenant session at all).
//
// Unlike the other settings operations it requires only authentication, not
// settings:read (ADR-042 P3). The body is shared with the tenant data plane's
// resolver — see tokenMasks in token_masks.go, which explains why both exist.
func (r *SettingsResolver) TokenMasks(ctx context.Context) (string, error) {
	return tokenMasks(ctx)
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
	// No per-key branching here on purpose. Shape validation belongs to the key's
	// own definition (the settingsdefs registry) and runs inside Service.Set, so it
	// covers every write path rather than this one mutation — and so a new setting
	// cannot be added without one. This chain used to name branding and basemap and
	// silently omit entity.token_masks; that omission is exactly what a list of
	// exceptions in a resolver cannot make visible.
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

// actingUser returns the authenticated caller's username for audit stamping, or
// "" when unauthenticated (the resolvers above require authority, so this is
// populated in practice).
func actingUser(ctx context.Context) string {
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		return claims.Username
	}
	return ""
}
