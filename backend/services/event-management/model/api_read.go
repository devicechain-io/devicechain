// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
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

// anchorKeySubquery returns the tenant-scoped set of EVENT IDS that carry the requested
// anchor, read from the event_anchors set table (ADR-013 addendum 2026-07-01). The anchor
// target is addressed by its stable per-tenant token (ADR-044). An event is found by any
// of its assignment dimensions because each is its own anchor row. Runs through DB(ctx)
// so the tenant predicate applies to the subquery too.
//
// 🔴 IT KEYS ON event_id, NOT ON (device_token, event_type, occurred_time), and the
// difference is not cosmetic on either side of it:
//
//   - The payload tables no longer share the base event's occurred_time. A reading is
//     stored at ITS OWN instant while an anchor row records the message's, so a natural-key
//     join returns NOTHING for any batched reading — an anchor-filtered search over a
//     store-and-forward fleet would come back empty and look like an absence of data.
//   - The natural key was never an identity anyway. AnchorsForEvent carries the same note
//     for the same reason: two distinct events sharing that tuple made the join return the
//     UNION of both, with nothing in the result saying which was which.
//
// occurred_time is deliberately NOT in the subquery for the PAYLOAD tables. It is the
// hypertable partition column, so dropping it costs chunk pruning on this join — but the
// caller's own occurred-time range (commonEventFilters) still prunes, and a fast wrong
// answer is not the trade to make here. The BASE event table is the exception, and uses
// anchorEventKeySubquery: an anchor row carries its event's own instant, so there the pair
// is the event's exact key rather than a guess at it.
func (api *Api) anchorKeySubquery(ctx context.Context, anchorType string, anchorToken string) *gorm.DB {
	return api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("event_id").
		Where("anchor_type = ? AND anchor_token = ?", anchorType, anchorToken)
}

// anchorFilter restricts a query to the events carrying the requested anchor by
// joining through the event_anchors set table. A no-op when no anchor is set.
func (api *Api) anchorFilter(ctx context.Context, criteria EventSearchCriteria, result *gorm.DB) *gorm.DB {
	if hasAnchor(criteria) {
		result = result.Where("event_id IN (?)",
			api.anchorKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorToken))
	}
	return result
}

// anchorEventKeySubquery is anchorKeySubquery for the BASE event table: it selects the
// base event's own key, (occurred_time, event_id). The event store's keys lead with
// (tenant_id, occurred_time), so an event_id alone is not a prefix that can be sought; the
// pair is, and the anchor-filtered read then probes events_pkey per anchored event instead
// of walking the tenant's range.
//
// 🔴 IT IS CORRECT ONLY BECAUSE AN ANCHOR ROW IS WRITTEN WITH ITS EVENT'S INSTANT. The one
// constructor of anchor rows (the persistence worker's anchorRows, which the grouped writer
// reuses) stamps event.OccurredTime, as the base row is stamped; AnchorsForEvent relies on
// the same fact. An anchor written at any other instant would make this filter return
// NOTHING for its event: TestAnAnchorFilteredEventIsFoundThroughTheWriter writes through
// the worker and reads through this filter so that cannot happen silently. The payload
// tables must keep anchorKeySubquery: a payload row carries its reading's instant.
func (api *Api) anchorEventKeySubquery(ctx context.Context, anchorType string, anchorToken string) *gorm.DB {
	return api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("occurred_time, event_id").
		Where("anchor_type = ? AND anchor_token = ?", anchorType, anchorToken)
}

// AnchorsForEvent returns the anchor set of one event, addressed by its EVENT ID.
// Backs the Event.anchors GraphQL field (ADR-013/044) — the tracked-relationship targets
// the event is queryable by, each as a (type, token) reference. Tenant-scoped via DB(ctx).
//
// 🔴 This took (device_token, event_type, occurred_time) and was therefore capable of
// returning the UNION of two different events' anchors whenever both shared that tuple,
// with nothing in the result marking which anchor belonged to which. occurred_time is
// still passed because it is the hypertable partition column and lets the read prune
// chunks; it is not part of the address.
func (api *Api) AnchorsForEvent(ctx context.Context, eventId []byte,
	occurredTime time.Time) ([]EventAnchor, error) {
	anchors := make([]EventAnchor, 0)
	err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Where("event_id = ? AND occurred_time = ?", eventId, occurredTime).
		Find(&anchors).Error
	if err != nil {
		return nil, err
	}
	return anchors, nil
}

