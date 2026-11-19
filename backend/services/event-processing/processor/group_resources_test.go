// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"

	epconfig "github.com/devicechain-io/dc-event-processing/config"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	msconfig "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func TestGroupResourceCeilingAndBuffers(t *testing.T) {
	for _, n := range []int{1, 2, 3, 16} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			k := 64 / n
			workers := make([]*ResolvedEventsProcessor, n)
			for g := range workers {
				ms := &core.Microservice{}
				if n > 1 {
					ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &msconfig.NatsShardGroupsConfiguration{Count: n, Suffixes: []string{streams.ResolvedEvents}}
				}
				w := NewResolvedEventsProcessor(ms, nil, nil, nil, nil, nil, nil, Config{Shards: k}, core.NewNoOpLifecycleCallbacks(), nil)
				w.worker = &workerIdentity{group: g, partition: groupPartition(g)}
				require.Equal(t, 64/n, cap(w.ruleUpdates), "constructor must divide buffers before attachment")
				require.Equal(t, 256/n, cap(w.armUpdates))
				require.Equal(t, 256/n, cap(w.attrUpdates))
				require.Equal(t, 256/n, cap(w.seriesDrops))
				require.Equal(t, 32/n, cap(w.fenceUpdates))
				workers[g] = w
			}
			p := workers[0]
			p.Lease, p.Gate = &messaging.DistributedLease{}, NewTermGate()
			require.NoError(t, p.attachGroupWorkers(workers[1:]...))
			for _, w := range workers {
				require.NoError(t, w.ExecuteInitialize(context.Background()))
				defer w.supCancel()
				defer w.signalStop()
				defer w.pcancel()
				require.Equal(t, 64/n, cap(w.ruleUpdates))
				require.Equal(t, 256/n, cap(w.armUpdates))
				require.Equal(t, 256/n, cap(w.attrUpdates))
				require.Equal(t, 256/n, cap(w.seriesDrops))
				require.Equal(t, 32/n, cap(w.fenceUpdates))
				engine := w.newDetector()
				for _, stat := range engine.(detectcore.Pipelined).ShardStats() {
					require.Equal(t, 1024/n, stat.QueueCapacity)
				}
				payload, err := engine.Snapshot()
				require.NoError(t, err)
				engine.(detectcore.Pipelined).Close()
				restored, err := w.restoreDetector(payload)
				require.NoError(t, err)
				for _, stat := range restored.(detectcore.Pipelined).ShardStats() {
					require.Equal(t, 1024/n, stat.QueueCapacity)
				}
				restored.(detectcore.Pipelined).Close()
			}
			// Exercise the real initialization gate and the internal assembly gate.
			if n > 1 {
				p.cfg.Shards = k + 1
				require.ErrorIs(t, p.ExecuteInitialize(context.Background()), epconfig.ErrDetectWorkerCeiling)
				require.ErrorIs(t, p.attachGroupWorkers(workers[1:]...), epconfig.ErrDetectWorkerCeiling)
			}
		})
	}
}
