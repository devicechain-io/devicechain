// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGroupPurgeCommitRetryAndRestart(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "both-hold-state", true: "empty-sibling"}[empty], func(t *testing.T) {
			nc := purgeConn(t, purgeBroker(t))
			first := newEvictionRig(t, newTestStore(t), "acme", "globex")
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, rdb.RegisterTenantScoping(db))
			require.NoError(t, db.AutoMigrate(&model.DetectSnapshot{}))
			tenants := []string{"acme", "globex"}
			if empty {
				tenants = []string{"globex"}
			}
			second := newEvictionRig(t, model.NewSnapshotStore(&rdb.RdbManager{Database: db}), tenants...)
			second.rp.worker = &workerIdentity{group: 1, partition: groupPartition(1)}
			second.rp.dirty = true
			require.True(t, second.rp.checkpoint(context.Background()))
			// The sibling also exercises eviction and restart with concurrent DETECT shards.
			second.rp.cfg.Shards = 2
			require.NoError(t, second.rp.restore(context.Background()))
			defer func() { second.stop(); second.rp.closeDetector() }()
			first.rp.groupWorkers = []*ResolvedEventsProcessor{second.rp}
			// Block a real SQL update, then roll it back before commit. A clean
			// reply during this window would let deletion finish over stale state.
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var fail atomic.Bool
			fail.Store(!empty)
			require.NoError(t, db.Callback().Update().After("gorm:update").Before("gorm:commit_or_rollback_transaction").Register("purge-fail-commit", func(tx *gorm.DB) {
				if fail.CompareAndSwap(true, false) {
					entered <- struct{}{}
					<-release
					tx.AddError(errors.New("injected commit failure"))
				}
			}))
			defer close(release)
			first.start(t)
			second.start(t)
			responder := NewTenantPurgeResponder(nc, evictInstance, first.rp)
			require.NoError(t, responder.Start())
			defer responder.Stop()
			expected := []string{singletonPartition, groupPartition(1)}
			if !empty {
				// The coordinator must keep gathering after g0's committed answer.
				done := make(chan messaging.DetectPurgeResult, 1)
				go func() {
					r, _ := messaging.PurgeTenantDetect(context.Background(), nc, evictInstance, "acme", expected)
					done <- r
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("group 1 never reached its commit")
				}
				select {
				case <-done:
					t.Fatal("purge finished before group 1 committed")
				case <-time.After(500 * time.Millisecond):
				}
				release <- struct{}{}
				var result messaging.DetectPurgeResult
				select {
				case result = <-done:
				case <-time.After(6 * time.Second):
					t.Fatal("gather did not end")
				}
				require.Len(t, result.Replies, 2, "a failed group commit must report its failure")
				failures := map[string]string{}
				for _, reply := range result.Replies {
					failures[reply.PartitionId] = reply.Error
				}
				require.Empty(t, failures[singletonPartition])
				require.NotEmpty(t, failures[groupPartition(1)])
				snap, ok, err := second.store.Load(context.Background(), groupPartition(1))
				require.NoError(t, err)
				require.True(t, ok)
				require.Contains(t, string(snap.Payload), "acme/", "rollback must retain the victim on disk")
			}
			result, err := messaging.PurgeTenantDetect(context.Background(), nc, evictInstance, "acme", expected)
			require.NoError(t, err)
			require.Len(t, result.Replies, 2, "both checkpoint partitions must answer, including an empty group")
			ids := map[string]bool{}
			for _, reply := range result.Replies {
				require.Empty(t, reply.Error)
				ids[reply.PartitionId] = true
				if empty && reply.PartitionId == groupPartition(1) {
					require.Zero(t, reply.Evicted)
				}
			}
			require.True(t, ids[singletonPartition] && ids[groupPartition(1)])
			first.stop()
			second.stop()
			for _, rig := range []*evictionRig{first, second} {
				// Restart through the processor's actual store/restore path, then
				// advance beyond the old absence timers with both rule sets loaded.
				w := newTestProcessor(rig.store, nil, 1000)
				w.cfg.Shards = rig.rp.cfg.Shards
				w.worker = rig.rp.worker
				w.registry = evictionRules(t, "acme", "globex")
				require.NoError(t, w.restore(context.Background()))
				payload, err := w.engine.Snapshot()
				require.NoError(t, err)
				require.False(t, strings.Contains(string(payload), "acme/"))
				require.Contains(t, string(payload), "globex/")
				w.engine.Advance(testBase.Add(24 * time.Hour))
				detections := w.engine.Drain()
				require.NotEmpty(t, detections, "the surviving tenant's restored absence timer must still fire")
				for _, det := range detections {
					body, err := json.Marshal(det)
					require.NoError(t, err)
					require.NotContains(t, string(body), "acme/")
				}
				if p, ok := w.engine.(detectcore.Pipelined); ok {
					p.Close()
				}
			}
			// Stop removes every partition subscription, not only the base one.
			require.NoError(t, responder.Stop())
			require.NoError(t, nc.Flush())
			_, err = nc.Request(messaging.DetectPurgeSubject(evictInstance), []byte(`{"tenant":"acme"}`), time.Second)
			require.ErrorIs(t, err, nats.ErrNoResponders)
		})
	}
}

