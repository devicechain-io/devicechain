// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm/schema"
)

// The hourly read path: a bucketed read whose interval is a whole number of hours is served from
// the measurement_rollups_1h aggregate (see NewRollup1hSchema) wherever an hour lies wholly
// inside the range the minute level would have served, and from the minute level at the edges.
//
// The result is the same as re-bucketing the minute level, because the hour level holds, per
// hour, exactly the partials of the minute rows that hour contains. The edges are what keep that
// true: the minute level's range is minute-granular (a start or end mid-hour includes only the
// minutes from there), and an hour row cannot be split, so the hours the range cuts through,
// the first one when the start is mid-hour and the last when the end falls before the hour
// closes (for a read ending now, the hour still filling), come from the minute level, and only
// the hours the range holds whole come from the hour level.
//
// Sums are added in a different order than a single pass over the minute rows would, so a sum
// or average of non-integral values can differ in the last bits of the float; min, max and count
// are exact.

const rollupHourSeconds = rollup1hBucketSeconds

const rollup1hCacheTTL = time.Minute

type rollup1hCompleteness struct {
	from, until time.Time
}

// rollupLevel is one row of rollup_levels: the instant from which a rollup level is complete.
type rollupLevel struct {
	Level        string
	CompleteFrom time.Time
}

// TableName is rollup_levels under the functional-area prefix, derived through the namer.
func (rollupLevel) TableName(namer schema.Namer) string { return namer.TableName("RollupLevel") }

// rollup1hCompleteFrom is the hour from which the hour level holds every hour it should, and
// false when the migration has not recorded one (the level is then not read at all). The
// aggregate is created without history, so hours before this instant are the minute level's.
// Cache for at most a minute: a background worker on any replica can move the marker
// back. A stale marker is conservative (more minute reads), but must not last forever.
func (api *Api) rollup1hCompleteFrom(ctx context.Context) (time.Time, bool, error) {
	now := time.Now()
	if api.now != nil {
		now = api.now()
	}
	if cached := api.rollup1hFrom.Load(); cached != nil && now.Before(cached.until) {
		return cached.from, true, nil
	}
	var rows []rollupLevel
	if err := api.RDB.DB(ctx).Where("level = ?", rollup1hLevel).Limit(1).Find(&rows).Error; err != nil {
		return time.Time{}, false, fmt.Errorf("reading the one-hour rollup's complete-from hour: %w", err)
	}
	if len(rows) == 0 {
		return time.Time{}, false, nil
	}
	from := rows[0].CompleteFrom
	api.rollup1hFrom.Store(&rollup1hCompleteness{from: from, until: now.Add(rollup1hCacheTTL)})
	return from, true, nil
}

// hourSpan is the half-open range [from, to) of whole hours the hour level serves for one read,
// with the retention floor the read was planned under so both levels apply the same one.
type hourSpan struct {
	from, to time.Time
	floor    time.Time
	hasFloor bool
}

// hourlySpan plans the hour level's share of a read, and reports false when it has none (the
// interval is not a whole number of hours, or the range holds no whole hour), in which case the
// whole read is the minute level's.
func (api *Api) hourlySpan(ctx context.Context, criteria MeasurementAggregationCriteria) (hourSpan, bool, error) {
	if criteria.IntervalSeconds < rollupHourSeconds || criteria.IntervalSeconds%rollupHourSeconds != 0 {
		return hourSpan{}, false, nil
	}
	completeFrom, ok, err := api.rollup1hCompleteFrom(ctx)
	if err != nil || !ok {
		return hourSpan{}, false, err
	}
	floor, hasFloor := api.rollupRetentionFloor()
	span, ok := hourlySpanOf(criteria, floor, hasFloor, completeFrom)
	return span, ok, nil
}

