// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type orphanFake struct {
	live     map[string]map[string]LiveDevice
	failures map[string]bool
	calls    map[string]int
}

func (f *orphanFake) Tenants(context.Context) ([]string, error) { return []string{"bad", "good"}, nil }
func (f *orphanFake) Devices(ctx context.Context, tenant string, tokens []string) (map[string]LiveDevice, error) {
	actual, _ := core.TenantFromContext(ctx)
	if actual != tenant {
		return nil, errors.New("wrong tenant context")
	}
	f.calls[tenant]++
	if f.failures[tenant] {
		return nil, errors.New("lookup unavailable")
	}
	return f.live[tenant], nil
}

func TestOrphanSweepDeletesOldIdentitiesAndStampsCurrentOnes(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}, &model.DroppedMeasurement{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	base := time.Now().UTC().Add(-time.Hour)
	fake := &orphanFake{live: map[string]map[string]LiveDevice{}, failures: map[string]bool{"bad": true}, calls: map[string]int{}}
	for _, tenant := range []string{"bad", "good"} {
		ctx := core.WithTenant(context.Background(), tenant)
		fake.live[tenant] = map[string]LiveDevice{}
		for _, token := range []string{"missing", "mismatch", "older", "newer", "same", "equal"} {
			row := model.DeviceState{Model: gorm.Model{CreatedAt: base, UpdatedAt: base}, DeviceToken: token}
			switch token {
			case "mismatch":
				row.Incarnation = sql.NullInt64{Int64: 17, Valid: true}
			case "same":
				row.Incarnation = sql.NullInt64{Int64: 23, Valid: true}
			case "newer":
				row.CreatedAt = base.Add(2 * time.Minute)
			case "equal":
				row.CreatedAt = base.Add(time.Minute)
			}
			require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
			require.NoError(t, api.RDB.DB(ctx).Create(&model.LatestLocation{DeviceToken: token}).Error)
			if token != "missing" {
				fake.live[tenant][token] = LiveDevice{Token: token, ID: 23, CreatedAt: base.Add(time.Minute)}
			}
		}
	}
	sweep := &OrphanSweep{Api: api, Source: fake}
	require.ErrorIs(t, sweep.Sweep(context.Background()), core.ErrPassPartial)
	for _, tenant := range []string{"bad", "good"} {
		ctx := core.WithTenant(context.Background(), tenant)
		var rows []model.DeviceState
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Order("device_token").Find(&rows).Error)
		if tenant == "bad" {
			require.Len(t, rows, 6)
			for _, row := range rows {
				if row.DeviceToken != "mismatch" && row.DeviceToken != "same" {
					require.False(t, row.Incarnation.Valid)
				}
			}
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&model.LatestLocation{}).Count(&n).Error)
			require.Equal(t, int64(6), n)
		} else {
			require.Len(t, rows, 3)
			require.Equal(t, []string{"equal", "newer", "same"}, []string{rows[0].DeviceToken, rows[1].DeviceToken, rows[2].DeviceToken})
			for _, row := range rows {
				require.Equal(t, sql.NullInt64{Int64: 23, Valid: true}, row.Incarnation)
			}
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&model.LatestLocation{}).Count(&n).Error)
			require.Equal(t, int64(3), n)
		}
	}
	require.Equal(t, 1, fake.calls["bad"])
	require.Equal(t, 1, fake.calls["good"])
}

func TestOrphanSweepMissingAnswerIsAnError(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceState{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "good")
	require.NoError(t, api.RDB.DB(ctx).Create(&model.DeviceState{DeviceToken: "truck-7"}).Error)
	fake := &orphanFake{live: map[string]map[string]LiveDevice{}, failures: map[string]bool{}, calls: map[string]int{}}
	d, st, err := (&OrphanSweep{Api: api, Source: fake}).sweepTenant(ctx, "good")
	require.Error(t, err)
	require.Zero(t, d)
	require.Zero(t, st)
	var row model.DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
	require.False(t, row.Incarnation.Valid)
}
