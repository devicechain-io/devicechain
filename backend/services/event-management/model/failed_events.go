// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The failed-events index: one row per record of the failed-events stream that the flood bound
// admitted, plus the count of those it folded in. This file is the row, its writes and its two
// tenant reads; the reader that fills it is processor/FailedEventsIndexer, and the bound that
// decides what is admitted is processor.FailedEventGate.
//
// 🔴 THE PAYLOAD IS NOT HERE. A record on the stream carries the archived event (or, for an
// undecodable message, only a locator); this row carries RecordSeq, the record's position on
// that stream, and nothing of the payload. The relational store therefore never holds a device
// payload, and fetching one is a read of the stream that answers "expired" once the cold
// tier's byte bound has rolled past it (see PayloadStatus).

// Bounds of the index. They are constants of the platform: the flood bound is what keeps this
// table small enough to be a diagnostic rather than a second event store, and a setting an
// operator could raise is a setting that could turn it into one.
const (
	// FailedEventsPerDeviceHour is how many rows one (tenant, reason, device) may add in any
	// sliding hour. A record beyond it is folded into the latest row's suppressed count.
	FailedEventsPerDeviceHour = 10
	// FailedEventsPerReasonHour is how many rows one (tenant, reason) may add in an hour across
	// ALL its devices. The per-device bound alone does not hold when the claimed tokens are all
	// different, which is exactly what a probe of unknown tokens sends; past this, records fold
	// into a single overflow row for the reason (OverflowDeviceToken).
	FailedEventsPerReasonHour = 1000
	// FailedEventsWindow is the "hour" of both bounds.
	FailedEventsWindow = time.Hour
	// OverflowDeviceToken stands in the device column of the row that counts a reason's records
	// beyond FailedEventsPerReasonHour. It is not a token (the grammar has no "*"), so it cannot
	// be mistaken for a device; the API serves it as no device.
	OverflowDeviceToken = "*"
	// MaxFailedEventError bounds the stored error text, in bytes.
	MaxFailedEventError = 512
	// MaxFailedEventsPageSize bounds a page of the tenant list.
	MaxFailedEventsPageSize = 100
	// MaxFailedEventSummaryDevices bounds the top-device list of the summary.
	MaxFailedEventSummaryDevices = 10
	// MaxFailedEventSummarySpan bounds how far back ANY read of the index looks (the list, the
	// summary and the per-device count), which is also the longest retention that is worth
	// reading: the table is bounded by its window and its flood bound, and an unbounded span
	// would make a read a scan of everything it holds.
	MaxFailedEventSummarySpan = 31 * 24 * time.Hour
)

// FailedEventRecord is one row of the failed-events index.
//
// OccurredTime is when the broker stored the record on the failed-events stream (server
// assigned, never supplied by the sender), not when the device sent the event: a failure is
// dated by when the platform refused it, and the sender's clock is the thing a refused event
// cannot be trusted on.
type FailedEventRecord struct {
	rdb.TenantScoped
	// RowID is the row's opaque public identity: random, assigned when the row is created, and
	// the only identifier the API serves for it. The stream position below is deliberately NOT
	// served: it is a position on a stream shared by every tenant, so the gap between two of a
	// tenant's rows would tell it how many records other tenants produced in between.
	RowID        string    `gorm:"type:varchar(32);not null"`
	OccurredTime time.Time `gorm:"not null"`
	// RecordSeq is the record's position on the failed-events stream: where its payload is. It
	// never leaves the service.
	RecordSeq int64 `gorm:"not null"`
	// SourceArea is the functional area that wrote the record.
	SourceArea string `gorm:"type:varchar(64);not null"`
	// Reason is the FailureReason number, stored as sent so a reason a newer writer adds is
	// kept exactly, never mapped to a neighbour.
	Reason int32 `gorm:"not null"`
	// DeviceToken is the token the SENDER CLAIMED. For most reasons no such device exists, and
	// it is a string the sender chose, so it is untrusted text; "" when the record named none.
	DeviceToken string `gorm:"type:varchar(128);not null"`
	// EventType is the wire event type when the record carried one.
	EventType   *int64
	Error       string `gorm:"type:varchar(512);not null"`
	Correlation string `gorm:"type:varchar(128);not null"`
	// PayloadRetained is whether the record's envelope carried an archived event at all, decided
	// when the record is indexed. False for a record whose writer kept nothing (a message that
	// would not parse is dead-lettered with no payload) and for the overflow row: no amount of
	// stream retention will ever produce an original for those, which is a different statement
	// from "the stream has rolled past it". No gorm default: a default would turn false into true.
	PayloadRetained bool `gorm:"not null"`
	// Suppressed counts records the flood bound folded into this row instead of adding one.
	Suppressed int64 `gorm:"not null"`
}

