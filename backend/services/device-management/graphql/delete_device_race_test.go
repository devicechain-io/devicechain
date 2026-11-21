// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// SQLite has no FOR UPDATE. Supply the result of the gateway-check lock to
// exercise its mapping through the served schema; the Postgres test forces the
// actual two-connection interleaving.
func TestDeleteDeviceGatewayLockMissAnswersFalse(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%t", wrapped), func(t *testing.T) {
			f := newIntegrationWireFixture(t)
			require.NoError(t, f.db.AutoMigrate(&model.EntityGroupMembership{}))
			ctx := f.operator()
			hits := 0
			require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register("test:gateway-lock-miss", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*model.Device); !ok {
					return
				}
				if _, locking := tx.Statement.Clauses["FOR"]; !locking {
					return
				}
				hits++
				err := gorm.ErrRecordNotFound
				if wrapped {
					err = fmt.Errorf("gateway lock: %w", err)
				}
				tx.AddError(err)
			}))
			// Never-existing tokens keep answering false without reaching the lock.
			missing := serve(t, ctx, `mutation { deleteDevice(token: "never-existed") }`, nil)
			require.Empty(t, missing.errs)
			require.Equal(t, false, missing.data["deleteDevice"])
			require.Zero(t, hits)

			got := serve(t, ctx, `mutation { deleteDevice(token: "acme-pump") }`, nil)
			require.Equal(t, 1, hits, "the gateway-check lock must be reached")
			require.Empty(t, got.errs)
			require.Equal(t, false, got.data["deleteDevice"])
			var n int64
			require.NoError(t, f.api.RDB.DB(ctx).Model(&model.PendingEntityDeletion{}).Count(&n).Error)
			require.Zero(t, n, "a losing delete must not queue a deletion")
		})
	}
}
