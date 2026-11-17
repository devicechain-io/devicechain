// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"testing/synctest"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestDispatchClaimsHonourExpiry(t *testing.T) {
	for _, byToken := range []bool{false, true} {
		name := "MarkSent"
		if byToken {
			name = "MarkSentByToken"
		}
		for _, status := range []CommandStatus{CommandQueued, CommandHeld, CommandParked} {
			for _, horizon := range []string{"past", "equal", "null"} {
				t.Run(name+"/"+status.String()+"/"+horizon, func(t *testing.T) {
					// Freeze time through the public claim, including the equality boundary.
					synctest.Test(t, func(t *testing.T) {
						api := newTestApi(t)
						db, err := api.RDB.Database.DB()
						require.NoError(t, err)
						defer db.Close()
						ctx := core.WithTenant(context.Background(), "A")
						now := time.Now()
						expires := sql.NullTime{Time: now, Valid: true}
						if horizon == "past" {
							expires.Time = now.Add(-time.Second)
						} else if horizon == "null" {
							expires = sql.NullTime{}
						}
						seedDrainable(t, api, ctx, 1, "dev-1", status, expires)
						cmd := loadOrFail(t, api, ctx, 1)
						var nonce string
						var claimed bool
						if byToken {
							nonce, claimed, err = api.MarkSentByToken(ctx, cmd.Token)
						} else {
							nonce, claimed, err = api.MarkSent(ctx, cmd.ID)
						}
						require.NoError(t, err)
						got := loadOrFail(t, api, ctx, 1)
						if horizon == "past" {
							require.False(t, claimed, "an expired but unswept command must not be dispatched")
							require.Empty(t, nonce)
							require.Equal(t, status.String(), got.Status)
							require.False(t, got.SentTime.Valid)
							require.False(t, got.DispatchNonce.Valid)
							count, _, err := api.ExpireStale(core.WithSystemContext(ctx), now)
							require.NoError(t, err)
							require.Equal(t, int64(1), count)
							require.Equal(t, CommandExpired.String(), loadOrFail(t, api, ctx, 1).Status)
						} else {
							require.True(t, claimed)
							require.NotEmpty(t, nonce)
							require.Equal(t, CommandSent.String(), got.Status)
							require.Equal(t, nonce, got.DispatchNonce.String)
							require.True(t, got.SentTime.Valid)
						}
					})
				})
			}
		}
	}
}