// BeforeCreate assigns the row's opaque id.
func (r *FailedEventRecord) BeforeCreate(*gorm.DB) error {
	if r.RowID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return fmt.Errorf("mint a failed-event row id: %w", err)
		}
		r.RowID = hex.EncodeToString(b[:])
	}
	return nil
}

// DefaultOrder implements rdb.Sortable: newest first, ending in the stream position, which is
// unique within a tenant, so the order is total.
func (FailedEventRecord) DefaultOrder() string {
	return "failed_event_records.occurred_time DESC, failed_event_records.record_seq DESC"
}

// AuditExempt opts the index out of the audit journal: it is machine-written diagnostic data,
// not a control-plane mutation.
func (FailedEventRecord) AuditExempt() bool { return true }

// FailedEventRef names one row: the arbiter of its insert and the key of an update to it.
type FailedEventRef struct {
	RecordSeq    int64
	OccurredTime time.Time
}

// SanitizeFailedEventText makes text fit a failed_event_records column: valid UTF-8 (the
// database refuses anything else), no NUL (a text column refuses it, and a poison message here
// would be a message the consumer can never store), and at most max bytes, cut on a rune
// boundary. The text can be derived from device bytes (a decode error quotes what it could not
// parse), which is the tenant's own traffic but arbitrary in content and size.
func SanitizeFailedEventText(text string, max int) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\x00", "")
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// InsertFailedEventRecord adds one row, returning false when the record was already indexed (a
// redelivery: the same stream position and broker time). It goes through the ORM so the tenant
// scope stamps the tenant and the erasure fence refuses a tenant whose data has been reclaimed
// (rdb.ErrTenantPurged) -- a raw statement would do neither.
func (api *Api) InsertFailedEventRecord(ctx context.Context, rec *FailedEventRecord) (bool, error) {
	res := api.RDB.DB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(rec)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// BumpFailedEventSuppressed adds n to the suppressed count of the row ref names. The row may be
// gone (retention dropped its chunk): that is not an error, there is nothing left to count on.
func (api *Api) BumpFailedEventSuppressed(ctx context.Context, ref FailedEventRef, n int64) error {
	return api.RDB.DB(ctx).Model(&FailedEventRecord{}).
		Where("record_seq = ? AND occurred_time = ?", ref.RecordSeq, ref.OccurredTime).
		UpdateColumn("suppressed", gorm.Expr("suppressed + ?", n)).Error
}

// RecentFailedEvents returns up to limit rows of one (reason, device) newer than since, newest
// first. The flood gate hydrates its per-device state from it.
func (api *Api) RecentFailedEvents(ctx context.Context, reason int32, device string,
	since time.Time, limit int) ([]FailedEventRef, error) {
	var rows []FailedEventRecord
	err := api.RDB.DB(ctx).Select("record_seq", "occurred_time").
		Where("reason = ? AND device_token = ? AND occurred_time > ?", reason, device, since).
		Order("occurred_time DESC, record_seq DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]FailedEventRef, len(rows))
	for i, r := range rows {
		out[i] = FailedEventRef{RecordSeq: r.RecordSeq, OccurredTime: r.OccurredTime}
	}
	return out, nil
}

// CountFailedEvents counts the rows of one reason newer than since, overflow row excluded: the
// gate's per-reason bound counts what it ADMITTED.
func (api *Api) CountFailedEvents(ctx context.Context, reason int32, since time.Time) (int64, error) {
	var n int64
	err := api.RDB.DB(ctx).Model(&FailedEventRecord{}).
		Where("reason = ? AND device_token <> ? AND occurred_time > ?", reason, OverflowDeviceToken, since).
		Count(&n).Error
	return n, err
}

// FailedEventSearchCriteria filters the tenant's failed-event list.
type FailedEventSearchCriteria struct {
	rdb.Pagination
	// Reason selects one FailureReason number. UnknownReasons selects the rows whose reason is
	// not in the vocabulary this build knows (KnownReasons), and wins over Reason.
	Reason         *int32
	UnknownReasons bool
	DeviceToken    *string
	Since          *time.Time
	Until          *time.Time
	// KnownReasons is the vocabulary UnknownReasons is measured against. It is passed in
	// because this package does not own the protobuf enum's reading of "known".
	KnownReasons []int32
}

// FailedEventSearchResults is one page of the tenant's failed events.
type FailedEventSearchResults struct {
	Results    []FailedEventRecord
	Pagination rdb.SearchResultsPagination
}

func failedEventFilters(c FailedEventSearchCriteria) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		switch {
		case c.UnknownReasons:
			if len(c.KnownReasons) > 0 {
				db = db.Where("reason NOT IN ?", c.KnownReasons)
			}
		case c.Reason != nil:
			db = db.Where("reason = ?", *c.Reason)
		}
		if c.DeviceToken != nil {
			db = db.Where("device_token = ?", *c.DeviceToken)
		}
		if c.Since != nil {
			db = db.Where("occurred_time >= ?", *c.Since)
		}
		if c.Until != nil {
			db = db.Where("occurred_time <= ?", *c.Until)
		}
		return db
	}
}

