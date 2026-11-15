// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

func TestEventListsWithoutTotal(t *testing.T) {
	api := newReadTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&LocationEvent{}, &MeasurementEvent{}, &AlertEvent{}, &StateChangeEvent{}, &EventAnchor{}); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "A")
	for _, i := range []int{2, 0, 3, 1, 4, 5, 6} {
		// Decoys exercise tenant, device and anchor filtering on both paths.
		tenant, device, anchor := "A", "device", "acme"
		if i == 4 {
			tenant = "B"
		}
		if i == 5 {
			device = "other"
		}
		if i == 6 {
			anchor = "other"
		}
		write := core.WithTenant(context.Background(), tenant)
		at := time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)
		id := []byte{byte(i + 1)}
		for _, row := range []any{
			&Event{EventId: id, DeviceToken: device, OccurredTime: at, Source: "test"},
			&LocationEvent{EventId: id, PayloadId: id, DeviceToken: device, OccurredTime: at},
			&MeasurementEvent{EventId: id, PayloadId: id, DeviceToken: device, OccurredTime: at},
			&AlertEvent{EventId: id, PayloadId: id, DeviceToken: device, OccurredTime: at},
			&StateChangeEvent{EventId: id, DeviceToken: device, OccurredTime: at},
			&EventAnchor{EventId: id, DeviceToken: device, OccurredTime: at, AnchorType: "customer", AnchorToken: anchor},
		} {
			if err := api.RDB.DB(write).Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	checkEventPages(t, api, ctx, "events", func(c EventSearchCriteria) ([]Event, rdb.SearchResultsPagination, error) {
		r, e := api.Events(ctx, c)
		if e != nil {
			return nil, rdb.SearchResultsPagination{}, e
		}
		return r.Results, r.Pagination, nil
	})
	checkEventPages(t, api, ctx, "locationEvents", func(c EventSearchCriteria) ([]LocationEvent, rdb.SearchResultsPagination, error) {
		r, e := api.LocationEvents(ctx, c)
		if e != nil {
			return nil, rdb.SearchResultsPagination{}, e
		}
		return r.Results, r.Pagination, nil
	})
	checkEventPages(t, api, ctx, "measurementEvents", func(c EventSearchCriteria) ([]MeasurementEvent, rdb.SearchResultsPagination, error) {
		r, e := api.MeasurementEvents(ctx, c)
		if e != nil {
			return nil, rdb.SearchResultsPagination{}, e
		}
		return r.Results, r.Pagination, nil
	})
	checkEventPages(t, api, ctx, "alertEvents", func(c EventSearchCriteria) ([]AlertEvent, rdb.SearchResultsPagination, error) {
		r, e := api.AlertEvents(ctx, c)
		if e != nil {
			return nil, rdb.SearchResultsPagination{}, e
		}
		return r.Results, r.Pagination, nil
	})
	checkEventPages(t, api, ctx, "stateChangeEvents", func(c EventSearchCriteria) ([]StateChangeEvent, rdb.SearchResultsPagination, error) {
		r, e := api.StateChangeEvents(ctx, c)
		if e != nil {
			return nil, rdb.SearchResultsPagination{}, e
		}
		return r.Results, r.Pagination, nil
	})
}

func checkEventPages[T any](t *testing.T, api *Api, ctx context.Context, name string, read func(EventSearchCriteria) ([]T, rdb.SearchResultsPagination, error)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		var statements []string
		callback := "capture_page_" + name
		if err := api.RDB.Database.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) { statements = append(statements, strings.ToLower(db.Statement.SQL.String())) }); err != nil {
			t.Fatal(err)
		}
		defer api.RDB.Database.Callback().Query().Remove(callback)
		no, yes := false, true
		device, anchorType, anchorToken := "device", "customer", "acme"
		for _, tc := range []struct {
			page, size int32
			more       bool
			rows       int
		}{{1, 3, true, 3}, {1, 4, false, 4}, {2, 2, false, 2}, {9, 2, false, 0}} {
			criteria := EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: tc.page, PageSize: tc.size}, DeviceToken: &device, AnchorType: &anchorType, AnchorToken: &anchorToken, IncludeTotal: &no}
			statements = nil
			got, pag, err := read(criteria)
			if err != nil {
				t.Fatal(err)
			}
			// The anchor subquery is captured as a statement of its own; what matters is that
			// nothing counts and the data read asks for exactly one row past the page.
			limited := 0
			for _, stmt := range statements {
				if strings.Contains(stmt, "count(") {
					t.Fatalf("count ran without a total: %v", statements)
				}
				if strings.HasSuffix(stmt, fmt.Sprintf("limit %d", tc.size+1)) ||
					strings.Contains(stmt, fmt.Sprintf("limit %d offset", tc.size+1)) {
					limited++
				}
			}
			if limited != 1 {
				t.Fatalf("want one data read with LIMIT %d, got %v", tc.size+1, statements)
			}
			if len(got) != tc.rows || !pag.TotalRecordsAbsent || pag.HasMore == nil || *pag.HasMore != tc.more {
				t.Fatalf("rows %d, pagination %+v", len(got), pag)
			}
			for _, include := range []*bool{nil, &yes} {
				criteria.IncludeTotal = include
				statements = nil
				want, defaultPag, err := read(criteria)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("rows differ: %v vs %v", got, want)
				}
				if defaultPag.TotalRecords != 4 || defaultPag.TotalRecordsAbsent || defaultPag.HasMore == nil || *defaultPag.HasMore != tc.more {
					t.Fatalf("default pagination %+v", defaultPag)
				}
				counted := 0
				for _, stmt := range statements {
					if strings.Contains(stmt, "count(") {
						counted++
					}
				}
				if counted != 1 {
					t.Fatalf("missing counted control: %v", statements)
				}
			}
		}
	})
}

// The default path's hasMore must not inherit the int32 overflow of pageEnd: a page far past the
// end of a 4-row result has nothing after it.
func TestDefaultHasMoreAtTheLargestPage(t *testing.T) {
	api := newReadTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	for i := 0; i < 4; i++ {
		at := time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)
		if err := api.RDB.DB(ctx).Create(&Event{EventId: []byte{byte(i + 1)}, DeviceToken: "device", OccurredTime: at, Source: "test"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		page, size int32
		more       bool
	}{{2147483647, 2, false}, {0, 2, true}, {1, 2, true}, {2, 2, false}} {
		r, err := api.Events(ctx, EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: tc.page, PageSize: tc.size}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Pagination.HasMore == nil || *r.Pagination.HasMore != tc.more {
			t.Fatalf("page %d size %d: hasMore %v, want %v (pagination %+v)", tc.page, tc.size, r.Pagination.HasMore, tc.more, r.Pagination)
		}
	}
}
