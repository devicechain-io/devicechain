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
		matches, err := api.AlarmsByToken(ctx, []string{*criteria.AlarmToken})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return empty, nil
		}
		alarmId = &matches[0].ID
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
