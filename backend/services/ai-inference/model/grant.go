// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// A tenant's AI model MENU is the set of providers it may draft with, and it is
// assembled from the two grant tables in this file (ADR-065 decision 10):
//
//	menu(tenant) = grants of the tenant's TIER  ∪  the tenant's own additive grants
//
// This replaces the retired instance-wide single-active pointer, which modeled
// "one model, globally" and could express no packaging at all.
//
// A GRANT IS AN ENTITLEMENT. It says a model is on the menu; it does not say which model
// anything USES. That second question is answered by a stored (tenant, function) →
// provider assignment (see function.go) and, failing that, by the tier's explicitly
// marked default grant (AIProviderTierGrant.IsDefault) — never by a property of these
// rows, and never by their COUNT.
//
// Both are STORED answers an operator wrote. Nothing on the read path may consult the
// size or the emptiness of a grant set to produce one: that is the shape that shipped as
// a bug five times, and AIProviderTierGrant.IsDefault carries the full history of it.
// Note where the default lives — on a tier GRANT row, not on the provider and not on the
// tenant grant. A tier that grants nothing has nowhere to put a default, so "AI is a
// tiered entitlement" holds by construction rather than by a runtime check.
//
// WHY THE JOIN LIVES HERE AND NOT ON THE TIER. The tier is a user-management entity
// (iam_tenant_tiers); providers are this service's. They share a database but sit in
// different functional-area schemas, i.e. a real service boundary — and
// user-management ships in every profile while ai-inference is opt-in, so
// user-management may never reference a provider. Something therefore has to give up
// referential integrity. It is given up on the side where dangling is HARMLESS: a
// tier cannot be deleted while any tenant references it (user-management's
// ErrTierInUse), so a grant naming a dead tier grants nothing to nobody. Provider
// deletion is the destructive direction — it is the one that would silently take a
// capability away from live tenants — so the association lives next to providers,
// where the id is a real column-level reference and DeleteAIProvider can refuse
// (ErrProviderInUse). Storing provider ids on the tier instead would put the list on
// the one side of the boundary where no such refusal is possible.
//
// TierToken is consequently a CROSS-SERVICE reference (ADR-044) carrying no FK and no
// validation at write: this service authenticates to user-management with a SERVICE
// token, the tier catalog lives on user-management's ADMIN plane, and that plane
// accepts identity tokens only — so there is no door this service's credential can
// reach to ask "does tier X exist?". A grant naming an unknown tier is therefore
// inert rather than rejected, and the admin console surfaces it as an unknown tier
// rather than filtering it out of sight.

