// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the read + operator API for raised Alarms (ADR-041). Alarms are
// created and mutated toward their terminal states by the DETECT edge integrator (ADR-057),
// not by a create mutation — an alarm exists because a condition fired, never because
// a user asked for one. The operations here are the human-facing half: reading alarms
// and the two operator transitions (acknowledge, clear).

// Get alarms by id.
func (api *Api) AlarmsById(ctx context.Context, ids []uint) ([]*Alarm, error) {
	found := make([]*Alarm, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get alarms by token.
func (api *Api) AlarmsByToken(ctx context.Context, tokens []string) ([]*Alarm, error) {
	found := make([]*Alarm, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Search for alarms that meet criteria. An Originator token, when supplied, is
// resolved to its id against the entity-type registry (an OriginatorType is then
// required so the registry knows which type to resolve against); an unresolvable
// originator yields an empty result rather than an error.
func (api *Api) Alarms(ctx context.Context, criteria AlarmSearchCriteria) (*AlarmSearchResults, error) {
	var originatorId *uint
	if criteria.Originator != nil {
		if criteria.OriginatorType == nil {
			return nil, errors.New("originatorType is required when filtering by originator")
		}
		id, err := api.ResolveEntityToken(ctx, *criteria.OriginatorType, *criteria.Originator)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &AlarmSearchResults{
					Results:    make([]Alarm, 0),
					Pagination: rdb.SearchResultsPagination{},
				}, nil
			}
			return nil, err
		}
		originatorId = &id
	}

	results := make([]Alarm, 0)
	db, pag := api.RDB.ListOf(ctx, &Alarm{}, func(result *gorm.DB) *gorm.DB {
		if criteria.OriginatorType != nil {
			result = result.Where("originator_type = ?", *criteria.OriginatorType)
		}
		if originatorId != nil {
			result = result.Where("originator_id = ?", *originatorId)
		}
		if criteria.State != nil {
			result = result.Where("state = ?", *criteria.State)
		}
		if criteria.Severity != nil {
			result = result.Where("severity = ?", *criteria.Severity)
		}
		if criteria.Acknowledged != nil {
			result = result.Where("acknowledged = ?", *criteria.Acknowledged)
		}
		if criteria.AlarmKey != nil {
			result = result.Where("alarm_key = ?", *criteria.AlarmKey)
		}
		// Deterministic order: newest cycle first, id as a stable tiebreak. Without
		// it the heap order shifts under every in-place UPDATE (ack, escalation, a
		// last-value write on each matching measurement), so a live console that
		// reconciles by re-querying would see rows reshuffle and page boundaries move.
		return result.Order("raised_time DESC, id DESC")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AlarmSearchResults{Results: results, Pagination: pag}, nil
}

// AcknowledgeAlarm records an operator acknowledgment of the alarm named by token.
// Acknowledgment is orthogonal to the ACTIVE/CLEARED state (a still-active alarm may
// be acknowledged) and is idempotent — re-acknowledging is a no-op that returns the
// current row. by is the acknowledging identity, stamped by the caller from the
// authenticated subject. Returns ErrRecordNotFound when the token names no alarm.
//
// The write is column-limited (only the three ack columns) rather than a full-row
// Save: the DETECT edge integrator mutates other columns of the same row in place
// (severity re-derivation, LastValue, contributor set), so writing back a stale full
// row would clobber a concurrent edge apply.
func (api *Api) AcknowledgeAlarm(ctx context.Context, token string, by *string) (*Alarm, error) {
	matches, err := api.AlarmsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	alarm := matches[0]
	if !alarm.Acknowledged {
		ackTime := sql.NullTime{Time: time.Now().UTC(), Valid: true}
		ackBy := rdb.NullStrOf(by)
		// Predicate on the unacked state so two concurrent acks don't both emit an
		// ACKNOWLEDGED event (and the loser doesn't overwrite the winner's identity);
		// gate the struct update + emit on a row actually changing.
		res := api.RDB.DB(ctx).Model(alarm).Where("acknowledged = ?", false).
			Updates(map[string]interface{}{
				"acknowledged":      true,
				"acknowledged_time": ackTime,
				"acknowledged_by":   ackBy,
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			alarm.Acknowledged = true
			alarm.AcknowledgedTime = ackTime
			alarm.AcknowledgedBy = ackBy
			api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(alarm, AlarmEventAcknowledged, "", ackTime.Time))
		}
	}
	return alarm, nil
}

// ClearAlarm records a manual operator clear of the alarm named by token, moving it
// to CLEARED and stamping ClearedTime. This is the human override; the DETECT edge
// integrator also clears when a rule's condition resolves (ADR-057). Idempotent — clearing an
// already-CLEARED alarm returns the current row. Returns ErrRecordNotFound when the
// token names no alarm. Column-limited for the same concurrency reason as
// AcknowledgeAlarm.
func (api *Api) ClearAlarm(ctx context.Context, token string) (*Alarm, error) {
	matches, err := api.AlarmsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	alarm := matches[0]
	if alarm.State != string(AlarmStateCleared) {
		clearedTime := sql.NullTime{Time: time.Now().UTC(), Valid: true}
		// Predicate on not-already-cleared so a concurrent clear (the integrator) and
		// this manual clear don't both emit CLEARED; gate the emit on RowsAffected.
		res := api.RDB.DB(ctx).Model(alarm).Where("state <> ?", string(AlarmStateCleared)).
			Updates(map[string]interface{}{
				"state":        string(AlarmStateCleared),
				"cleared_time": clearedTime,
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			alarm.State = string(AlarmStateCleared)
			alarm.ClearedTime = clearedTime
			api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(alarm, AlarmEventCleared, "", clearedTime.Time))
		}
	}
	return alarm, nil
}
