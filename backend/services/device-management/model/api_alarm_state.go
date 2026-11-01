// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
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
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// alarmFacets are the filters an alarm search and an alarm count share.
type alarmFacets struct {
	originatorType  *string
	originatorToken *string
	state           *string
	severity        *string
	acknowledged    *bool
	alarmKey        *string
}

// resolve turns the facets into a filter. An Originator token, when supplied, is resolved to
// its id against the entity-type registry (an OriginatorType is then required so the registry
// knows which type to resolve against). ok is false when the originator names nothing, which the
// caller answers with an empty result rather than an error.
func (f alarmFacets) resolve(ctx context.Context, api *Api) (filter func(*gorm.DB) *gorm.DB, ok bool, err error) {
	var originatorId *uint
	if f.originatorToken != nil {
		if f.originatorType == nil {
			return nil, false, errors.New("originatorType is required when filtering by originator")
		}
		id, err := api.ResolveEntityToken(ctx, *f.originatorType, *f.originatorToken)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, nil
			}
			return nil, false, err
		}
		originatorId = &id
	}
	return func(result *gorm.DB) *gorm.DB {
		if f.originatorType != nil {
			result = result.Where("alarms.originator_type = ?", *f.originatorType)
		}
		if originatorId != nil {
			result = result.Where("alarms.originator_id = ?", *originatorId)
		}
		if f.state != nil {
			result = result.Where("alarms.state = ?", *f.state)
		}
		if f.severity != nil {
			result = result.Where("alarms.severity = ?", *f.severity)
		}
		if f.acknowledged != nil {
			result = result.Where("alarms.acknowledged = ?", *f.acknowledged)
		}
		if f.alarmKey != nil {
			result = result.Where("alarms.alarm_key = ?", *f.alarmKey)
		}
		return result
	}, true, nil
}

// alarmDevices is the ONE device read that both the device-type filter of an alarm search and
// the per-type alarm count classify alarms by, so a type's count and the rows its filter returns
// can never disagree about which alarms belong to it. It is an ordinary read of the caller's
// devices, built through gorm, so the tenant predicate and — for a member whose access is limited
// to some customers — the device scope rule apply to it: a scoped caller classifies only the
// devices its scope currently owns.
func (api *Api) alarmDevices(ctx context.Context) *gorm.DB {
	return api.RDB.DB(ctx).Model(&Device{})
}

// alarmDeviceTypeFilter is the ONE device-type facet: the search's DeviceType filter and the
// per-severity count's both narrow by it, so a type's rows and its severity counts cannot disagree
// about which alarms are on that type. A nil token is no facet (the identity filter); an unknown
// token reports !ok, which the caller answers with nothing, as for an unknown originator.
func (api *Api) alarmDeviceTypeFilter(ctx context.Context, deviceType *string) (func(*gorm.DB) *gorm.DB, bool, error) {
	if deviceType == nil {
		return func(db *gorm.DB) *gorm.DB { return db }, true, nil
	}
	types, err := api.DeviceTypesByToken(ctx, []string{*deviceType})
	if err != nil {
		return nil, false, err
	}
	if len(types) == 0 {
		return nil, false, nil
	}
	id := types[0].ID
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("alarms.originator_type = ? AND alarms.originator_id IN (?)",
			string(entity.TypeDevice), api.alarmDevices(ctx).Select("devices.id").Where("devices.device_type_id = ?", id))
	}, true, nil
}

