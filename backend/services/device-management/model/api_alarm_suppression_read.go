// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlarmCycleSuppressionsFor reads only the supplied current cycles, in one query.
// Tenant, scope and position withholding apply to the history model itself.
func (api *Api) AlarmCycleSuppressionsFor(ctx context.Context, alarms []Alarm) (map[uint]string, error) {
	out := make(map[uint]string)
	if len(alarms) == 0 {
		return out, nil
	}
	pairs := make([][]any, 0, len(alarms))
	for _, a := range alarms {
		pairs = append(pairs, []any{a.ID, a.RaisedTime})
	}
	var rows []AlarmCycleSuppression
	if err := api.RDB.DB(ctx).Where("(alarm_id, raised_time) IN ?", pairs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.AlarmId] = row.WindowToken
	}
	return out, nil
}

func alarmCycleSuppressionFilter(db *gorm.DB, stamped bool) *gorm.DB {
	predicate := "EXISTS (SELECT 1 FROM ? AS cycle_suppression WHERE cycle_suppression.tenant_id = ? AND cycle_suppression.alarm_id = ? AND cycle_suppression.raised_time = ?)"
	if !stamped {
		predicate = "NOT " + predicate
	}
	return db.Where(clause.Expr{SQL: predicate, Vars: []any{
		clause.Table{Name: db.NamingStrategy.TableName("AlarmCycleSuppression")},
		clause.Column{Table: clause.CurrentTable, Name: "tenant_id"},
		clause.Column{Table: clause.CurrentTable, Name: "id"},
		clause.Column{Table: clause.CurrentTable, Name: "raised_time"},
	}})
}

type AlarmSuppressionStatusResult struct {
	AlarmToken string
	Match      *SuppressionMatch
}

// AlarmSuppressionStatus captures one wall time and one compiled window snapshot.
// Public alarm reads establish visibility before the internal fact reads remove claims.
func (api *Api) AlarmSuppressionStatus(ctx context.Context, tokens []string) ([]AlarmSuppressionStatusResult, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if len(tokens) > 200 {
		return nil, suppressionInvalid("at most 200 alarm tokens are allowed")
	}
	out := make([]AlarmSuppressionStatusResult, 0)
	if len(tokens) == 0 {
		return out, nil
	}
	alarms := make([]*Alarm, 0, len(tokens))
	for start := 0; start < len(tokens); start += rdb.MaxLookupKeys {
		found, err := api.AlarmsByToken(ctx, tokens[start:min(start+rdb.MaxLookupKeys, len(tokens))])
		if err != nil {
			return nil, err
		}
		alarms = append(alarms, found...)
	}
	now := time.Now().UTC()
	var windows []compiledWindow
	loaded := false
	seen := map[uint]bool{}
	for _, alarm := range alarms {
		if seen[alarm.ID] {
			continue
		}
		seen[alarm.ID] = true
		var match *SuppressionMatch
		if alarm.State == string(AlarmStateActive) && alarm.OriginatorType == string(entity.TypeDevice) {
			if !loaded {
				var err error
				windows, err = api.suppressionWindows(ctx)
				if err != nil {
					return nil, err
				}
				loaded = true
			}
			var err error
			match, err = api.suppressionForWindows(ctx, alarm.OriginatorId, alarm.AlarmKey, alarm.Severity, now, windows)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, AlarmSuppressionStatusResult{AlarmToken: alarm.Token, Match: match})
	}
	return out, nil
}

// AlarmSuppression evaluates a visible alarm at the requested time (zero means now).
func (api *Api) AlarmSuppression(ctx context.Context, alarm *Alarm, at time.Time) (*SuppressionMatch, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if alarm == nil {
		return nil, nil
	}
	visible, err := api.AlarmsByToken(ctx, []string{alarm.Token})
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, nil
	}
	alarm = visible[0]
	if alarm.State != string(AlarmStateActive) || alarm.OriginatorType != string(entity.TypeDevice) {
		return nil, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return api.SuppressionFor(ctx, alarm.OriginatorId, alarm.AlarmKey, alarm.Severity, at)
}

type AlarmSuppressionWindowSearchCriteria struct {
	rdb.Pagination
	Status      *string
	TargetToken *string
}

type AlarmSuppressionWindowSearchResults struct {
	Results    []AlarmSuppressionWindow
	Pagination rdb.SearchResultsPagination
}

// boundedSuppressionWindows refuses oversized tenants before filtering or pagination.
func (api *Api) boundedSuppressionWindows(ctx context.Context, tokens []string) ([]AlarmSuppressionWindow, error) {
	var count int64
	if tokens == nil {
		if err := api.RDB.DB(suppressionFactContext(ctx)).Model(&AlarmSuppressionWindow{}).Count(&count).Error; err != nil {
			return nil, err
		}
		if count > 10000 {
			return nil, suppressionInvalid("more than 10000 suppression windows cannot be filtered")
		}
	}
	db := api.RDB.DB(ctx).Order((AlarmSuppressionWindow{}).DefaultOrder())
	if tokens != nil {
		db = db.Where("token IN ?", tokens)
	}
	var rows []AlarmSuppressionWindow
	if err := db.Limit(10001).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, suppressionInvalid("more than 10000 suppression windows cannot be filtered")
	}
	visible := make([]AlarmSuppressionWindow, 0, len(rows))
	for _, row := range rows {
		ok, err := api.suppressionTargetsInScope(ctx, &row)
		if err != nil {
			return nil, err
		}
		if ok {
			visible = append(visible, row)
		}
	}
	return visible, nil
}

