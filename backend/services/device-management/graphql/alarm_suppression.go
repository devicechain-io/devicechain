// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type AlarmSuppressionWindowResolver struct {
	M model.AlarmSuppressionWindow
	S *SchemaResolver
}

func (r *AlarmSuppressionWindowResolver) Id() gql.ID           { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *AlarmSuppressionWindowResolver) Token() string        { return r.M.Token }
func (r *AlarmSuppressionWindowResolver) Name() *string        { return util.NullStr(r.M.Name) }
func (r *AlarmSuppressionWindowResolver) Description() *string { return util.NullStr(r.M.Description) }
func (r *AlarmSuppressionWindowResolver) Metadata() *string    { return util.MetadataStr(r.M.Metadata) }
func (r *AlarmSuppressionWindowResolver) CreatedAt() *string   { return util.FormatTime(r.M.CreatedAt) }
func (r *AlarmSuppressionWindowResolver) UpdatedAt() *string   { return util.FormatTime(r.M.UpdatedAt) }
func (r *AlarmSuppressionWindowResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}
func (r *AlarmSuppressionWindowResolver) Reason() string { return r.M.Reason }
func (r *AlarmSuppressionWindowResolver) Kind() model.AlarmSuppressionKind {
	return model.AlarmSuppressionKind(r.M.Kind)
}
func suppressionTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	// UTC, so a create response and a later read print one instant identically whatever
	// zone the database driver hands back.
	return util.FormatTime(t.UTC())
}
func (r *AlarmSuppressionWindowResolver) StartTime() *string  { return suppressionTime(r.M.StartTime) }
func (r *AlarmSuppressionWindowResolver) EndTime() *string    { return suppressionTime(r.M.EndTime) }
func (r *AlarmSuppressionWindowResolver) ValidFrom() *string  { return suppressionTime(r.M.ValidFrom) }
func (r *AlarmSuppressionWindowResolver) ValidUntil() *string { return suppressionTime(r.M.ValidUntil) }
func (r *AlarmSuppressionWindowResolver) EndedAt() *string    { return suppressionTime(r.M.EndedAt) }
func (r *AlarmSuppressionWindowResolver) EndedBy() *string    { return util.NullStr(r.M.EndedBy) }
func (r *AlarmSuppressionWindowResolver) CreatedBy() *string  { return util.NullStr(r.M.CreatedBy) }
func (r *AlarmSuppressionWindowResolver) Timezone() *string   { return util.NullStr(r.M.Timezone) }
func (r *AlarmSuppressionWindowResolver) BypassSeverity() *string {
	return util.NullStr(r.M.BypassSeverity)
}
func (r *AlarmSuppressionWindowResolver) Slots() *string {
	if r.M.Slots == nil {
		return nil
	}
	s := string(r.M.Slots)
	return &s
}
func (r *AlarmSuppressionWindowResolver) AlarmKeys() []string {
	if r.M.AlarmKeys == nil {
		return []string{}
	}
	return r.M.AlarmKeys
}
func (r *AlarmSuppressionWindowResolver) Targets() []*AlarmSuppressionTargetResolver {
	out := make([]*AlarmSuppressionTargetResolver, 0, len(r.M.Targets))
	for _, t := range r.M.Targets {
		out = append(out, &AlarmSuppressionTargetResolver{M: t})
	}
	return out
}
func (r *AlarmSuppressionWindowResolver) Status() string {
	return model.AlarmSuppressionWindowStatus(r.M, time.Now().UTC())
}
func (r *AlarmSuppressionWindowResolver) Openings(args struct{ Limit gql.NullInt }) ([]*AlarmSuppressionIntervalResolver, error) {
	limit := int32(5)
	if args.Limit.Value != nil {
		limit = *args.Limit.Value
	}
	if limit < 1 {
		return nil, fmt.Errorf("%w: openings limit must be positive", model.ErrInvalidAlarmSuppression)
	}
	intervals := model.AlarmSuppressionWindowOpenings(r.M, time.Now().UTC(), int(limit))
	out := make([]*AlarmSuppressionIntervalResolver, 0, len(intervals))
	for _, iv := range intervals {
		out = append(out, &AlarmSuppressionIntervalResolver{M: iv})
	}
	return out, nil
}

type AlarmSuppressionTargetResolver struct{ M model.AlarmSuppressionTarget }

func (r *AlarmSuppressionTargetResolver) Type() string { return r.M.Type }
func (r *AlarmSuppressionTargetResolver) Token() *string {
	if r.M.Token == "" {
		return nil
	}
	t := r.M.Token
	return &t
}

type AlarmSuppressionIntervalResolver struct{ M model.Interval }

func (r *AlarmSuppressionIntervalResolver) Start() string {
	return r.M.Start.UTC().Format(time.RFC3339Nano)
}
func (r *AlarmSuppressionIntervalResolver) End() string {
	return r.M.End.UTC().Format(time.RFC3339Nano)
}

type AlarmSuppressionResolver struct {
	M model.SuppressionMatch
	S *SchemaResolver
}

