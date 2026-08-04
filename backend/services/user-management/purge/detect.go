// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/rs/zerolog/log"
)

// detectSchema is the functional area that owns the DETECT engine. Its presence in the
// catalog is what tells this store whether the area has ever run on this instance.
const detectSchema = "event-processing"

// detectPartitionsQuery lists the partitions that hold a durable checkpoint.
//
// It reads another area's table directly, which the purge does elsewhere too — the
// relational sweep deletes from every area's tables in this same database. What it must
// never do is read it through a MODEL: the shape belongs to event-processing, and one
// column of one table is a far smaller thing to depend on than a struct that area is free
// to change. The column and table names are pinned by a test that runs against the real
// migrated schema, so a rename fails there rather than here, at a purge.
const detectPartitionsQuery = `SELECT partition_id FROM "event-processing"."detect_snapshots"`

// Detect erases a tenant from the DETECT engine's in-process state (ADR-077 slice 2c).
//
// # Why this store is not a query
//
// Every other store deletes. This one ASKS, because the data is in a place a query cannot
// go: the engine's open detection windows, running timers, accumulators and dead-man
// armings live in a running process's memory, and are checkpointed to `detect_snapshots`
// as one opaque JSON blob per partition holding every tenant at once. Tenancy is inside
// that blob, carried as the prefix of each rule id, so no predicate reaches it — and
// deleting the row would not be an erasure but a corruption, since it is the checkpoint
// every OTHER tenant's replay-correct recovery depends on.
//
// So the erasure runs where the state is owned. This store sends one request per pass to
// the engines running on the instance; each evicts the tenant on its single-writer loop,
// commits the checkpoint, and answers with what it removed. The row is rewritten without
// the tenant rather than deleted.
//
// # What "clean" rests on
//
// A reply, but not merely a reply: the responder sends it only AFTER the checkpoint
// commits, so a partition that answers has made the eviction durable. The three ways this
// store declines to claim clean are all cases where that chain is broken —
//
//   - no engine answered at all (the broker says there is no subscriber);
//   - an engine that has durable state did not answer within the gather window;
//   - an engine answered with an error, having evicted in memory but failed to commit.
//
// each of which leaves the tenant's windows and timers in a checkpoint on disk. All three
// are deferrals rather than failures, and they are deferrals that can persist: an instance
// whose event-processing deployment is scaled to zero will never complete a purge. That is
// the correct behaviour and not a deadlock in disguise — the data really is still there,
// the deletion record says which partition holds it, and starting the service resolves it.
// It is the opposite case from the broker's unattributable MQTT sessions, which are
// counted rather than deferred precisely because no operator action could ever clear them.
type Detect struct {
	conn       messaging.Connection
	db         Database
	instanceId string
}

// NewDetect builds the store over the service's NATS connection (how it reaches the
// engines) and its own database handle (how it learns which partitions exist).
func NewDetect(conn messaging.Connection, db Database, instanceId string) *Detect {
	return &Detect{conn: conn, db: db, instanceId: instanceId}
}

func (d *Detect) Name() string { return StoreDetect }

