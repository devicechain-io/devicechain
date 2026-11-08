// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInvalidAlarmDisposition refuses a label outside the vocabulary, a note over its bound, or a
// raisedTime that is not a timestamp. INVALID_VALUE; the sentence it is wrapped with says which.
var ErrInvalidAlarmDisposition = integrity.NewRefusal(integrity.ClassInvalid,
	"the alarm disposition is not valid")

// ErrAlarmCycleMoved refuses a label stated for a cycle the alarm has left: it was raised again
// after the caller read it, so the verdict the caller reached is about a cycle that is no longer
// the alarm's current one. It is a stale-write refusal, never a conflict.Error, for the reason
// rdb.StaleWriteError gives: CONFLICT reads as "already exists, carry on", and a label that was
// not written must not be reported as written.
var ErrAlarmCycleMoved = rdb.NewStaleWriteError("alarm")

// validDispositionLabel checks a label against the vocabulary.
func validDispositionLabel(label string) error {
	if !AlarmDispositionLabel(label).Valid() {
		return fmt.Errorf("%w: disposition %q is not one of %s, %s or %s", ErrInvalidAlarmDisposition, label,
			AlarmDispositionTruePositive, AlarmDispositionFalsePositive, AlarmDispositionExpected)
	}
	return nil
}

// SetAlarmDisposition labels the current cycle of the alarm named by req.Token, or the cycle
// req.RaisedTime names when that is still the current one. by is the labelling identity, stamped
// by the caller from the authenticated subject. Returns the alarm and the label as written.
//
// Labelling a cycle again replaces its label. A note that is empty after trimming is no note.
//
// The label is a fact about a cycle that has already been raised, so it changes nothing about the
// alarm itself: state, acknowledgement and severity are untouched, and no alarm event is emitted.
// Labelling a CLEARED alarm is the ordinary case (the verdict usually comes after the fact).
func (api *Api) SetAlarmDisposition(ctx context.Context, req AlarmDispositionRequest, by *string) (*Alarm, *AlarmDisposition, error) {
	if err := validDispositionLabel(req.Disposition); err != nil {
		return nil, nil, err
	}
	note := sql.NullString{}
	if req.Note != nil {
		if trimmed := strings.TrimSpace(*req.Note); trimmed != "" {
			if n := utf8.RuneCountInString(trimmed); n > MaxAlarmDispositionNoteLength {
				return nil, nil, fmt.Errorf("%w: the note is %d characters; the limit is %d",
					ErrInvalidAlarmDisposition, n, MaxAlarmDispositionNoteLength)
			}
			note = sql.NullString{String: trimmed, Valid: true}
		}
	}
	var stated *time.Time
	if req.RaisedTime != nil {
		t, err := time.Parse(time.RFC3339Nano, *req.RaisedTime)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: raisedTime must be an RFC 3339 timestamp: %v",
				ErrInvalidAlarmDisposition, err)
		}
		stated = &t
	}

	matches, err := api.AlarmsByToken(ctx, []string{req.Token})
	if err != nil {
		return nil, nil, err
	}
	if len(matches) == 0 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	alarm := matches[0]
	if stated != nil && !stated.Equal(alarm.RaisedTime) {
		return nil, nil, ErrAlarmCycleMoved
	}

	now := time.Now().UTC()
	row := &AlarmDisposition{
		AlarmId:      alarm.ID,
		RaisedTime:   alarm.RaisedTime,
		Disposition:  req.Disposition,
		Note:         note,
		RecordedBy:   rdb.NullStrOf(by),
		RecordedTime: now,
		ScopeAnchors: alarm.ScopeAnchors,
	}
	// One row per cycle. The conflict target names the tenant column, so the tenant guard's
	// update arm needs no review marker. scope_anchors is rewritten with the rest: it is the
	// alarm's stamp for this cycle, and the alarm's stamp is the authority on who may see it.
	err = api.RDB.DB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "alarm_id"}, {Name: "raised_time"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"disposition", "note", "recorded_by", "recorded_time", "scope_anchors", "updated_at",
		}),
	}).Create(row).Error
	if err != nil {
		return nil, nil, err
	}
	return alarm, row, nil
}

// AlarmDispositionOf returns the label of the alarm's CURRENT cycle, or nil when that cycle is
// unlabelled. A label written on an earlier cycle is never returned for a later one: that is the
// whole reason labels are keyed by cycle.
func (api *Api) AlarmDispositionOf(ctx context.Context, alarm *Alarm) (*AlarmDisposition, error) {
	var found []AlarmDisposition
	if err := api.RDB.DB(ctx).Where("alarm_id = ? AND raised_time = ?", alarm.ID, alarm.RaisedTime).
		Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// AlarmDispositions lists labels, newest cycle first. It is the read a backtest scores against:
// every labelled cycle, including the ones before an alarm's current cycle.
func (api *Api) AlarmDispositions(ctx context.Context, criteria AlarmDispositionSearchCriteria) (*AlarmDispositionSearchResults, error) {
	empty := &AlarmDispositionSearchResults{Results: make([]AlarmDisposition, 0)}
	if criteria.Disposition != nil {
		if err := validDispositionLabel(*criteria.Disposition); err != nil {
			return nil, err
		}
	}
	start, err := alarmRangeBound("startTime", criteria.StartTime)
	if err != nil {
		return nil, err
	}
	end, err := alarmRangeBound("endTime", criteria.EndTime)
	if err != nil {
		return nil, err
	}
	var alarmId *uint
	if criteria.AlarmToken != nil {
		id, found, err := api.alarmIdForLabels(ctx, *criteria.AlarmToken)
		if err != nil {
			return nil, err
		}
		if !found {
			return empty, nil
		}
		alarmId = &id
	}

	results := make([]AlarmDisposition, 0)
	db, pag := api.RDB.ListOf(ctx, &AlarmDisposition{}, func(q *gorm.DB) *gorm.DB {
		if alarmId != nil {
			q = q.Where("alarm_dispositions.alarm_id = ?", *alarmId)
		}
		if criteria.Disposition != nil {
			q = q.Where("alarm_dispositions.disposition = ?", *criteria.Disposition)
		}
		if start != nil {
			q = q.Where("alarm_dispositions.raised_time >= ?", *start)
		}
		if end != nil {
			q = q.Where("alarm_dispositions.raised_time <= ?", *end)
		}
		return q
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AlarmDispositionSearchResults{Results: results, Pagination: pag}, nil
}

// alarmIdForLabels resolves an alarm token to its id for the label listing's alarm facet, within
// the caller's tenant but NOT under the caller's alarm scope.
//
// 🔑 The alarm's scope stamp is its CURRENT cycle's, and a label is visible by the stamp of the
// cycle it labels. A member who could see an earlier cycle (raised while the device was its
// customer's) still reads that cycle's label, so filtering by the alarm's token must not answer
// "nothing" just because the alarm has since been raised for another customer. What keeps the
// member inside its own labels is the listing's own scope predicate on alarm_dispositions; the id
// resolved here is only a filter on that already-scoped read, and is never served.
func (api *Api) alarmIdForLabels(ctx context.Context, token string) (uint, bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return 0, false, core.ErrNoTenant
	}
	var ids []uint
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&Alarm{}).
		Where("tenant_id = ? AND token = ?", tenant, token).Limit(1).Pluck("id", &ids).Error; err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	return ids[0], true, nil
}
