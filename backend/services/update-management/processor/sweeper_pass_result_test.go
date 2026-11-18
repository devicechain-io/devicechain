// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeadlineSweepRunOnceClassifiesWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   int
		failed int
	}{
		{"nothing due", 0, 0},
		{"all succeed", 2, 0},
		{"some fail", 3, 2},
		{"every transition fails", 3, 3},
		{"only transition fails", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, nil)
			for _, m := range schema.Migrations {
				require.NoError(t, m.Migrate(db))
			}
			require.NoError(t, rdb.RegisterTenantScoping(db))
			ctx := core.WithTenant(context.Background(), "acme")
			now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			var attempts []model.UpdateAttempt
			for i := 0; i < tc.rows; i++ {
				asg := model.UpdateAssignment{Token: fmt.Sprintf("assignment-%d", i), DeviceToken: fmt.Sprintf("dev-%d", i), Component: "firmware"}
				require.NoError(t, db.WithContext(ctx).Create(&asg).Error)
				attempt := model.UpdateAttempt{Token: fmt.Sprintf("attempt-%d", i), AssignmentId: asg.ID,
					DeviceToken: asg.DeviceToken, Component: "firmware", State: string(ota.StateQueued),
					LastReportAt: now.Add(-time.Duration(tc.rows-i) * time.Hour)}
				require.NoError(t, db.WithContext(ctx).Create(&attempt).Error)
				attempts = append(attempts, attempt)
			}
			writeErr := errors.New("injected deadline write failure")
			writes := 0
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:deadline-write", func(tx *gorm.DB) {
				writes++
				if writes < tc.failed {
					tx.AddError(errors.New("earlier deadline write failure"))
				} else if writes == tc.failed {
					tx.AddError(writeErr)
				}
			}))
			api := &model.Api{RDB: &rdb.RdbManager{Database: db}, Clock: func() time.Time { return now },
				Policy: ota.Policy{Acknowledge: time.Minute, Download: time.Minute, Verify: time.Minute,
					Install: time.Minute, Confirm: time.Minute}}
			// runOnce is driven directly, so the task (and its pass metrics) never runs.
			s := NewDeadlineSweeper(api, time.Minute, nil, &core.PeriodicTaskMetrics{})
			s.page = 2 // Failures must not prevent the keyset walk reaching the next page.
			err := s.runOnce(context.Background())
			switch {
			case tc.failed == 0:
				require.NoError(t, err)
			case tc.failed == tc.rows && tc.rows > 1:
				require.Error(t, err, "every transition failed, so the pass cannot be complete")
				require.NotErrorIs(t, err, core.ErrPassPartial)
				require.ErrorIs(t, err, writeErr)
			default:
				require.ErrorIs(t, err, core.ErrPassPartial)
				require.ErrorContains(t, err, writeErr.Error())
			}
			require.Equal(t, tc.rows, writes, "a failure must not stop the remaining rows")
			for i, attempt := range attempts {
				var got model.UpdateAttempt
				require.NoError(t, db.WithContext(ctx).First(&got, attempt.ID).Error)
				want := ota.StateTimedOut
				if i < tc.failed {
					want = ota.StateQueued
				}
				require.Equal(t, string(want), got.State)
			}
		})
	}
}
