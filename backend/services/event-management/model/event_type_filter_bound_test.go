// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func repeatedEventTypes(kind eventtype.EventType, n int) []eventtype.EventType {
	types := make([]eventtype.EventType, n)
	for i := range types {
		types[i] = kind
	}
	return types
}

func requireEventTypeLimit(t *testing.T, err error) {
	t.Helper()
	le, ok := limit.As(err)
	require.True(t, ok, "want typed limit refusal, got %v", err)
	assert.Equal(t, "eventTypes", le.What)
	assert.Equal(t, rdb.MaxLookupKeys+1, le.Got)
	assert.Equal(t, rdb.MaxLookupKeys, le.Max)
	assert.Equal(t, "LIMIT_EXCEEDED", le.Extensions()["code"])
}

func TestEventTypeFilterBoundPages(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	id := []byte("event-type-bound-row")
	payloadID := []byte("event-type-bound-payload")
	ctx := core.WithTenant(context.Background(), "event-type-bound")
	cases := []struct {
		name string
		kind eventtype.EventType
		seed any
		read func(*Api, context.Context, EventSearchCriteria) (any, error)
	}{
		{"Events", eventtype.Alert,
			&Event{EventId: id, DeviceToken: "d1", EventType: eventtype.Alert, OccurredTime: at},
			func(api *Api, ctx context.Context, c EventSearchCriteria) (any, error) { return api.Events(ctx, c) }},
		{"LocationEvents", eventtype.Location,
			&LocationEvent{EventId: id, PayloadId: payloadID, DeviceToken: "d1", EventType: eventtype.Location, OccurredTime: at},
			func(api *Api, ctx context.Context, c EventSearchCriteria) (any, error) {
				return api.LocationEvents(ctx, c)
			}},
		{"MeasurementEvents", eventtype.Measurement,
			&MeasurementEvent{EventId: id, PayloadId: payloadID, DeviceToken: "d1", EventType: eventtype.Measurement, OccurredTime: at, Name: "temperature"},
			func(api *Api, ctx context.Context, c EventSearchCriteria) (any, error) {
				return api.MeasurementEvents(ctx, c)
			}},
		{"AlertEvents", eventtype.Alert,
			&AlertEvent{EventId: id, PayloadId: payloadID, DeviceToken: "d1", EventType: eventtype.Alert, OccurredTime: at, Message: "seeded alert"},
			func(api *Api, ctx context.Context, c EventSearchCriteria) (any, error) {
				return api.AlertEvents(ctx, c)
			}},
		{"StateChangeEvents", eventtype.StateChange,
			&StateChangeEvent{EventId: id, DeviceToken: "d1", EventType: eventtype.StateChange, OccurredTime: at, State: "ONLINE"},
			func(api *Api, ctx context.Context, c EventSearchCriteria) (any, error) {
				return api.StateChangeEvents(ctx, c)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, rdb.RegisterTenantScoping(db))
			require.NoError(t, db.AutoMigrate(tc.seed))
			api := NewApi(&rdb.RdbManager{Database: db})
			require.NoError(t, api.RDB.DB(ctx).Create(tc.seed).Error)

			queries := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("event_type_bound:queries", func(*gorm.DB) { queries++ }))
			criteria := EventSearchCriteria{
				Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
				EventTypes: repeatedEventTypes(tc.kind, rdb.MaxLookupKeys+1),
			}
			_, err = tc.read(api, ctx, criteria)
			requireEventTypeLimit(t, err)
			assert.Zero(t, queries, "refuse before even ListOf's COUNT query")

			for _, n := range []int{rdb.MaxLookupKeys, 1, 0, -1} {
				t.Run(fmt.Sprintf("entries_%d", n), func(t *testing.T) {
					criteria.EventTypes = nil
					if n >= 0 {
						criteria.EventTypes = repeatedEventTypes(tc.kind, n)
					}
					page, err := tc.read(api, ctx, criteria)
					require.NoError(t, err)
					// Check the seeded identity through each public result type, so an
					// accepted request returning an empty page cannot satisfy the control.
					switch p := page.(type) {
					case *EventSearchResults:
						require.Len(t, p.Results, 1)
						assert.Equal(t, id, p.Results[0].EventId)
					case *LocationEventSearchResults:
						require.Len(t, p.Results, 1)
						assert.Equal(t, id, p.Results[0].EventId)
					case *MeasurementEventSearchResults:
						require.Len(t, p.Results, 1)
						assert.Equal(t, id, p.Results[0].EventId)
					case *AlertEventSearchResults:
						require.Len(t, p.Results, 1)
						assert.Equal(t, id, p.Results[0].EventId)
					case *StateChangeEventSearchResults:
						require.Len(t, p.Results, 1)
						assert.Equal(t, id, p.Results[0].EventId)
					default:
						t.Fatalf("unexpected page type %T", page)
					}
				})
			}
		})
	}
}

func TestEventTypeFilterBoundAggregation(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "event-type-bound")
	for _, source := range []struct {
		name     string
		interval int64
		table    string
	}{
		{"raw", 1, "measurement_events"},
		{"rollup", 60, "measurement_rollups"},
	} {
		for _, named := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/named_%t", source.name, named), func(t *testing.T) {
				api := newRowBoundTestApi(t)
				sqlDB, err := api.RDB.Database.DB()
				require.NoError(t, err)
				t.Cleanup(func() { _ = sqlDB.Close() })
				criteria := rowRead(2, source.interval, nil)
				at := *criteria.StartTime
				seedNames(t, api, "event-type-bound", at, "temperature")
				seedRollupNames(t, api, "event-type-bound", at.Truncate(time.Minute), "temperature")
				if named {
					name := "temperature"
					criteria.Name = &name
				}
				criteria.EventTypes = repeatedEventTypes(eventtype.Measurement, rdb.MaxLookupKeys+1)

				queries, aggregations := 0, 0
				sentinel := errors.New("aggregation reached")
				require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("event_type_bound:queries", func(*gorm.DB) { queries++ }))
				// Scan executes the Row callback chain; stop before SQLite encounters
				// TimescaleDB syntax, and record the source the public method selected.
				require.NoError(t, api.RDB.Database.Callback().Row().Before("gorm:row").Register("event_type_bound:aggregation", func(db *gorm.DB) {
					aggregations++
					assert.Equal(t, source.table, db.Statement.Table)
					db.AddError(sentinel)
				}))
				_, err = api.BucketedMeasurements(ctx, criteria)
				requireEventTypeLimit(t, err)
				assert.Zero(t, queries, "refuse before the distinct-name lookup")
				assert.Zero(t, aggregations, "refuse before aggregation SQL")

				for _, n := range []int{rdb.MaxLookupKeys, 1, 0, -1} {
					criteria.EventTypes = nil
					if n >= 0 {
						criteria.EventTypes = repeatedEventTypes(eventtype.Measurement, n)
					}
					before := aggregations
					_, err = api.BucketedMeasurements(ctx, criteria)
					require.ErrorIs(t, err, sentinel, "allowed input of size %d must reach aggregation", n)
					assert.Equal(t, before+1, aggregations)
				}
			})
		}
	}
}
