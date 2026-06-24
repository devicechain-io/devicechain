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
	"context"
	"strings"

	"gorm.io/gorm"
)

// commonEventFilters applies the device, event-type and occurred-time-range
// filters that are shared by every event read. The anchor filter is NOT applied
// here because the typed tables do not carry the Rel* columns; the base-event
// read adds the anchor clause directly.
func commonEventFilters(criteria EventSearchCriteria) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if criteria.DeviceId != nil {
			db = db.Where("device_id = ?", *criteria.DeviceId)
		}
		if len(criteria.EventTypes) > 0 {
			db = db.Where("event_type IN ?", criteria.EventTypes)
		}
		if criteria.StartTime != nil {
			db = db.Where("occurred_time >= ?", *criteria.StartTime)
		}
		if criteria.EndTime != nil {
			db = db.Where("occurred_time <= ?", *criteria.EndTime)
		}
		return db
	}
}

// anchorColumnFor resolves the Rel* column for the given anchor type, or "" if
// the anchor type is unknown.
func anchorColumnFor(anchorType *string) string {
	if anchorType == nil {
		return ""
	}
	return anchorColumns[strings.ToLower(strings.TrimSpace(*anchorType))]
}

// Search for base events that meet criteria. The base Event table carries the
// Rel* anchor columns, so the anchor filter is applied directly here.
func (api *Api) Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error) {
	results := make([]Event, 0)
	db, pag := api.RDB.ListOf(ctx, &Event{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		if col := anchorColumnFor(criteria.AnchorType); col != "" && criteria.AnchorId != nil {
			result = result.Where(col+" = ?", *criteria.AnchorId)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EventSearchResults{Results: results, Pagination: pag}, nil
}

// typedAnchorFilter applies an anchor filter to a typed-event query by
// restricting to the (device_id, event_type, occurred_time) keys whose base
// event row matches the anchor. The subquery runs through DB(ctx) so it is
// tenant-scoped as well.
func (api *Api) typedAnchorFilter(ctx context.Context, criteria EventSearchCriteria, result *gorm.DB) *gorm.DB {
	if col := anchorColumnFor(criteria.AnchorType); col != "" && criteria.AnchorId != nil {
		sub := api.RDB.DB(ctx).Model(&Event{}).
			Select("device_id, event_type, occurred_time").
			Where(col+" = ?", *criteria.AnchorId)
		result = result.Where("(device_id, event_type, occurred_time) IN (?)", sub)
	}
	return result
}

// Search for location events that meet criteria.
func (api *Api) LocationEvents(ctx context.Context, criteria EventSearchCriteria) (*LocationEventSearchResults, error) {
	results := make([]LocationEvent, 0)
	db, pag := api.RDB.ListOf(ctx, &LocationEvent{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		return api.typedAnchorFilter(ctx, criteria, result)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &LocationEventSearchResults{Results: results, Pagination: pag}, nil
}

// Search for measurement events that meet criteria.
func (api *Api) MeasurementEvents(ctx context.Context, criteria EventSearchCriteria) (*MeasurementEventSearchResults, error) {
	results := make([]MeasurementEvent, 0)
	db, pag := api.RDB.ListOf(ctx, &MeasurementEvent{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		return api.typedAnchorFilter(ctx, criteria, result)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &MeasurementEventSearchResults{Results: results, Pagination: pag}, nil
}

// Search for alert events that meet criteria.
func (api *Api) AlertEvents(ctx context.Context, criteria EventSearchCriteria) (*AlertEventSearchResults, error) {
	results := make([]AlertEvent, 0)
	db, pag := api.RDB.ListOf(ctx, &AlertEvent{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		return api.typedAnchorFilter(ctx, criteria, result)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AlertEventSearchResults{Results: results, Pagination: pag}, nil
}
