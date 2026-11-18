// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAuditActorByteBound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor string
		over  bool
	}{
		{"ASCII at limit", strings.Repeat("a", 256), false},
		{"ASCII over limit", strings.Repeat("a", 257), true},
		{"UTF-8 at limit", strings.Repeat("\u00e9", 128), false},
		{"UTF-8 one byte over", strings.Repeat("\u00e9", 128) + "a", true},
		{"UTF-8 one rune over", strings.Repeat("\u00e9", 129), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, RegisterTenantScoping(db))
			require.NoError(t, db.AutoMigrate(&AuditEvent{}))
			ctx := core.WithTenant(context.Background(), "actor-bound")
			mgr := &RdbManager{Database: db}
			seedActor := tc.actor
			if tc.over {
				seedActor = "unrelated"
			}
			require.NoError(t, mgr.DB(ctx).Create(&AuditEvent{
				TenantId: "actor-bound", Actor: seedActor, Category: "auth", Operation: "login",
			}).Error)

			queries := 0
			// SQLite has no ILIKE. Translate only that operator in this fixture so
			// boundary inputs execute real reads; PostgreSQL matching is covered by
			// TestAuditActorFilterIsLiteral. Count both the count and page queries.
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:audit_actor", func(tx *gorm.DB) {
				queries++
				c := tx.Statement.Clauses["WHERE"]
				if where, ok := c.Expression.(clause.Where); ok {
					for i, expression := range where.Exprs {
						if expr, ok := expression.(clause.Expr); ok {
							expr.SQL = strings.ReplaceAll(expr.SQL, " ILIKE ", " LIKE ")
							where.Exprs[i] = expr
						}
					}
					c.Expression = where
					tx.Statement.Clauses["WHERE"] = c
				}
			}))
			page, err := mgr.AuditEvents(ctx, AuditEventSearchCriteria{
				Pagination: Pagination{PageNumber: 1, PageSize: 10}, Actor: &tc.actor,
			})
			if tc.over {
				le, ok := limit.As(err)
				require.True(t, ok, "want limit refusal, got page=%+v err=%v", page, err)
				require.Equal(t, "LIMIT_EXCEEDED", le.Extensions()["code"])
				require.Equal(t, "actor bytes", le.What)
				require.Equal(t, 256, le.Max)
				require.Equal(t, len(tc.actor), le.Got)
				require.Nil(t, page)
				require.Zero(t, queries, "refuse before even the count query")
				return
			}
			require.NoError(t, err)
			require.Len(t, page.Results, 1)
			require.Equal(t, tc.actor, page.Results[0].Actor)
			require.Equal(t, int32(1), page.Pagination.TotalRecords)
			require.Equal(t, 2, queries)
		})
	}
}
