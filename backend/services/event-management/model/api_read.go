// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"gorm.io/gorm"
)

// commonEventFilters applies the device, event-type and occurred-time-range
// filters that are shared by every event read. The anchor filter is applied
// separately (anchorFilter) because it joins through the event_anchors set table.
func commonEventFilters(criteria EventSearchCriteria) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if criteria.DeviceToken != nil {
			db = db.Where("device_token = ?", *criteria.DeviceToken)
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

// hasAnchor reports whether the criteria carries a usable (anchor_type, anchor_token)
// pair to filter on.
func hasAnchor(criteria EventSearchCriteria) bool {
	return criteria.AnchorType != nil && criteria.AnchorToken != nil
}

// anchorKeySubquery returns the tenant-scoped set of event keys
// (device_token, event_type, occurred_time) that carry the requested anchor, read
// from the event_anchors set table (ADR-013 addendum 2026-07-01). The anchor target
// is addressed by its stable per-tenant token (ADR-044). An event is found by any of
// its assignment dimensions because each is its own anchor row. Runs through DB(ctx)
// so the tenant predicate applies to the subquery too.
func (api *Api) anchorKeySubquery(ctx context.Context, anchorType string, anchorToken string) *gorm.DB {
	return api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("device_token, event_type, occurred_time").
		Where("anchor_type = ? AND anchor_token = ?", anchorType, anchorToken)
}

// anchorFilter restricts a query to the events carrying the requested anchor by
// joining through the event_anchors set table. A no-op when no anchor is set.
func (api *Api) anchorFilter(ctx context.Context, criteria EventSearchCriteria, result *gorm.DB) *gorm.DB {
	if hasAnchor(criteria) {
		result = result.Where("(device_token, event_type, occurred_time) IN (?)",
			api.anchorKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorToken))
	}
	return result
}

