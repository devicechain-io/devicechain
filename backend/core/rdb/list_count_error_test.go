// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type listCountRow struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
}

func (listCountRow) TableName() string    { return "list_count_rows" }
func (listCountRow) DefaultOrder() string { return "list_count_rows.name ASC" }

// Exercise both public entry points with the same failure and recovery assertions.
func TestListCountError(t *testing.T) {
	for _, all := range []bool{false, true} {
		name := "ListOf"
		if all {
			name = "ListAllOf"
		}
		for _, canceled := range []bool{false, true} {
			failure := "sentinel"
			if canceled {
				failure = "canceled"
			}
			t.Run(name+"/"+failure, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sqlDB.Close() })
				if err := RegisterTenantScoping(db); err != nil {
					t.Fatal(err)
				}
				if err := db.AutoMigrate(&listCountRow{}); err != nil {
					t.Fatal(err)
				}
				ctx := core.WithTenant(context.Background(), "A")
				// Anti-sorted rows, another tenant, and a filtered-out row distinguish
				// correct counts and data from queries that lose context or filters.
				for _, seed := range []struct{ tenant, name string }{
					{"A", "keep-z"}, {"A", "keep-a"}, {"A", "skip"}, {"B", "keep-b"},
				} {
					if err := db.WithContext(core.WithTenant(context.Background(), seed.tenant)).
						Create(&listCountRow{Name: seed.name}).Error; err != nil {
						t.Fatal(err)
					}
				}
				manager := &RdbManager{Database: db}
				list := func(ctx context.Context) (*gorm.DB, SearchResultsPagination) {
					filters := func(q *gorm.DB) *gorm.DB { return q.Where("name LIKE ?", "keep-%") }
					if all {
						return manager.ListAllOf(ctx, &listCountRow{}, filters)
					}
					return manager.ListOf(ctx, &listCountRow{}, filters, Pagination{PageNumber: 1, PageSize: 1})
				}

				countAttempts, dataSelects := 0, 0
				const failCallback = "test:list_count_failure"
				wantErr := errors.New("list COUNT failed")
				failedCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				if canceled {
					wantErr = context.Canceled
				}
				if err := db.Callback().Query().Before("gorm:query").Register(failCallback, func(q *gorm.DB) {
					if q.Statement.Table != "list_count_rows" {
						return
					}
					// GORM Count uses an *int64 destination; Find uses the row slice.
					if _, isCount := q.Statement.Dest.(*int64); !isCount {
						return
					}
					countAttempts++
					if canceled {
						// Cancel during COUNT and let the driver return the context error.
						cancel()
					} else {
						q.AddError(wantErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Query().After("gorm:query").Register("test:list_data_selects", func(q *gorm.DB) {
					if q.Statement.Table == "list_count_rows" && q.Error == nil {
						if _, isCount := q.Statement.Dest.(*int64); !isCount {
							dataSelects++
						}
					}
				}); err != nil {
					t.Fatal(err)
				}

				query, _ := list(failedCtx)
				if !errors.Is(query.Error, wantErr) {
					t.Errorf("list error = %v, want %v", query.Error, wantErr)
				}
				var rows []listCountRow
				if err := query.Find(&rows).Error; !errors.Is(err, wantErr) {
					t.Errorf("Find error = %v, want %v", err, wantErr)
				}
				if countAttempts != 1 || dataSelects != 0 || len(rows) != 0 {
					t.Fatalf("failed list: count attempts=%d, data SELECTs=%d, rows=%+v", countAttempts, dataSelects, rows)
				}

				if err := db.Callback().Query().Remove(failCallback); err != nil {
					t.Fatal(err)
				}
				// With the callback removed and a live context, the same data query
				// succeeds, preserving tenant isolation, filters, order and pagination.
				query, pag := list(ctx)
				if err := query.Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				wantRows := 1
				if all {
					wantRows = 2
				}
				if dataSelects != 1 || len(rows) != wantRows || pag.TotalRecords != 2 ||
					pag.PageStart != 1 || pag.PageEnd != int32(wantRows) {
					t.Fatalf("recovered list: SELECTs=%d, rows=%+v, pagination=%+v", dataSelects, rows, pag)
				}
				if rows[0].Name != "keep-a" || rows[0].TenantId != "A" {
					t.Fatalf("first row = %+v", rows[0])
				}
				if all && (rows[1].Name != "keep-z" || rows[1].TenantId != "A") {
					t.Fatalf("second row = %+v", rows[1])
				}
			})
		}
	}
}