// hourlySpanOf derives the span from the minute level's own bounds: its lower bound is the later
// of the start (minute-floored), the retention floor and the hour the level is complete from
// (completeFrom, itself on an hour boundary), its upper bound is the end,
// minute-floored and inclusive. An hour starting at h holds the minutes h .. h+59, so it lies
// wholly inside the range when h >= lower and h+59min <= upper.
func hourlySpanOf(criteria MeasurementAggregationCriteria, floor time.Time, hasFloor bool,
	completeFrom time.Time) (hourSpan, bool) {
	if criteria.IntervalSeconds < rollupHourSeconds || criteria.IntervalSeconds%rollupHourSeconds != 0 ||
		criteria.StartTime == nil || criteria.EndTime == nil {
		return hourSpan{}, false
	}
	hour := time.Duration(rollupHourSeconds) * time.Second
	minute := time.Duration(rollupBucketSeconds) * time.Second
	lower := criteria.StartTime.Truncate(minute)
	if hasFloor && floor.After(lower) {
		lower = floor
	}
	if completeFrom.After(lower) {
		lower = completeFrom
	}
	from := ceilTo(lower, hour)
	to := criteria.EndTime.Truncate(minute).Add(minute).Truncate(hour)
	if !from.Before(to) {
		return hourSpan{}, false
	}
	return hourSpan{from: from, to: to, floor: floor, hasFloor: hasFloor}, true
}

// ceilTo rounds t up to a multiple of d (t itself when it already is one).
func ceilTo(t time.Time, d time.Duration) time.Time {
	floored := t.Truncate(d)
	if floored.Equal(t) {
		return t
	}
	return floored.Add(d)
}

// rollupPartial is one group of partial aggregates, from either level, before avg is derived.
type rollupPartial struct {
	BucketStart time.Time
	Name        string
	SumValue    sql.NullFloat64
	MinValue    sql.NullFloat64
	MaxValue    sql.NullFloat64
	CountValue  int64
}

const rollupPartialSelect = "time_bucket(make_interval(secs => ?), bucket) AS bucket_start, " +
	"name, sum(sum_value) AS sum_value, min(min_value) AS min_value, max(max_value) AS max_value, " +
	"sum(count_value)::bigint AS count_value"

// bucketedMeasurementsFromHourly serves an hourly-interval read: the whole hours of span from
// the hour level, the cut hours at either end from the minute level, merged by output bucket.
func (api *Api) bucketedMeasurementsFromHourly(ctx context.Context, criteria MeasurementAggregationCriteria,
	span hourSpan) ([]MeasurementBucket, error) {
	minute := time.Duration(rollupBucketSeconds) * time.Second

	var middle []rollupPartial
	hourly := api.rollupFilters(ctx, criteria, &MeasurementRollupHourly{}, span.floor, span.hasFloor).
		Where("bucket >= ? AND bucket < ?", span.from, span.to).
		Select(rollupPartialSelect, criteria.IntervalSeconds).
		Group("bucket_start, name").Order("bucket_start ASC, name ASC")
	if err := hourly.Scan(&middle).Error; err != nil {
		return nil, err
	}

	var edges []rollupPartial
	cut := api.rollupFilters(ctx, criteria, &MeasurementRollup{}, span.floor, span.hasFloor).
		Where("bucket >= ?", criteria.StartTime.Truncate(minute)).
		Where("bucket <= ?", criteria.EndTime.Truncate(minute)).
		Where("(bucket < ? OR bucket >= ?)", span.from, span.to).
		Select(rollupPartialSelect, criteria.IntervalSeconds).
		Group("bucket_start, name").Order("bucket_start ASC, name ASC")
	if err := cut.Scan(&edges).Error; err != nil {
		return nil, err
	}

	merged := mergeRollupPartials(middle, edges)
	if criteria.Name == nil && len(middle) > 0 && len(edges) > 0 {
		// Two sorted streams can only be interleaved by comparing names, and the database's
		// collation is not Go's byte order. Ask the database for the order of the names in play.
		rank, err := api.orderNames(ctx, merged)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(merged, func(i, j int) bool {
			if !merged[i].BucketStart.Equal(merged[j].BucketStart) {
				return merged[i].BucketStart.Before(merged[j].BucketStart)
			}
			return rank[merged[i].Name] < rank[merged[j].Name]
		})
	}
	return derivePartialBuckets(merged), nil
}

