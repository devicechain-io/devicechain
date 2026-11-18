// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/shardgroups"
	"github.com/devicechain-io/dc-microservice/streams"
)

// workerIdentity binds scalar positions to one physical stream for the worker's lifetime.
// A nil identity preserves the legacy constructor and its injectable replay seam.
type workerIdentity struct {
	group               int
	physical, partition string
	replay              messaging.GroupReplaySource
}

const singletonPartition = "singleton"

func groupPartition(group int) string {
	if group == 0 {
		return singletonPartition
	}
	return messaging.GroupSuffix(streams.ResolvedEvents, group)
}

func (rp *ResolvedEventsProcessor) partitionID() string {
	if rp.worker != nil {
		return rp.worker.partition
	}
	return rp.cfg.PartitionId
}

func (rp *ResolvedEventsProcessor) openReplay(start uint64) (messaging.ReplayReader, uint64, error) {
	if rp.worker != nil {
		return rp.worker.replay.NewReplayReader(start)
	}
	return rp.Replay.NewReplayReader(rp.cfg.Suffix, start)
}

func (rp *ResolvedEventsProcessor) openRange(first, last uint64) (messaging.ReplayReader, error) {
	if rp.worker != nil {
		return rp.worker.replay.NewRangeReader(first, last)
	}
	if rp.Replay == nil {
		return nil, fmt.Errorf("no range reader is configured")
	}
	return rp.Replay.NewRangeReader(rp.cfg.Suffix, first, last)
}

// groupInputs is deliberately private: S2 has no production activation switch.
// Package tests can construct fresh group workers here; fact routing, metrics and purge
// aggregation must be finished before main may use this path (S2b/S3/S7).
func groupInputs(nmgr *messaging.NatsManager, store committedSeqLoader, gate *TermGate) ([]workerIdentity, []messaging.MessageReader, error) {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups
	if cfg == nil || gate == nil {
		return nil, nil, fmt.Errorf("group workers require selected resolved-events sharding")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	identities := make([]workerIdentity, cfg.Count)
	for g := range identities {
		source, err := nmgr.NewGroupReplaySource(streams.ResolvedEvents, g)
		if err != nil {
			return nil, nil, err
		}
		identities[g] = workerIdentity{group: g, physical: messaging.GroupSuffix(streams.ResolvedEvents, g), partition: groupPartition(g), replay: source}
	}
	readers, err := shardgroups.NewReadersWith(nmgr, streams.ResolvedEvents, func(g int) []messaging.ReaderOption {
		return append(ResolvedEventsReaderOptions(store, identities[g].partition), messaging.ReaderWithTermGate(gate.Held))
	})
	return identities, readers, err
}

func (rp *ResolvedEventsProcessor) termWorkers() []*ResolvedEventsProcessor {
	return append([]*ResolvedEventsProcessor{rp}, rp.groupWorkers...)
}

// buildWorkers builds serially to bound restore/replay pressure. Any worker failure ends
// the common term; none of the children acquires a lease or opens/closes the gate.
func (rp *ResolvedEventsProcessor) buildWorkers(ctx context.Context) error {
	for _, w := range rp.termWorkers() {
		if w != rp {
			w.procMu.Lock()
			w.procCtx, w.procCancel = ctx, rp.pcancel
			w.procMu.Unlock()
		}
		if err := w.buildTerm(ctx); err != nil {
			return fmt.Errorf("DETECT partition %q: %w", w.partitionID(), err)
		}
	}
	return nil
}

// attachGroupWorkers is an internal test hook, not an activation API. Until S2b,
// only workers with explicitly isolated fixture registries and no fact projections
// are accepted. In particular, duplicating global roster loads could arm other tenants.
func (rp *ResolvedEventsProcessor) attachGroupWorkers(children ...*ResolvedEventsProcessor) error {
	if rp.worker == nil || rp.worker.group != 0 || rp.partitionID() != singletonPartition || !rp.leadershipEnabled() {
		return fmt.Errorf("group supervisor requires group zero and the singleton lease/gate")
	}
	workers := append([]*ResolvedEventsProcessor{rp}, children...)
	for g, w := range workers {
		if w == nil || w.worker == nil || w.worker.group != g || (g > 0 && w.Lease != nil) || len(w.groupWorkers) != 0 {
			return fmt.Errorf("invalid group worker %d: workers cannot acquire leases or supervise workers", g)
		}
		if w.RuleUpdatesReader != nil || w.RosterReader != nil || w.EntityDeletedReader != nil || w.AttributeReader != nil || w.FenceSetReader != nil ||
			w.RuleStore != nil || w.RosterStore != nil || w.ProfileActiveStore != nil || w.AttributeStore != nil || w.DeviceManagement != nil || w.FenceSets != nil || w.VersionedFenceSets != nil {
			return fmt.Errorf("group workers require S2b fact routing before using fact projections")
		}
	}
	rp.groupWorkers = append([]*ResolvedEventsProcessor(nil), children...)
	for _, w := range children {
		w.Gate, w.supervised = rp.Gate, true
	}
	return nil
}
