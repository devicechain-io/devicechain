// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/patch"
	"gorm.io/gorm"
)

// Role-catalog and tenant errors (ADR-033). Sentinels for the resolver layer.
var (
	ErrRoleNotFound         = errors.New("role not found")
	ErrProtectedRole        = errors.New("the superuser system role cannot be deleted")
	ErrTenantHasMemberships = errors.New("tenant still has memberships; remove them first")
	ErrTierNotFound         = errors.New("tenant tier not found")
	ErrTierInUse            = errors.New("tenant tier still has tenants; move them to another tier first")
	ErrUnknownTierColor     = errors.New("unknown tier color (must be a palette token or empty)")

	// ErrTenantTokenReserved refuses a create at the token of a tenant that is being
	// purged (ADR-077). The token is the isolation key every other area stores, so
	// handing it to a new tenant before the old one's rows are gone would hand over the
	// old one's data. The reservation lasts exactly as long as the purge: reclamation
	// removes the tenant row, and the token is then free.
	//
	// 🔴 THE WORDING IS LOAD-BEARING, and not for style. dcctl's sim flow wraps its
	// createTenant call in tolerateExists (backend/cli/sim/admin.go), which swallows any
	// error whose text contains "already exists", "duplicate" or "unique" so that a
	// re-create is idempotent. If this refusal read like one of those, the caller most
	// likely to hit it would report success and carry on — minting an identity and a
	// membership against a tenant it cannot actually enter, then failing at login with
	// nothing pointing back here. (It is not a disclosure: resolveTenantGrant refuses a
	// deleted tenant, so the layer below still holds. It is a silent, misattributed
	// break.) Keep those three phrases out of it — both sides have tests.
	// ErrTenantDeleted refuses a write against a tenant that has been through the delete
	// door — it exists only to hold its token, and nothing may be attached to it.
	ErrTenantDeleted = errors.New("that tenant has been deleted and no longer accepts changes")

	ErrTenantTokenReserved = errors.New("that tenant token is reserved: a tenant at this token " +
		"is being deleted, and its token stays taken until every functional area has reclaimed " +
		"the rows it keys on that token. Retry once the deletion finishes, or pick another token")
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

// RoleUpdateRequest is the data to update a role: its identity (scope, token) is
// fixed and carried by the mutation's own arguments, so it is not representable here.
//
// Every field carries the platform's three update states — absent leaves the stored
// value alone, an explicit null clears it, a value sets it. It replaces a
// RoleMutableInput of plain strings and a plain slice, which had only two states and
// therefore erased any field the caller did not restate.
type RoleUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// Authorities REPLACES the role's authority set wholesale when sent; an explicit
	// null and an empty list both empty it, which for a list are one request spelled
	// two ways (see dcgraphql.OptionalStringList).
	//
	// 🔴 EMPTYING IT IS DELIBERATELY ALLOWED, and the reason is that the create path
	// allows it: `createRole(request: {scope:"tenant", token:"observer", authorities: []})`
	// has always been legal, and a role granting nothing is a real thing an operator
	// builds — a placeholder a membership can hold while its capabilities are decided.
	// Refusing it on update only would make a role reachable by creation and
	// uncorrectable afterwards, which is the shape a partial update exists to remove.
	// Contrast an OAuth client's scopes and redirect URIs, which the create path
	// REQUIRES and which UpdateOAuthClient therefore refuses to empty.
	Authorities dcgraphql.OptionalStringList
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
	if err := validateAuthorities(scope, in.Authorities); err != nil {
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

// UpdateRole applies a partial update to the role named by (scope, token).
//
// 🔴 scope AND token TOGETHER NAME THE ROW, and neither is representable in the
// request — roles are scoped, so "operator" at system scope and "operator" at tenant
// scope are two different roles with two different authority vocabularies. The
// conversion must not change which one is addressed: both still come from the
// mutation's own arguments, and loadRole still looks the row up by the pair.
//
// The authority check runs on the RESULTING set rather than on what the caller sent,
// because under a partial update those are not the same list: a request that renames
// the role and says nothing about authorities must still leave a valid role behind.
// Validating first, then mutating, then writing is what makes an unknown authority
// refuse the whole update rather than half-apply it.
func (s *Service) UpdateRole(ctx context.Context, scope, token string, request *RoleUpdateRequest) (*iam.Role, error) {
	rs, err := parseScope(scope)
	if err != nil {
		return nil, err
	}
	r, err := s.loadRole(ctx, rs, token)
	if err != nil {
		return nil, err
	}
	authorities := request.Authorities.ApplyTo(r.Authorities)
	if err := validateAuthorities(rs, authorities); err != nil {
		return nil, err
	}
	r.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(r.Name)))
	r.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(r.Description)))
	r.Authorities = authorities
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
	// ShedPriority is the per-tenant ADR-063 shed-priority override (1–100) — NOT a
	// ceiling like the fields above but a contention PREFERENCE (who degrades last). A
	// scalar with its own band scale, carried here only because it is the same kind of
	// per-tenant override an admin declares once and it embeds in both tenant inputs.
	// nil means "inherit" — the tier's shedPriority, then the platform fail-safe.
	ShedPriority *int
	// HeldCommandCeiling is the per-tenant ceiling on how many commands may sit in the
	// HELD state — dispatch deliberately withheld because the device is known absent.
	// A genuine ceiling like the rate fields above (how much), not a preference like
	// ShedPriority (who degrades last) — but a standalone SCALAR, with no burst and no
	// rate unit, which is why it is not a governance dimension. nil means "inherit" —
	// the tier's heldCommandCeiling, then the enforcing service's own default. It never
	// means unlimited, and nothing downstream can read it that way.
	HeldCommandCeiling *int
	// The three per-tenant geofence caps (ADR-023 governance, ADR-065 tier). Ceilings like
	// the rate fields (how much), standalone scalars like HeldCommandCeiling (no burst, no
	// per-second unit) — and unlike either, each carries a platform MAXIMUM as well as a floor.
	// The three maxima bound three DIFFERENT costs — a quadratic compile, a broker message, and
	// a shared cache — and core/governance states which is which beside each constant; do not
	// collapse them into one reason here, as this comment once did. nil means "inherit" — the
	// tier's key, then the platform default — and never unlimited. Each cascades independently;
	// there is no all-or-nothing group.
	GeoFencePositionCeiling *int
	GeoFenceCeiling         *int
	GeoFencePositionBudget  *int
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
	if err := validateShedPriorityOverride("shedPriority", g.ShedPriority); err != nil {
		return err
	}
	// A ceiling, so it goes through the same positive-or-inherit rule as a burst rather
	// than the shed priority's band check beside it: any positive whole number is a
	// meaningful backlog bound, and capping it at 100 would make every realistic fleet
	// unconfigurable.
	if err := validateBurstOverride("heldCommandCeiling", g.HeldCommandCeiling); err != nil {
		return err
	}
	// 🔴 THE GEOFENCE CAPS GO THROUGH A BOUNDED CHECK, NOT validateBurstOverride, AND THIS IS
	// THE DOOR THAT WOULD OTHERWISE BE LEFT OPEN. iam.ValidateTierConfig enforces these maxima
	// on a TIER write, but it is called only from the two tier mutations — a per-tenant
	// override never passes through it, and reaches the database through this function alone.
	// Bounding them here is what makes the maximum a property of the platform rather than of
	// one of the two ways to set a cap.
	for _, c := range []struct {
		field   string
		value   *int
		max     int
		because string
	}{
		{iam.GeoFencePositionCeilingConfigKey, g.GeoFencePositionCeiling,
			governance.MaxGeoFencePositionCeiling, governance.GeoFencePositionCeilingBecause},
		{iam.GeoFenceCeilingConfigKey, g.GeoFenceCeiling,
			governance.MaxGeoFenceCeiling, governance.GeoFenceCeilingBecause},
		{iam.GeoFencePositionBudgetConfigKey, g.GeoFencePositionBudget,
			governance.MaxTenantGeometryPositions, governance.GeoFencePositionBudgetBecause},
	} {
		if err := validateBoundedOverride(c.field, c.value, c.max, c.because); err != nil {
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
	t.ShedPriority = g.ShedPriority
	t.HeldCommandCeiling = g.HeldCommandCeiling
	t.GeoFencePositionCeiling = g.GeoFencePositionCeiling
	t.GeoFenceCeiling = g.GeoFenceCeiling
	t.GeoFencePositionBudget = g.GeoFencePositionBudget
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

// TenantUpdateRequest is the data to update a tenant: its token is fixed and carried
// by the mutation's own argument.
//
// # 🔴 THE GOVERNANCE OVERRIDES ARE FLATTENED HERE RATHER THAN EMBEDDED, AND nil NO
// LONGER MEANS TWO THINGS
//
// GovernanceOverrides (which TenantInput still embeds, because a create has nothing to
// preserve) carries eleven *float64 / *int fields where nil already means something
// load-bearing: "this tenant declares no override — fall back to its tier, then to the
// platform default, and NEVER to unlimited" (ADR-023). Under the full-replace shape
// that pointer had to carry ABSENT and CLEARED at once, so it could not distinguish
// "say nothing about the ingest ceiling" from "remove the ingest ceiling override" —
// which meant every update that did not restate an override removed it.
//
// Each is an Optional* here, so all three states survive to storage:
//
//	omitted        leave the tenant's override exactly as it is, set or absent
//	explicit null  REMOVE the override, so the tenant falls back to its tier then the
//	               platform default — never to zero, and never to unlimited
//	a value        set the override to it
//
// The eleven are flattened rather than held in an embedded struct because the
// exhaustiveness guard and the graphql-go input packer both walk the request type's
// exported fields, and an embedded struct is one field to each of them: the packer
// would have nowhere to put `ingestBurst`, and the guard would report the embedded
// struct as a field that cannot tell absent from null. governanceFor below is what
// keeps the shared GovernanceOverrides validate/applyTo pair as the single place the
// rules live.
type TenantUpdateRequest struct {
	Name dcgraphql.OptionalString
	// TierToken re-packages the tenant, which is a legitimate live operation (ADR-065
	// decision 14: settings-only — nothing durable is keyed on the tier, so it needs no
	// flush or drain and converges on core/governance's 60s TTL).
	//
	// It is OPTIONAL and folded with ApplyToRequired: omitted keeps the tenant at its
	// current tier, a token re-tiers it, and an explicit null is REFUSED because the FK
	// is NOT NULL and there is no un-tiered tenant. It used to be a required `String!`,
	// on the reasoning that "an omitted tier would be indistinguishable from clear it" —
	// which was true only of the full-replace shape this replaces. Under three states
	// they are distinguishable, so the reasoning dissolves and the requirement with it:
	// forcing every rename to restate the tier is how a console that has not loaded the
	// tier list re-tiers a tenant by accident.
	TierToken dcgraphql.OptionalString
	// Config is the freeform JSON object, as a string. Absent leaves it alone; a null
	// (or an empty string, which is the same request a form sends) clears it; an object
	// replaces it wholesale. Under the full-replace shape an omitted config CLEARED it,
	// so renaming a tenant dropped its config.
	Config dcgraphql.OptionalString

	IngestMessagesPerSecond   dcgraphql.OptionalFloat64
	IngestBurst               dcgraphql.OptionalInt32
	OutboundMessagesPerSecond dcgraphql.OptionalFloat64
	OutboundBurst             dcgraphql.OptionalInt32
	// AiExternalEnabled is the per-tenant external-AI consent (ADR-056 §6), a nullable
	// column where null and false both mean "not opted in" (fail-closed). Clearable,
	// unlike the required booleans elsewhere on the platform: null is a state the column
	// genuinely holds and the read path renders.
	AiExternalEnabled            dcgraphql.OptionalBool
	AiInferenceRequestsPerMinute dcgraphql.OptionalFloat64
	AiInferenceBurst             dcgraphql.OptionalInt32
	ShedPriority                 dcgraphql.OptionalInt32
	HeldCommandCeiling           dcgraphql.OptionalInt32
	GeoFencePositionCeiling      dcgraphql.OptionalInt32
	GeoFenceCeiling              dcgraphql.OptionalInt32
	GeoFencePositionBudget       dcgraphql.OptionalInt32
}

// governanceFor folds every override in this request onto the tenant's CURRENT values,
// producing the set the update should leave behind. It is what lets the request keep
// three states per field while validate() and applyTo() stay the single statement of
// what an override may be and which column it lands in.
//
// 🔴 IT FOLDS ONTO t RATHER THAN BUILDING FROM THE REQUEST ALONE, and that is the whole
// point: a request naming only `name` produces a GovernanceOverrides identical to what
// the tenant already holds, so applyTo rewrites the same values instead of eleven NULLs.
func (r *TenantUpdateRequest) governanceFor(t *iam.Tenant) GovernanceOverrides {
	return GovernanceOverrides{
		IngestMessagesPerSecond:      r.IngestMessagesPerSecond.ApplyTo(t.IngestMessagesPerSecond),
		IngestBurst:                  patch.IntPtr(r.IngestBurst, t.IngestBurst),
		OutboundMessagesPerSecond:    r.OutboundMessagesPerSecond.ApplyTo(t.OutboundMessagesPerSecond),
		OutboundBurst:                patch.IntPtr(r.OutboundBurst, t.OutboundBurst),
		AiInferenceRequestsPerMinute: r.AiInferenceRequestsPerMinute.ApplyTo(t.AiInferenceRequestsPerMinute),
		AiInferenceBurst:             patch.IntPtr(r.AiInferenceBurst, t.AiInferenceBurst),
		ShedPriority:                 patch.IntPtr(r.ShedPriority, t.ShedPriority),
		HeldCommandCeiling:           patch.IntPtr(r.HeldCommandCeiling, t.HeldCommandCeiling),
		GeoFencePositionCeiling:      patch.IntPtr(r.GeoFencePositionCeiling, t.GeoFencePositionCeiling),
		GeoFenceCeiling:              patch.IntPtr(r.GeoFenceCeiling, t.GeoFenceCeiling),
		GeoFencePositionBudget:       patch.IntPtr(r.GeoFencePositionBudget, t.GeoFencePositionBudget),
	}
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

// validateShedPriorityOverride bounds a per-tenant ADR-063 shed-priority override to
// the 1–100 band scale (ADR-063 decision 1). Unlike a ceiling, it is not "positive or
// inherit": it names a point on a fixed scale, so a value outside [1,100] names no
// band and is rejected. nil is inherit.
func validateShedPriorityOverride(field string, v *int) error {
	if v != nil && (*v < 1 || *v > 100) {
		return fmt.Errorf("%s override must be between 1 and 100 (got %d); omit it to inherit the platform default", field, *v)
	}
	return nil
}

// validateBoundedOverride is validateBurstOverride plus a platform MAXIMUM: positive, and no
// larger than max. nil is inherit.
//
// It exists because the geofence caps are the first per-tenant overrides whose over-large value
// is not a tenant overprovisioned but an instance degraded. Both the NUMBER and the REASON come
// from core/governance — the same two constants iam's tier validator reads — so the two doors
// cannot disagree about where the wall is or about why it is there.
//
// 🔴 THE REASON IS A PARAMETER BECAUSE THE THREE CAPS DO NOT SHARE ONE. This function used to
// state a single rationale for all of them — "it bounds a resource shared by every tenant on the
// instance" — which is true of the whole-fence-set budget and false of the fence count, whose
// maximum is a broker message size. An error that explains a limit with the wrong cost sends an
// operator to change the wrong thing.
func validateBoundedOverride(field string, v *int, max int, because string) error {
	if err := validateBurstOverride(field, v); err != nil {
		return err
	}
	if v != nil && *v > max {
		return fmt.Errorf("%s override must be at most %d (got %d); %s", field, max, *v, because)
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

	// The token reservation (ADR-077). Checked explicitly rather than left to the unique
	// index: a raw constraint violation names a column, reads as "already exists" to
	// every tolerant client, and would let a caller conclude the tenant it just tried to
	// create is the one it now holds.
	//
	// An ACTIVE tenant at this token still falls through to the create below and its
	// duplicate-key error, which is deliberate — that IS an already-exists, and callers
	// rely on tolerating it to make create idempotent.
	existing, lookupErr := s.iam.TenantByToken(ctx, in.Token)
	switch {
	case lookupErr == nil && existing.PurgeState.Deleted():
		return nil, fmt.Errorf("create tenant %q: %w", in.Token, ErrTenantTokenReserved)
	case lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound):
		return nil, lookupErr
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

// UpdateTenant applies a partial update to a tenant: an omitted field leaves the stored
// value alone, an explicit null clears it, a value sets it.
//
// 🔴 EVERY DECISION IS MADE BEFORE THE FIRST ASSIGNMENT, so a refused request writes
// nothing. A tenant is where the platform's ceilings live, and half-applying an update
// that then failed on an unknown tier would leave a tenant metered at limits nobody
// chose, with a caller who was told the update failed.
//
// The governance overrides are validated as the RESULTING set (see governanceFor), not
// as what the caller sent: under a partial update those differ, and it is the resulting
// row that has to be a legal one.
func (s *Service) UpdateTenant(ctx context.Context, token string, request *TenantUpdateRequest) (*iam.Tenant, error) {
	t, err := s.loadTenant(ctx, token)
	if err != nil {
		return nil, err
	}

	overrides := request.governanceFor(t)
	if err := overrides.validate(); err != nil {
		return nil, err
	}

	// The tier the tenant currently sits at, so an OMITTED tierToken keeps it. Preloaded
	// by TenantByToken; a tenant reaching here without one has a broken NOT NULL FK, and
	// resolveTier's "tierToken is required" refusal is the loud, fail-closed reading —
	// never a silent re-tier to whatever id happens to be in the row.
	currentTier := ""
	if t.Tier != nil {
		currentTier = t.Tier.Token
	}
	tierToken, err := request.TierToken.ApplyToRequired("tierToken", currentTier)
	if err != nil {
		return nil, err
	}
	tier, err := s.resolveTier(ctx, tierToken)
	if err != nil {
		return nil, err
	}

	config := t.Config
	if request.Config.Set {
		if config, err = ParseConfigJSON(request.Config.Value); err != nil {
			return nil, err
		}
	}

	t.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(t.Name)))
	t.TierID = tier.ID
	t.Config = config
	t.AiExternalEnabled = request.AiExternalEnabled.ApplyTo(t.AiExternalEnabled)
	overrides.applyTo(t)
	if err := s.iam.UpdateTenant(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantByToken(ctx, token)
}

// ParseConfigJSON decodes an optional JSON object string into a config map, the one way
// this service reads a `config:` field on either the create or the update path.
//
// A nil pointer, an empty string and "{}" all yield a NIL map — the three spellings of
// "this record declares no config" a caller can send, folded to one so the column holds
// NULL rather than an empty document in some of them and NULL in others. A non-object or
// malformed JSON is an error, never a silently empty map.
func ParseConfigJSON(s *string) (map[string]any, error) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(*s), &cfg); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %w", err)
	}
	if len(cfg) == 0 {
		return nil, nil
	}
	return cfg, nil
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

// DeleteTenant cuts a tenant's access and begins its purge (ADR-077).
//
// It no longer removes the row. The tenant moves to `purging`, disabled and stamped
// with an epoch: access is gone immediately, its token is reserved FOR AS LONG AS THE
// PURGE RUNS, and the reclamation of its rows across every store happens afterwards —
// driven by the purge coordinator, which removes the row once every store has reported
// clean, and that removal is what releases the token. What this returns is therefore
// "the delete door was walked through", not "the data is gone" — a distinction the
// console copy has to carry too, since the old wording claimed an irreversible deletion
// while removing exactly one row.
//
// Idempotent in both directions: a missing tenant and an already-purging one both
// return (false, nil), so a retry after a partly-failed teardown converges. Still
// rejected while memberships reference the tenant, because removing those is what
// actually revokes human access.
func (s *Service) DeleteTenant(ctx context.Context, token string) (bool, error) {
	t, err := s.iam.TenantByToken(ctx, token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if t.PurgeState.Deleted() {
		return false, nil
	}
	n, err := s.iam.CountMembershipsInTenant(ctx, token)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, ErrTenantHasMemberships
	}
	if err := s.iam.BeginTenantPurge(ctx, t, time.Now().UTC()); err != nil {
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
	// Color is a palette token (iam.ValidTierColor) or "" for no pill. Presentation
	// only (ADR-065 S5c). DisplayOrder is not settable here — a new tier lands at 0 and
	// is arranged with ReorderTenantTiers, so ordering is one gesture rather than a
	// number an operator hand-manages against collisions.
	Color string
}

// TierUpdateRequest is the data to update a tier: its token is fixed and carried by the
// mutation's own argument. Everything else is editable, deliberately — packaging is
// data, not a code deploy (decision 4), and what a tier includes is a product decision
// that changes.
//
// # THE ASYMMETRY THIS TYPE USED TO CARRY IS GONE, BECAUSE IT WAS THE MISSING STATE
//
// Under the full-replace shape, name and description were cleared by omission while
// Config alone was a hand-rolled patch (`*map[string]any`: nil left it alone, a non-nil
// empty map cleared it). That exception existed for a real reason — clearing a name is
// cosmetic, while clearing config silently re-prices every tenant at the tier, dropping
// them to the platform default within core/governance's 60s TTL with no error and no
// log, so `updateTenantTier(token:"gold", request:{name:"Gold Plus"})` would have
// re-priced every gold tenant. What it actually wanted was the ABSENT state, and one
// field got a bespoke version of it while the others went without.
//
// Every field now has it, so config needs no exception: omitted leaves it alone,
// explicit null (or "" or "{}") clears it, an object replaces it.
type TierUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// Config is the tier's settings blob as a JSON object string, validated against the
	// ADR-065 key registry when supplied.
	Config dcgraphql.OptionalString
	// Color is a palette token (iam.ValidTierColor) or "" for no pill. Its column is NOT
	// NULL with a default of '', so "" is a value it genuinely holds and an explicit null
	// writes that rather than being refused — see patch.EmptiableString for why this is
	// not ApplyToRequired. DisplayOrder is not here: it is set by ReorderTenantTiers,
	// never by editing one tier in isolation.
	Color dcgraphql.OptionalString
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
	if !iam.ValidTierColor(in.Color) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTierColor, in.Color)
	}
	t := &iam.TenantTier{
		Token:  in.Token,
		Config: in.Config,
		Color:  in.Color,
		NamedEntity: rdb.NamedEntity{
			Name: rdb.NullStrOf(&in.Name), Description: rdb.NullStrOf(&in.Description),
		},
	}
	if err := s.iam.CreateTenantTier(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantTierByToken(ctx, in.Token)
}

// UpdateTenantTier applies a partial update to a tier: an omitted field leaves the
// stored value alone, an explicit null clears it, a value sets it.
//
// Editing a tier changes behavior for EVERY tenant at it, live and with no deploy.
// That is the point of an entity rather than an enum, but it is a wide blast radius
// on a running system and a commercial act — hence the audit trail (ADR-065
// consequences). It needs no flush: nothing durable is keyed on the tier, so the
// change converges on core/governance's 60s TTL (decision 14).
//
// Everything is decided before the first assignment, so an unknown color or an invalid
// config key refuses the whole update — a tier half-re-priced is a tier nobody chose.
func (s *Service) UpdateTenantTier(ctx context.Context, token string, request *TierUpdateRequest) (*iam.TenantTier, error) {
	t, err := s.loadTier(ctx, token)
	if err != nil {
		return nil, err
	}

	color := patch.EmptiableString(request.Color, t.Color)
	if !iam.ValidTierColor(color) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTierColor, color)
	}

	config := t.Config
	if request.Config.Set {
		if config, err = ParseConfigJSON(request.Config.Value); err != nil {
			return nil, err
		}
		if err := iam.ValidateTierConfig(config); err != nil {
			return nil, err
		}
	}

	t.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(t.Name)))
	t.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(t.Description)))
	t.Color = color
	t.Config = config
	if err := s.iam.UpdateTenantTier(ctx, t); err != nil {
		return nil, err
	}
	return s.iam.TenantTierByToken(ctx, token)
}

