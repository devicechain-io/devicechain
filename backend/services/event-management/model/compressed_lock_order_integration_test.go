// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The two SHARE-mode index builds lock the compressed relations of events' compressed chunks
// in the same LOCK statement as events itself, so a session writing to one is met at the lock,
// under lock_timeout, and reported busy. Were the lookup to return nothing, the LOCK would take
// events alone and the build would meet the holder inside its build allowance — here shorter
// than lock_timeout — and fail there instead. TestIntegrationTimeLeadingKeysLockChunksAndCompressedRelationsFirst
// is the same check for the ACCESS EXCLUSIVE re-key.
func TestIntegrationIndexBuildsLockCompressedRelationsFirst(t *testing.T) {
	timing := timeLeadingKeysTiming{lockTimeout: time.Second, lockAttempt: 1500 * time.Millisecond,
		pause: 200 * time.Millisecond, tableBuild: 500 * time.Millisecond, minBuild: 10 * time.Millisecond,
		budget: 2500 * time.Millisecond, countTimeout: 10 * time.Second, maxRows: 1_000_000, maxChunks: eventStoreMaxChunks, buildMemory: "64MB"}

	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T) (string, *gorm.DB)
		migrate  func(db *gorm.DB) error
		replaces string
		builds   string
		busy     string
	}{
		{"tenant-led device index", func(t *testing.T) (string, *gorm.DB) {
			inst, _, sys := afterRekey(t, "ittdizipheld")
			return inst, sys
		}, func(db *gorm.DB) error { return newTenantDeviceIndexSchema(timing).Migrate(db) },
			tenantDeviceIndex.replaces, tenantDeviceIndex.name, "stayed busy"},
		{"device-scoped alternate-id key", func(t *testing.T) (string, *gorm.DB) {
			inst, _, sys := beforeAltKey(t, "italtidzipheld")
			return inst, sys
		}, func(db *gorm.DB) error { return newDeviceAltIdKeySchema(timing).Migrate(db) },
			deviceAltIdKey.replaces, deviceAltIdKey.name, "failed and was rolled back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, sys := tc.setup(t)
			twoChunkSeed(t, sys)
			compressOldest(t, sys)
			rels, err := compressedRelations(sys, "events")
			require.NoError(t, err)
			require.Len(t, rels, 1, "compressOldest compresses exactly one events chunk")

			holder := connectInstance(t, inst)
			_, err = holder.Exec(context.Background(), `BEGIN; LOCK TABLE `+rels[0]+` IN ROW EXCLUSIVE MODE`)
			require.NoError(t, err)
			defer func() { _, _ = holder.Exec(context.Background(), `ROLLBACK`) }()

			err = tc.migrate(sys)
			require.Error(t, err, "a held compressed relation blocks the build")
			assert.Contains(t, err.Error(), tc.busy)
			assert.NotContains(t, err.Error(), "dcctl destroy", "a held compressed relation must never read as too slow")
			var pgErr *pgconn.PgError
			require.True(t, errors.As(err, &pgErr), "the last server error stays wrapped: %v", err)
			assert.Equal(t, "55P03", pgErr.Code, "met at the LOCK, under lock_timeout, not inside the build")
			assert.True(t, hasIndex(t, sys, tc.replaces), "the old index stays")
			assert.False(t, hasIndex(t, sys, tc.builds), "the new index does not exist")
		})
	}
}
