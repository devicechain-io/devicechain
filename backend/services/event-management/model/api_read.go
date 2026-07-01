// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"gorm.io/gorm"
)

// commonEventFilters applies the device, event-type and occurred-time-range
// filters that are shared by every event read. The anchor filter is NOT applied
// here because the typed tables do not carry the anchor columns; the base-event
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

// hasAnchor reports whether the criteria carries a usable (anchor_type, anchor_id)
// pair to filter on.
func hasAnchor(criteria EventSearchCriteria) bool {
	return criteria.AnchorType != nil && criteria.AnchorId != nil
}

// Search for base events that meet criteria. The base Event table carries the
// uniform (anchor_type, anchor_id) columns, so the anchor filter is applied
// directly here.
func (api *Api) Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error) {
	results := make([]Event, 0)
	db, pag := api.RDB.ListOf(ctx, &Event{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		if hasAnchor(criteria) {
			result = result.Where("anchor_type = ? AND anchor_id = ?", *criteria.AnchorType, *criteria.AnchorId)
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
	if hasAnchor(criteria) {
		sub := api.RDB.DB(ctx).Model(&Event{}).
			Select("device_id, event_type, occurred_time").
			Where("anchor_type = ? AND anchor_id = ?", *criteria.AnchorType, *criteria.AnchorId)
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

// BucketedMeasurements returns measurement values aggregated into fixed-width
// time_bucket intervals (TimescaleDB), grouped by measurement name. Every
// standard aggregate (avg/min/max/sum/count) is computed per bucket so a single
// query can drive a chart under any aggregation without a refetch.
//
// The query runs through DB(ctx) with the MeasurementEvent model, so the
// fail-closed tenant-scope callback injects the tenant predicate (ADR-015) just
// as it does for the paginated reads. The optional anchor filter reuses the same
// tenant-scoped (device_id, event_type, occurred_time) subquery as the typed
// reads.
func (api *Api) BucketedMeasurements(ctx context.Context, criteria MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	results := make([]MeasurementBucket, 0)
	db := api.RDB.DB(ctx).Model(&MeasurementEvent{}).
		Select("time_bucket(make_interval(secs => ?), occurred_time) AS bucket_start, "+
			"name, "+
			"avg(value) AS avg, min(value) AS min, max(value) AS max, "+
			"sum(value) AS sum, count(value) AS count", criteria.IntervalSeconds)
	if criteria.DeviceId != nil {
		db = db.Where("device_id = ?", *criteria.DeviceId)
	}
	if len(criteria.EventTypes) > 0 {
		db = db.Where("event_type IN ?", criteria.EventTypes)
	}
	if criteria.Name != nil {
		db = db.Where("name = ?", *criteria.Name)
	}
	if criteria.StartTime != nil {
		db = db.Where("occurred_time >= ?", *criteria.StartTime)
	}
	if criteria.EndTime != nil {
		db = db.Where("occurred_time <= ?", *criteria.EndTime)
	}
	if criteria.AnchorType != nil && criteria.AnchorId != nil {
		sub := api.RDB.DB(ctx).Model(&Event{}).
			Select("device_id, event_type, occurred_time").
			Where("anchor_type = ? AND anchor_id = ?", *criteria.AnchorType, *criteria.AnchorId)
		db = db.Where("(device_id, event_type, occurred_time) IN (?)", sub)
	}
	db = db.Group("bucket_start, name").Order("bucket_start ASC, name ASC")
	if err := db.Scan(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
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
