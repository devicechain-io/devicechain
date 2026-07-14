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

const (
	// AuthorityAll is the super-authority: a subject holding it passes every
	// authorization check. The bootstrap admin role grants it.
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
)

// vocabulary is the set of every known authority. A Role may only grant
// authorities in this set (plus AuthorityAll), so a typo in a role definition is
// rejected at write time rather than silently granting nothing. It is extended as
// each service's resolvers are brought under enforcement.
var vocabulary = map[Authority]struct{}{
	AuthorityAll:      {},
	UserRead:          {},
	UserWrite:         {},
	RoleRead:          {},
	RoleWrite:         {},
	TenantRead:        {},
	TenantWrite:       {},
	DeviceRead:        {},
	DeviceWrite:       {},
	EventRead:         {},
	StateRead:         {},
	AlarmRead:         {},
	AlarmWrite:        {},
	CommandRead:       {},
	CommandWrite:      {},
	DashboardRead:     {},
	DashboardWrite:    {},
	NotificationRead:  {},
	NotificationWrite: {},
	AuditRead:         {},
	SettingsRead:      {},
	SettingsWrite:     {},
	ClientRead:        {},
	ClientWrite:       {},
	ConnectorRead:     {},
	ConnectorWrite:    {},
	BrandingWrite:     {},
}

// ValidAuthority reports whether s names a known authority (including the
// super-authority "*").
func ValidAuthority(s string) bool {
	_, ok := vocabulary[Authority(s)]
	return ok
}

// Authorities returns the full known authority vocabulary, sorted, including the
// super-authority "*". The admin API exposes it so the console can offer a
// checklist when defining a role rather than asking for free-text authority
// strings (which a typo would silently break).
func Authorities() []string {
	out := make([]string, 0, len(vocabulary))
	for a := range vocabulary {
		out = append(out, string(a))
	}
	sort.Strings(out)
	return out
}