func TestGroupPurgeBeforeFirstCheckpointReportsFailureAndRetries(t *testing.T) {
	nc := purgeConn(t, purgeBroker(t))
	first := newEvictionRig(t, newTestStore(t), "globex")
	second := newEvictionRig(t, newTestStore(t), "acme", "globex")
	second.rp.worker = &workerIdentity{group: 1, partition: groupPartition(1)}
	// The rig seeded a singleton row; g1 has live state but has never written its row.
	_, exists, err := second.store.Load(context.Background(), groupPartition(1))
	require.NoError(t, err)
	require.False(t, exists)
	first.rp.groupWorkers = []*ResolvedEventsProcessor{second.rp}
	first.start(t)
	defer first.stop()
	second.start(t)
	defer second.stop()
	responder := NewTenantPurgeResponder(nc, evictInstance, first.rp)
	// Fail before the loop can erase the tenant, then allow the real eviction on retry.
	realEvict := responder.children[0].evict
	var fail atomic.Bool
	fail.Store(true)
	responder.children[0].evict = func(ctx context.Context, tenant string) (int64, error) {
		if fail.Load() {
			return 0, errors.New("g1 eviction unavailable")
		}
		return realEvict(ctx, tenant)
	}
	require.NoError(t, responder.Start())
	defer responder.Stop()
	// Use the expanded expected set; failures must still be recorded explicitly.
	for _, retry := range []bool{false, true} {
		fail.Store(!retry)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := messaging.PurgeTenantDetect(ctx, nc, evictInstance, "acme",
			[]string{singletonPartition, groupPartition(1)})
		cancel()
		require.NoError(t, err)
		require.Len(t, result.Replies, 2)
		for _, reply := range result.Replies {
			if reply.PartitionId == groupPartition(1) {
				if retry {
					require.Empty(t, reply.Error)
				} else {
					require.Contains(t, reply.Error, "g1 eviction unavailable")
				}
			}
		}
	}
	snapshot, exists, err := second.store.Load(context.Background(), groupPartition(1))
	require.NoError(t, err)
	require.True(t, exists)
	require.NotContains(t, string(snapshot.Payload), "acme/")
}

// A group worker the supervisor has not built yet holds no term context. The shared gate
// is already open by then, so a purge reaching it must be refused as no-term-held, not
// dereference the missing context.
func TestGroupPurgeBeforeTheWorkerIsBuiltRefusesWithoutPanicking(t *testing.T) {
	child := newTestProcessor(newTestStore(t), nil, 1)
	child.procCtx, child.procCancel = nil, nil // as the supervisor leaves an unbuilt child
	child.supervised = true
	child.Gate = NewTermGate()
	child.Gate.Enter(func() bool { return true })
	require.Nil(t, child.pctx(), "fixture: the child has no term yet")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := child.EvictTenant(ctx, "acme")
	require.ErrorIs(t, err, errNoTermHeld)
}
