/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package model

import (
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Criteria used when searching for events of any type. The same criteria is
// shared by the base-event and typed-event reads. Time range is applied to the
// occurred_time column; the optional relationship anchor maps an anchor type
// (e.g. "customer") to the corresponding Rel* column (e.g. rel_customer_id).
//
// Anchor filtering currently targets the discrete Rel* columns; when ADR-013
// lands and these collapse into a generic (entity_type, entity_id) anchor, the
// AnchorType/AnchorId pair already models that shape.
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

// anchorColumns maps an anchor type to the Rel* column it filters on. This is
// the single source of truth for the anchorType -> column mapping used by the
// read API.
var anchorColumns = map[string]string{
	"device":        "rel_device_id",
	"devicegroup":   "rel_device_group_id",
	"customer":      "rel_customer_id",
	"customergroup": "rel_customer_group_id",
	"area":          "rel_area_id",
	"areagroup":     "rel_area_group_id",
	"asset":         "rel_asset_id",
	"assetgroup":    "rel_asset_group_id",
}

// IsAnchorType reports whether t is a recognized relationship anchor type. The
// read path drops an unrecognized anchor (no column), so callers must reject an
// unknown type up front rather than silently returning unfiltered results.
func IsAnchorType(t string) bool {
	_, ok := anchorColumns[t]
	return ok
}