// Search for alarms that meet criteria. An unresolvable originator or device type yields an
// empty result rather than an error.
func (api *Api) Alarms(ctx context.Context, criteria AlarmSearchCriteria) (*AlarmSearchResults, error) {
	// The anchor is judged first: a refusal must not depend on whether the rest of the criteria
	// happen to name something, or it would confirm what an empty answer hides.
	atAnchor, err := alarmAnchorFilter(ctx, criteria.Anchor)
	if err != nil {
		return nil, err
	}
	empty := &AlarmSearchResults{Results: make([]Alarm, 0), Pagination: rdb.SearchResultsPagination{}}
	filter, ok, err := alarmFacets{
		originatorType: criteria.OriginatorType, originatorToken: criteria.OriginatorToken, state: criteria.State,
		severity: criteria.Severity, acknowledged: criteria.Acknowledged, alarmKey: criteria.AlarmKey,
	}.resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	if !ok {
		return empty, nil
	}
	start, err := alarmRangeBound("startTime", criteria.StartTime)
	if err != nil {
		return nil, err
	}
	end, err := alarmRangeBound("endTime", criteria.EndTime)
	if err != nil {
		return nil, err
	}
	ofType, ok, err := api.alarmDeviceTypeFilter(ctx, criteria.DeviceType)
	if err != nil {
		return nil, err
	}
	if !ok {
		return empty, nil
	}

	results := make([]Alarm, 0)
	db, pag := api.RDB.ListOf(ctx, &Alarm{}, func(result *gorm.DB) *gorm.DB {
		result = atAnchor(ofType(filter(result)))
		// Overlap, not containment: an alarm raised before the range and still active (or cleared
		// inside it) was present during the range. A re-raise nulls cleared_time, so NULL means
		// active. No index is added for these: the caller that sends a range (a chart's event
		// markers) also sends one originator, and uix_alarm_originator_key narrows that to one
		// row per alarm key of the device before either bound is tested.
		if end != nil {
			result = result.Where("alarms.raised_time <= ?", *end)
		}
		if start != nil {
			result = result.Where("(alarms.cleared_time IS NULL OR alarms.cleared_time >= ?)", *start)
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

// alarmRangeBound parses one bound of an alarm search's time range: absent is no bound, and a
// value that is not RFC 3339 is an error rather than a silently unbounded search.
func alarmRangeBound(name string, value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 timestamp: %w", name, err)
	}
	t = t.UTC()
	return &t, nil
}

// AlarmCountsByDeviceType counts the alarms matching criteria per originating device type and
// severity, in one grouped read — the summary an alarm view grouped by type badges its tabs from,
// without reading the alarms themselves. Only alarms raised on a device the caller can read are
// counted, by the same device read the search's DeviceType filter uses (alarmDevices), so for every
// type the count equals the total of the search filtered to it. Ordered by type id, then severity.
func (api *Api) AlarmCountsByDeviceType(ctx context.Context, criteria AlarmCountCriteria) ([]AlarmDeviceTypeCount, error) {
	atAnchor, err := alarmAnchorFilter(ctx, criteria.Anchor)
	if err != nil {
		return nil, err
	}
	filter, ok, err := alarmFacets{
		originatorType: criteria.OriginatorType, originatorToken: criteria.OriginatorToken, state: criteria.State,
		severity: criteria.Severity, acknowledged: criteria.Acknowledged, alarmKey: criteria.AlarmKey,
	}.resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	out := make([]AlarmDeviceTypeCount, 0)
	if !ok {
		return out, nil
	}
	type grouped struct {
		DeviceTypeId uint
		Severity     string
		Count        int
	}
	var rows []grouped
	q := api.RDB.DB(ctx).Model(&Alarm{}).
		Select("d.device_type_id AS device_type_id, alarms.severity AS severity, COUNT(*) AS count").
		Joins("JOIN (?) AS d ON d.id = alarms.originator_id AND d.tenant_id = alarms.tenant_id",
			api.alarmDevices(ctx).Select("devices.id", "devices.device_type_id", "devices.tenant_id")).
		Where("alarms.originator_type = ?", string(entity.TypeDevice))
	if err := atAnchor(filter(q)).Group("d.device_type_id, alarms.severity").
		Order("d.device_type_id, alarms.severity").Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(rows))
	seen := map[uint]bool{}
	for _, r := range rows {
		if !seen[r.DeviceTypeId] {
			seen[r.DeviceTypeId] = true
			ids = append(ids, r.DeviceTypeId)
		}
	}
	types, err := api.DeviceTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}
	byId := make(map[uint]*DeviceType, len(types))
	for _, t := range types {
		byId[t.ID] = t
	}
	for _, r := range rows {
		// A device's type cannot be deleted while devices use it, so a missing type is a row
		// whose type vanished between the two reads; it is left out rather than shown nameless.
		if t, found := byId[r.DeviceTypeId]; found {
			out = append(out, AlarmDeviceTypeCount{DeviceType: t, Severity: r.Severity, Count: r.Count})
		}
	}
	return out, nil
}

// AlarmCountsBySeverity counts the alarms matching criteria per severity, in one grouped read.
// With no deviceType it counts EVERY alarm the search would return -- whatever its originator
// (a device, an asset, an area, a customer, a group) -- under the same tenant and scope rules, so
// the counts sum to the search's total. With a deviceType it counts only the alarms on devices of
// that type, by the search's own type facet (alarmDeviceTypeFilter), so they sum to the
// type-filtered search's total; an unknown type counts nothing. Severities with no alarm are
// absent. Ordered by severity.
func (api *Api) AlarmCountsBySeverity(ctx context.Context, criteria AlarmCountCriteria, deviceType *string) ([]AlarmSeverityCount, error) {
	atAnchor, err := alarmAnchorFilter(ctx, criteria.Anchor)
	if err != nil {
		return nil, err
	}
	out := make([]AlarmSeverityCount, 0)
	filter, ok, err := alarmFacets{
		originatorType: criteria.OriginatorType, originatorToken: criteria.OriginatorToken, state: criteria.State,
		severity: criteria.Severity, acknowledged: criteria.Acknowledged, alarmKey: criteria.AlarmKey,
	}.resolve(ctx, api)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	ofType, ok, err := api.alarmDeviceTypeFilter(ctx, deviceType)
	if err != nil {
		return nil, err
	}
	if !ok {
		return out, nil
	}
	var rows []AlarmSeverityCount
	q := api.RDB.DB(ctx).Model(&Alarm{}).Select("alarms.severity AS severity, COUNT(*) AS count")
	if err := atAnchor(ofType(filter(q))).Group("alarms.severity").Order("alarms.severity").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return append(out, rows...), nil
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

	// The request bound (MaxBulkAcknowledgeAlarms) is above the per-lookup key bound
	// (rdb.MaxLookupKeys), so the lookup is chunked to stay under the latter.
	byToken := make(map[string]*Alarm, len(ordered))
	for start := 0; start < len(ordered); start += rdb.MaxLookupKeys {
		end := min(start+rdb.MaxLookupKeys, len(ordered))
		matches, err := api.AlarmsByToken(ctx, ordered[start:end])
		if err != nil {
			return nil, err
		}
		for _, alarm := range matches {
			byToken[alarm.Token] = alarm
		}
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
