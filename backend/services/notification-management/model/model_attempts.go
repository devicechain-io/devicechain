// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
)

// The outcome of one delivery attempt, as the ledger records it.
//
// The set is served as a STRING, not a GraphQL enum, on purpose: a later slice records
// notifications that never reached a channel (held by quiet hours, suppressed by a
// throttle), and a value added to a served enum is a breaking change for every client that
// switches on it exhaustively. A string documented as open lets those values arrive in a
// minor.
const (
	// AttemptStatusSent — the channel's adapter accepted the notification.
	AttemptStatusSent = "SENT"
	// AttemptStatusFailed — the attempt failed in a way a retry might fix (a timeout, a
	// 5xx, a refused connection).
	AttemptStatusFailed = "FAILED"
	// AttemptStatusRefused — the attempt was refused terminally: the destination is one
	// outbound traffic may not reach, or the channel's credential configuration is refused.
	// Retrying cannot change the answer, so no retry follows it.
	AttemptStatusRefused = "REFUSED"
	// AttemptStatusNotAttempted — the channel was planned but never tried: its delivery
	// secret could not be read, or the dispatch's time budget was spent before reaching it.
	AttemptStatusNotAttempted = "NOT_ATTEMPTED"
)

// Why an attempt did not end in SENT. Empty on a SENT attempt.
const (
	AttemptErrorEgressBlocked     = "EGRESS_BLOCKED"
	AttemptErrorCredentialRefused = "CREDENTIAL_REFUSED"
	AttemptErrorTimeout           = "TIMEOUT"
	AttemptErrorDelivery          = "DELIVERY_ERROR"
	AttemptErrorSecretUnavailable = "SECRET_UNAVAILABLE"
	AttemptErrorBudgetSpent       = "BUDGET_SPENT"
)

// What caused the delivery an attempt belongs to.
const (
	// AttemptTriggerAlarm — an alarm state change arrived on the alarm-events stream.
	AttemptTriggerAlarm = "ALARM"
	// AttemptTriggerEscalation — the escalation scheduler re-notified an open alarm.
	AttemptTriggerEscalation = "ESCALATION"
	// AttemptTriggerQuietHoursRelease — a notification held by quiet hours was released.
	AttemptTriggerQuietHoursRelease = "QUIET_HOURS_RELEASE"
)

// NotificationAttempt is one row of the delivery ledger: one try at sending one alarm's
// notification through one channel to its recipients. It answers the questions a
// notification policy alone cannot: was this alarm actually paged, to whom, through what,
// and if not, why not.
//
// The ledger is append-only and BEST-EFFORT. A row is written after the attempt it
// describes, off the delivery path, and a write that fails is counted and dropped rather
// than retried: the ledger must never delay or block a page. So a missing row does not
// prove an attempt never happened, though a row always proves one did.
//
// 🔴 IT HOLDS PERSONAL DATA. Recipients are email addresses and similar. The row carries
// tenant_id, so the catalog-driven tenant purge erases it with the tenant and the erasure
// fence refuses a write for a purged tenant; and it is pruned after a retention window
// (AttemptRetentionSeconds) so it does not accumulate addresses forever.
//
// Channel and policy are recorded by TOKEN, not by foreign key: the ledger has to outlive
// a channel that is deleted after it paged, which is exactly when someone asks what it
// did.
type NotificationAttempt struct {
	ID        uint      `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"not null"`

	// TenantId leads every index the migration creates on this table (by alarm, by
	// channel, by time); the tenant-scope callback matches the field by this name.
	TenantId string `gorm:"not null;size:128;index"`

	// AlarmToken names the alarm the notification was about.
	AlarmToken string `gorm:"not null;size:128"`
	// Trigger is what caused the delivery (AttemptTrigger*).
	Trigger string `gorm:"column:trigger_kind;not null;size:32"`
	// PolicyToken is the policy whose rule planned the delivery. When two policies route
	// to the same channel and recipients, the delivery is made once and the first policy
	// is recorded.
	PolicyToken  sql.NullString `gorm:"size:128"`
	ChannelToken string         `gorm:"not null;size:128"`
	ChannelType  string         `gorm:"not null;size:32"`
	// Recipients is the resolved recipient list as a JSON array of strings.
	Recipients datatypes.JSON
	// SubjectHash is the hex SHA-256 of the rendered subject line, so two attempts can be
	// shown to have carried the same message without the ledger storing the message.
	SubjectHash string `gorm:"size:64"`

	Status string `gorm:"not null;size:32"`
	// Attempt is this try's number (1-based) and MaxAttempts the tries the channel was
	// allowed. A NOT_ATTEMPTED row carries 0.
	Attempt     int
	MaxAttempts int
	// LatencyMillis is how long the adapter call took; null when no call was made.
	LatencyMillis sql.NullInt64
	// ErrorCode classifies a non-SENT outcome (AttemptError*); ErrorMessage is the
	// provider's error text with URLs removed and the length capped.
	ErrorCode    sql.NullString `gorm:"size:32"`
	ErrorMessage sql.NullString `gorm:"size:512"`
}

// DefaultOrder implements rdb.Sortable: newest first, tiebroken on the row id.
func (NotificationAttempt) DefaultOrder() string {
	return "notification_attempts.created_at DESC, notification_attempts.id DESC"
}

// NotificationAttemptSearchCriteria filters the ledger. Every filter is optional.
type NotificationAttemptSearchCriteria struct {
	rdb.Pagination
	AlarmToken   *string
	ChannelToken *string
	Status       *string
}

// NotificationAttemptSearchResults is a page of ledger rows.
type NotificationAttemptSearchResults struct {
	Results    []NotificationAttempt
	Pagination rdb.SearchResultsPagination
}

// ChannelHealth is a channel's recent delivery record, read from the ledger.
type ChannelHealth struct {
	LastSuccessAt       sql.NullTime
	LastFailureAt       sql.NullTime
	ConsecutiveFailures int64
}