// orderNames ranks the measurement names of rows by the database's own ordering of text, the
// order the minute level's ORDER BY name returns (the name column carries the database's
// default collation, as does a bound text value). The names are sorted in the database from the
// values themselves, so a name missing from any level cannot fail the read.
func (api *Api) orderNames(ctx context.Context, rows []rollupPartial) (map[string]int, error) {
	seen := map[string]struct{}{}
	var args []any
	for _, r := range rows {
		if _, ok := seen[r.Name]; !ok {
			seen[r.Name] = struct{}{}
			args = append(args, r.Name)
		}
	}
	var ordered []struct{ Name string }
	query := "SELECT n AS name FROM (VALUES " + strings.TrimSuffix(strings.Repeat("(?::text),", len(args)), ",") +
		") AS v(n) ORDER BY n"
	if err := api.RDB.DB(ctx).Raw(query, args...).Scan(&ordered).Error; err != nil {
		return nil, fmt.Errorf("ordering measurement names: %w", err)
	}
	if len(ordered) != len(args) {
		return nil, fmt.Errorf("ordering measurement names returned %d of %d", len(ordered), len(args))
	}
	rank := make(map[string]int, len(ordered))
	for i, o := range ordered {
		rank[o.Name] = i
	}
	return rank, nil
}

// mergeRollupPartials adds up the partials that share an output bucket and name, in time order.
// Rows of equal time keep the order they came in; a read that interleaves names from both
// levels sorts them afterwards (see bucketedMeasurementsFromHourly).
func mergeRollupPartials(a, b []rollupPartial) []rollupPartial {
	type key struct {
		at   int64
		name string
	}
	index := make(map[key]int, len(a)+len(b))
	out := make([]rollupPartial, 0, len(a)+len(b))
	for _, rows := range [][]rollupPartial{a, b} {
		for _, r := range rows {
			k := key{r.BucketStart.UnixNano(), r.Name}
			i, ok := index[k]
			if !ok {
				index[k] = len(out)
				out = append(out, r)
				continue
			}
			out[i] = addPartials(out[i], r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BucketStart.Before(out[j].BucketStart) })
	return out
}

func addPartials(x, y rollupPartial) rollupPartial {
	x.CountValue += y.CountValue
	x.SumValue = addNull(x.SumValue, y.SumValue)
	x.MinValue = pickNull(x.MinValue, y.MinValue, func(a, b float64) bool { return b < a })
	x.MaxValue = pickNull(x.MaxValue, y.MaxValue, func(a, b float64) bool { return b > a })
	return x
}

func addNull(a, b sql.NullFloat64) sql.NullFloat64 {
	switch {
	case !a.Valid:
		return b
	case !b.Valid:
		return a
	}
	return sql.NullFloat64{Float64: a.Float64 + b.Float64, Valid: true}
}

// pickNull keeps the better of two nullable values; better(a, b) reports that b beats a.
func pickNull(a, b sql.NullFloat64, better func(a, b float64) bool) sql.NullFloat64 {
	switch {
	case !a.Valid:
		return b
	case !b.Valid:
		return a
	case better(a.Float64, b.Float64):
		return b
	}
	return a
}

// derivePartialBuckets turns partials into the served buckets. avg is sum/count, NULL when
// nothing was counted, as the minute-level query's sum/NULLIF(count, 0) is.
func derivePartialBuckets(rows []rollupPartial) []MeasurementBucket {
	out := make([]MeasurementBucket, 0, len(rows))
	for _, r := range rows {
		b := MeasurementBucket{BucketStart: r.BucketStart, Name: r.Name, Min: r.MinValue, Max: r.MaxValue,
			Sum: r.SumValue, Count: r.CountValue}
		if r.CountValue != 0 && r.SumValue.Valid {
			b.Avg = sql.NullFloat64{Float64: r.SumValue.Float64 / float64(r.CountValue), Valid: true}
		}
		out = append(out, b)
	}
	return out
}
