// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Criteria used when searching for events of any type. The same criteria is
// shared by the base-event and typed-event reads. Time range is applied to the
// occurred_time column; the optional relationship anchor filters on the uniform
// (anchor_type, anchor_id) columns (ADR-013) — the type names an entity class
// (e.g. "customer") and the id is that entity's row id.
type EventSearchCriteria struct {
	rdb.Pagination
	DeviceId   *uint
	EventTypes []esmodel.EventType
	StartTime  *time.Time
	EndTime    *time.Time
	AnchorType *string
	AnchorId   *uint
}

// Search results returned from a base event query.
type EventSearchResults struct {
	Results    []Event
	Pagination rdb.SearchResultsPagination
}

// Search results returned from a location event query.
type LocationEventSearchResults struct {
	Results    []LocationEvent
	Pagination rdb.SearchResultsPagination
}

// Search results returned from a measurement event query.
type MeasurementEventSearchResults struct {
	Results    []MeasurementEvent
	Pagination rdb.SearchResultsPagination
}

// Search results returned from an alert event query.
type AlertEventSearchResults struct {
	Results    []AlertEvent
	Pagination rdb.SearchResultsPagination
}

// IsAnchorType reports whether t is a recognized entity (anchor) type. The read
// path drops an unrecognized anchor (no filter), so callers must reject an
// unknown type up front rather than silently returning unfiltered results.
func IsAnchorType(t string) bool {
	return entity.Type(t).Valid()
}
