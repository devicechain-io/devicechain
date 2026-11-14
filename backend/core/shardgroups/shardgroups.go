// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package shardgroups

import (
	"context"
	"errors"
	"github.com/devicechain-io/dc-microservice/messaging"
	"slices"
	"sync"

	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/nats-io/nats.go"
)

// Enable connects opt-in stream routing to the instance's placement mirror.
// Call before creating any reader or writer. The caller stops the watch after
// stopping its processors and before stopping or terminating the NATS manager.
func Enable(nmgr *messaging.NatsManager) (context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if err := enable(ctx, nmgr); err != nil {
		cancel()
		return nil, err
	}
	return cancel, nil
}

func enable(ctx context.Context, nmgr *messaging.NatsManager) error {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups
	if cfg == nil {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	kv, err := nmgr.PartitionPlacementStore()
	if err != nil {
		return err
	}
	w, err := kv.Watch(partition.TableKey, nats.Context(ctx))
	if err != nil {
		return err
	}
	r := &placementStreamRouter{router: partition.NewRouter(nmgr.Microservice.InstanceId), err: partition.ErrNoPlacement}
	// Consume the initial snapshot before publishing any handles. Watch's nil
	// sentinel also covers an empty mirror: startup succeeds, publishes refuse.
	for {
		select {
		case <-ctx.Done():
			_ = w.Stop()
			return ctx.Err()
		case entry, ok := <-w.Updates():
			if !ok {
				_ = w.Stop()
				return partition.ErrNoPlacement
			}
			if entry == nil {
				if err := nmgr.EnableStreamSharding(r, cfg); err != nil {
					_ = w.Stop()
					return err
				}
				go func() {
					defer w.Stop()
					defer r.refuse()
					for {
						select {
						case <-ctx.Done():
							return
						case e, ok := <-w.Updates():
							if !ok {
								return
							}
							if e != nil {
								r.update(e)
							}
						}
					}
				}()
				return nil
			}
			r.update(entry)
		}
	}
}

type placementStreamRouter struct {
	mu     sync.RWMutex
	router *partition.Router
	err    error
}

func (r *placementStreamRouter) refuse() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = partition.ErrNoPlacement
}

func (r *placementStreamRouter) update(e nats.KeyValueEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Operation() != nats.KeyValuePut {
		r.err = partition.ErrNoPlacement
		return
	}
	t, err := partition.Decode(e.Value())
	if err == nil {
		err = r.router.Update(t)
	}
	if !errors.Is(err, partition.ErrStale) {
		r.err = err
	}
}

func (r *placementStreamRouter) ShardGroup(tenant string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.err != nil {
		return 0, r.err
	}
	return r.router.ShardGroup(tenant)
}

// NewReaders creates one reader for each selected group, or exactly
// the legacy reader when this suffix is not sharded.
func NewReaders(nmgr *messaging.NatsManager, suffix string) ([]messaging.MessageReader, error) {
	cfg := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups
	selected := cfg != nil && slices.Contains(cfg.Suffixes, suffix)
	n := 1
	if selected {
		n = cfg.Count
	}
	readers := make([]messaging.MessageReader, 0, n)
	for g := 0; g < n; g++ {
		var opts []messaging.ReaderOption
		if selected {
			opts = append(opts, messaging.ReaderWithShardGroup(g))
		}
		r, err := nmgr.NewReader(suffix, opts...)
		if err != nil {
			return nil, err
		}
		readers = append(readers, r)
	}
	return readers, nil
}
