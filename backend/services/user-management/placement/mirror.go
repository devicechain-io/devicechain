// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package placement keeps the KV mirror of the placement table (core/partition) equal to the
// database's copy.
//
// The database is the authority; the mirror exists so that no hot path ever queries a database
// to route. Nothing reads the mirror yet, so a stale or missing one changes no behaviour in this
// release; what this package must never do is let it DISAGREE quietly, which is why every pass
// verifies after it writes.
package placement

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/rs/zerolog/log"
)

// Source is where the authoritative table is read from (iam.Store).
type Source interface {
	Placement(ctx context.Context) (partition.Table, error)
}

// Sync copies the database's table into the mirror and verifies that the mirror now holds
// exactly it. It returns partition.ErrPChanged (wrapped) when the mirror records a different P.
//
// A publisher whose read of the database went stale (another replica published a newer
// generation first) is refused by the mirror (partition.ErrStale); it re-reads and offers the
// newer table once, instead of waiting a whole interval.
func Sync(ctx context.Context, src Source, bucket partition.Bucket) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var table partition.Table
		table, err = src.Placement(ctx)
		if err != nil {
			return fmt.Errorf("reading the placement table: %w", err)
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
// The mirror is read by nothing yet, so being unable to open it must not stop the service from
// starting; it must only fail the mirror's own passes, which retry. A bucket opened eagerly at
// start would turn an unreachable or full broker into a service that does not come up.
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
// database records is not the P the mirror records. Everything else (the broker is away, a bucket
// is full) is transient or harmless today and is retried by the periodic pass.
//
// 🔴 ONLY THE START ACTS ON IT, ON PURPOSE. Past start the same error fails the periodic pass
// loudly (the pass is counted failed, so the placement_mirror alert fires, and each failure is
// logged at error with the cause) but does not take the process down: this service is the
// instance's identity provider, nothing reads the mirror yet, and a mirror that disagrees with
// the database mid-run is a hand edit of the bucket, not a database that has moved. Exiting every
// replica at once over it would turn that into a login outage for a value no request depends on.
// The day something routes by the mirror, this is the place to revisit.
func IsFatal(err error) bool { return errors.Is(err, partition.ErrPChanged) }

// logChangedP is how a periodic pass says a changed P out loud: at error, with what it means. It
// is the one failure here that is not transient and will not clear on retry.
func logChangedP(err error) {
	log.Error().Err(err).Msg("The placement mirror records a different P from the database. This will not " +
		"clear on retry: the vpartition count is fixed for the life of the instance. Every pass fails until " +
		"the mirror or the database is repaired. The service keeps running because nothing routes by the " +
		"mirror yet; a restart refuses to start.")
}

// mirrorPass is one scheduled pass. A changed P is returned as itself (wrapped), so the pass is
// counted failed, and is reported through onFatal. onFatal is a parameter so a test can observe it
// without swapping the global logger, which no test in the repository may do.
func mirrorPass(src Source, bucket partition.Bucket, onFatal func(error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		err := Sync(ctx, src, bucket)
		if IsFatal(err) {
			onFatal(err)
		}
		return err
	}
}

// NewMirrorTask schedules Sync. The first pass is the caller's, at start; this one keeps an
// unchanged mirror alive (its entries expire) and picks up a changed placement.
func NewMirrorTask(ms *core.Microservice, src Source, bucket partition.Bucket, interval time.Duration,
	callbacks core.LifecycleCallbacks) *core.PeriodicTask {
	return core.NewPeriodicTask(ms.FunctionalArea, "placement-mirror", interval,
		mirrorPass(src, bucket, logChangedP),
		callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("placement_mirror")))
}