// ReorderTenantTiers sets the operator's listing order for the whole catalog (ADR-065
// S5c). orderedTokens must be exactly the current tiers — a stale client is refused
// (iam.ErrTierReorderMismatch) rather than silently dropping a tier it had not loaded.
// Presentation only: it moves nothing but where tiers appear in a list.
func (s *Service) ReorderTenantTiers(ctx context.Context, orderedTokens []string) error {
	return s.iam.ReorderTenantTiers(ctx, orderedTokens)
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
// capability, so a typo cannot create a role that silently grants nothing — and if
// any authority belongs to a tier this role's scope cannot carry (ADR-065).
//
// The tier half exists because a role's scope and its authorities used to be
// unrelated: a TENANT-scoped role could be granted ai:admin (or tenant:write, or
// settings:write), which is how an instance-scoped operator resource became
// reachable from the tenant console. Authorize now refuses a system-tier authority
// on a tenant access token regardless, so such a role would grant nothing it
// claims to — this makes the lie unwritable rather than merely inert, and names
// the valid alternatives so the fix is obvious from the error.
//
// The super-authority "*" is deliberately allowed at either scope: it means
// "everything at the bearer's tier", which the check-time rule now makes literally
// true (a tenant-admin's "*" grants every tenant-tier authority and no more). The
// seeded system `superuser` and tenant `tenant-admin` roles both rely on this.
func validateAuthorities(scope iam.RoleScope, authorities []string) error {
	want := auth.TierTenant
	if scope == iam.ScopeSystem {
		want = auth.TierSystem
	}
	for _, a := range authorities {
		if !auth.ValidAuthority(a) {
			return fmt.Errorf("unknown authority %q", a)
		}
		if auth.Authority(a) == auth.AuthorityAll {
			continue
		}
		tiers, known := auth.TiersOf(auth.Authority(a))
		if !known {
			// Unreachable while ValidAuthority and the tier map share one source, but
			// fail closed rather than granting an authority whose tier is unknown.
			return fmt.Errorf("authority %q declares no tier", a)
		}
		// A dual-tier authority (audit:read) is grantable at either scope — the same
		// capability genuinely exists on both planes.
		if !tiers.Has(want) {
			return fmt.Errorf("authority %q is %s-tier and cannot be granted to a %s-scoped role; %s-scoped roles may grant: %s",
				a, tiers[0], scope, scope, strings.Join(auth.AuthoritiesForScope(want), ", "))
		}
	}
	return nil
}
