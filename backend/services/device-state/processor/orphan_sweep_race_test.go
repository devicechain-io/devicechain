// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// pausingSource answers the sweep's lookup only after running between, which stands for
// everything that can happen while the sweep holds an answer and no lock.
type pausingSource struct {
	answer  map[string]LiveDevice
	between func(context.Context)
}

func (s *pausingSource) Tenants(context.Context) ([]string, error) { return []string{"a"}, nil }
func (s *pausingSource) Devices(ctx context.Context, _ string, _ []string) (map[string]LiveDevice, error) {
	s.between(ctx)
	return s.answer, nil
}

func raceUpdate(token string, incarnation uint64, at time.Time, value float64) model.ProjectionUpdate {
	return model.ProjectionUpdate{Tenant: "a", DeviceToken: token, OccurredAt: at, Incarnation: incarnation,
		Measurements: []model.LatestMeasurementInput{{Name: "temperature", Value: sql.NullFloat64{Float64: value, Valid: true}, OccurredTime: at}},
		Locations:    []model.LatestLocationInput{{Latitude: sql.NullFloat64{Float64: value, Valid: true}, OccurredTime: at}},
		Dropped:      []model.DroppedMeasurementInput{{Name: "text", Reason: resolvedevent.DroppedReasons[0], Value: "x", OccurredTime: at}}}
}

func requireProjection(t *testing.T, api *model.Api, ctx context.Context, token string, want int64) {
	t.Helper()
	for _, table := range []any{&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}, &model.DroppedMeasurement{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(table).Where("device_token = ?", token).Count(&n).Error)
		require.Equal(t, want, n, "%T token=%s", table, token)
	}
}

// The sweep scans incarnation 17 and asks device-management about it. Before it takes the
// row lock, 17 is deleted, 23 is created at the same token, and 23's telemetry resets the
// row in place (same id). The answer the sweep holds — 17, or no device at all — is about a
// device that no longer owns the row, and must not erase the replacement's live data.
func TestOrphanSweepStaleAnswerCannotEraseAReplacementIncarnation(t *testing.T) {
	for name, answer := range map[string]map[string]LiveDevice{
		"answer names the old incarnation": {"d": {Token: "d", ID: 17, CreatedAt: time.Now().Add(-2 * time.Hour)}},
		"answer is absence":                {},
	} {
		t.Run(name, func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, nil)
			require.NoError(t, rdb.RegisterTenantScoping(db))
			require.NoError(t, db.AutoMigrate(&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}, &model.DroppedMeasurement{}))
			api := model.NewApi(&rdb.RdbManager{Database: db})
			ctx := core.WithTenant(context.Background(), "a")
			at := time.Now().UTC().Add(-time.Hour)
			require.NoError(t, api.MergeProjectionBatch(ctx, []model.ProjectionUpdate{raceUpdate("d", 17, at, 17)}))
			var before model.DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&before).Error)
			require.Equal(t, sql.NullInt64{Int64: 17, Valid: true}, before.Incarnation)

			src := &pausingSource{answer: answer, between: func(ctx context.Context) {
				require.NoError(t, api.MergeProjectionBatch(ctx, []model.ProjectionUpdate{raceUpdate("d", 23, at.Add(time.Minute), 23)}))
			}}
			require.NoError(t, (&OrphanSweep{Api: api, Source: src}).Sweep(context.Background()))

			var after model.DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&after).Error)
			require.Equal(t, before.ID, after.ID, "the reset keeps the row's id, which is why the id check alone cannot see it")
			require.Equal(t, sql.NullInt64{Int64: 23, Valid: true}, after.Incarnation)
			requireProjection(t, api, ctx, "d", 1)
			var m model.LatestMeasurement
			require.NoError(t, api.RDB.DB(ctx).First(&m).Error)
			require.Equal(t, float64(23), m.Value.Float64)
		})
	}
}
