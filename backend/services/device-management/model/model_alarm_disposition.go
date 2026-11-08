// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// AlarmDispositionLabel is an operator's verdict on one alarm CYCLE: was the condition the rule
// fired on real, and did it need anyone? It is ground truth for measuring a rule rather than
// operating one: a backtest replays a rule over history and scores the alarms it would have
// raised against the cycles people labelled, so precision becomes a number instead of an
// impression.
//
// It is a closed vocabulary a client may branch on, and on purpose a small one. A new value is a
// new meaning every scorer has to account for, so it is added deliberately or not at all.
type AlarmDispositionLabel string

const (
	// AlarmDispositionTruePositive: the condition was real and needed attention.
	AlarmDispositionTruePositive AlarmDispositionLabel = "TRUE_POSITIVE"
	// AlarmDispositionFalsePositive: the condition was not real (a bad reading, a threshold
	// set wrong, a sensor fault the rule mistook for a process fault). A rule change should
	// have prevented it.
	AlarmDispositionFalsePositive AlarmDispositionLabel = "FALSE_POSITIVE"
	// AlarmDispositionExpected: the condition was real but expected and needed no action
	// (planned maintenance, a commissioning test, a known seasonal swing). It is kept apart
	// from FALSE_POSITIVE because the rule was right: the fix, if any, is suppression, not a
	// different threshold.
	AlarmDispositionExpected AlarmDispositionLabel = "EXPECTED"
)

// Valid reports whether the label names one of the known dispositions. Matching is exact: a
// lower-case or padded spelling is refused, not normalised, as AlarmState is.
func (l AlarmDispositionLabel) Valid() bool {
	switch l {
	case AlarmDispositionTruePositive, AlarmDispositionFalsePositive, AlarmDispositionExpected:
		return true
	default:
		return false
	}
}

// MaxAlarmDispositionNoteLength bounds the free-text note on a disposition, in characters. A
// note says why ("pump P-7 was drained for service"); it is not a place for logs.
const MaxAlarmDispositionNoteLength = 1000

// AlarmDisposition is the label one alarm cycle carries.
//
// # Why a table of its own, and not columns on the alarm
//
// An alarm ROW is reused: a re-raise flips a CLEARED row back to ACTIVE in place and moves its
// raised_time (see Alarm). A label is a verdict on one CYCLE, so a column on the row would either
// be wiped by the next raise or, worse, silently carry the last cycle's verdict onto a new one
// nobody has looked at. Keyed by (alarm, raised_time) instead, every cycle keeps its own label,
// and the cycles before the current one stay labelled for the backtests that need them.
//
// One row per cycle: labelling a cycle again replaces its verdict (the audit journal keeps who
// wrote what). The row goes when its alarm goes — the alarm foreign key cascades.
//
// # Visibility
//
// ScopeAnchors is copied from the alarm when the label is written, which is the anchor set that
// cycle was raised under, so a member whose access is limited to some customers reads the labels
// of the cycles it could see. Writing a label is not open to such a member (see ScopedBy).
type AlarmDisposition struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	// TenantId leads the cycle key, which is also the table's tenant index, so it is declared
	// here rather than embedded from rdb.TenantScoped (whose own index would be a duplicate).
	TenantId string `gorm:"not null;size:128;uniqueIndex:uix_alarm_dispositions_cycle,priority:1"`

	// AlarmId is the alarm row the labelled cycle belongs to.
	// Its own index serves the alarm foreign key's ON DELETE CASCADE, which otherwise scans the
	// whole table for every alarm deleted.
	AlarmId uint `gorm:"not null;uniqueIndex:uix_alarm_dispositions_cycle,priority:2;index:idx_alarm_dispositions_alarm"`
	// RaisedTime is the cycle: the alarm's raised_time when it was labelled.
	RaisedTime time.Time `gorm:"not null;uniqueIndex:uix_alarm_dispositions_cycle,priority:3"`
	// AlarmToken is the alarm's token, copied when the label is written. The listing filters by
	// it on this table, under this table's scope rule, so a member who could see a labelled cycle
	// finds it by the alarm's token even after the alarm's current cycle was raised for another
	// customer. Resolving the token through the alarm would apply the alarm's CURRENT stamp
	// instead. An alarm's token never changes.
	AlarmToken string `gorm:"not null;size:255"`

	Disposition  string         `gorm:"not null;size:32"` // one of AlarmDispositionLabel
	Note         sql.NullString // why, in the labeller's words
	RecordedBy   sql.NullString // the authenticated subject that wrote the label
	RecordedTime time.Time      `gorm:"not null"` // when the label was last written

	ScopeAnchors rdb.Anchors `gorm:"type:text[]"`
}

// DefaultOrder implements rdb.Sortable: newest cycle first, the order a scorer or a review list
// walks history in. raised_time is not unique across alarms, so the id is the total tiebreak.
func (AlarmDisposition) DefaultOrder() string {
	return "alarm_dispositions.raised_time DESC, alarm_dispositions.id DESC"
}

// ScopedBy: a scoped caller reads the labels of the alarm cycles its scope could see, by the
// anchors copied from the alarm. It may not write one: a label is ground truth for a tenant's
// rules, which a member limited to some customers does not author. The write is an upsert, which
// the scope callback refuses to a scoped caller whatever the rule says.
func (AlarmDisposition) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorColumn("scope_anchors")
}

// AlarmDispositionRequest is one label to write on the alarm named by Token.
type AlarmDispositionRequest struct {
	Token       string
	Disposition string
	Note        *string
	// RaisedTime, when set, is the cycle the caller is labelling, as the alarm's raisedTime was
	// served to it. The label is refused when the alarm has been raised again since, so a verdict
	// reached on one cycle can never land on the next. Nil labels whatever cycle the alarm is in.
	RaisedTime *string
}

// AlarmDispositionSearchCriteria selects labels. Every facet is optional and they AND together.
type AlarmDispositionSearchCriteria struct {
	rdb.Pagination
	// AlarmToken restricts to the cycles of one alarm. An unknown token finds nothing.
	AlarmToken *string
	// Label restricts to one label. A value outside the vocabulary is an error, not an empty
	// answer, so a misspelt filter cannot read as "nothing was labelled that way". (The served
	// schema types it as an enum, so GraphQL refuses one first; this check is the API's own.)
	Label *string
	// StartTime and EndTime (RFC 3339, inclusive) bound the labelled cycles' raised time.
	StartTime *string
	EndTime   *string
}

// AlarmDispositionSearchResults is one page of labels.
type AlarmDispositionSearchResults struct {
	Results    []AlarmDisposition
	Pagination rdb.SearchResultsPagination
}
