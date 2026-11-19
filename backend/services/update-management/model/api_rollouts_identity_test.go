// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRolloutHoldsUnknownIncarnationWithoutAdoptingOrSkipping(t *testing.T) {
	for _, current := range []string{"none", "stamped", "legacy"} {
		t.Run(current, func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			ctx := tenantCtx("acme")
			db := e.api.RDB.DB(ctx)
			if current != "none" {
				v, err := e.api.AssignArtifact(ctx, "dev-0", "fw-2")
				require.NoError(t, err)
				if current == "legacy" {
					require.NoError(t, db.Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).
						Update("device_incarnation", nil).Error)
				}
			}
			e.create(t, "r1", 1, nil)
			e.api.Devices = &syncDevices{err: errors.New("hold inline dispatch")}
			_, err := e.api.StartRollout(ctx, "r1")
			require.NoError(t, err)
			before := e.targets(t, "r1", nil)
			var assignments []UpdateAssignment
			var attempts []UpdateAttempt
			require.NoError(t, db.Find(&assignments).Error)
			require.NoError(t, db.Find(&attempts).Error)
			// Legacy rows could otherwise be adopted by the creation-time fallback.
			e.api.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
				return DeviceFacts{CreatedAt: e.clock.Add(-time.Hour), Profile: "gateway"}, true, nil
			})
			res := e.step(t, "r1")
			require.Zero(t, res.Dispatched)
			require.Zero(t, res.Skipped)
			require.Equal(t, TargetPending, e.targets(t, "r1", nil)[0].Target.Status)
			require.Equal(t, before, e.targets(t, "r1", nil), "unknown identity must write nothing")
			var assignmentsAfter []UpdateAssignment
			var attemptsAfter []UpdateAttempt
			require.NoError(t, db.Find(&assignmentsAfter).Error)
			require.NoError(t, db.Find(&attemptsAfter).Error)
			require.Equal(t, assignments, assignmentsAfter)
			require.Equal(t, attempts, attemptsAfter)
		})
	}
}

func TestAdoptCurrentLocksAssignmentOnPostgres(t *testing.T) {
	// DryRun verifies the PostgreSQL clause in the ordinary gate; the integration test proves waiting.
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost user=postgres dbname=unused"}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	stop := errors.New("query inspected")
	var query string
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:inspect_adoption", func(tx *gorm.DB) {
		query = tx.Statement.SQL.String()
		tx.AddError(stop) // Stop before adoptCurrent asks for the attempt.
	}))
	_, err = (&Api{}).adoptCurrent(db, "dev-0", &Artifact{Component: "firmware"}, DeviceFacts{Incarnation: 17})
	require.ErrorIs(t, err, stop)
	require.Contains(t, query, "FOR UPDATE")
}
