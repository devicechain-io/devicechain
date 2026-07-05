// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"

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

func (r *AlarmResolver) Message() *string { return util.NullStr(r.M.Message) }

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
