// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"gorm.io/gorm"
)

// Role-catalog and tenant errors (ADR-033). Sentinels for the resolver layer.
var (
	ErrRoleNotFound         = errors.New("role not found")
	ErrProtectedRole        = errors.New("the superuser system role cannot be deleted")
	ErrTenantHasMemberships = errors.New("tenant still has memberships; remove them first")
	ErrTierNotFound         = errors.New("tenant tier not found")
	ErrTierInUse            = errors.New("tenant tier still has tenants; move them to another tier first")
)

// RoleInput is the data to create a role (ADR-008 RBAC / ADR-033). Scope is
// "system" or "tenant"; every authority must name a known capability.
type RoleInput struct {
	Scope       string
	Token       string
	Name        string
	Description string
	Authorities []string
}

// RoleMutableInput is the data to update a role: its identity (scope, token) is
// fixed, only the name/description/authorities change.
type RoleMutableInput struct {
	Name        string
	Description string
	Authorities []string
}

// ListRoles returns the role catalog, optionally filtered to a scope.
func (s *Service) ListRoles(ctx context.Context, scope *iam.RoleScope) ([]iam.Role, error) {
	return s.iam.ListRoles(ctx, scope)
}

// CreateRole creates a role after validating its scope and authorities.
func (s *Service) CreateRole(ctx context.Context, in RoleInput) (*iam.Role, error) {
	scope, err := parseScope(in.Scope)
	if err != nil {
		return nil, err
	}
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := validateAuthorities(in.Authorities); err != nil {
		return nil, err
	}
	r := &iam.Role{
		Scope: scope, Token: in.Token, Authorities: in.Authorities,
		NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description)},
	}
	if err := s.iam.CreateRole(ctx, r); err != nil {
		return nil, err
	}
	return s.iam.RoleByScopeToken(ctx, scope, in.Token)
}

// UpdateRole replaces a role's name/description/authorities.
func (s *Service) UpdateRole(ctx context.Context, scope, token string, in RoleMutableInput) (*iam.Role, error) {
	rs, err := parseScope(scope)
	if err != nil {
		return nil, err
	}
	if err := validateAuthorities(in.Authorities); err != nil {
		return nil, err
	}
	r, err := s.loadRole(ctx, rs, token)
	if err != nil {
		return nil, err
	}
	r.Name = rdb.NullStrOf(&in.Name)
	r.Description = rdb.NullStrOf(&in.Description)
	r.Authorities = in.Authorities
	if err := s.iam.UpdateRole(ctx, r); err != nil {
		return nil, err
	}
	return s.iam.RoleByScopeToken(ctx, rs, token)
}

