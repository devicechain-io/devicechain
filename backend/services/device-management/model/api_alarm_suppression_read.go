// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
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
	Severity   string
	State      string
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
	// Services need complete tenant status, including position-derived and
	// unclassified alarms. Human reads retain scope and position withholding.
	readCtx := ctx
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.TokenType == auth.TokenTypeService {
		if err := auth.AuthorizeService(ctx, auth.AlarmRead); err != nil {
			return nil, err
		}
		readCtx = auth.WithClaims(ctx, nil)
	}
	alarms := make([]*Alarm, 0, len(tokens))
	for start := 0; start < len(tokens); start += rdb.MaxLookupKeys {
		found, err := api.AlarmsByToken(readCtx, tokens[start:min(start+rdb.MaxLookupKeys, len(tokens))])
		if err != nil {
			return nil, err
		}
		alarms = append(alarms, found...)
	}
	reader := api.NewAlarmSuppressionReader(time.Time{})
	// Loading the snapshot is a batch failure; fact evaluation below is per alarm.
	for _, alarm := range alarms {
		if alarm.State == string(AlarmStateActive) && alarm.OriginatorType == string(entity.TypeDevice) {
			if err := reader.load(ctx); err != nil {
				return nil, err
			}
			break
		}
	}
	seen := map[uint]bool{}
	for _, alarm := range alarms {
		if seen[alarm.ID] {
			continue
		}
		seen[alarm.ID] = true
		var match *SuppressionMatch
		if alarm.State == string(AlarmStateActive) && alarm.OriginatorType == string(entity.TypeDevice) {
			var err error
			match, err = reader.Match(ctx, alarm)
			if err != nil {
				suppressionEvaluationFailed(err, "status")
				match = nil
			}
		}
		out = append(out, AlarmSuppressionStatusResult{AlarmToken: alarm.Token, Severity: alarm.Severity, State: alarm.State, Match: match})
	}
	tokensToShow := []string{}
	for _, row := range out {
		if row.Match != nil {
			tokensToShow = append(tokensToShow, row.Match.WindowToken)
		}
	}
	visibleTokens, err := api.VisibleSuppressionWindowTokens(ctx, tokensToShow)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Match != nil {
			out[i].Match.WindowToken = visibleTokens[out[i].Match.WindowToken]
		}
	}
	return out, nil
}

// AlarmSuppression evaluates an already-visible alarm row (zero time means now).
func (api *Api) AlarmSuppression(ctx context.Context, alarm *Alarm, at time.Time) (*SuppressionMatch, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	if alarm == nil {
		return nil, nil
	}
	match, err := api.NewAlarmSuppressionReader(at).Match(ctx, alarm)
	if err != nil || match == nil {
		return match, err
	}
	match.WindowToken, err = api.VisibleSuppressionWindowToken(ctx, match.WindowToken)
	return match, err
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

// Only caller-visible storage rows count toward this whole-window visibility scan.
// Tests may lower the bound; package tests using this seam must run serially.
var suppressionVisibleScanLimit = 10000

func (api *Api) visibleSuppressionWindows(ctx context.Context, db *gorm.DB, limit int) ([]AlarmSuppressionWindow, error) {
	var rows []AlarmSuppressionWindow
	if err := db.Order((AlarmSuppressionWindow{}).DefaultOrder()).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, suppressionInvalid("too many caller-visible suppression windows to filter (limit %d)", limit)
	}
	return api.filterSuppressionWindowsInScope(ctx, rows)
}

func suppressionTargetFilter(token *string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if token == nil {
			return db
		}
		if db.Dialector.Name() == "postgres" {
			target, _ := json.Marshal([]map[string]string{{"token": *token}})
			return db.Where("targets @> ?::jsonb", string(target))
		}
		// SQLite's JSON table function keeps history filtering and pagination in SQL too.
		return db.Where("EXISTS (SELECT 1 FROM json_each(targets) AS target WHERE json_extract(target.value, '$.token') = ?)", *token)
	}
}

