// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type hierarchyLeaderKey struct{}

func TestContainmentConcurrentWritersPostgres(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprintf("cycle=%t", cycle), func(t *testing.T) {
			api := NewApi(newPostgresRdbManager(t))
			ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(),
				fmt.Sprintf("hierarchy-race-%d", time.Now().UnixNano())), 45*time.Second)
			defer cancel()
			assets := seedAssets(t, api, ctx, "a", "b", "c", "d", "child")
			_, err := api.EnsureContainmentType(ctx)
			require.NoError(t, err)
			inserted, release := make(chan int, 1), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			// Park the first writer after its insert but before commit. All rivals
			// must wait on THIS transaction's advisory lock, not on timing luck.
			require.NoError(t, api.RDB.Database.Callback().Create().After("gorm:create").Register("test:park-hierarchy", func(tx *gorm.DB) {
				if tx.Statement.Table != "entity_relationships" || tx.Statement.Context.Value(hierarchyLeaderKey{}) != true || tx.Error != nil {
					return
				}
				var pid int
				probe := tx.Session(&gorm.Session{NewDB: true})
				if err := probe.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					tx.AddError(err)
					return
				}
				inserted <- pid
				select {
				case <-release:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
			}))
			done := make(chan error, 4)
			child := "child"
			if cycle {
				child = "b"
			}
			go func() {
				_, err := api.SetAssetParent(context.WithValue(ctx, hierarchyLeaderKey{}, true), child, "a")
				done <- err
			}()
			var leader int
			select {
			case leader = <-inserted:
			case err := <-done:
				t.Fatalf("leader failed before insertion: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			rivals := 3
			if cycle {
				rivals = 1
			}
			for _, parent := range []string{"b", "c", "d"}[:rivals] {
				go func(parent string) {
					target := child
					if cycle {
						target = "a"
					}
					_, err := api.SetAssetParent(ctx, target, parent)
					done <- err
				}(parent)
			}
			require.Eventually(t, func() bool {
				var waiting int64
				err := api.RDB.Database.WithContext(ctx).Raw(`SELECT count(*) FROM pg_locks
					WHERE locktype = 'advisory' AND NOT granted AND ? = ANY(pg_blocking_pids(pid))`, leader).Scan(&waiting).Error
				return err == nil && waiting >= int64(rivals)
			}, 15*time.Second, 20*time.Millisecond, "rivals never waited on leader's advisory lock")
			close(release)
			refused := 0
			for i := 0; i <= rivals; i++ {
				select {
				case err := <-done:
					if cycle && err != nil {
						require.ErrorIs(t, err, ErrAssetParentCycle)
						refused++
					} else {
						require.NoError(t, err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if cycle {
				require.Equal(t, 1, refused)
				for _, token := range []string{"a", "b"} {
					_, err := api.AssetAncestors(ctx, token)
					require.NoError(t, err, "cycle corrupted ancestor traversal")
				}
			} else {
				var count int64
				require.NoError(t, api.containmentEdges(api.RDB.DB(ctx)).Where("target_id = ?", assets["child"].ID).Count(&count).Error)
				require.EqualValues(t, 1, count)
			}
		})
	}
}
