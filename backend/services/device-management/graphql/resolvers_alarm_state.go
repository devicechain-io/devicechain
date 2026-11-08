// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
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
}

func (r *AlarmDispositionResolver) AlarmId() gql.ID { return gql.ID(fmt.Sprint(r.M.AlarmId)) }

// Alarm resolves the labelled alarm by id, under the caller's own read: null when the caller
// cannot see it.
func (r *AlarmDispositionResolver) Alarm(ctx context.Context) (*AlarmResolver, error) {
	found, err := r.S.GetApi(ctx).AlarmsById(ctx, []uint{r.M.AlarmId})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &AlarmResolver{M: *found[0], S: r.S, C: ctx}, nil
}

func (r *AlarmDispositionResolver) RaisedTime() string {
	return r.M.RaisedTime.Format(time.RFC3339Nano)
}

func (r *AlarmDispositionResolver) Disposition() string { return r.M.Disposition }

func (r *AlarmDispositionResolver) Note() *string { return util.NullStr(r.M.Note) }

func (r *AlarmDispositionResolver) RecordedBy() *string { return util.NullStr(r.M.RecordedBy) }

func (r *AlarmDispositionResolver) RecordedTime() string {
	return r.M.RecordedTime.Format(time.RFC3339Nano)
}

type AlarmDispositionSearchResultsResolver struct {
	M model.AlarmDispositionSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AlarmDispositionSearchResultsResolver) Results() []*AlarmDispositionResolver {
	resolvers := make([]*AlarmDispositionResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &AlarmDispositionResolver{M: current, S: r.S})
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
