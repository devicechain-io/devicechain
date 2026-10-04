// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/gorm"
)

// A save that lands in the same clock instant as the read must still move the policy's
// version, or an editor holding the version they read is not refused and overwrites it.
//
// 🔴 THIS FIXTURE IS NOT staleTestApi. That one's clock ticks forward on every call, so the
// version always moves by itself and a test on it cannot fail when the write stops moving
// it. Here the clock is frozen.

// frozenAt is an instant with sub-microsecond digits. It is in the past on purpose.
var frozenAt = time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)

func frozenPolicyApi(t *testing.T, now *time.Time) *Api {
	t.Helper()
	return newTestApiConfigured(t, func(t *testing.T, d *gorm.DB) {
		sqlDB, err := d.DB()
		if err != nil {
			t.Fatalf("reach the pool: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		d.Config.NowFunc = func() time.Time { return *now }
	})
}

func TestEveryPolicyWriteMovesTheVersion(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "update without a precondition"
		if guarded {
			name = "guarded update"
		}
		t.Run(name, func(t *testing.T) {
			now := frozenAt
			api := frozenPolicyApi(t, &now)
			ctx := tenantCtx("A")
			seedStalePolicy(t, api, ctx)
			read := readStalePolicy(t, api, ctx)
			v0 := versionOf(read)

			var expected *string
			if guarded {
				expected = &v0
			}
			if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
				Name: dcgraphql.OptionalStringOf("Writer"),
			}, expected); err != nil {
				t.Fatalf("a save from the current version was refused: %v", err)
			}

			stored := readStalePolicy(t, api, ctx)
			if !stored.UpdatedAt.After(read.UpdatedAt) {
				t.Fatalf("the write did not move the version forward: read %v stored %v", read.UpdatedAt, stored.UpdatedAt)
			}
			if stored.Name.String != "Writer" {
				t.Fatalf("stored name is %q, want %q", stored.Name.String, "Writer")
			}

			// The editor who read the version before the write is refused and changes nothing.
			_, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
				Name: dcgraphql.OptionalStringOf("Late"),
			}, &v0)
			if err != ErrConflict {
				t.Fatalf("a save from the version read before the write returned %v, want ErrConflict", err)
			}
			if after := readStalePolicy(t, api, ctx); after.Name.String != "Writer" {
				t.Fatalf("the refused save changed the name to %q", after.Name.String)
			}
		})
	}
}

// The clock moving on is what is stored: the floor never invents a version when time has moved.
func TestPolicyWriteStoresTheClockWhenItHasMoved(t *testing.T) {
	now := frozenAt
	api := frozenPolicyApi(t, &now)
	ctx := tenantCtx("A")
	seedStalePolicy(t, api, ctx)

	now = frozenAt.Add(time.Hour)
	if _, err := api.UpdateNotificationPolicy(ctx, stalePolicyToken, &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Writer"),
	}, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if stored := readStalePolicy(t, api, ctx); !stored.UpdatedAt.Equal(now) {
		t.Fatalf("stored version %v, want the clock %v", stored.UpdatedAt, now)
	}
}
