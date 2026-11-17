// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package placement keeps the KV mirror of the placement table (core/partition) equal to the
// database's copy.
//
// The database is the authority; the mirror exists so that no hot path ever queries a database
// to route. With nats.shardGroups configured, event-sources, device-management and
// event-management route sharded streams through this mirror. Every pass verifies after
// writing so a disagreement is reported for operator repair.
package placement

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/rs/zerolog/log"
)

// Source is where the authoritative table is read from (iam.Store).
type Source interface {
	Placement(ctx context.Context) (partition.Table, error)
}

// Sync copies the database's table into the mirror and verifies that the mirror now holds
// exactly it. It refuses tables naming a group outside configured nats.shardGroups.count
// before touching the bucket. It returns partition.ErrPChanged (wrapped) for a different P.
//
// A publisher whose read of the database went stale (another replica published a newer
// generation first) is refused by the mirror (partition.ErrStale); it re-reads and offers the
// newer table once, instead of waiting a whole interval.
func Sync(ctx context.Context, src Source, bucket partition.Bucket, groups *config.NatsShardGroupsConfiguration) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var table partition.Table
		table, err = src.Placement(ctx)
		if err != nil {
			return fmt.Errorf("reading the placement table: %w", err)
		}
		if groups != nil {
			// Each group is compared directly: Table.S() is the highest group plus one, which
			// overflows for a group of math.MaxInt.
			for vp, g := range table.Groups {
				if g >= groups.Count {
					return fmt.Errorf("placement names shard group %d, count %d (vpartition %d); nats.shardGroups.count must be raised first",
						g, groups.Count, vp)
				}
			}
		}
		if err = partition.Publish(bucket, table); err != nil {
			if errors.Is(err, partition.ErrStale) {
				continue
			}
			return err
		}
		return partition.Verify(bucket, table)
	}
	return err
}

// LazyBucket opens its KV bucket on first use and retries the open on every use until it works.
//
// An unavailable mirror fails passes, which retry, while the identity provider stays up.
// Sharded stream routing holds or refuses work until a usable mirror is available. Opening
// eagerly would turn an unreachable or full broker into an identity service startup failure.
type LazyBucket struct {
	open func() (partition.Bucket, error)

	mu sync.Mutex
	b  partition.Bucket
}

// NewLazyBucket wraps an opener.
func NewLazyBucket(open func() (partition.Bucket, error)) *LazyBucket {
	return &LazyBucket{open: open}
}

func (l *LazyBucket) get() (partition.Bucket, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b != nil {
		return l.b, nil
	}
	b, err := l.open()
	if err != nil {
		return nil, fmt.Errorf("opening the placement mirror bucket: %w", err)
	}
	l.b = b
	return b, nil
}

func (l *LazyBucket) Get(key string) ([]byte, uint64, bool, error) {
	b, err := l.get()
	if err != nil {
		return nil, 0, false, err
	}
	return b.Get(key)
}

func (l *LazyBucket) Create(key string, value []byte) error {
	b, err := l.get()
	if err != nil {
		return err
	}
	return b.Create(key, value)
}

func (l *LazyBucket) Update(key string, value []byte, revision uint64) error {
	b, err := l.get()
	if err != nil {
		return err
	}
	return b.Update(key, value, revision)
}

// IsFatal reports whether a Sync error is the one that stops the service AT START: the P the
// database records is not the P the mirror records. Other failures are retried by periodic passes.
//
// Only startup acts on this by stopping the service. Periodic failures are logged and counted
// for the placement_mirror alert while the identity provider stays up. Sharded stream routing
// holds or refuses work when the mirror is unusable; operators must repair the disagreement.
func IsFatal(err error) bool { return errors.Is(err, partition.ErrPChanged) }

// logChangedP is how a periodic pass says a changed P out loud: at error, with what it means. It
// is the one failure here that is not transient and will not clear on retry.
func logChangedP(err error) {
	log.Error().Err(err).Msg("The placement mirror records a different P from the database. This will not " +
		"clear on retry: the vpartition count is fixed for the life of the instance. Every pass fails until " +
		"the mirror or the database is repaired. The identity provider keeps running; sharded stream " +
		"routing holds or refuses work while the mirror is unusable. A restart refuses to start.")
}

// mirrorPass is one scheduled pass. A changed P is returned as itself (wrapped), so the pass is
// counted failed, and is reported through onFatal. onFatal is a parameter so a test can observe it
// without swapping the global logger, which no test in the repository may do.
func mirrorPass(src Source, bucket partition.Bucket, groups *config.NatsShardGroupsConfiguration, onFatal func(error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		err := Sync(ctx, src, bucket, groups)
		if IsFatal(err) {
			onFatal(err)
		}
		return err
	}
}

// NewMirrorTask schedules Sync. The first pass is the caller's, at start; this one keeps an
// unchanged mirror alive (its entries expire) and picks up a changed placement.
func NewMirrorTask(ms *core.Microservice, src Source, bucket partition.Bucket, groups *config.NatsShardGroupsConfiguration, interval time.Duration,
	callbacks core.LifecycleCallbacks) *core.PeriodicTask {
	return core.NewPeriodicTask(ms.FunctionalArea, "placement-mirror", interval,
		mirrorPass(src, bucket, groups, logChangedP),
		callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("placement_mirror")))
}
