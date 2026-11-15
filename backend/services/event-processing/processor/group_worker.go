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

// GroupSnapshotPartition is the checkpoint namespace for one physical stream.
// Group zero preserves the legacy singleton row and durable start position.
func GroupSnapshotPartition(group int) string { return groupPartition(group) }

// ConfigureGroupWorkers binds fresh processors to their immutable replay sources
// and installs the singleton fact router. The caller constructs group readers with
// NewReadersWith and supplies the same durable projections to every processor.
// Production activation remains refused by main until the S7 support-guard lift.
func (rp *ResolvedEventsProcessor) ConfigureGroupWorkers(nmgr *messaging.NatsManager, children ...*ResolvedEventsProcessor) error {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups
	workers := append([]*ResolvedEventsProcessor{rp}, children...)
	if cfg == nil || len(workers) != cfg.Count {
		return fmt.Errorf("DETECT requires exactly one worker per configured group")
	}
	for g, w := range workers {
		if w == nil || w.worker != nil {
			return fmt.Errorf("DETECT group %d requires a fresh worker", g)
		}
		source, err := nmgr.NewGroupReplaySource(streams.ResolvedEvents, g)
		if err != nil {
			return err
		}
		w.worker = &workerIdentity{group: g, physical: messaging.GroupSuffix(streams.ResolvedEvents, g), partition: groupPartition(g), replay: source}
	}
	return rp.attachRoutedRuleWorkers(nmgr, children...)
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
// Package tests can construct fresh group workers here. Main uses NewReadersWith
// and ConfigureGroupWorkers behind its S3/S7 startup refusal.
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
	if rp.ruleRouting != nil {
		rp.ruleRouting.termReady = make(chan struct{})
	}
	for _, w := range rp.termWorkers() {
		if w != rp {
			w.procMu.Lock()
			w.procCtx, w.procCancel = ctx, rp.pcancel
			w.procMu.Unlock()
		}
	}
	if rp.ruleRouting != nil {
		// Catch up the ONE shared fact projection before any worker loads rules or
		// replays events. In particular, g0 must not replay under pre-catch-up rules.
		if err := rp.bindTermReaders(); err != nil {
			return fmt.Errorf("bind shared rule facts: %w", err)
		}
		if err := rp.catchUpFactProjections(); err != nil {
			return err
		}
	}
	for _, w := range rp.termWorkers() {
		if err := w.buildTerm(ctx); err != nil {
			return fmt.Errorf("DETECT partition %q: %w", w.partitionID(), err)
		}
	}
	// Start loops together: the supervisor's periodic source repair can signal any
	// worker, so it must obey the same initialization barrier as the fact consumers.
	if rp.ruleRouting != nil {
		for _, w := range rp.termWorkers() {
			w.readerWG.Add(1)
			go w.run()
		}
		close(rp.ruleRouting.termReady)
		if rp.RuleUpdatesReader != nil {
			rp.readerWG.Add(1)
			go rp.runRuleConsumer()
		}
		if rp.RosterReader != nil {
			rp.readerWG.Add(1)
			go rp.runRosterConsumer()
		}
		if rp.EntityDeletedReader != nil {
			rp.readerWG.Add(1)
			go rp.runEntityDeletedConsumer()
		}
		if rp.AttributeReader != nil {
			rp.readerWG.Add(1)
			go rp.runAttributeConsumer()
		}
		if rp.FenceSetReader != nil {
			rp.readerWG.Add(1)
			go rp.runFenceSetConsumer()
		}
	}
	return nil
}

// attachGroupWorkers validates the singleton supervisor relationship. Rule
// and all fact projections require attachRoutedRuleWorkers.
func (rp *ResolvedEventsProcessor) attachGroupWorkers(children ...*ResolvedEventsProcessor) error {
	if rp.worker == nil || rp.worker.group != 0 || rp.partitionID() != singletonPartition || !rp.leadershipEnabled() {
		return fmt.Errorf("group supervisor requires group zero and the singleton lease/gate")
	}
	workers := append([]*ResolvedEventsProcessor{rp}, children...)
	for g, w := range workers {
		if w == nil || w.worker == nil || w.worker.group != g || (g > 0 && w.Lease != nil) || len(w.groupWorkers) != 0 {
			return fmt.Errorf("invalid group worker %d: workers cannot acquire leases or supervise workers", g)
		}
		if ((w.RuleUpdatesReader != nil || w.RuleStore != nil || w.ProfileActiveStore != nil || w.DeviceManagement != nil) && w.ruleRouting == nil) ||
			((w.RosterReader != nil || w.EntityDeletedReader != nil || w.RosterStore != nil || w.AttributeReader != nil || w.FenceSetReader != nil ||
				w.AttributeStore != nil || w.FenceSets != nil || w.VersionedFenceSets != nil || w.FenceManifests != nil) && w.ruleRouting == nil) {
			return fmt.Errorf("group workers require S2b fact routing before using fact projections")
		}
	}
	rp.groupWorkers = append([]*ResolvedEventsProcessor(nil), children...)
	// Reuse the supervisor's process-owned collectors. Each group gets its own
	// gauge recording handle and shard sampling cursors, including group zero.
	if metrics := rp.metrics; metrics != nil {
		for g, w := range workers {
			w.metrics = metrics.ForGroup(g, len(workers))
		}
	}
	for _, w := range children {
		w.Gate, w.supervised = rp.Gate, true
	}
	return nil
}
