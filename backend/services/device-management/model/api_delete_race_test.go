// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeleteDeviceGatewayLockErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"row gone", gorm.ErrRecordNotFound},
		{"database failure", errors.New("gateway lock failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newDeleteEmitTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			device := &Device{}
			device.Token = "victim"
			require.NoError(t, api.RDB.DB(ctx).Create(device).Error)
			hits := 0
			require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:gateway-lock-error", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*Device); !ok {
					return
				}
				if _, locking := tx.Statement.Clauses["FOR"]; locking {
					hits++
					tx.AddError(tc.err)
				}
			}))
			deleted, err := api.DeleteDevice(ctx, "victim")
			require.Equal(t, 1, hits, "exercise the gateway check's locked read")
			require.False(t, deleted)
			if errors.Is(tc.err, gorm.ErrRecordNotFound) {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.err, "other database errors must still propagate")
			}
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&PendingEntityDeletion{}).Count(&n).Error)
			require.Zero(t, n, "a losing delete must not emit a deletion fact")
		})
	}
}
