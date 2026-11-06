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
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
)

// Source is where the authoritative table is read from (iam.Store).
type Source interface {
	Placement(ctx context.Context) (partition.Table, error)
}

// Sync copies the database's table into the mirror and verifies that the mirror now holds
// exactly it. It returns partition.ErrPChanged (wrapped) when the mirror records a different P.
func Sync(ctx context.Context, src Source, bucket partition.Bucket) error {
	table, err := src.Placement(ctx)
	if err != nil {
		return fmt.Errorf("reading the placement table: %w", err)
	}
	if err := partition.Publish(bucket, table); err != nil {
		return err
	}
	return partition.Verify(bucket, table)
}

// IsFatal reports whether a Sync error means the service must not carry on: the P the database
// records is not the P the mirror records. Everything else (the broker is away, a bucket is
// full) is transient or harmless today and is retried by the periodic pass.
func IsFatal(err error) bool { return errors.Is(err, partition.ErrPChanged) }

// NewMirrorTask schedules Sync. The first pass is the caller's, at start; this one keeps an
// unchanged mirror alive (its entries expire) and picks up a changed placement.
func NewMirrorTask(ms *core.Microservice, src Source, bucket partition.Bucket, interval time.Duration,
	callbacks core.LifecycleCallbacks) *core.PeriodicTask {
	return core.NewPeriodicTask(ms.FunctionalArea, "placement-mirror", interval,
		func(ctx context.Context) error { return Sync(ctx, src, bucket) },
		callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("placement_mirror")))
}
