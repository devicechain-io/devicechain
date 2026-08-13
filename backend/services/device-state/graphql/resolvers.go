// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

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

func (r *DeviceStateResolver) DeviceToken() string {
	return r.M.DeviceToken
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

// ExternalId is the device's transport-native identity, denormalized (ADR-049); nil
// when the device has no external id.
func (r *DeviceStateResolver) ExternalId() *string {
	if r.M.ExternalId == "" {
		return nil
	}
	return &r.M.ExternalId
}

// Source is the event source that last drove this device. Null rather than empty when
// unset, matching ExternalId: both are denormalized from events, so a device that has not
// yet produced one has no answer rather than a blank one.
func (r *DeviceStateResolver) Source() *string {
	if r.M.Source == "" {
		return nil
	}
	return &r.M.Source
}

// PresenceSource is INFERRED or ASSERTED (ADR-067).
func (r *DeviceStateResolver) PresenceSource() string {
	return r.M.PresenceSource
}

// SessionId is the last-applied presence transition's ordering epoch, returned as a
// String because it is a UnixNano value that overflows a 32-bit GraphQL Int.
func (r *DeviceStateResolver) SessionId() string {
	return strconv.FormatUint(r.M.SessionId, 10)
}

// PresenceTime is the last-applied presence transition's time; nil until the first.
func (r *DeviceStateResolver) PresenceTime() *string {
	if r.M.PresenceTime.Valid {
		return util.FormatTime(r.M.PresenceTime.Time)
	}
	return nil
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

func (r *LatestMeasurementResolver) DeviceToken() string {
	return r.M.DeviceToken
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

// Unit is the bound metric definition's unit, denormalized (ADR-016); nil for an
// undeclared measurement.
func (r *LatestMeasurementResolver) Unit() *string {
	return r.M.Unit
}

// DataType is the bound metric definition's data type, denormalized so a reader can
// interpret the value (e.g. render a 0/1 BOOLEAN as false/true); nil when unbound.
func (r *LatestMeasurementResolver) DataType() *string {
	return r.M.DataType
}

// OccurredTime is the reading's timestamp; it is a required field and always set.
func (r *LatestMeasurementResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

// -----------------------
// Latest location resolver
// -----------------------

type LatestLocationResolver struct {
	M model.LatestLocation
	S *SchemaResolver
	C context.Context
}

func (r *LatestLocationResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *LatestLocationResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *LatestLocationResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *LatestLocationResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *LatestLocationResolver) DeviceToken() string {
	return r.M.DeviceToken
}

// The coordinates are nullable end to end: a fix reports only what its sensor
// produced, and null must stay null rather than surfacing as 0 — the equator, sea
// level, stationary and due north are all real values a consumer would believe.
func (r *LatestLocationResolver) Latitude() *float64  { return nullFloat(r.M.Latitude) }
func (r *LatestLocationResolver) Longitude() *float64 { return nullFloat(r.M.Longitude) }
func (r *LatestLocationResolver) Elevation() *float64 { return nullFloat(r.M.Elevation) }
func (r *LatestLocationResolver) Accuracy() *float64  { return nullFloat(r.M.Accuracy) }
func (r *LatestLocationResolver) Speed() *float64     { return nullFloat(r.M.Speed) }
func (r *LatestLocationResolver) Heading() *float64   { return nullFloat(r.M.Heading) }

// OccurredTime is the fix's timestamp; it is a required field and always set.
func (r *LatestLocationResolver) OccurredTime() string {
	if s := util.FormatTime(r.M.OccurredTime); s != nil {
		return *s
	}
	return ""
}

// nullFloat maps a nullable column to a nullable GraphQL Float. It returns a pointer
// into a COPY, never into r.M — taking the address of a struct field here would let a
// consumer's write reach the resolver's model.
func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
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
