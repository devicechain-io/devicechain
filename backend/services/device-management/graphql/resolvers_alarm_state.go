// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/entity"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// nullTimeStr formats a nullable timestamp as the platform's string time, or nil.
func nullTimeStr(v sql.NullTime) *string {
	if !v.Valid {
		return nil
	}
	return util.FormatTime(v.Time)
}

// nullFloat adapts a nullable float column to an optional GraphQL Float.
func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// -------------
// Alarm resolver
// -------------

type AlarmResolver struct {
	M model.Alarm
	S *SchemaResolver
	C context.Context

	// disposition is the current cycle's label when the resolver was built by a write that has
	// it in hand (setAlarmDisposition), so the field is answered without a read. dispositionKnown
	// says the value is authoritative; otherwise Disposition reads it.
	disposition      *model.AlarmDisposition
	dispositionKnown bool
}

func (r *AlarmResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AlarmResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *AlarmResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *AlarmResolver) DeletedAt() *string { return util.FormatTime(r.M.DeletedAt.Time) }

func (r *AlarmResolver) Token() string { return r.M.Token }

func (r *AlarmResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

func (r *AlarmResolver) OriginatorType() string { return r.M.OriginatorType }

func (r *AlarmResolver) OriginatorId() gql.ID { return gql.ID(fmt.Sprint(r.M.OriginatorId)) }

// OriginatorToken resolves the originator's token on demand — a device lookup by id,
// mirroring AlarmEvent.OriginatorToken so the query and the live stream expose the
// originator identically. Returns nil when the originator is not a device or no
// longer exists, and surfaces a lookup failure as a field error. Lazy by design: the
// lookup runs only when the client selects this field, keeping it off the list path
// when unused. It takes graphql-go's per-resolve context, so it inherits that call's
// timeout and stays tenant-scoped.
func (r *AlarmResolver) OriginatorToken(ctx context.Context) (*string, error) {
	if r.M.OriginatorType != string(entity.TypeDevice) {
		return nil, nil
	}
	devices, err := r.S.GetApi(ctx).DevicesById(ctx, []uint{r.M.OriginatorId})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, nil
	}
	t := devices[0].Token
	return &t, nil
}

func (r *AlarmResolver) AlarmKey() string { return r.M.AlarmKey }

func (r *AlarmResolver) MetricKey() string { return r.M.MetricKey }

func (r *AlarmResolver) State() string { return r.M.State }

func (r *AlarmResolver) Acknowledged() bool { return r.M.Acknowledged }

func (r *AlarmResolver) Severity() string { return r.M.Severity }

func (r *AlarmResolver) RaisedTime() *string { return util.FormatTime(r.M.RaisedTime) }

func (r *AlarmResolver) ClearedTime() *string { return nullTimeStr(r.M.ClearedTime) }

func (r *AlarmResolver) AcknowledgedTime() *string { return nullTimeStr(r.M.AcknowledgedTime) }

func (r *AlarmResolver) AcknowledgedBy() *string { return util.NullStr(r.M.AcknowledgedBy) }

func (r *AlarmResolver) LastValue() *float64 { return nullFloat(r.M.LastValue) }

// Disposition resolves the label of the alarm's current cycle, lazily: the read runs only when a
// client selects the field. A lookup failure is a field error, never a null that would read as
// "unlabelled".
func (r *AlarmResolver) Disposition(ctx context.Context) (*AlarmDispositionResolver, error) {
	label := r.disposition
	if !r.dispositionKnown {
		found, err := r.S.GetApi(ctx).AlarmDispositionOf(ctx, &r.M)
		if err != nil {
			return nil, err
		}
		label = found
	}
	if label == nil {
		return nil, nil
	}
	return &AlarmDispositionResolver{M: *label, S: r.S}, nil
}

// -------------------------
// Alarm disposition resolver
// -------------------------

