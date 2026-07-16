// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "sort"

// Authority is an atomic, capability-style permission of the form
// "resource:action" (e.g. "device:write"). Authorization is capability-based
// (ADR-008 RBAC): a Role bundles a set of authorities, a user's effective
// authorities are the union of their roles', and a resolver checks for the
// specific authority it requires — never a role name — so a role can be
// re-scoped without code changes.
type Authority string

// Tier declares the plane an authority belongs to, mirroring iam.RoleScope on the
// role that grants it (ADR-033). It closes a gap the two had between them: a role
// carried a scope, but the authorities inside it carried none, so nothing stopped a
// *tenant*-scoped role from being granted an instance-global capability like
// ai:admin — which is exactly how the ADR-065 provider screen ended up reachable
// from the tenant console (ADR-065, "how it arose").
//
// The tier is enforced in two places, and it takes both to close the hole:
//
//   - At the CHECK (Authorize): a tenant access token can never satisfy a
//     system-tier authority, however privileged it is. This is what the flat
//     vocabulary could not express — the seeded `tenant-admin` role grants "*",
//     which HasAuthority passes for every check, so tiering the authority alone
//     would have changed nothing. Service tokens are NOT bound by this: they are
//     instance-level machine callers minted from the shared service secret with an
//     explicit least-privilege list (see TokenTypeService), not tenants — the
//     enforcing services read tenant:read that way.
//   - At the GRANT (user-management's role validation): a tenant-scoped role may
//     not name a system-tier authority, so an operator cannot build a role that
//     LOOKS like it confers provider admin while the check above silently refuses
//     it.
type Tier string

const (
	// TierSystem is instance-global: the capability spans tenants and is an
	// operator concern (the tenant registry, the role catalog, instance settings,
	// the OAuth client allowlist, the AI provider list). It rides an identity token
	// on an admin plane, or a service token between services.
	TierSystem Tier = "system"
	// TierTenant is scoped to one tenant's own resources. It rides a tenant access
	// token on the data plane.
	TierTenant Tier = "tenant"
)

