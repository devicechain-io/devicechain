// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package shardgroups

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// ErrGroupsExceedCount means a placement names a group outside the configured streams.
var ErrGroupsExceedCount = errors.New("shardgroups: placement exceeds configured group count")

// Enable connects opt-in stream routing to the instance's placement mirror.
// Call before creating any reader or writer. The caller stops the watch after
// the service's managers have stopped or terminated, so publishes still draining
// during shutdown are not refused for placement.
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
	r := &placementStreamRouter{router: partition.NewRouter(nmgr.Microservice.InstanceId), count: cfg.Count, err: partition.ErrNoPlacement}
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
				r.unavailable = nmgr.Microservice.NewGauge("jetstream_shard_placement_unavailable", "Whether shard placement is unavailable and forwarding is held (0 or 1).")
				r.setUnavailable()
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
	mu          sync.RWMutex
	router      *partition.Router
	err         error
	count       int
	unavailable prometheus.Gauge
}

func (r *placementStreamRouter) Ready() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.err
}

// Called under mu, or before the watch goroutine starts.
func (r *placementStreamRouter) setUnavailable() {
	if r.unavailable != nil {
		value := 0.0
		if r.err != nil {
			value = 1
		}
		r.unavailable.Set(value)
	}
}

func (r *placementStreamRouter) setError(err error) {
	wasReady := r.err == nil
	r.err = err
	if wasReady == (err == nil) {
		return
	}
	r.setUnavailable()
	if err != nil {
		log.Error().Err(err).Msg("Shard placement unavailable; holding forwarding until an adoptable placement arrives.")
	} else {
		log.Info().Msg("Shard placement available; resuming forwarding.")
	}
}

func (r *placementStreamRouter) refuse() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setError(partition.ErrNoPlacement)
}

func (r *placementStreamRouter) update(e nats.KeyValueEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Operation() != nats.KeyValuePut {
		r.setError(partition.ErrNoPlacement)
		return
	}
	t, err := partition.Decode(e.Value())
	if err == nil {
		err = r.router.Update(t)
		if err == nil {
			// Groups contains group indices, not a group count. Index count is
			// already outside the configured range [0, count).
			for vp, group := range t.Groups {
				if group >= r.count {
					err = fmt.Errorf("%w: vpartition %d has group %d outside [0,%d)", ErrGroupsExceedCount, vp, group, r.count)
					break
				}
			}
		}
	}
	if !errors.Is(err, partition.ErrStale) {
		r.setError(err)
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