// FailedEvents returns one page of the caller's tenant's failed events, newest first. The
// tenant predicate is the ORM's, so a row of another tenant cannot be returned by any criteria.
func (api *Api) FailedEvents(ctx context.Context, c FailedEventSearchCriteria) (*FailedEventSearchResults, error) {
	if c.PageSize > MaxFailedEventsPageSize {
		c.PageSize = MaxFailedEventsPageSize
	}
	results := make([]FailedEventRecord, 0)
	db, pag := api.RDB.ListOf(ctx, &FailedEventRecord{}, failedEventFilters(c), c.Pagination)
	if err := db.Find(&results).Error; err != nil {
		return nil, err
	}
	return &FailedEventSearchResults{Results: results, Pagination: pag}, nil
}

// FailedEventReasonCount is one reason's line of the summary.
type FailedEventReasonCount struct {
	Reason int32
	// Records is the rows plus what the flood bound folded into them: every record the
	// platform refused for this reason, as far as the index knows.
	Records    int64
	Suppressed int64
	// Devices are the device tokens with the most records for the reason, most first. The
	// overflow row is excluded: it names no device.
	Devices []FailedEventDeviceCount
}

// FailedEventDeviceCount is one device's records for a reason.
type FailedEventDeviceCount struct {
	DeviceToken string
	Records     int64
}

// FailedEventSummary counts the caller's tenant's failures since the given time, per reason.
func (api *Api) FailedEventSummary(ctx context.Context, since time.Time) ([]FailedEventReasonCount, error) {
	type agg struct {
		Reason     int32
		RowCount   int64
		Suppressed int64
	}
	var aggs []agg
	if err := api.RDB.DB(ctx).Model(&FailedEventRecord{}).
		Select("reason, count(*) AS row_count, CAST(COALESCE(sum(suppressed), 0) AS bigint) AS suppressed").
		Where("occurred_time >= ?", since).Group("reason").Order("reason").Scan(&aggs).Error; err != nil {
		return nil, fmt.Errorf("summarize failed events: %w", err)
	}
	out := make([]FailedEventReasonCount, 0, len(aggs))
	for _, a := range aggs {
		line := FailedEventReasonCount{Reason: a.Reason, Records: a.RowCount + a.Suppressed, Suppressed: a.Suppressed}
		var devs []struct {
			DeviceToken string
			Records     int64
		}
		if err := api.RDB.DB(ctx).Model(&FailedEventRecord{}).
			Select("device_token, CAST(count(*) + COALESCE(sum(suppressed), 0) AS bigint) AS records").
			Where("reason = ? AND device_token <> '' AND device_token <> ? AND occurred_time >= ?",
				a.Reason, OverflowDeviceToken, since).
			Group("device_token").Order("records DESC, device_token").Limit(MaxFailedEventSummaryDevices).
			Scan(&devs).Error; err != nil {
			return nil, fmt.Errorf("summarize failed events by device: %w", err)
		}
		for _, d := range devs {
			line.Devices = append(line.Devices, FailedEventDeviceCount{DeviceToken: d.DeviceToken, Records: d.Records})
		}
		out = append(out, line)
	}
	return out, nil
}

// DeviceFailedEventCount is the records refused for one claimed device token since a time: what
// "N events from this device were rejected" is built from.
func (api *Api) DeviceFailedEventCount(ctx context.Context, deviceToken string, since time.Time) (int64, error) {
	var res struct{ Records int64 }
	err := api.RDB.DB(ctx).Model(&FailedEventRecord{}).
		Select("CAST(count(*) + COALESCE(sum(suppressed), 0) AS bigint) AS records").
		Where("device_token = ? AND occurred_time >= ?", deviceToken, since).Scan(&res).Error
	return res.Records, err
}
