// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListOfDBUsesTransactionAndTenantContext(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&listCountRow{}))

	ctx := core.WithTenant(context.Background(), "A")
	tx := db.WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	for _, name := range []string{"keep-z", "keep-a", "skip"} {
		require.NoError(t, tx.Create(&listCountRow{Name: name}).Error)
	}
	require.NoError(t, tx.WithContext(core.WithTenant(ctx, "B")).Create(&listCountRow{Name: "keep-b"}).Error)

	manager := &RdbManager{Database: db}
	filter := func(q *gorm.DB) *gorm.DB { return q.Where("name LIKE ?", "keep-%") }
	query, pag := manager.ListOfDB(tx, &listCountRow{}, filter, Pagination{PageNumber: 1, PageSize: 1})
	var rows []listCountRow
	require.NoError(t, query.Find(&rows).Error)
	require.EqualValues(t, 2, pag.TotalRecords, "COUNT must see the transaction's uncommitted tenant A rows")
	require.EqualValues(t, 1, pag.PageStart)
	require.EqualValues(t, 1, pag.PageEnd)
	require.Len(t, rows, 1)
	require.Equal(t, "keep-a", rows[0].Name, "filter, ordering and LIMIT must match ListOf")
	require.Equal(t, "A", rows[0].TenantId)

	// A caller-supplied transaction still fails closed without tenant context.
	query, _ = manager.ListOfDB(tx.WithContext(context.Background()), &listCountRow{}, nil, Pagination{})
	require.Error(t, query.Error)
	require.Error(t, query.Find(&rows).Error)
}