func (api *Api) AlarmSuppressionWindowsSearch(ctx context.Context, criteria AlarmSuppressionWindowSearchCriteria) (*AlarmSuppressionWindowSearchResults, error) {
	scope, err := auth.AuthorizeScoped(ctx, auth.AlarmRead)
	if err != nil {
		return nil, err
	}
	if criteria.Status != nil && !slices.Contains([]string{"SCHEDULED", "ACTIVE", "ENDED"}, *criteria.Status) {
		return nil, suppressionInvalid("unknown suppression window status")
	}
	now := time.Now().UTC()
	base := api.RDB.DB(ctx).Scopes(suppressionTargetFilter(criteria.TargetToken))
	if criteria.Status != nil {
		// Only live candidates need schedule evaluation in Go (bounded by the 500-live cap).
		var live []AlarmSuppressionWindow
		if err := base.Session(&gorm.Session{}).Scopes(suppressionLiveAt(now)).Order((AlarmSuppressionWindow{}).DefaultOrder()).Find(&live).Error; err != nil {
			return nil, err
		}
		filtered := make([]AlarmSuppressionWindow, 0, len(live))
		endedIDs := []uint{}
		for _, row := range live {
			status := AlarmSuppressionWindowStatus(row, now)
			if status == "ENDED" {
				endedIDs = append(endedIDs, row.ID)
			}
			if status == *criteria.Status {
				filtered = append(filtered, row)
			}
		}
		if *criteria.Status != "ENDED" {
			if scope.IsScoped() {
				filtered, err = api.filterSuppressionWindowsInScope(ctx, filtered)
				if err != nil {
					return nil, err
				}
			}
			results, pag := rdb.PageSlice(filtered, criteria.Pagination)
			return &AlarmSuppressionWindowSearchResults{Results: results, Pagination: pag}, nil
		}
		// A recurrence can have no next opening before its validity bound. Include
		// those bounded live candidates as well as all expired history. Soft-deleted
		// windows remain excluded by GORM, as on every other window read.
		base = base.Where("(ended_at <= ? OR end_time <= ? OR valid_until <= ? OR id IN ?)", now, now, now, endedIDs)
	}
	if scope.IsScoped() {
		return api.scopedSuppressionWindowPage(ctx, base, criteria.Pagination)
	}
	query, pag := api.RDB.ListOfDB(base, &AlarmSuppressionWindow{}, nil, criteria.Pagination)
	rows := []AlarmSuppressionWindow{}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowSearchResults{Results: rows, Pagination: pag}, nil
}

// Scoped totals count whole-window-visible rows among the first 10,000
// storage-visible candidates. Pages continue beyond that bound without refusal.
func (api *Api) scopedSuppressionWindowPage(ctx context.Context, base *gorm.DB, pagination rdb.Pagination) (*AlarmSuppressionWindowSearchResults, error) {
	size := int64(pagination.EffectivePageSize())
	offset := max(int64(pagination.PageNumber)-1, 0) * size
	out := &AlarmSuppressionWindowSearchResults{Results: []AlarmSuppressionWindow{}}
	var seen, total int64
	for storageOffset := 0; ; {
		var rows []AlarmSuppressionWindow
		chunk := 500
		if storageOffset < suppressionVisibleScanLimit {
			chunk = min(chunk, suppressionVisibleScanLimit-storageOffset)
		}
		if err := base.Session(&gorm.Session{}).Order((AlarmSuppressionWindow{}).DefaultOrder()).Offset(storageOffset).Limit(chunk).Find(&rows).Error; err != nil {
			return nil, err
		}
		visible, err := api.filterSuppressionWindowsInScope(ctx, rows)
		if err != nil {
			return nil, err
		}
		for _, row := range visible {
			if storageOffset < suppressionVisibleScanLimit {
				total++
			}
			if seen >= offset && int64(len(out.Results)) < size {
				out.Results = append(out.Results, row)
			}
			seen++
		}
		storageOffset += len(rows)
		if len(rows) < chunk || (storageOffset >= suppressionVisibleScanLimit && seen >= offset+size) {
			break
		}
	}
	clamp := func(n int64) int32 { return int32(min(n, int64(1<<31-1))) }
	out.Pagination = rdb.SearchResultsPagination{TotalRecords: clamp(total), PageStart: clamp(offset + 1), PageEnd: clamp(min(offset+int64(len(out.Results)), seen))}
	return out, nil
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
	return api.visibleSuppressionWindows(ctx, api.RDB.DB(ctx).Where("token IN ?", tokens), 100)
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
	rows, err := api.visibleSuppressionWindows(ctx, api.RDB.DB(ctx).Scopes(suppressionLiveAt(at)).Where("start_time <= ? OR (kind = ? AND (valid_from IS NULL OR valid_from <= ?))", at, SuppressionRecurring, at), 500)
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
