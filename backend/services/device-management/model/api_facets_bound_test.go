// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/limit"
	"log"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestFacetValuesBound(t *testing.T) {
	for _, tc := range []struct {
		facet  FacetKind
		table  string
		column string
	}{
		{FacetManufacturer, "device_types", "manufacturer"},
		{FacetModel, "device_types", "model"},
		{FacetCategory, "device_profiles", "category"},
	} {
		t.Run(string(tc.facet), func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, nil)
			require.NoError(t, rdb.RegisterTenantScoping(db))
			// Only the columns read by this query are needed; query through the
			// real model below so tenant and soft-delete callbacks still apply.
			require.NoError(t, db.Exec(fmt.Sprintf(
				"CREATE TABLE %s (id INTEGER PRIMARY KEY, tenant_id TEXT, deleted_at DATETIME, %s TEXT)",
				tc.table, tc.column)).Error)
			api := NewApi(&rdb.RdbManager{Database: db})
			ctx := core.WithTenant(context.Background(), "acme")
			seed := func(tenant string, value any, deletedAt any) {
				t.Helper()
				// Raw fixture inserts permit explicit cross-tenant rows without
				// bypassing the callbacks on the FacetValues read under test.
				require.NoError(t, db.Exec(fmt.Sprintf(
					"INSERT INTO %s (tenant_id, %s, deleted_at) VALUES (?, ?, ?)",
					tc.table, tc.column), tenant, value, deletedAt).Error)
			}
			var sql bytes.Buffer
			api.RDB.Database = db.Session(&gorm.Session{Logger: logger.New(log.New(&sql, "", 0), logger.Config{LogLevel: logger.Info})})
			values, err := api.FacetValues(ctx, tc.facet)
			require.NoError(t, err)
			require.Equal(t, []string{}, values)

			want := make([]string, 1000)
			// Reverse insertion order makes the ordering assertion meaningful.
			for i := 999; i >= 0; i-- {
				want[i] = fmt.Sprintf("value-%04d", i)
				seed("acme", want[i], nil)
			}
			seed("acme", want[0], nil) // duplicates do not consume capacity
			seed("acme", "", nil)
			seed("acme", nil, nil)
			seed("acme", "deleted-only", "2026-01-01 00:00:00")
			seed("other", "other-only", nil) // must not trigger overflow
			sql.Reset()
			values, err = api.FacetValues(ctx, tc.facet)
			require.NoError(t, err)
			require.Equal(t, want, values)
			require.Contains(t, sql.String(), "LIMIT 1001")
			require.Contains(t, sql.String(), "ORDER BY "+tc.column)

			seed("acme", "value-1000", nil)
			sql.Reset()
			values, err = api.FacetValues(ctx, tc.facet)
			var limitErr *limit.Error
			require.ErrorAs(t, err, &limitErr)
			require.Contains(t, err.Error(), string(tc.facet))
			require.Nil(t, values, "overflow must not serve a partial list")
			require.Contains(t, sql.String(), "LIMIT 1001", "overflow refusal alone does not bound materialization")

			sql.Reset()
			values, err = api.FacetValues(ctx, FacetKind("unknown"))
			require.Error(t, err)
			require.Nil(t, values)
			require.Empty(t, sql.String(), "unknown facets must not reach SQL")
		})
	}
}
