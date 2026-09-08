// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SeverityAny is the rule severity wildcard: a rule with Severity == "*" matches
// an alarm of any severity. Any other value matches that severity exactly.
const SeverityAny = "*"

// NotificationPolicy is a tenant's routing configuration (ADR-017): it decides
// which raised alarms get delivered, to whom, and through which channels. A policy
// is an aggregate — its per-severity Rules are owned by it and edited as a set
// through the policy (there is no standalone rule API).
//
// DeviceTypeToken optionally scopes the policy to one device profile (NULL =
// tenant-wide); it is an opaque soft reference (device types live in
// device-management), so its existence is not validated here — the dispatcher (N.C)
// resolves an alarm's originator to a device type when it evaluates scope, and the
// cross-service referential-integrity strategy (ADR-044) is a separate decision.
// ThrottleSeconds is the minimum gap between notifications for the same alarm (NULL
// = no throttle); the dispatcher enforces it against the per-alarm NotificationState.
type NotificationPolicy struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeToken sql.NullString
	ThrottleSeconds sql.NullInt64
	Enabled         bool

	// Escalation (ADR-017 N.D). When EscalateAfterSeconds is set (> 0), an alarm this
	// policy notified that stays neither acknowledged nor cleared this long after its
	// last notification is re-notified through the same channels by the escalation
	// scheduler, up to MaxEscalations times (NULL/0 = the service-wide default cap). A
	// NULL/0 EscalateAfterSeconds disables escalation for the policy — a policy pages
	// once on RAISED (and again on ESCALATED) but never re-pages on a timer.
	//
	// Escalation state is ONE clock and tier per alarm, not per policy: when several
	// escalation-enabled policies match the same alarm, the shortest EscalateAfterSeconds
	// drives the re-notification cadence (each re-notification re-arms the shared clock)
	// and every policy's cap is measured against the shared tier. So a fast policy and a
	// slow policy on one alarm do not run independent escalation chains — the fast one
	// paces re-notification for both. Independent per-policy escalation schedules would
	// need per-(alarm, policy) state and is a deferred enhancement.
	EscalateAfterSeconds sql.NullInt64
	MaxEscalations       sql.NullInt64

	// Rules are the policy's owned routing rules; loaded with the policy and
	// replaced wholesale on update. The FK is the shortened PolicyId (not the GORM
	// default NotificationPolicyID), so it is named explicitly.
	Rules []NotificationRule `gorm:"foreignKey:PolicyId"`
}

// DefaultOrder implements rdb.Sortable: the registry default — newest first, tiebroken
// on the policy's per-tenant token, which is unique and so makes the order total.
//
// Qualification earns its keep here even though the paged read adds no JOIN: the search
// closure preloads Rules and Rules.Channel, and NotificationChannel carries a token
// column too. A bare `token ASC` is one refactor away from being ambiguous rather than
// merely unqualified.
func (NotificationPolicy) DefaultOrder() string {
	return "notification_policies.created_at DESC, notification_policies.token ASC"
}

// NotificationRule is one routing rule within a policy: for alarms matching
// Severity (exact, or SeverityAny), deliver through Channel to Recipients. It is a
// child of exactly one NotificationPolicy (PolicyId) and is never addressed on its
// own, so it carries no token. Recipients is an opaque JSON array of strings the
// channel adapter interprets (email addresses for SMTP; may be empty for a webhook,
// whose endpoint is the channel's config).
type NotificationRule struct {
	gorm.Model
	rdb.TenantScoped

	PolicyId   uint
	Severity   string
	ChannelId  uint
	Channel    *NotificationChannel
	Recipients *datatypes.JSON
}

// NotificationRuleCreateRequest is one rule inside a policy create/update. The
// channel is named by token and resolved to the owning channel on write; an
// unknown or cross-tenant token fails the whole policy write.
type NotificationRuleCreateRequest struct {
	Severity     string
	ChannelToken string
	Recipients   *string
}

// NotificationPolicyCreateRequest is the data required to CREATE a policy together with
// its full rule set. It is no longer the update input; NotificationPolicyUpdateRequest
// is.
type NotificationPolicyCreateRequest struct {
	Token                string
	Name                 *string
	Description          *string
	DeviceTypeToken      *string
	ThrottleSeconds      *int32
	EscalateAfterSeconds *int32
	MaxEscalations       *int32
	Enabled              bool
	Rules                []*NotificationRuleCreateRequest
	Metadata             *string
}

// NotificationPolicyUpdateRequest is the three-state update input: an OMITTED field
// leaves the stored value alone, an explicit NULL clears it, and a value sets it.
//
// It carries NO Token. The `token` argument names the record, and this input has no way
// to disagree with it — where before the two were reconciled and a disagreement refused.
// A policy's token is referenced by nothing and pinned by nothing, so there is no rename
// mutation to replace it with: a policy is renamed by creating one and deleting the other.
//
// # 🔴 Rules IS OPTIONAL, AND THAT IS THE POINT OF THE CONVERSION
//
// The create input declares `Rules []*NotificationRuleCreateRequest` — required, and on
// the old update path it was applied unconditionally: every update DELETED every rule and
// reinserted the request's, so an edit that said nothing about rules destroyed and
// recreated them (new ids, new updated_at) or, if the caller omitted the list, emptied the
// policy outright and returned success. For the service that carries alarms to humans,
// that is an alerting outage spelled as a metadata edit.
//
// Optional makes the absent state representable:
//
//	ABSENT   -> the rule set is untouched, ids and all
//	NULL, [] -> the policy now has no rules
//	[a, b]   -> the rule set is exactly [a, b] (the whole-replace that was the only option)
//
// See OptionalNotificationRuleList for why null and [] are one state rather than two.
//
// # 🔴 deviceTypeToken IS DELIBERATELY ABSENT FROM THIS INPUT
//
// A non-empty deviceTypeToken is REFUSED at write (validateDeviceTypeScoping): the
// dispatcher skips a device-type-scoped policy rather than applying it tenant-wide, so
// accepting one would return success on a policy that delivers nothing. The create input
// keeps the field, and keeps the refusal, because that is where an operator meets the
// limitation and reads the explanation.
//
// On UPDATE the field would have no reachable meaning at all. Every stored policy's
// device_type_token is NULL — the create path is the only way one is written, and it
// refuses anything else — so a value is refused, and a null clears a column that is
// already null. A field whose only accepted request is a no-op is not a field, and
// leaving it here would mean declaring it to the partial-update harness with a seeded
// value no create path can produce. When the cross-service originator→device-type
// resolution lands, this field comes back here alongside the dispatcher change that
// honours it.
type NotificationPolicyUpdateRequest struct {
	Name                 dcgraphql.OptionalString
	Description          dcgraphql.OptionalString
	ThrottleSeconds      dcgraphql.OptionalInt32
	EscalateAfterSeconds dcgraphql.OptionalInt32
	MaxEscalations       dcgraphql.OptionalInt32
	Enabled              dcgraphql.OptionalBool
	Rules                OptionalNotificationRuleList
	Metadata             dcgraphql.OptionalString
}

// NotificationPolicySearchCriteria locates policies by optional filters.
type NotificationPolicySearchCriteria struct {
	rdb.Pagination
	DeviceTypeToken *string
	Enabled         *bool
}

// NotificationPolicySearchResults is a page of policy search results.
type NotificationPolicySearchResults struct {
	Results    []NotificationPolicy
	Pagination rdb.SearchResultsPagination
}