// DeleteRole removes a role and clears its assignments. Idempotent: a missing
// role returns (false, nil). The seeded superuser system role is protected so the
// instance cannot be locked out of its own admin plane.
func (s *Service) DeleteRole(ctx context.Context, scope, token string) (bool, error) {
	rs, err := parseScope(scope)
	if err != nil {
		return false, err
	}
	if rs == iam.ScopeSystem && token == iam.SuperuserRoleToken {
		return false, ErrProtectedRole
	}
	r, err := s.iam.RoleByScopeToken(ctx, rs, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.iam.DeleteRole(ctx, r); err != nil {
		return false, err
	}
	return true, nil
}

// TenantInput is the data to create a tenant. Config is freeform JSON (ADR-033).
// The Ingest* / Outbound* governance overrides are nil to inherit the platform
// default.
// GovernanceOverrides carries every per-tenant ADR-023 rate ceiling an admin may
// declare. Each is nullable: nil means "inherit the platform default", never
// unlimited. Grouped in one struct — and embedded in both tenant inputs — so a new
// dimension is added once here rather than threaded through two input structs and a
// validator's positional argument list (which, at six interchangeable numeric
// pointers, is a swap waiting to happen).
type GovernanceOverrides struct {
	IngestMessagesPerSecond   *float64
	IngestBurst               *int
	OutboundMessagesPerSecond *float64
	OutboundBurst             *int
	// AI-inference rate (ADR-056 §6). Declared per MINUTE, unlike the per-second
	// device-traffic dimensions above: drafting is a human-paced authoring action.
	AiInferenceRequestsPerMinute *float64
	AiInferenceBurst             *int
}

// validate rejects a non-positive override on any governance dimension. A nil field
// means "inherit the platform default"; a provided value must be positive — a zero
// or negative ceiling is never a valid override (the platform default, itself always
// positive, is the fail-safe floor), so callers clear an override by omitting it,
// not by setting it to zero. Field names match the GraphQL input so the error points
// at what the caller actually sent.
func (g GovernanceOverrides) validate() error {
	for _, r := range []struct {
		field string
		value *float64
	}{
		{"ingestMessagesPerSecond", g.IngestMessagesPerSecond},
		{"outboundMessagesPerSecond", g.OutboundMessagesPerSecond},
		{"aiInferenceRequestsPerMinute", g.AiInferenceRequestsPerMinute},
	} {
		if err := validateRateOverride(r.field, r.value); err != nil {
			return err
		}
	}
	for _, b := range []struct {
		field string
		value *int
	}{
		{"ingestBurst", g.IngestBurst},
		{"outboundBurst", g.OutboundBurst},
		{"aiInferenceBurst", g.AiInferenceBurst},
	} {
		if err := validateBurstOverride(b.field, b.value); err != nil {
			return err
		}
	}
	return nil
}

// applyTo writes every override onto a tenant row. A nil field writes NULL, which
// clears the override back to the platform default — the update is a full replace of
// the governance fields, not a partial patch. Every field set here must also appear
// in Store.UpdateTenant's Select allowlist or the write is silently dropped.
func (g GovernanceOverrides) applyTo(t *iam.Tenant) {
	t.IngestMessagesPerSecond = g.IngestMessagesPerSecond
	t.IngestBurst = g.IngestBurst
	t.OutboundMessagesPerSecond = g.OutboundMessagesPerSecond
	t.OutboundBurst = g.OutboundBurst
	t.AiInferenceRequestsPerMinute = g.AiInferenceRequestsPerMinute
	t.AiInferenceBurst = g.AiInferenceBurst
}

// TenantInput is the data to create a tenant.
type TenantInput struct {
	Token string
	Name  string
	// TierToken names the tier this tenant is packaged at (ADR-065 decision 3).
	// REQUIRED: every tenant has a tier, so there is no unset state and nothing to
	// default. It names the tier rather than carrying its id so the API is stable
	// against reseeding and legible in an audit row.
	TierToken string
	Config    map[string]any
	GovernanceOverrides
	// AiExternalEnabled is the per-tenant external-AI consent (ADR-056 §6):
	// nil/false = not opted in (fail-closed), true = the operator has recorded this
	// tenant's consent to route its data to an external frontier model. Distinct from
	// the governance ceilings above: it gates WHETHER the tenant's data may leave the
	// boundary, not how often.
	AiExternalEnabled *bool
}

// TenantMutableInput is the data to update a tenant: its token is fixed.
type TenantMutableInput struct {
	Name string
	// TierToken is required on update too, and re-tiering a tenant is a legitimate,
	// live operation (ADR-065 decision 14: settings-only — nothing durable is keyed
	// on the tier, so it needs no flush or drain and converges on core/governance's
	// 60s TTL). Required rather than optional-means-unchanged because this input is
	// a full replace of the mutable fields, not a patch: an omitted tier here would
	// be indistinguishable from "clear it", which the FK forbids anyway.
	TierToken string
	Config    map[string]any
	GovernanceOverrides
	AiExternalEnabled *bool
}

func validateRateOverride(field string, v *float64) error {
	if v != nil && *v <= 0 {
		return fmt.Errorf("%s override must be positive (got %v); omit it to inherit the platform default", field, *v)
	}
	return nil
}

func validateBurstOverride(field string, v *int) error {
	if v != nil && *v <= 0 {
		return fmt.Errorf("%s override must be positive (got %d); omit it to inherit the platform default", field, *v)
	}
	return nil
}

// CreateTenant registers a new tenant (enabled by default) at the named tier.
func (s *Service) CreateTenant(ctx context.Context, in TenantInput) (*iam.Tenant, error) {
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	tier, err := s.resolveTier(ctx, in.TierToken)
	if err != nil {
		return nil, err
	}
	t := &iam.Tenant{
		Token: in.Token, Enabled: true, Config: in.Config, TierID: tier.ID,
		NamedEntity:       rdb.NamedEntity{Name: rdb.NullStrOf(&in.Name)},
		AiExternalEnabled: in.AiExternalEnabled,
	}
	in.applyTo(t)
	if err := s.iam.CreateTenant(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, in.Token)
}

// resolveTier maps a tier token to its row, rejecting an empty or unknown tier.
// This is where ADR-065 decision 3's "required at creation" is enforced in terms a
// caller can read: the NOT NULL FK behind it would refuse the write anyway, but as
// a constraint violation naming a column, not a mistake naming a tier.
func (s *Service) resolveTier(ctx context.Context, token string) (*iam.TenantTier, error) {
	if token == "" {
		return nil, fmt.Errorf("tierToken is required: every tenant is packaged at a tier")
	}
	tier, err := s.iam.TenantTierByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: %q", ErrTierNotFound, token)
	}
	return tier, err
}