type AlarmDispositionResolver struct {
	M model.AlarmDisposition
	S *SchemaResolver

	// page, when set, is the listing page this label came from: its alarms are read once for the
	// whole page rather than once per label. Nil for a label resolved on its own.
	page *dispositionPageAlarms
}

// dispositionPageAlarms reads the alarms of one listing page in ONE AlarmsById call, the first
// time any label on the page asks for its alarm, and shares the answer with the rest. The read is
// the caller's own, so an alarm the caller cannot see is absent and its labels answer null.
type dispositionPageAlarms struct {
	ids   []uint
	once  sync.Once
	byId  map[uint]*model.Alarm
	err   error
	reads int // AlarmsById calls made, for the test that pins one per page
}

func (p *dispositionPageAlarms) get(ctx context.Context, s *SchemaResolver, id uint) (*model.Alarm, error) {
	p.once.Do(func() {
		p.reads++
		found, err := s.GetApi(ctx).AlarmsById(ctx, p.ids)
		if err != nil {
			p.err = err
			return
		}
		p.byId = make(map[uint]*model.Alarm, len(found))
		for _, a := range found {
			p.byId[a.ID] = a
		}
	})
	if p.err != nil {
		return nil, p.err
	}
	return p.byId[id], nil
}

func (r *AlarmDispositionResolver) AlarmId() gql.ID { return gql.ID(fmt.Sprint(r.M.AlarmId)) }

// Alarm resolves the labelled alarm by id, under the caller's own read: null when the caller
// cannot see it.
func (r *AlarmDispositionResolver) Alarm(ctx context.Context) (*AlarmResolver, error) {
	page := r.page
	if page == nil {
		page = &dispositionPageAlarms{ids: []uint{r.M.AlarmId}}
	}
	alarm, err := page.get(ctx, r.S, r.M.AlarmId)
	if err != nil || alarm == nil {
		return nil, err
	}
	return &AlarmResolver{M: *alarm, S: r.S, C: ctx}, nil
}

func (r *AlarmDispositionResolver) RaisedTime() string {
	return r.M.RaisedTime.Format(time.RFC3339Nano)
}

// Label is the verdict, served as the AlarmDispositionLabel enum (graphql-go serves an enum from a
// string). Only vocabulary values are ever written, so a stored value is always a member.
func (r *AlarmDispositionResolver) Label() string { return r.M.Disposition }

func (r *AlarmDispositionResolver) AlarmToken() string { return r.M.AlarmToken }

func (r *AlarmDispositionResolver) Note() *string { return util.NullStr(r.M.Note) }

func (r *AlarmDispositionResolver) RecordedBy() *string { return util.NullStr(r.M.RecordedBy) }

func (r *AlarmDispositionResolver) RecordedTime() string {
	return r.M.RecordedTime.Format(time.RFC3339Nano)
}

type AlarmDispositionSearchResultsResolver struct {
	M model.AlarmDispositionSearchResults
	S *SchemaResolver
	C context.Context

	pageOnce sync.Once
	page     *dispositionPageAlarms
}

func (r *AlarmDispositionSearchResultsResolver) Results() []*AlarmDispositionResolver {
	r.pageOnce.Do(func() {
		ids := make([]uint, 0, len(r.M.Results))
		seen := make(map[uint]bool, len(r.M.Results))
		for _, current := range r.M.Results {
			if !seen[current.AlarmId] {
				seen[current.AlarmId] = true
				ids = append(ids, current.AlarmId)
			}
		}
		r.page = &dispositionPageAlarms{ids: ids}
	})
	resolvers := make([]*AlarmDispositionResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &AlarmDispositionResolver{M: current, S: r.S, page: r.page})
	}
	return resolvers
}

func (r *AlarmDispositionSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// ---------------------------
// Alarm search results resolver
// ---------------------------

type AlarmSearchResultsResolver struct {
	M model.AlarmSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AlarmSearchResultsResolver) Results() []*AlarmResolver {
	resolvers := make([]*AlarmResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &AlarmResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *AlarmSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