// AIProviderTierGrant offers one provider to every tenant at one tier. It is
// instance-global (an operator's packaging decision), so it is deliberately NOT
// tenant-scoped.
//
// Rows are HARD-deleted: this is a join row with no identity worth preserving, and
// soft-deleting it would drag in the tombstone-counting unique-index bug the repo
// already carries elsewhere (a UNIQUE over live+deleted rows refuses to re-grant a
// provider that was previously ungranted). The audit journal records the delete
// independently of the row's survival, so nothing auditable is lost.
type AIProviderTierGrant struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// TierToken is the user-management tier this grant offers the provider to. A
	// cross-service reference (see the package note above): no FK, not validated at
	// write, inert when unknown.
	//
	// No `index` tag: uix_ai_tier_grant_pair (tier_token, provider_id) leads with this
	// column and already serves the `WHERE tier_token = ?` lookup, so a second index
	// would be dead weight — and declaring one here that the migration does not create
	// is the sort of drift that misleads the next reader into believing it exists.
	TierToken string `gorm:"not null;size:128"`
	// ProviderID references AIProvider.ID — the immutable id, not the token, so a
	// token rename keeps the grant bound (the same reasoning as the secret handle).
	ProviderID uint `gorm:"not null;index"`
	// IsDefault marks this grant as the TIER's default model: what a tenant at this tier
	// gets for a function it never assigned, provided the model is still on its menu
	// (Api.ResolveModelForFunction). At most one per tier —
	// uix_ai_tier_grant_default is the storage backstop.
	//
	// THIS MARK WAS DELETED AND RESTORED, AND THE HISTORY IS THE DOCUMENTATION. It once
	// lived here (and on the tenant grant), was removed after one bug shipped five times,
	// and came back when the instance-wide platform baseline it was replaced with turned
	// out to be the wrong shape — a default every tier shares is not a per-tier default.
	//
	// THE MARK WAS NEVER THE BUG. The bug was that the mark's PRESENCE was INFERRED from a
	// set operators can change, so the answer re-answered when they changed it:
	//
	//   - a "sole granted model" fallback over the tier∪tenant union — a per-tenant grant
	//     vaporised the tier's default;
	//   - the same fallback on the tenant axis;
	//   - the same on the tier axis — a second UNMARKED grant killed the default for every
	//     tenant on the tier;
	//   - an auto-mark probing the MARK set ("no default exists → mark this one"). Operators
	//     can EMPTY that set (ClearTierDefault, or revoking the marked grant), so a later
	//     grant silently overturned an explicit "no default";
	//   - a dormant TENANT mark that sprang alive when its tier was unpackaged.
	//
	// So: THE SERVER NEVER INFERS THE DEFAULT. There is no auto-mark, no sole-model
	// fallback, and no "if the tier has no default, use X". A default exists IFF an
	// operator explicitly set it (Api.SetTierDefault). GRANTING IS NOT DEFAULTING — they
	// are separate acts with separate mutations, which is why GrantProviderToTier takes no
	// makeDefault flag: that flag is exactly where the fourth bug lived.
	//
	// The deliberate consequence: A TIER THAT GRANTS MODELS WITH NO DEFAULT MARKED
	// RESOLVES TO NONE, even when it grants exactly one. That is the correct answer, not a
	// gap to be helpfully filled — the alternative is a rule that reads the menu's size.
	// What stops it being a footgun is the CONSOLE, not the server: the grant screen
	// presents the default alongside the grants, pre-selects a radio, and issues an
	// explicit mutation the operator can see and confirm. A pre-selection an operator
	// confirms is a choice; the same pre-selection made server-side is an inference.
	//
	// No gorm `default` tag: a `default:false` would make gorm substitute the DB default
	// for the Go zero value, the shape that once made AIProvider.Enabled
	// unpersistable-as-false.
	IsDefault bool `gorm:"not null"`
}

func (AIProviderTierGrant) TableName() string { return "ai_provider_tier_grants" }

// AuditLabel names the grant in the ADR-065 decision-7 audit trail as the pair it
// actually is. The provider is identified by id because a grant holds no token.
func (g AIProviderTierGrant) AuditLabel() string {
	return g.TierToken + " → provider#" + strconv.FormatUint(uint64(g.ProviderID), 10)
}

// AIProviderTenantGrant offers one provider to ONE tenant, over and above whatever
// its tier already offers — ADR-065 decision 7's audited exception, in Derek's
// framing: "a bronze-tier tenant could be given access to Fable when it's not offered
// in the bronze contract".
//
// ADDITIVE-ONLY, and that is a modeling decision, not a missing feature: there is no
// deny row and no revoke column, so the exception can only ever move in the tenant's
// favor and the tier stays a legible floor. Cutting a tenant off is not this table's
// job — the kill switch already exists on the consent axis (ai_external_enabled,
// operator-set, ADR-056 §6), which is a different question ("may my data leave the
// boundary") from this one ("which models may I choose from").
//
// Tenant-scoped ON PURPOSE via rdb.TenantScoped. That earns the un-skippable tenant
// predicate on every read, which is what stands between one tenant's grants and
// another's; the inference path resolves under the tenant stamped from the verified
// service token, so isolation there is enforced by the callback rather than by a
// WHERE clause a future edit could forget. The admin plane holds no tenant (it is an
// identity-token plane), so operator writes must take the sanctioned
// core.WithSystemContext bypass and set TenantId explicitly — see Api.sys.
type AIProviderTenantGrant struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped

	// ProviderID references AIProvider.ID. See AIProviderTierGrant.ProviderID.
	ProviderID uint `gorm:"not null;index"`
}

func (AIProviderTenantGrant) TableName() string { return "ai_provider_tenant_grants" }

// AuditLabel names the grant in the audit trail. TenantId is the tenant token.
func (g AIProviderTenantGrant) AuditLabel() string {
	return g.TenantId + " → provider#" + strconv.FormatUint(uint64(g.ProviderID), 10)
}
