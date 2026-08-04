// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// KeyValue erases a tenant's cached resolutions from the key-value store.
//
// Like the broker store, almost nothing of the erasure is here: the bucket inventory, the
// key encoding and the membership test belong to core/messaging, which owns all three. What
// this type supplies is the connection, the instance, and the translation into a ledger
// line.
//
// # What it holds back, and where that is recorded
//
// Four buckets are deliberately not swept — refresh tokens, OAuth codes, locks and leases.
// They do NOT block completion, because none of them retains this tenant's data, so they
// cannot be deferrals. They ride in the outcome's Notes instead and land in the deletion
// record, so a reader of that record sees the judgement and not only its result.
type KeyValue struct {
	conn       messaging.Connection
	instanceId string
}

// NewKeyValue builds the key-value store over the service's existing NATS connection.
func NewKeyValue(conn messaging.Connection, instanceId string) *KeyValue {
	return &KeyValue{conn: conn, instanceId: instanceId}
}

func (k *KeyValue) Name() string { return StoreKeyValue }

// Erase scans the tenant-scoped buckets and deletes what belongs to the tenant.
//
// # The exemptions reach the deletion record as NOTES
//
// messaging.KvPurgeExemptions explains the buckets this deliberately does not touch —
// refresh tokens, OAuth codes, locks and leases. None of them retains this tenant's data,
// so none belongs in Deferred: a deferral blocks completion, and these must not. They ride
// in Outcome.Notes instead, which qualifies a clean pass without holding it open, so the
// record of the erasure carries the judgement that was made rather than only its result.
//
// They are written on EVERY pass rather than once, because the record is read one line at
// a time — a reader looking at the line that closed the purge would otherwise have to know
// that an earlier line said something this one does not.
func (k *KeyValue) Erase(ctx context.Context, tenant string, _ time.Time) (Outcome, error) {
	nc := k.conn.Conn()
	if nc == nil {
		return Outcome{}, fmt.Errorf("the broker connection is not available, so nothing about %q "+
			"can be erased from the key-value store — the ledger records this as retryable", tenant)
	}
	// Asked per pass rather than cached: nc.JetStream() does no I/O, and a context built
	// once would outlive reconnects this store would then have to reason about.
	js, err := nc.JetStream()
	if err != nil {
		return Outcome{}, fmt.Errorf("reaching the key-value store for %q: %w", tenant, err)
	}

	res, err := messaging.PurgeTenantKv(ctx, js, k.instanceId, tenant)
	if err != nil {
		return Outcome{}, fmt.Errorf("purging the key-value store for %q: %w", tenant, err)
	}
	return Outcome{Rows: res.Keys, Notes: messaging.KvPurgeExemptions()}, nil
}
