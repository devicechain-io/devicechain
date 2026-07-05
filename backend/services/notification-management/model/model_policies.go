// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

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
	// Rules are the policy's owned routing rules; loaded with the policy and
	// replaced wholesale on update. The FK is the shortened PolicyId (not the GORM
	// default NotificationPolicyID), so it is named explicitly.
	Rules []NotificationRule `gorm:"foreignKey:PolicyId"`
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

// NotificationPolicyCreateRequest is the data required to create or update a
// policy together with its full rule set (Rules replaces any existing rules on
// update).
type NotificationPolicyCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	DeviceTypeToken *string
	ThrottleSeconds *int32
	Enabled         bool
	Rules           []*NotificationRuleCreateRequest
	Metadata        *string
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
