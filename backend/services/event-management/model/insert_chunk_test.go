// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// insertStatement is one INSERT the write path built: its table, how many rows it carried
// and how many parameters it bound.
type insertStatement struct {
	table      string
	rows, vars int
}

// newDryRunApi is an Api whose inserts are BUILT, through the production callback chain and
// the production ON CONFLICT clauses, and never sent — so a statement larger than sqlite
// would take can still be measured — with every INSERT it builds recorded.
func newDryRunApi(t *testing.T) (*Api, func() []insertStatement) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true, Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	var mu sync.Mutex
	var seen []insertStatement
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:record_insert", func(tx *gorm.DB) {
		rows := 1
		if v := reflect.Indirect(reflect.ValueOf(tx.Statement.Dest)); v.Kind() == reflect.Slice {
			rows = v.Len()
		}
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, insertStatement{table: tx.Statement.Table, rows: rows, vars: len(tx.Statement.Vars)})
	}))
	return NewApi(&rdb.RdbManager{Database: db}), func() []insertStatement {
		mu.Lock()
		defer mu.Unlock()
		return append([]insertStatement(nil), seen...)
	}
}

func rowsPerInsert(t *testing.T, api *Api, ctx context.Context, model any) int {
	t.Helper()
	n, err := rdb.RowsPerInsert(api.RDB.DB(ctx), model)
	require.NoError(t, err)
	return n
}

// Every insert on the event-store write path, given one row more than a statement can
// carry, splits it into two statements and binds no more than the driver accepts in
// either. What is measured is what gorm actually built — the site's own ON CONFLICT
// clause, the tenant stamp, every bound column — not the column count the bound was
// derived from; and the first statement carrying a full chunk shows the bound is not
// looser than it needs to be.
func TestEveryEventStoreInsertFitsTheDriversLimit(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	parent := Event{EventId: []byte("parent"), DeviceToken: "d1", EventType: esmodel.Measurement,
		OccurredTime: at, Source: "mqtt", ProcessedTime: at}
	unit, dataType, classifier := "C", "float", uint(3)

	// distinctParents is n events that each need their own parent row.
	distinctParents := func(n int) []Event {
		out := make([]Event, n)
		for i := range out {
			out[i] = parent
			out[i].EventId = []byte(fmt.Sprintf("e%06d", i))
			out[i].EventType = esmodel.StateChange
		}
		return out
	}

	for _, tc := range []struct {
		name string
		// table is the one the site's split insert writes; model its row type.
		table string
		model any
		write func(api *Api, db *gorm.DB, n int) error
		// parentsSplit is true for a site handed n DISTINCT parents, so the parent
		// insert must split as well.
		parentsSplit bool
	}{
		{name: "measurements", table: "measurement_events", model: &MeasurementEvent{},
			write: func(api *Api, db *gorm.DB, n int) error {
				reqs := make([]*MeasurementEventCreateRequest, n)
				for i := range reqs {
					reqs[i] = &MeasurementEventCreateRequest{Event: parent, EntryOccurredTime: at,
						Name: fmt.Sprintf("m%06d", i), Value: f64(1.5), Classifier: &classifier,
						Unit: &unit, DataType: &dataType}
				}
				_, err := api.CreateMeasurementEvents(ctx, db, reqs)
				return err
			}},
		{name: "locations", table: "location_events", model: &LocationEvent{},
			write: func(api *Api, db *gorm.DB, n int) error {
				reqs := make([]*LocationEventCreateRequest, n)
				for i := range reqs {
					lat := float64(i%90) + 0.5
					reqs[i] = &LocationEventCreateRequest{Event: parent, EntryOccurredTime: at,
						Latitude: &lat, Longitude: f64(1), Elevation: f64(2), Accuracy: f64(3),
						Speed: f64(4), Heading: f64(5)}
				}
				_, err := api.CreateLocationEvents(ctx, db, reqs)
				return err
			}},
		{name: "alerts", table: "alert_events", model: &AlertEvent{},
			write: func(api *Api, db *gorm.DB, n int) error {
				reqs := make([]*AlertEventCreateRequest, n)
				for i := range reqs {
					reqs[i] = &AlertEventCreateRequest{Event: parent, EntryOccurredTime: at,
						Type: fmt.Sprintf("a%06d", i), Level: 2, Message: "m", Source: "s"}
				}
				_, err := api.CreateAlertEvents(ctx, db, reqs)
				return err
			}},
		{name: "state changes and their parents", table: "state_change_events", model: &StateChangeEvent{},
			parentsSplit: true,
			write: func(api *Api, db *gorm.DB, n int) error {
				reqs := make([]*StateChangeEventCreateRequest, n)
				for i, ev := range distinctParents(n) {
					reqs[i] = &StateChangeEventCreateRequest{Event: ev, State: "connected",
						Reason: "r", SessionId: uint64(i + 1)}
				}
				_, _, err := api.CreateStateChangeEvents(ctx, db, reqs)
				return err
			}},
		{name: "anchors", table: "event_anchors", model: &EventAnchor{},
			write: func(api *Api, db *gorm.DB, n int) error {
				anchors := make([]*EventAnchor, n)
				for i := range anchors {
					anchors[i] = &EventAnchor{EventId: parent.EventId, DeviceToken: "d1",
						EventType: esmodel.Measurement, OccurredTime: at, AnchorType: "area",
						AnchorToken: fmt.Sprintf("a%06d", i)}
				}
				return api.CreateEventAnchors(ctx, db, anchors)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, inserts := newDryRunApi(t)
			per := rowsPerInsert(t, api, ctx, tc.model)
			n := per + 1
			perParent := rowsPerInsert(t, api, ctx, &Event{})
			if tc.parentsSplit && perParent >= n {
				// The parent insert must split too, or this case says nothing about it.
				n = perParent + 1
			}
			require.NoError(t, tc.write(api, api.RDB.DB(ctx), n))

			var target, parents []insertStatement
			for _, s := range inserts() {
				assert.LessOrEqualf(t, s.vars, rdb.MaxBindParameters,
					"an INSERT into %s of %d rows binds %d parameters", s.table, s.rows, s.vars)
				switch s.table {
				case tc.table:
					target = append(target, s)
				case "events":
					parents = append(parents, s)
				}
			}
			require.Len(t, target, 2, "%d rows into %s must take two statements", n, tc.table)
			assert.Equal(t, per, target[0].rows, "the first statement must carry a full chunk")
			perRow := target[0].vars / target[0].rows
			assert.Greater(t, target[0].vars+perRow, rdb.MaxBindParameters,
				"a full chunk of %d rows binds %d parameters, so one row more would still have fit",
				target[0].rows, target[0].vars)
			assert.Equal(t, n, target[0].rows+target[1].rows, "every row must be written")
			if tc.parentsSplit {
				require.Len(t, parents, 2, "%d distinct parents must take two statements", n)
				assert.Equal(t, perParent, parents[0].rows)
				assert.Equal(t, n, parents[0].rows+parents[1].rows)
			}
		})
	}
}