func (r *AlarmSuppressionResolver) WindowToken() string { return r.M.WindowToken }
func (r *AlarmSuppressionResolver) Until() string       { return r.M.Until.UTC().Format(time.RFC3339Nano) }
func (r *AlarmSuppressionResolver) Window(ctx context.Context) (*AlarmSuppressionWindowResolver, error) {
	if r.M.WindowToken == model.RestrictedSuppressionWindowToken {
		return nil, nil
	}
	w, err := r.S.GetApi(ctx).AlarmSuppressionWindowByToken(ctx, r.M.WindowToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowResolver{M: *w, S: r.S}, nil
}
func suppressionResolver(m *model.SuppressionMatch, s *SchemaResolver) *AlarmSuppressionResolver {
	if m == nil {
		return nil
	}
	return &AlarmSuppressionResolver{M: *m, S: s}
}

type AlarmSuppressionStatusResolver struct {
	M model.AlarmSuppressionStatusResult
	S *SchemaResolver
}

func (r *AlarmSuppressionStatusResolver) AlarmToken() string { return r.M.AlarmToken }
func (r *AlarmSuppressionStatusResolver) Severity() string   { return r.M.Severity }
func (r *AlarmSuppressionStatusResolver) State() string      { return r.M.State }
func (r *AlarmSuppressionStatusResolver) Suppression() *AlarmSuppressionResolver {
	return suppressionResolver(r.M.Match, r.S)
}

type AlarmSuppressionWindowSearchResultsResolver struct {
	M model.AlarmSuppressionWindowSearchResults
	S *SchemaResolver
}

func (r *AlarmSuppressionWindowSearchResultsResolver) Results() []*AlarmSuppressionWindowResolver {
	return r.S.suppressionWindows(r.M.Results)
}
func (r *AlarmSuppressionWindowSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination}
}
func (r *SchemaResolver) suppressionWindows(windows []model.AlarmSuppressionWindow) []*AlarmSuppressionWindowResolver {
	out := make([]*AlarmSuppressionWindowResolver, 0, len(windows))
	for _, w := range windows {
		out = append(out, &AlarmSuppressionWindowResolver{M: w, S: r})
	}
	return out
}
func (r *SchemaResolver) AlarmSuppressionWindows(ctx context.Context, args struct {
	Criteria model.AlarmSuppressionWindowSearchCriteria
}) (*AlarmSuppressionWindowSearchResultsResolver, error) {
	found, err := r.GetApi(ctx).AlarmSuppressionWindowsSearch(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowSearchResultsResolver{M: *found, S: r}, nil
}
func (r *SchemaResolver) AlarmSuppressionWindowsByToken(ctx context.Context, args struct{ Tokens []string }) ([]*AlarmSuppressionWindowResolver, error) {
	found, err := r.GetApi(ctx).AlarmSuppressionWindowsByTokens(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	return r.suppressionWindows(found), nil
}
func (r *SchemaResolver) AlarmSuppressionsForDevice(ctx context.Context, args struct {
	DeviceToken string
	At          *string
}) ([]*AlarmSuppressionWindowResolver, error) {
	at, err := model.ParseAlarmSuppressionTime("at", args.At)
	if err != nil {
		return nil, err
	}
	when := time.Time{}
	if at != nil {
		when = *at
	}
	found, err := r.GetApi(ctx).AlarmSuppressionsForDevice(ctx, args.DeviceToken, when)
	if err != nil {
		return nil, err
	}
	return r.suppressionWindows(found), nil
}
func (r *SchemaResolver) AlarmSuppressionStatus(ctx context.Context, args struct{ Tokens []string }) ([]*AlarmSuppressionStatusResolver, error) {
	found, err := r.GetApi(ctx).AlarmSuppressionStatus(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	out := make([]*AlarmSuppressionStatusResolver, 0, len(found))
	for _, s := range found {
		out = append(out, &AlarmSuppressionStatusResolver{M: s, S: r})
	}
	return out, nil
}

type AlarmSuppressionWindowCreateRequest struct {
	Token                                                      string
	Name, Description, Metadata                                *string
	Reason                                                     string
	Targets                                                    []model.AlarmSuppressionTargetInput
	AlarmKeys                                                  *[]string
	BypassSeverity                                             *string
	Kind                                                       model.AlarmSuppressionKind
	StartTime, EndTime, Timezone, Slots, ValidFrom, ValidUntil *string
}

func (r *SchemaResolver) CreateAlarmSuppressionWindow(ctx context.Context, args struct {
	Request AlarmSuppressionWindowCreateRequest
}) (*AlarmSuppressionWindowResolver, error) {
	input := args.Request
	w := model.AlarmSuppressionWindow{TokenReference: rdb.TokenReference{Token: input.Token},
		NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(input.Name), Description: rdb.NullStrOf(input.Description)},
		Reason:      input.Reason, Kind: string(input.Kind), BypassSeverity: rdb.NullStrOf(input.BypassSeverity), Timezone: rdb.NullStrOf(input.Timezone)}
	var err error
	w.Metadata, err = rdb.JSONInputOf("metadata", input.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", model.ErrInvalidAlarmSuppression, err)
	}
	for _, f := range []struct {
		name  string
		input *string
		dest  **time.Time
	}{
		{"startTime", input.StartTime, &w.StartTime}, {"endTime", input.EndTime, &w.EndTime},
		{"validFrom", input.ValidFrom, &w.ValidFrom}, {"validUntil", input.ValidUntil, &w.ValidUntil},
	} {
		*f.dest, err = model.ParseAlarmSuppressionTime(f.name, f.input)
		if err != nil {
			return nil, err
		}
	}
	for _, t := range input.Targets {
		w.Targets = append(w.Targets, t.Target())
	}
	if input.AlarmKeys != nil {
		w.AlarmKeys = *input.AlarmKeys
	}
	if input.Slots != nil {
		w.Slots = datatypes.JSON(*input.Slots)
	}
	// Reach and whole-window scope authorization belong to the model.
	created, err := r.GetApi(ctx).CreateAlarmSuppressionWindow(ctx, &w)
	if err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowResolver{M: *created, S: r}, nil
}
func (r *SchemaResolver) UpdateAlarmSuppressionWindow(ctx context.Context, args struct {
	Token   string
	Request model.AlarmSuppressionUpdateRequest
}) (*AlarmSuppressionWindowResolver, error) {
	w, err := r.GetApi(ctx).UpdateAlarmSuppressionWindow(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowResolver{M: *w, S: r}, nil
}
func (r *SchemaResolver) EndAlarmSuppressionWindow(ctx context.Context, args struct{ Token string }) (*AlarmSuppressionWindowResolver, error) {
	w, err := r.GetApi(ctx).EndAlarmSuppressionWindow(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AlarmSuppressionWindowResolver{M: *w, S: r}, nil
}
func (r *SchemaResolver) DeleteAlarmSuppressionWindow(ctx context.Context, args struct{ Token string }) (bool, error) {
	_, err := r.GetApi(ctx).DeleteAlarmSuppressionWindow(ctx, args.Token)
	return err == nil, err
}
func (r *AlarmResolver) Suppression(ctx context.Context) (*AlarmSuppressionResolver, error) {
	if r.M.State != string(model.AlarmStateActive) || r.M.OriginatorType != "device" {
		return nil, nil
	}
	page := r.suppressionPage
	if page == nil {
		page = &alarmSuppressionPage{alarms: []model.Alarm{r.M}}
	}
	page.loadMatches(ctx, r.S)
	match, err := page.matches[r.M.ID], page.matchErrors[r.M.ID]
	if err != nil {
		return nil, err
	}
	return suppressionResolver(match, r.S), nil
}

// History is lazy and batched for every alarm on the page, keyed by current cycle.
func (r *AlarmResolver) RaisedDuringSuppression(ctx context.Context) (*string, error) {
	page := r.suppressionPage
	if page == nil {
		page = &alarmSuppressionPage{alarms: []model.Alarm{r.M}}
	}
	page.historyOnce.Do(func() {
		api := r.S.GetApi(ctx)
		page.stamps, page.historyErr = api.AlarmCycleSuppressionsFor(ctx, page.alarms)
		if page.historyErr != nil {
			return
		}
		tokens := []string{}
		for _, token := range page.stamps {
			tokens = append(tokens, token)
		}
		visible, err := api.VisibleSuppressionWindowTokens(ctx, tokens)
		if err != nil {
			page.historyErr = err
			return
		}
		for id, token := range page.stamps {
			page.stamps[id] = visible[token]
		}
	})
	stamps, err := page.stamps, page.historyErr
	if err != nil {
		return nil, err
	}
	token := stamps[r.M.ID]
	if token == "" {
		return nil, nil
	}
	return &token, nil
}

// Like dispositionPageAlarms, a page owns lazy, concurrency-safe request reads.
type alarmSuppressionPage struct {
	alarms      []model.Alarm
	once        sync.Once
	matchesOnce sync.Once
	matches     map[uint]*model.SuppressionMatch
	matchErrors map[uint]error
	reader      *model.AlarmSuppressionReader
	historyOnce sync.Once
	stamps      map[uint]string
	historyErr  error
}

func (p *alarmSuppressionPage) init(ctx context.Context, s *SchemaResolver) {
	p.once.Do(func() { p.reader = s.GetApi(ctx).NewAlarmSuppressionReader(time.Time{}) })
}

func (p *alarmSuppressionPage) loadMatches(ctx context.Context, s *SchemaResolver) {
	p.init(ctx, s)
	p.matchesOnce.Do(func() {
		p.matches, p.matchErrors = map[uint]*model.SuppressionMatch{}, map[uint]error{}
		tokens := []string{}
		for i := range p.alarms {
			a := &p.alarms[i]
			match, err := p.reader.Match(ctx, a)
			p.matches[a.ID], p.matchErrors[a.ID] = match, err
			if match != nil {
				tokens = append(tokens, match.WindowToken)
			}
		}
		visible, err := s.GetApi(ctx).VisibleSuppressionWindowTokens(ctx, tokens)
		for id, match := range p.matches {
			if match != nil {
				if err != nil {
					p.matchErrors[id] = err
					continue
				}
				match.WindowToken = visible[match.WindowToken]
			}
		}
	})
}