// Every event read below is newest-first, tiebroken on the table's own row identity.
// That order is no longer written here: it is each model's DefaultOrder (see
// events.go), which rdb.ListOf applies to the data query, so a read cannot be added
// that forgets it.
//
// 🔴 The history is worth keeping in view. Every event read in this file once issued
// LIMIT/OFFSET with NO ORDER BY at all. Postgres row order is unspecified without one,
// so the defect was not merely "results look shuffled": pagination could repeat a row
// on one page and skip another entirely, and `pageSize: 1` — the obvious way to ask
// for the most recent reading — returned an ARBITRARY row rather than the latest. That
// made "where is this device now" silently wrong rather than slow.

// Search for base events that meet criteria. The anchor filter joins through the
// event_anchors set table (the base event no longer carries a single anchor), on the base
// event's full key (anchorEventKeySubquery).
func (api *Api) Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error) {
	results := make([]Event, 0)
	db, pag := api.RDB.ListOf(ctx, &Event{}, func(result *gorm.DB) *gorm.DB {
		result = commonEventFilters(criteria)(result)
		if hasAnchor(criteria) {
			result = result.Where("(occurred_time, event_id) IN (?)",
				api.anchorEventKeySubquery(ctx, *criteria.AnchorType, *criteria.AnchorToken))
		}
		return result
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

// MaxMeasurementBuckets is the most time buckets one aggregation read may produce:
// ceil((endTime - startTime) / intervalSeconds). A read that would produce more is
// refused (LIMIT_EXCEEDED) rather than truncated, so the caller narrows the range or
// widens the interval. 10,000 points is far beyond what a chart can draw.
//
// The count is ceil(range / interval). time_bucket aligns buckets to a fixed origin, not
// to startTime, so a range that straddles a bucket boundary can touch one bucket more
// than that: a read at the cap may return cap+1 buckets per measurement name. The bound
// is on cost, not an exact row count, so the extra bucket is accepted rather than
// counted.
const MaxMeasurementBuckets = 10_000

// errStartTimeRequired refuses an aggregation with no start of range: without one the
// read covers a tenant's whole measurement history.
var errStartTimeRequired = integrity.NewRefusal(integrity.ClassInvalid,
	"startTime is required: an aggregation must name the range it covers")

// boundBucketedRange applies the range rules to an aggregation read: startTime is
// required, endTime defaults to now, the range must not run backwards, and the number
// of buckets it produces is capped at MaxMeasurementBuckets. It returns the criteria
// with endTime filled in.
func boundBucketedRange(criteria MeasurementAggregationCriteria, now time.Time) (MeasurementAggregationCriteria, error) {
	if criteria.IntervalSeconds < 1 {
		return criteria, integrity.NewRefusal(integrity.ClassInvalid, "intervalSeconds must be >= 1")
	}
	if criteria.StartTime == nil {
		return criteria, errStartTimeRequired
	}
	if criteria.EndTime == nil {
		end := now
		criteria.EndTime = &end
	}
	if criteria.EndTime.Before(*criteria.StartTime) {
		return criteria, integrity.NewRefusal(integrity.ClassInvalid, "endTime must not be before startTime")
	}
	rangeSeconds := int64(criteria.EndTime.Sub(*criteria.StartTime) / time.Second)
	buckets := rangeSeconds / criteria.IntervalSeconds
	if rangeSeconds%criteria.IntervalSeconds != 0 {
		buckets++
	}
	if buckets > MaxMeasurementBuckets {
		return criteria, limit.Exceeded("buckets", int(buckets), MaxMeasurementBuckets)
	}
	return criteria, nil
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
	criteria, err := boundBucketedRange(criteria, time.Now())
	if err != nil {
		return nil, err
	}
	if !api.RollupReadsDisabled && useMeasurementRollup(criteria) {
		return api.bucketedMeasurementsFromRollup(ctx, criteria)
	}
	return api.bucketedMeasurementsFromRaw(ctx, criteria)
}

// bucketedMeasurementsFromRaw is the exact path: it aggregates the raw
// measurement_events hypertable per time_bucket. It runs through DB(ctx) with the
// MeasurementEvent model, so the fail-closed tenant-scope callback injects the
// tenant predicate (ADR-015) just as it does for the paginated reads. The optional
// anchor filter reuses the same tenant-scoped event_id subquery as the typed payload reads.
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
		db = db.Where("event_id IN (?)",
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
