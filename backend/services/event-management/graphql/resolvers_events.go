// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// eventKey builds a stable synthetic id for an event from its composite key
// (the event tables use (deviceId, eventType, occurredTime) as their natural
// key rather than a surrogate id column).
func eventKey(deviceId uint, eventType int64, occurredTime time.Time) string {
	return fmt.Sprintf("%d:%d:%d", deviceId, eventType, occurredTime.UnixNano())
}

// Converts a *uint to a string pointer (graphql ids are strings).
func uintPtrStr(value *uint) *string {
	if value == nil {
		return nil
	}
	str := fmt.Sprint(*value)
	return &str
}

// Converts a sql.NullFloat64 to a float pointer.
func nullFloat(value sql.NullFloat64) *float64 {
	if value.Valid {
		return &value.Float64
	}
	return nil
}

// --------------
// Event resolver
// --------------

type EventResolver struct {
	M model.Event
	S *SchemaResolver
	C context.Context
}

func (r *EventResolver) Id() gql.ID {
	return gql.ID(eventKey(r.M.DeviceId, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *EventResolver) DeviceId() string {
	return fmt.Sprint(r.M.DeviceId)
}

func (r *EventResolver) EventType() int32 {
	return int32(r.M.EventType)
}

func (r *EventResolver) OccurredTime() *string {
	return util.FormatTime(r.M.OccurredTime)
}

func (r *EventResolver) ProcessedTime() *string {
	return util.FormatTime(r.M.ProcessedTime)
}

func (r *EventResolver) Source() string {
	return r.M.Source
}

func (r *EventResolver) AltId() *string {
	return util.NullStr(r.M.AltId)
}

func (r *EventResolver) RelDeviceId() *string {
	return uintPtrStr(r.M.RelDeviceId)
}

func (r *EventResolver) RelDeviceGroupId() *string {
	return uintPtrStr(r.M.RelDeviceGroupId)
}

func (r *EventResolver) RelCustomerId() *string {
	return uintPtrStr(r.M.RelCustomerId)
}

func (r *EventResolver) RelCustomerGroupId() *string {
	return uintPtrStr(r.M.RelCustomerGroupId)
}

func (r *EventResolver) RelAreaId() *string {
	return uintPtrStr(r.M.RelAreaId)
}

func (r *EventResolver) RelAreaGroupId() *string {
	return uintPtrStr(r.M.RelAreaGroupId)
}

func (r *EventResolver) RelAssetId() *string {
	return uintPtrStr(r.M.RelAssetId)
}

func (r *EventResolver) RelAssetGroupId() *string {
	return uintPtrStr(r.M.RelAssetGroupId)
}

// mapResolvers builds a resolver slice from a model slice, preserving the
// schema/context wiring shared by every typed result resolver.
func mapResolvers[M any, R any](src []M, wrap func(M) R) []R {
	out := make([]R, 0, len(src))
	for i := range src {
		out = append(out, wrap(src[i]))
	}
	return out
}

// -----------------------------
// Event search results resolver
// -----------------------------

type EventSearchResultsResolver struct {
	M model.EventSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EventSearchResultsResolver) Results() []*EventResolver {
	return mapResolvers(r.M.Results, func(m model.Event) *EventResolver {
		return &EventResolver{M: m, S: r.S, C: r.C}
	})
}

func (r *EventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// -----------------------
// Location event resolver
// -----------------------

type LocationEventResolver struct {
	M model.LocationEvent
	S *SchemaResolver
	C context.Context
}

func (r *LocationEventResolver) Id() gql.ID {
	return gql.ID(eventKey(r.M.DeviceId, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *LocationEventResolver) DeviceId() string {
	return fmt.Sprint(r.M.DeviceId)
}

func (r *LocationEventResolver) EventType() int32 {
	return int32(r.M.EventType)
}

func (r *LocationEventResolver) OccurredTime() *string {
	return util.FormatTime(r.M.OccurredTime)
}

func (r *LocationEventResolver) Latitude() *float64 {
	return nullFloat(r.M.Latitude)
}

func (r *LocationEventResolver) Longitude() *float64 {
	return nullFloat(r.M.Longitude)
}

func (r *LocationEventResolver) Elevation() *float64 {
	return nullFloat(r.M.Elevation)
}

type LocationEventSearchResultsResolver struct {
	M model.LocationEventSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *LocationEventSearchResultsResolver) Results() []*LocationEventResolver {
	return mapResolvers(r.M.Results, func(m model.LocationEvent) *LocationEventResolver {
		return &LocationEventResolver{M: m, S: r.S, C: r.C}
	})
}

func (r *LocationEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// --------------------------
// Measurement event resolver
// --------------------------

type MeasurementEventResolver struct {
	M model.MeasurementEvent
	S *SchemaResolver
	C context.Context
}

func (r *MeasurementEventResolver) Id() gql.ID {
	return gql.ID(eventKey(r.M.DeviceId, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *MeasurementEventResolver) DeviceId() string {
	return fmt.Sprint(r.M.DeviceId)
}

func (r *MeasurementEventResolver) EventType() int32 {
	return int32(r.M.EventType)
}

func (r *MeasurementEventResolver) OccurredTime() *string {
	return util.FormatTime(r.M.OccurredTime)
}

func (r *MeasurementEventResolver) Name() string {
	return r.M.Name
}

func (r *MeasurementEventResolver) Value() *float64 {
	return nullFloat(r.M.Value)
}

func (r *MeasurementEventResolver) Classifier() *string {
	return uintPtrStr(r.M.Classifier)
}

type MeasurementEventSearchResultsResolver struct {
	M model.MeasurementEventSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *MeasurementEventSearchResultsResolver) Results() []*MeasurementEventResolver {
	return mapResolvers(r.M.Results, func(m model.MeasurementEvent) *MeasurementEventResolver {
		return &MeasurementEventResolver{M: m, S: r.S, C: r.C}
	})
}

func (r *MeasurementEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// --------------------
// Alert event resolver
// --------------------

type AlertEventResolver struct {
	M model.AlertEvent
	S *SchemaResolver
	C context.Context
}

func (r *AlertEventResolver) Id() gql.ID {
	return gql.ID(eventKey(r.M.DeviceId, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *AlertEventResolver) DeviceId() string {
	return fmt.Sprint(r.M.DeviceId)
}

func (r *AlertEventResolver) EventType() int32 {
	return int32(r.M.EventType)
}

func (r *AlertEventResolver) OccurredTime() *string {
	return util.FormatTime(r.M.OccurredTime)
}

func (r *AlertEventResolver) Type() string {
	return r.M.Type
}

func (r *AlertEventResolver) Level() int32 {
	return int32(r.M.Level)
}

func (r *AlertEventResolver) Message() *string {
	msg := r.M.Message
	return &msg
}

func (r *AlertEventResolver) Source() *string {
	src := r.M.Source
	return &src
}

type AlertEventSearchResultsResolver struct {
	M model.AlertEventSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AlertEventSearchResultsResolver) Results() []*AlertEventResolver {
	return mapResolvers(r.M.Results, func(m model.AlertEvent) *AlertEventResolver {
		return &AlertEventResolver{M: m, S: r.S, C: r.C}
	})
}

func (r *AlertEventSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