// UpdateTenant replaces a tenant's name, tier, config, and governance overrides. A
// nil override field clears it (reverting the tenant to the platform default), so
// the update is a full replace of the mutable fields, not a partial patch.
func (s *Service) UpdateTenant(ctx context.Context, token string, in TenantMutableInput) (*iam.Tenant, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tier, err := s.resolveTier(ctx, in.TierToken)
	if err != nil {
		return nil, err
	}
	t, err := s.loadTenant(ctx, token)
	if err != nil {
		return nil, err
	}
	t.Name = rdb.NullStrOf(&in.Name)
	t.TierID = tier.ID
	t.Config = in.Config
	t.AiExternalEnabled = in.AiExternalEnabled
	in.applyTo(t)
	if err := s.iam.UpdateTenant(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, token)
}

// SetTenantEnabled enables or disables a tenant.
func (s *Service) SetTenantEnabled(ctx context.Context, token string, enabled bool) (*iam.Tenant, error) {
	t, err := s.loadTenant(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.iam.SetTenantEnabled(ctx, t, enabled); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, token)
}

// DeleteTenant removes a tenant. Idempotent: a missing tenant returns (false,
// nil). Rejected when memberships still reference it, so it cannot orphan access.
func (s *Service) DeleteTenant(ctx context.Context, token string) (bool, error) {
	t, err := s.iam.TenantByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := s.iam.CountMembershipsInTenant(ctx, token)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, ErrTenantHasMemberships
	}
	if err := s.iam.DeleteTenant(ctx, t); err != nil {
		return false, err
	}
	return true, nil
}

// TierInput is the data to create a tenant tier (ADR-065). Config is the settings
// blob; it is validated against the key registry once consuming keys exist
// (decision 8) — until then an empty blob is the only honest value, since a key
// nothing validates fails open.
type TierInput struct {
	Token       string
	Name        string
	Description string
	Config      map[string]any
}

// TierMutableInput is the data to update a tier: its token is fixed. Everything
// else is editable, deliberately — packaging is data, not a code deploy (decision
// 4), and what a tier includes is a product decision that changes.
//
// Name and Description are a full replace, matching the tenant inputs above: an
// omitted one is cleared. CONFIG IS NOT — it is an explicit patch, and the
// asymmetry is deliberate.
//
// Clearing a name is cosmetic and instantly visible. Clearing Config silently
// re-prices every tenant at the tier: they fall back to the platform default within
// core/governance's 60s TTL, with no error and no log. Under a full-replace rule,
// `updateTenantTier(token:"gold", request:{name:"Gold Plus"})` — a mutation that
// states only a rename — would drop every gold tenant from 2000/s to 1000/s. That is
// too destructive to be reachable by omission, so nil means "leave it alone" and an
// explicit empty map means "clear it".
type TierMutableInput struct {
	Name        string
	Description string
	// Config replaces the tier's settings when non-nil; nil leaves them unchanged.
	// A non-nil empty map clears them (re-inheriting the platform default), so
	// "clear" stays expressible — just never by accident.
	Config *map[string]any
}

// ListTenantTiers returns the tier catalog (ADR-065).
func (s *Service) ListTenantTiers(ctx context.Context) ([]iam.TenantTier, error) {
	return s.iam.ListTenantTiers(ctx)
}

// CountTenantsAtTier returns how many tenants are packaged at a tier — surfaced so
// the console can show the blast radius of editing one, and why deleting it is
// refused.
func (s *Service) CountTenantsAtTier(ctx context.Context, tierID uint) (int64, error) {
	return s.iam.CountTenantsAtTier(ctx, tierID)
}

