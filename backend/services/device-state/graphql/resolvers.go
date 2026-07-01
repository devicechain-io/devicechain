// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-state/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------
// Device state resolver
// --------------------

type DeviceStateResolver struct {
	M model.DeviceState
	S *SchemaResolver
	C context.Context
}

func (r *DeviceStateResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceStateResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceStateResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceStateResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceStateResolver) DeviceId() int32 {
	return int32(r.M.DeviceId)
}

func (r *DeviceStateResolver) Active() bool {
	return r.M.Active
}

func (r *DeviceStateResolver) LastConnectTime() *string {
	if r.M.LastConnectTime.Valid {
		return util.FormatTime(r.M.LastConnectTime.Time)
	}
	return nil
}

func (r *DeviceStateResolver) LastDisconnectTime() *string {
	if r.M.LastDisconnectTime.Valid {
		return util.FormatTime(r.M.LastDisconnectTime.Time)
	}
	return nil
}

func (r *DeviceStateResolver) LastActivityTime() *string {
	if r.M.LastActivityTime.Valid {
		return util.FormatTime(r.M.LastActivityTime.Time)
	}
	return nil
}

func (r *DeviceStateResolver) InactivityAlarmTime() *string {
	if r.M.InactivityAlarmTime.Valid {
		return util.FormatTime(r.M.InactivityAlarmTime.Time)
	}
	return nil
}

func (r *DeviceStateResolver) InactivityTimeout() int32 {
	return int32(r.M.InactivityTimeout)
}

// --------------------------
// Latest measurement resolver
// --------------------------

type LatestMeasurementResolver struct {
	M model.LatestMeasurement
	S *SchemaResolver
	C context.Context
}

func (r *LatestMeasurementResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *LatestMeasurementResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *LatestMeasurementResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *LatestMeasurementResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *LatestMeasurementResolver) DeviceId() int32 {
	return int32(r.M.DeviceId)
}

func (r *LatestMeasurementResolver) Name() string {
	return r.M.Name
}

func (r *LatestMeasurementResolver) Value() *float64 {
	if r.M.Value.Valid {
		return &r.M.Value.Float64
	}
	return nil
}

func (r *LatestMeasurementResolver) Classifier() *int32 {
	if r.M.Classifier != nil {
		v := int32(*r.M.Classifier)
		return &v
	}
	return nil
}

// OccurredTime is the reading's timestamp; it is a required field and always set.
func (r *LatestMeasurementResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

// ------------------------------------
// Device state search results resolver
// ------------------------------------

type DeviceStateSearchResultsResolver struct {
	M model.DeviceStateSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceStateSearchResultsResolver) Results() []*DeviceStateResolver {
	resolvers := make([]*DeviceStateResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceStateResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceStateSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