const (
	// AuthorityAll is the super-authority: a subject holding it passes every
	// authorization check AT ITS OWN TIER. The bootstrap superuser's system role
	// grants it, and so does the seeded tenant-admin tenant role — which is why
	// Authorize tiers the check rather than trusting "*" to mean the same thing on
	// both planes. On a tenant access token "*" means "every tenant-tier
	// authority", which is now literally what it does.
	AuthorityAll Authority = "*"

	// Identity + role directory (user-management). UserRead/UserWrite gate the
	// admin identity and membership operations; RoleRead/RoleWrite gate the role
	// catalog (ADR-033).
	UserRead  Authority = "user:read"
	UserWrite Authority = "user:write"
	RoleRead  Authority = "role:read"
	RoleWrite Authority = "role:write"

	// Tenant control plane (user-management admin API, ADR-033).
	TenantRead  Authority = "tenant:read"
	TenantWrite Authority = "tenant:write"

	// Device management (device-management). Enforcement is rolled out per service
	// in later PRs; the vocabulary is defined here up front so resolvers across
	// services reference one canonical set.
	DeviceRead  Authority = "device:read"
	DeviceWrite Authority = "device:write"

	// Event history (event-management) + live state (device-state).
	EventRead Authority = "event:read"
	StateRead Authority = "state:read"

	// Alarms (device-management, ADR-041). AlarmRead gates reading raised alarms;
	// AlarmWrite gates the operator transitions (acknowledge, clear). Distinct from
	// device:* because acknowledging an alarm is an operational action a monitoring
	// operator performs, not a change to the device or its profile.
	AlarmRead  Authority = "alarm:read"
	AlarmWrite Authority = "alarm:write"

	// Command delivery (command-delivery).
	CommandRead  Authority = "command:read"
	CommandWrite Authority = "command:write"

	// Dashboards (dashboard-management, ADR-039). Gate the dashboard-definition
	// CRUD API; the live telemetry a dashboard renders is still gated by EventRead
	// on event-management's subscription.
	DashboardRead  Authority = "dashboard:read"
	DashboardWrite Authority = "dashboard:write"

	// Notifications (notification-management, ADR-017). Gate the per-tenant
	// notification configuration API: the delivery channels (SMTP/webhook, with
	// their write-only secrets) and the routing policies that map alarm severities
	// to channels + recipients. Distinct from alarm:* because configuring who gets
	// paged is an administrative concern, separate from acknowledging an alarm.
	NotificationRead  Authority = "notification:read"
	NotificationWrite Authority = "notification:write"

	// Audit journal read (ADR-019). Gates the read-side query over the append-only
	// audit_events table; the journal is written by construction and is never
	// mutated through the API, so there is no audit:write.
	AuditRead Authority = "audit:read"

	// System settings (user-management settings API, ADR-042 P2). Instance-global,
	// admin-edited configuration (e.g. token masks); gates the read/write of the
	// system_settings override store. Only a system-authority holder (superuser)
	// carries these — they are not tenant-scoped.
	SettingsRead  Authority = "settings:read"
	SettingsWrite Authority = "settings:write"

	// OAuth client registry (user-management admin API, ADR-047). Instance-global:
	// gate the read/write of the OAuth 2.1 client allowlist (client_id, redirect
	// URIs, permitted scopes) that the Authorization Server validates the
	// authorization-code flow against. System-scoped, like the role/tenant catalog.
	ClientRead  Authority = "client:read"
	ClientWrite Authority = "client:write"

	// Outbound connectors (outbound-connectors, ADR-060). Gate the per-tenant
	// versioned Connector CRUD API — the registered {type, config, write-only
	// SecretRef} targets a `publish` REACT action delivers through. Distinct from
	// notification:* because a connector is an automation-egress resource (an
	// external system a rule pushes to), not a human-notification channel.
	ConnectorRead  Authority = "connector:read"
	ConnectorWrite Authority = "connector:write"

	// Tenant branding self-service (user-management, ADR-038). Gates the
	// self-scoped setTenantBranding mutation — a tenant admin white-labeling their
	// OWN tenant (title/palette/logo). Reads need no authority: resolved branding
	// rides the self-scoped `tenant` query, visible to any member (it's their own
	// brand), like tenantGovernance. Distinct from tenant:write, which is the
	// instance-scoped operator authority over any tenant's control-plane record.
	BrandingWrite Authority = "branding:write"

	// AI inference provider administration (ai-inference, ADR-056). Gates the
	// INSTANCE-scoped, operator-managed AIProvider CRUD + the active-provider pointer
	// — the registered inference providers (kind, endpoint, model, write-only API key)
	// NL→rule authoring routes through. Instance-global like settings:*/client:*: a
	// provider list + its keys are an operator concern, not a tenant's (a tenant only
	// CONSENTS to external routing, gated separately by the ADR-023 governance flag).
	AIAdmin Authority = "ai:admin"

	// AIInfer gates the inference CALL itself (ai-inference inferRuleCandidate,
	// ADR-056). It is deliberately separate from — and far narrower than — ai:admin:
	// its sole holder is the event-processing service token that carries a human's
	// NL→rule authoring prompt to the active provider (Slice 1). The inference service
	// holds NO ambient authority over tenant data with it (ADR-047 confused-deputy red
	// line): the call returns only a candidate string, which flows back through the
	// deterministic rules.Compile firewall carrying the human's own token. A holder
	// can run a prompt through the active provider; it cannot read, list, or change the
	// provider config (that is ai:admin) or touch any tenant resource.
	AIInfer Authority = "ai:infer"
)

