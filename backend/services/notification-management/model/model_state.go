// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NotificationState is the per-alarm notification/escalation state (ADR-017): one
// row per RAISED alarm (keyed by its unique AlarmToken), tracking what the service
// has done about that alarm, not the alarm itself. It is deliberately a small
// upserted state row — NOT an alarm-transition history (which is owned by
// device-management/event-management, ADR-041).
//
// 🔴 A RE-RAISE IS NOT A NEW ROW. device-management reactivates the SAME alarm on a fresh
// cycle — same token, back to ACTIVE, acknowledgement reset — so one row serves every
// cycle of an alarm, and CycleRaisedAt says which cycle its per-cycle fields describe.
//
// This slice (N.B) defines the table and its read surface as the substrate the
// dispatcher (N.C) writes on each delivery and the escalation scheduler (N.D)
// reads to decide re-notification: AcknowledgedAt drives escalation stop, the
// notified timestamps drive throttle, and the escalation fields drive tiering.
// Population is intentionally deferred to N.C — the columns exist now so those
// slices are pure additions, never a schema migration on live data.
type NotificationState struct {
	gorm.Model
	rdb.TenantScoped

	// AlarmToken is the raised alarm's unique token — the natural key (one state
	// row per raised alarm). AlarmKey is the (originator, alarmKey) logical key,
	// kept for reference/grouping. Severity is the alarm's current severity.
	AlarmToken string `gorm:"not null;size:128"`
	AlarmKey   string `gorm:"not null;size:256"`
	Severity   string `gorm:"size:16"`

	// Notification progress (written by the N.C dispatcher).
	FirstNotifiedAt sql.NullTime
	LastNotifiedAt  sql.NullTime
	NotifyCount     int

	// Lifecycle stamps that gate escalation: once AcknowledgedAt or ClearedAt is
	// set, the N.D scheduler stops re-notifying.
	AcknowledgedAt sql.NullTime
	ClearedAt      sql.NullTime

	// Escalation tiering (written by the N.D scheduler).
	EscalationLevel int
	LastEscalatedAt sql.NullTime

	// CycleRaisedAt is the RaisedTime of the alarm cycle this row's per-cycle fields
	// belong to: the two terminal stamps, the escalation tier, FirstNotifiedAt and
	// NotifyCount. An event from a LATER cycle resets them before it applies (see
	// Api.RecordNotification and Api.markTerminal); an event from an EARLIER cycle is
	// stale and changes nothing. LastNotifiedAt is deliberately NOT per-cycle: it is the
	// throttle basis, and a flapping alarm's re-raise is exactly what the throttle is
	// for. NULL on a row written before the column existed; the first event that names
	// a cycle adopts it, resetting the row only when that cycle was raised after the
	// row's clear (see enterCycle).
	CycleRaisedAt sql.NullTime

	// ClearNotifiedAt is when this cycle's clear was CLAIMED for announcement to the channels
	// that paged it (see Api.ClaimClearAnnouncement). Per cycle: a new cycle resets it. NULL
	// means no clear has been announced for the cycle the row describes.
	ClearNotifiedAt sql.NullTime

	// PagedTargets is a JSON array of PagedTarget: the (channel, recipients) targets that
	// actually delivered a page in this cycle, merged in as pages go out. A clear is sent only
	// to these, so it reaches what was paged and nothing else. Per cycle; NULL until a page is
	// recorded (including every row written before the column existed).
	PagedTargets *datatypes.JSON
}

// PagedTarget names one destination that delivered a page: the channel by token and the
// resolved recipients, the same identity the dispatcher dedups deliveries on.
type PagedTarget struct {
	Channel    string   `json:"channel"`
	Recipients []string `json:"recipients,omitempty"`
}

// Key is the target's identity, matching the dispatcher's delivery dedup key.
func (t PagedTarget) Key() string { return t.Channel + "|" + strings.Join(t.Recipients, ",") }

// PagedTargetSet decodes the row's paged targets; a missing or unreadable value is the empty
// set, which sends no clear (an unreadable record must not turn into a clear sent anywhere).
func (s *NotificationState) PagedTargetSet() map[string]bool {
	out := map[string]bool{}
	if s == nil || s.PagedTargets == nil {
		return out
	}
	var targets []PagedTarget
	if err := json.Unmarshal(*s.PagedTargets, &targets); err != nil {
		return out
	}
	for _, t := range targets {
		out[t.Key()] = true
	}
	return out
}

// SameCycleAs reports whether an event raised at `raised` belongs to the cycle the row
// describes. A row or an event that names no cycle cannot be ordered and is taken as the same.
func (s *NotificationState) SameCycleAs(raised time.Time) bool {
	k := cycleKey(raised)
	return !s.CycleRaisedAt.Valid || !k.Valid || s.CycleRaisedAt.Time.Equal(k.Time)
}

// DefaultOrder implements rdb.Sortable: newest first, tiebroken on the row id.
//
// The tiebreak is id rather than a token because this table has none — alarm_token
// names the ALARM this row is about, not this row, and it is a plain column rather than
// an rdb.TokenReference. It is unique per tenant in practice (one state row per raised
// alarm), but the total order rests on id, which is unique by construction. The
// timestamps that actually matter operationally (LastNotifiedAt, LastEscalatedAt) are
// nullable and would need explicit NULLS placement; created_at is not, so the registry
// default applies unchanged.
func (NotificationState) DefaultOrder() string {
	return "notification_states.created_at DESC, notification_states.id DESC"
}

// NotificationStateSearchCriteria locates per-alarm notification state by optional
// filters.
type NotificationStateSearchCriteria struct {
	rdb.Pagination
	AlarmKey *string
	Severity *string
}

// NotificationStateSearchResults is a page of notification-state search results.
type NotificationStateSearchResults struct {
	Results    []NotificationState
	Pagination rdb.SearchResultsPagination
}
