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
// (the event tables use (deviceToken, eventType, occurredTime) as their natural
// key rather than a surrogate id column).
func eventKey(deviceToken string, eventType int64, occurredTime time.Time) string {
	return fmt.Sprintf("%s:%d:%d", deviceToken, eventType, occurredTime.UnixNano())
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
	return gql.ID(eventKey(r.M.DeviceToken, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *EventResolver) DeviceToken() string {
	return r.M.DeviceToken
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

// Anchors resolves the event's anchor set (ADR-013/044): the tracked-relationship
// targets it is queryable by, each named by (type, token). Read from event_anchors
// by the event's natural key. Only resolved when the caller selects `anchors`, so a
// plain event list pays nothing.
func (r *EventResolver) Anchors() ([]*EventAnchorRefResolver, error) {
	api := r.S.GetApi(r.C)
	anchors, err := api.AnchorsForEvent(r.C, r.M.DeviceToken, r.M.EventType, r.M.OccurredTime)
	if err != nil {
		return nil, err
	}
	return mapResolvers(anchors, func(m model.EventAnchor) *EventAnchorRefResolver {
		return &EventAnchorRefResolver{M: m}
	}), nil
}

// EventAnchorRefResolver exposes one anchor as its (type, token) address.
type EventAnchorRefResolver struct {
	M model.EventAnchor
}

func (r *EventAnchorRefResolver) Type() string  { return r.M.AnchorType }
func (r *EventAnchorRefResolver) Token() string { return r.M.AnchorToken }

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
	return gql.ID(eventKey(r.M.DeviceToken, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *LocationEventResolver) DeviceToken() string {
	return r.M.DeviceToken
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
	return gql.ID(eventKey(r.M.DeviceToken, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *MeasurementEventResolver) DeviceToken() string {
	return r.M.DeviceToken
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

// Unit is the UCUM unit of the bound metric definition, denormalized at resolve
// time (ADR-016); nil for an undeclared measurement.
func (r *MeasurementEventResolver) Unit() *string {
	return r.M.Unit
}

// DataType is the bound metric definition's data type (DOUBLE/INT/BOOLEAN),
// denormalized at resolve time so a reader can interpret the stored numeric value
// (e.g. render a 0/1 BOOLEAN as false/true); nil for an undeclared measurement.
func (r *MeasurementEventResolver) DataType() *string {
	return r.M.DataType
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

// ---------------------------------
// Measurement bucket (aggregate) resolver
// ---------------------------------

type MeasurementBucketResolver struct {
	M model.MeasurementBucket
	S *SchemaResolver
	C context.Context
}

// BucketStart is the RFC3339 start of the time bucket; it is a grouping key and
// therefore always present.
func (r *MeasurementBucketResolver) BucketStart() string {
	if s := util.FormatTime(r.M.BucketStart); s != nil {
		return *s
	}
	return ""
}

func (r *MeasurementBucketResolver) Name() string {
	return r.M.Name
}

func (r *MeasurementBucketResolver) Avg() *float64 {
	return nullFloat(r.M.Avg)
}

func (r *MeasurementBucketResolver) Min() *float64 {
	return nullFloat(r.M.Min)
}

func (r *MeasurementBucketResolver) Max() *float64 {
	return nullFloat(r.M.Max)
}

func (r *MeasurementBucketResolver) Sum() *float64 {
	return nullFloat(r.M.Sum)
}

func (r *MeasurementBucketResolver) Count() int32 {
	return int32(r.M.Count)
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
	return gql.ID(eventKey(r.M.DeviceToken, int64(r.M.EventType), r.M.OccurredTime))
}

func (r *AlertEventResolver) DeviceToken() string {
	return r.M.DeviceToken
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