// vocabulary maps every known authority to its tier. A Role may only grant
// authorities in this set (plus AuthorityAll), so a typo in a role definition is
// rejected at write time rather than silently granting nothing. It is extended as
// each service's resolvers are brought under enforcement.
//
// AuthorityAll is deliberately absent: "*" is the super-authority and belongs to
// whichever tier its token carries it on, so it has no tier of its own. Every
// lookup below treats it as a special case rather than giving it a wrong answer.
var vocabulary = map[Authority]Tier{
	// Instance-global operator surfaces. Each is served on an admin plane behind an
	// identity token, or read between services over a service token — never by a
	// tenant acting in its own tenant.
	UserRead:    TierSystem,
	UserWrite:   TierSystem,
	RoleRead:    TierSystem,
	RoleWrite:   TierSystem,
	TenantRead:  TierSystem,
	TenantWrite: TierSystem,
	// The AI provider list is instance config an operator owns — the ADR-065
	// correction. A tenant only CONSENTS to external routing (a separate flag).
	AIAdmin:       TierSystem,
	SettingsRead:  TierSystem,
	SettingsWrite: TierSystem,
	ClientRead:    TierSystem,
	ClientWrite:   TierSystem,

	// A tenant's own resources.
	DeviceRead:        TierTenant,
	DeviceWrite:       TierTenant,
	EventRead:         TierTenant,
	StateRead:         TierTenant,
	AlarmRead:         TierTenant,
	AlarmWrite:        TierTenant,
	CommandRead:       TierTenant,
	CommandWrite:      TierTenant,
	DashboardRead:     TierTenant,
	DashboardWrite:    TierTenant,
	NotificationRead:  TierTenant,
	NotificationWrite: TierTenant,
	ConnectorRead:     TierTenant,
	ConnectorWrite:    TierTenant,
	BrandingWrite:     TierTenant,
	// audit:read is tenant-tier because a tenant reads its OWN journal on
	// device-management's data plane. user-management's admin plane also gates on
	// it, which is fine and not a contradiction: the tier restricts what a tenant
	// ACCESS token may satisfy, so an operator's identity token holding a
	// tenant-tier authority is unaffected.
	AuditRead: TierTenant,
	// ai:infer rides a service token carrying one tenant's authoring prompt. It is
	// deliberately NOT system-tier despite being held only by a service: it acts
	// within a tenant, and the inference service holds no ambient authority over
	// tenant data with it (ADR-047 confused-deputy red line).
	AIInfer: TierTenant,
}

// ValidAuthority reports whether s names a known authority (including the
// super-authority "*").
func ValidAuthority(s string) bool {
	if Authority(s) == AuthorityAll {
		return true
	}
	_, ok := vocabulary[Authority(s)]
	return ok
}

// TierOf returns the tier an authority belongs to. It reports ok=false for the
// super-authority "*" (which has no tier of its own — it means "everything at the
// bearer's tier") and for an unknown authority, so a caller must decide what an
// untiered authority means rather than receiving a plausible default. Callers that
// gate on the answer must fail closed when ok is false.
func TierOf(a Authority) (Tier, bool) {
	t, ok := vocabulary[a]
	return t, ok
}

// Authorities returns the full known authority vocabulary, sorted, including the
// super-authority "*". The admin API exposes it so the console can offer a
// checklist when defining a role rather than asking for free-text authority
// strings (which a typo would silently break).
func Authorities() []string {
	out := make([]string, 0, len(vocabulary)+1)
	out = append(out, string(AuthorityAll))
	for a := range vocabulary {
		out = append(out, string(a))
	}
	sort.Strings(out)
	return out
}

// AuthoritiesForScope returns the authorities a role at the given tier may grant,
// sorted, including the super-authority "*" (valid at either tier — it expands to
// everything the bearer's own tier allows). It backs the console's role editor, so
// the checklist offers only authorities the role can actually carry rather than
// letting an operator pick one the write path will reject.
func AuthoritiesForScope(tier Tier) []string {
	out := make([]string, 0, len(vocabulary)+1)
	out = append(out, string(AuthorityAll))
	for a, t := range vocabulary {
		if t == tier {
			out = append(out, string(a))
		}
	}
	sort.Strings(out)
	return out
}