// CreateTenantTier registers a new tier. Its config is validated against the key
// registry (ADR-065 decision 8): an unknown key is rejected here rather than
// accepted and silently ignored at read.
func (s *Service) CreateTenantTier(ctx context.Context, in TierInput) (*iam.TenantTier, error) {
	if in.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	if err := iam.ValidateTierConfig(in.Config); err != nil {
		return nil, err
	}
	t := &iam.TenantTier{
		Token:  in.Token,
		Config: in.Config,
		NamedEntity: rdb.NamedEntity{
			Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description),
		},
	}
	if err := s.iam.CreateTenantTier(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantTierByToken(ctx, in.Token)
}

// UpdateTenantTier replaces a tier's name and description, and replaces its config
// only when one is supplied (see TierMutableInput: config is a patch, precisely so a
// rename cannot silently re-price every tenant at the tier).
//
// Editing a tier changes behavior for EVERY tenant at it, live and with no deploy.
// That is the point of an entity rather than an enum, but it is a wide blast radius
// on a running system and a commercial act — hence the audit trail (ADR-065
// consequences). It needs no flush: nothing durable is keyed on the tier, so the
// change converges on core/governance's 60s TTL (decision 14).
func (s *Service) UpdateTenantTier(ctx context.Context, token string, in TierMutableInput) (*iam.TenantTier, error) {
	if in.Config != nil {
		if err := iam.ValidateTierConfig(*in.Config); err != nil {
			return nil, err
		}
	}
	t, err := s.loadTier(ctx, token)
	if err != nil {
		return nil, err
	}
	t.Name = rdb.NullStrOf(&in.Name)
	t.Description = rdb.NullStrOf(&in.Description)
	if in.Config != nil {
		t.Config = *in.Config
	}
	if err := s.iam.UpdateTenantTier(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantTierByToken(ctx, token)
}

// DeleteTenantTier removes a tier. Idempotent: a missing tier returns (false, nil).
// REFUSED while any tenant is still packaged at it (ADR-065 decision 9, the ADR-044
// ErrEntityInUse pattern) — a tenant's tier is a required FK, so there is no
// coherent state on the far side of deleting one that is in use. The FK is RESTRICT
// and would refuse it regardless; this check is what makes the refusal legible.
func (s *Service) DeleteTenantTier(ctx context.Context, token string) (bool, error) {
	t, err := s.iam.TenantTierByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := s.iam.CountTenantsAtTier(ctx, t.ID)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, fmt.Errorf("%w (%d tenant(s) at %q)", ErrTierInUse, n, token)
	}
	if err := s.iam.DeleteTenantTier(ctx, t); err != nil {
		return false, err
	}
	return true, nil
}

// loadTier resolves a tier by token, mapping not-found to ErrTierNotFound.
func (s *Service) loadTier(ctx context.Context, token string) (*iam.TenantTier, error) {
	t, err := s.iam.TenantTierByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTierNotFound
	}
	return t, err
}

// loadRole resolves a role by (scope, token), mapping not-found to ErrRoleNotFound.
func (s *Service) loadRole(ctx context.Context, scope iam.RoleScope, token string) (*iam.Role, error) {
	r, err := s.iam.RoleByScopeToken(ctx, scope, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	return r, err
}

// loadTenant resolves a tenant by token, mapping not-found to ErrTenantNotFound.
func (s *Service) loadTenant(ctx context.Context, token string) (*iam.Tenant, error) {
	t, err := s.iam.TenantByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTenantNotFound
	}
	return t, err
}

// parseScope maps the wire scope string to a RoleScope, rejecting anything else.
func parseScope(s string) (iam.RoleScope, error) {
	scope := iam.RoleScope(s)
	if !scope.Valid() {
		return "", fmt.Errorf("invalid role scope %q (want %q or %q)", s, iam.ScopeSystem, iam.ScopeTenant)
	}
	return scope, nil
}

// validateAuthorities rejects the request if any authority is not a known
// capability, so a typo cannot create a role that silently grants nothing.
func validateAuthorities(authorities []string) error {
	for _, a := range authorities {
		if !auth.ValidAuthority(a) {
			return fmt.Errorf("unknown authority %q", a)
		}
	}
	return nil
}