func (api *Api) AlarmSuppressionWindowsSearch(ctx context.Context, criteria AlarmSuppressionWindowSearchCriteria) (*AlarmSuppressionWindowSearchResults, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if criteria.Status != nil && !slices.Contains([]string{"SCHEDULED", "ACTIVE", "ENDED"}, *criteria.Status) {
		return nil, suppressionInvalid("unknown suppression window status")
	}
	rows, err := api.boundedSuppressionWindows(ctx, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	filtered := make([]AlarmSuppressionWindow, 0, len(rows))
	for _, row := range rows {
		if criteria.Status != nil && AlarmSuppressionWindowStatus(row, now) != *criteria.Status {
			continue
		}
		if criteria.TargetToken != nil {
			found := false
			for _, target := range row.Targets {
				found = found || target.Token == *criteria.TargetToken
			}
			if !found {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	size := int64(criteria.EffectivePageSize())
	start := max(int64(0), (int64(criteria.PageNumber)-1)*size)
	end := min(start+size, int64(len(filtered)))
	pag := rdb.SearchResultsPagination{TotalRecords: int32(len(filtered)), PageStart: int32(min(start+1, int64(1<<31-1))), PageEnd: int32(min((max(int64(criteria.PageNumber), 1))*size, int64(len(filtered))))}
	results := []AlarmSuppressionWindow{}
	if start < int64(len(filtered)) {
		results = filtered[start:end]
		pag.PageStart, pag.PageEnd = int32(start+1), int32(end)
	}
	return &AlarmSuppressionWindowSearchResults{Results: results, Pagination: pag}, nil
}

func (api *Api) AlarmSuppressionWindowsByTokens(ctx context.Context, tokens []string) ([]AlarmSuppressionWindow, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if len(tokens) > 100 {
		return nil, suppressionInvalid("at most 100 window tokens are allowed")
	}
	if len(tokens) == 0 {
		return []AlarmSuppressionWindow{}, nil
	}
	return api.boundedSuppressionWindows(ctx, tokens)
}

type Interval = maintwindow.Interval

// AlarmSuppressionWindowOpenings returns bounded, nonempty intervals. Omitting
// the limit defaults to five; an explicit limit below one is refused.
func AlarmSuppressionWindowOpenings(w AlarmSuppressionWindow, from time.Time, limits ...int) []Interval {
	limit := 5
	if len(limits) > 0 {
		limit = limits[0]
	}
	out := []Interval{}
	if limit < 1 || w.DeletedAt.Valid {
		return out
	}
	limit = min(limit, 20)
	if err := ValidateAlarmSuppression(&w); err != nil {
		return out
	}
	if from.IsZero() {
		from = time.Now().UTC()
	}
	if w.Kind == SuppressionOneShot {
		end := *w.EndTime
		if w.EndedAt != nil && w.EndedAt.Before(end) {
			end = *w.EndedAt
		}
		if end.After(from) && end.After(*w.StartTime) {
			out = append(out, Interval{Start: w.StartTime.UTC(), End: end.UTC()})
		}
		return out
	}
	schedule, err := suppressionSchedule(&w)
	if err != nil {
		return out
	}
	cursor := from
	if w.ValidFrom != nil && cursor.Before(*w.ValidFrom) {
		cursor = *w.ValidFrom
	}
	for len(out) < limit {
		if (w.ValidUntil != nil && !cursor.Before(*w.ValidUntil)) || (w.EndedAt != nil && !cursor.Before(*w.EndedAt)) {
			break
		}
		iv := schedule.Next(cursor)
		if w.ValidFrom != nil && iv.Start.Before(*w.ValidFrom) {
			iv.Start = *w.ValidFrom
		}
		if w.ValidUntil != nil && iv.End.After(*w.ValidUntil) {
			iv.End = *w.ValidUntil
		}
		if w.EndedAt != nil && iv.End.After(*w.EndedAt) {
			iv.End = *w.EndedAt
		}
		if !iv.End.After(iv.Start) || !iv.End.After(cursor) {
			break
		}
		out = append(out, Interval{Start: iv.Start.UTC(), End: iv.End.UTC()})
		cursor = iv.End
	}
	return out
}

func AlarmSuppressionWindowStatus(w AlarmSuppressionWindow, now time.Time) string {
	openings := AlarmSuppressionWindowOpenings(w, now, 1)
	if len(openings) == 0 {
		return "ENDED"
	}
	if openings[0].Start.After(now) {
		return "SCHEDULED"
	}
	return "ACTIVE"
}

// AlarmSuppressionsForDevice is a preview of visible applicable windows. It
// ignores alarm keys and bypass severity; it is not an alarm-specific decision.
func (api *Api) AlarmSuppressionsForDevice(ctx context.Context, deviceToken string, at time.Time) ([]AlarmSuppressionWindow, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	rows, err := api.boundedSuppressionWindows(ctx, nil)
	if err != nil {
		return nil, err
	}
	compiled := compileSuppressionWindows(rows)
	out := []AlarmSuppressionWindow{}
	if len(compiled) == 0 {
		return out, nil
	}
	facts, err := api.suppressionDeviceFacts(suppressionFactContext(ctx), devices[0], compiled)
	if err != nil {
		return nil, err
	}
	for _, c := range compiled {
		preview := c
		preview.window.AlarmKeys, preview.window.BypassSeverity.Valid = nil, false
		if matchWindows([]compiledWindow{preview}, facts, "", string(AlarmSeverityMinor), at) != nil {
			out = append(out, c.window)
		}
	}
	return out, nil
}