// AnchorsForEvent returns the anchor set of one event, addressed by its natural key
// (device_token, event_type, occurred_time). Backs the Event.anchors GraphQL field
// (ADR-013/044) — the tracked-relationship targets the event is queryable by, each
// as a (type, token) reference. Tenant-scoped via DB(ctx).
func (api *Api) AnchorsForEvent(ctx context.Context, deviceToken string,
	eventType esmodel.EventType, occurredTime time.Time) ([]EventAnchor, error) {
	anchors := make([]EventAnchor, 0)
	err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Where("device_token = ? AND event_type = ? AND occurred_time = ?",
			deviceToken, eventType, occurredTime).
		Find(&anchors).Error
	if err != nil {
		return nil, err
	}
	return anchors, nil
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

// rollupBucketSeconds is the measurement_rollups continuous-aggregate base bucket,
// in seconds — kept in sync with MeasurementRollupBucket (1 minute). A read whose
// interval is a whole multiple of this can be served exactly from the rollup.
const rollupBucketSeconds = 60

// useMeasurementRollup reports whether a bucketed read can be served from the
// measurement_rollups continuous aggregate (ADR-026) instead of scanning raw
// measurement_events. The rollup is EXACT only when both hold:
//   - no anchor filter — the rollup pre-aggregates across all events and carries no
//     anchor dimension, so an anchor-scoped read must go to the raw path; and
//   - the requested interval is a whole number of the 1-minute base bucket, so the
//     coarser buckets tile the base buckets exactly (time_bucket shares the epoch
//     origin, so an N*60-second bucket contains an integer number of 1-minute rollup
//     buckets and re-bucketing is loss-free).
//
// Sub-minute or anchor-filtered reads fall back to the raw path.
func useMeasurementRollup(criteria MeasurementAggregationCriteria) bool {
	if criteria.AnchorType != nil && criteria.AnchorToken != nil {
		return false
	}
	return criteria.IntervalSeconds >= rollupBucketSeconds &&
		criteria.IntervalSeconds%rollupBucketSeconds == 0
}

// BucketedMeasurements returns measurement values aggregated into fixed-width
// time_bucket intervals (TimescaleDB), grouped by measurement name. Every
// standard aggregate (avg/min/max/sum/count) is computed per bucket so a single
// query can drive a chart under any aggregation without a refetch.
//
// It routes to the pre-aggregated measurement_rollups continuous aggregate when the
// read is rollup-eligible (see useMeasurementRollup) and the rollup path is enabled,
// otherwise it scans the raw hypertable. The two paths return identical results when
// the requested time range aligns to the rollup's base bucket; for a range whose
// edges fall mid-bucket the rollup is base-bucket-granular at those edges (it
// includes whole overlapping base buckets rather than splitting them), which is
// immaterial for charts. The rollup is a read optimization, not a separate source of
// truth — an operator who needs the exact raw path can force it with the kill-switch.
func (api *Api) BucketedMeasurements(ctx context.Context, criteria MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	if !api.RollupReadsDisabled && useMeasurementRollup(criteria) {
		return api.bucketedMeasurementsFromRollup(ctx, criteria)
	}
	return api.bucketedMeasurementsFromRaw(ctx, criteria)
}

// bucketedMeasurementsFromRaw is the exact path: it aggregates the raw
// measurement_events hypertable per time_bucket. It runs through DB(ctx) with the
// MeasurementEvent model, so the fail-closed tenant-scope callback injects the
// tenant predicate (ADR-015) just as it does for the paginated reads. The optional
// anchor filter reuses the same tenant-scoped (device_token, event_type,
// occurred_time) subquery as the typed reads.
func (api *Api) bucketedMeasurementsFromRaw(ctx context.Context, criteria MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	results := make([]MeasurementBucket, 0)
	db := api.RDB.DB(ctx).Model(&MeasurementEvent{}).
		Select("time_bucket(make_interval(secs => ?), occurred_time) AS bucket_start, "+
			"name, "+
			"avg(value) AS avg, min(value) AS min, max(value) AS max, "+
			"sum(value) AS sum, count(value) AS count", criteria.IntervalSeconds)
	if criteria.DeviceToken != nil {
		db = db.Where("device_token = ?", *criteria.DeviceToken)
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
	if criteria.AnchorType != nil && criteria.AnchorToken != nil {
		db = db.Where("(device_token, event_type, occurred_time) IN (?)",
			api.anchorKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorToken))
	}
	db = db.Group("bucket_start, name").Order("bucket_start ASC, name ASC")
	if err := db.Scan(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
}

// bucketedMeasurementsFromRollup serves the read from the measurement_rollups
// continuous aggregate: it re-buckets the 1-minute partial aggregates up to the
// requested interval. avg is derived as sum(sum_value)/sum(count_value) (an average
// of averages would be wrong); min/max/sum/count roll up directly. NULLIF guards the
// empty-bucket division so an all-NULL group yields a NULL avg, matching the raw
// path. Runs through DB(ctx) with the MeasurementRollup model so the same fail-closed
// tenant predicate is injected. The time-range filter is on the bucket column, so at
// the range edges the result is bucket-granular (an interval boundary that falls
// mid-bucket includes/excludes the whole base bucket) — immaterial for charts and the
// price of reading pre-bucketed rollups.
func (api *Api) bucketedMeasurementsFromRollup(ctx context.Context, criteria MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	results := make([]MeasurementBucket, 0)
	db := api.RDB.DB(ctx).Model(&MeasurementRollup{}).
		Select("time_bucket(make_interval(secs => ?), bucket) AS bucket_start, "+
			"name, "+
			"sum(sum_value) / NULLIF(sum(count_value), 0) AS avg, "+
			"min(min_value) AS min, max(max_value) AS max, "+
			// count is sum(count_value) — cast back to bigint so it scans into
			// MeasurementBucket.Count exactly like the raw path's count() does
			// (sum(bigint) is numeric in Postgres).
			"sum(sum_value) AS sum, sum(count_value)::bigint AS count", criteria.IntervalSeconds)
	if criteria.DeviceToken != nil {
		db = db.Where("device_token = ?", *criteria.DeviceToken)
	}
	if len(criteria.EventTypes) > 0 {
		db = db.Where("event_type IN ?", criteria.EventTypes)
	}
	if criteria.Name != nil {
		db = db.Where("name = ?", *criteria.Name)
	}
	// The rollup carries only whole base buckets, so floor the range bounds to the
	// base-bucket boundary: a mid-bucket StartTime/EndTime then includes the whole
	// overlapping base bucket rather than silently dropping its partial data. This is
	// the "base-bucket-granular edges" behavior documented on BucketedMeasurements.
	bucketWidth := time.Duration(rollupBucketSeconds) * time.Second
	if criteria.StartTime != nil {
		db = db.Where("bucket >= ?", criteria.StartTime.Truncate(bucketWidth))
	}
	if criteria.EndTime != nil {
		db = db.Where("bucket <= ?", criteria.EndTime.Truncate(bucketWidth))
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
