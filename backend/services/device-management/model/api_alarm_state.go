// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	return rdb.FindByIds[Alarm](api.RDB.DB(ctx), ids)
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
		// No .Order() here: the deterministic newest-cycle-first order this search
		// needs is Alarm.DefaultOrder(), which ListOf injects for every paged read of
		// the model. One place per model says what the order is, and it says it
		// table-qualified.
		return result
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
	if err := api.applyAcknowledgement(ctx, alarm, by); err != nil {
		return nil, err
	}
	return alarm, nil
}

// applyAcknowledgement is the ONE acknowledgment transition: the CAS'd column-limited write plus the
// event emit that must accompany a row that actually changed. Both the single and the bulk
// acknowledge call it, so the two can never drift into different state rules, different audit
// behaviour, or different concurrency posture — the bulk path is a fan-out over this, not a second
// implementation of it. It mutates alarm in place on success so the caller can return the post-ack
// row without re-reading.
//
// Idempotent: an already-acknowledged alarm is a no-op with no event, exactly as before.
func (api *Api) applyAcknowledgement(ctx context.Context, alarm *Alarm, by *string) error {
	if alarm.Acknowledged {
		return nil
	}
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
		return res.Error
	}
	if res.RowsAffected > 0 {
		alarm.Acknowledged = true
		alarm.AcknowledgedTime = ackTime
		alarm.AcknowledgedBy = ackBy
		api.emitAlarmEvent(ctx, newAlarmStateChangeEvent(alarm, AlarmEventAcknowledged, "", ackTime.Time))
	}
	return nil
}

// MaxBulkAcknowledgeAlarms bounds how many alarms one acknowledge request may name.
//
// It is a REQUEST bound, not a fleet bound — the console chunks a larger selection — so its job is
// only to stop one document from carrying an arbitrarily large variable and turning an operator
// click into a memory event. 2000 matches the request bound already used for batch enqueue
// validation, because it is the same shape of protection against the same shape of caller.
const MaxBulkAcknowledgeAlarms = 2000

// AlarmAckRefusalCode classifies why one named alarm could not be acknowledged. It is a small closed
// vocabulary a client may branch on; the human `Reason` is never parsed.
type AlarmAckRefusalCode string

// AlarmAckNotFound: the token names no alarm in this tenant. It is the only refusal a bulk
// acknowledge can produce, and that is a fact about acknowledgment rather than an omission:
// acknowledgment is orthogonal to ACTIVE/CLEARED (either may be acknowledged) and is idempotent, so
// there is no alarm STATE that refuses it. A token that names nothing is the whole failure space.
const AlarmAckNotFound AlarmAckRefusalCode = "ALARM_NOT_FOUND"

// AlarmAckRefusal is one named alarm the bulk acknowledge did not act on, and why.
type AlarmAckRefusal struct {
	Token  string
	Code   AlarmAckRefusalCode
	Reason string
}

// BulkAcknowledgeResult is what one bulk acknowledge did. Acknowledged holds the alarms that are now
// in the acknowledged state — INCLUDING any that already were, matching the single mutation, which
// also returns an already-acknowledged alarm rather than treating the repeat as a failure. Refusals
// holds the tokens that named nothing.
//
// Every token the caller supplied appears in exactly one of the two, so a caller can reconcile its
// selection completely rather than inferring from a count.
type BulkAcknowledgeResult struct {
	Acknowledged []Alarm
	Refusals     []AlarmAckRefusal
}

// AcknowledgeAlarms acknowledges many alarms in one request — the fan-out counterpart of
// AcknowledgeAlarm, with the same authority, the same transition, and the same audit trail (it
// shares applyAcknowledgement, so it cannot drift from the single path). by is the acknowledging
// identity, stamped by the caller from the authenticated subject.
//
// 🔑 IT IS PARTIAL BY DESIGN. A token that names no alarm produces a refusal; every other named
// alarm is still acknowledged. That follows the batch-command precedent rather than inventing a
// second convention, and it is the right call for the situation this exists for: an operator
// clearing an alarm storm has a stale selection almost by definition (alarms are being erased,
// devices deleted), and failing 200 acknowledgments because one row went away would leave the
// operator with no way to make progress except to bisect the list by hand.
//
// 🔴 AN EMPTY LIST ACKNOWLEDGES NOTHING, and it is held that way by TWO independent things, because
// the blast radius here is not a stray read but every alarm in the tenant silently acknowledged —
// destroying the signal an operator relies on during a storm and leaving a plausible audit trail
// behind it. First, the write loop below is driven by the REQUESTED tokens, never by what the read
// returned, so "no condition" in the read cannot become "acknowledge everything". Second, the empty
// case returns before any statement is built at all. The second is the belt to the first's braces,
// and it is what would still hold if this were ever rewritten into one set-based UPDATE — the
// obvious optimisation, and precisely the shape in which `token in ?` over an empty slice stops
// being a filter (the mistake that once turned an id lookup into a full-table read).
//
// An over-large request is an ERROR, not a refusal list: it is a fault in how the caller chunked its
// work, not a verdict about any alarm, and answering it per-alarm would report a client bug as a
// fleet condition.
//
// Duplicate tokens are collapsed, and both lists come back in the order each token was FIRST named,
// so the answer is a deterministic function of the request.
func (api *Api) AcknowledgeAlarms(ctx context.Context, tokens []string, by *string) (*BulkAcknowledgeResult, error) {
	result := &BulkAcknowledgeResult{Acknowledged: []Alarm{}, Refusals: []AlarmAckRefusal{}}
	if len(tokens) > MaxBulkAcknowledgeAlarms {
		return nil, fmt.Errorf("cannot acknowledge %d alarms in one request; the limit is %d",
			len(tokens), MaxBulkAcknowledgeAlarms)
	}

	// Collapse duplicates while preserving first-named order.
	ordered := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		ordered = append(ordered, token)
	}
	if len(ordered) == 0 {
		return result, nil // the empty case, answered before a statement exists
	}

	matches, err := api.AlarmsByToken(ctx, ordered)
	if err != nil {
		return nil, err
	}
	byToken := make(map[string]*Alarm, len(matches))
	for _, alarm := range matches {
		byToken[alarm.Token] = alarm
	}

	for _, token := range ordered {
		alarm, found := byToken[token]
		if !found {
			result.Refusals = append(result.Refusals, AlarmAckRefusal{
				Token: token, Code: AlarmAckNotFound, Reason: "no alarm with this token",
			})
			continue
		}
		// A store failure is NOT a refusal — a refusal is a verdict about the alarm, and "the database
		// was unreachable" is not one. It aborts the whole request so the caller retries rather than
		// reading a transient outage as "that alarm cannot be acknowledged". Acknowledgment is
		// idempotent, so the retry re-acknowledges the already-done ones harmlessly.
		if err := api.applyAcknowledgement(ctx, alarm, by); err != nil {
			return nil, err
		}
		result.Acknowledged = append(result.Acknowledged, *alarm)
	}
	return result, nil
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
