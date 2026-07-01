// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"gorm.io/gorm"
)

// commonEventFilters applies the device, event-type and occurred-time-range
// filters that are shared by every event read. The anchor filter is applied
// separately (anchorFilter) because it joins through the event_anchors set table.
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

// anchorKeySubquery returns the tenant-scoped set of event keys
// (device_id, event_type, occurred_time) that carry the requested anchor, read
// from the event_anchors set table (ADR-013 addendum 2026-07-01). An event is
// found by any of its assignment dimensions because each is its own anchor row.
// Runs through DB(ctx) so the tenant predicate applies to the subquery too.
func (api *Api) anchorKeySubquery(ctx context.Context, anchorType string, anchorId uint) *gorm.DB {
	return api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("device_id, event_type, occurred_time").
		Where("anchor_type = ? AND anchor_id = ?", anchorType, anchorId)
}

// anchorFilter restricts a query to the events carrying the requested anchor by
// joining through the event_anchors set table. A no-op when no anchor is set.
func (api *Api) anchorFilter(ctx context.Context, criteria EventSearchCriteria, result *gorm.DB) *gorm.DB {
	if hasAnchor(criteria) {
		result = result.Where("(device_id, event_type, occurred_time) IN (?)",
			api.anchorKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorId))
	}
	return result
}

// Search for base events that meet criteria. The anchor filter joins through the
// event_anchors set table (the base event no longer carries a single anchor).
func (api *Api) Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error) {
	results := make([]Event, 0)
	db, pag := api.RDB.ListOf(ctx, &Event{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		return api.anchorFilter(ctx, criteria, result)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EventSearchResults{Results: results, Pagination: pag}, nil
}

// Search for location events that meet criteria.
func (api *Api) LocationEvents(ctx context.Context, criteria EventSearchCriteria) (*LocationEventSearchResults, error) {
	results := make([]LocationEvent, 0)
	db, pag := api.RDB.ListOf(ctx, &LocationEvent{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		return api.anchorFilter(ctx, criteria, result)
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
		return api.anchorFilter(ctx, criteria, result)
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
		db = db.Where("(device_id, event_type, occurred_time) IN (?)",
			api.anchorKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorId))
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
		return api.anchorFilter(ctx, criteria, result)
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AlertEventSearchResults{Results: results, Pagination: pag}, nil
}