// Erase asks every DETECT engine on the instance to evict the tenant and re-checkpoint.
//
// The epoch is unused, and unlike the broker store's the reason is not that the tenant row
// still exists. The engine's state is addressed by token alone and is evicted in full, so
// there is no residue for an epoch to tell apart from a successor's rows: anything a
// successor accumulates was built from events that arrived after this call.
func (d *Detect) Erase(ctx context.Context, tenant string, _ time.Time) (Outcome, error) {
	nc := d.conn.Conn()
	if nc == nil {
		return Outcome{}, fmt.Errorf("the broker connection is not available, so the DETECT engine "+
			"cannot be asked to evict %q — the ledger records this as retryable", tenant)
	}

	// 🔴 "IS THE AREA DEPLOYED HERE" IS ANSWERED BY THE CATALOG, NOT BY WHO REPLIES.
	//
	// Without this, an instance that runs no event-processing is indistinguishable from one
	// whose engine is down: both produce no responder, and deferring on both would block
	// every purge forever on an ingest-only instance that never had a DETECT engine to hold
	// anything. A schema is created by its owning service's first startup and by nothing
	// else, so its absence is the one honest "there is nothing here" this store can get.
	//
	// It is logged rather than passed over silently, for the reason the telemetry store
	// logs the same shape: "the area was never deployed" and "swept and found nothing" both
	// write Rows=0, Complete=true, and only the log distinguishes them.
	present, err := tenantpurge.SchemaExists(ctx, d.db.DB(ctx), detectSchema)
	if err != nil {
		return Outcome{}, fmt.Errorf("checking whether %s has ever run on this instance: %w",
			detectSchema, err)
	}
	if !present {
		log.Info().Str("tenant", tenant).Str("store", StoreDetect).
			Msg("No event-processing schema on this instance, so there is no DETECT engine and no " +
				"engine state to evict; reporting clean.")
		return Outcome{}, nil
	}

	partitions, err := d.checkpointedPartitions(ctx)
	if err != nil {
		return Outcome{}, err
	}

	res, err := messaging.PurgeTenantDetect(ctx, nc, d.instanceId, tenant, partitions)
	if err != nil {
		return Outcome{}, fmt.Errorf("asking the DETECT engine to evict %q: %w", tenant, err)
	}

	return d.outcome(tenant, partitions, res), nil
}

// checkpointedPartitions lists the partitions holding a durable checkpoint.
//
// This is the set an answer is REQUIRED from, and it is derived from the data rather than
// declared: GA runs one DETECT partition per instance, but the column exists so a sharded
// fleet can checkpoint per shard, and a store that assumed one would report the whole
// engine clean the moment the first partition replied. An engine that is running but has
// never checkpointed is outside this set and is still counted when it answers — it holds
// state no row names yet, and its first eviction writes the row that names it.
func (d *Detect) checkpointedPartitions(ctx context.Context) ([]string, error) {
	var ids []string
	if err := d.db.DB(ctx).Raw(detectPartitionsQuery).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("listing the DETECT partitions holding a checkpoint: %w", err)
	}
	sort.Strings(ids) // stable ledger text across passes
	return ids, nil
}

// outcome turns the replies into what the ledger records.
func (d *Detect) outcome(tenant string, partitions []string, res messaging.DetectPurgeResult) Outcome {
	var out Outcome
	answered := make(map[string]bool, len(res.Replies))
	for _, r := range res.Replies {
		answered[r.PartitionId] = true
		out.Rows += r.Evicted
		if r.Error != "" {
			out.Deferred = append(out.Deferred, fmt.Sprintf("the DETECT engine on partition %q "+
				"could not complete the eviction, so this tenant's open detection windows and "+
				"timers are still in its checkpoint: %s", r.PartitionId, r.Error))
		}
	}

	var silent []string
	for _, p := range partitions {
		if !answered[p] {
			silent = append(silent, p)
		}
	}
	if len(silent) > 0 {
		out.Deferred = append(out.Deferred, fmt.Sprintf("no DETECT engine answered for partition(s) "+
			"%s, whose checkpoint holds this tenant's open detection windows and timers. The engine "+
			"is not running, or did not answer within %s. Nothing else can erase this state; start "+
			"event-processing and the next pass will.",
			strings.Join(quoteAll(silent), ", "), messaging.DetectPurgeWindow))
	}

	// No responder at all, with no partition to name. Reachable when the area's schema exists
	// (so it ran here once) but nothing has checkpointed and nothing is running — an engine
	// that never got far enough to write a row. There is no durable state to point at, but
	// there is also nothing that could have evicted an in-memory one, so this is reported
	// rather than assumed away.
	if res.NoResponders && len(partitions) == 0 {
		log.Warn().Str("tenant", tenant).Str("store", StoreDetect).
			Msg("event-processing has run on this instance but no DETECT engine is running and none " +
				"has checkpointed, so there is no engine state to evict; reporting clean.")
	}
	return out
}

// quoteAll renders partition ids for a ledger sentence.
func quoteAll(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%q", id))
	}
	return out
}
