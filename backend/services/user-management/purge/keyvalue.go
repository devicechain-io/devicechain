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
// # It is the last store, so it is the one that lets a purge finish
//
// Every registered store now erases. That does not mean a purge completes today — the
// broker still carries three deferrals it cannot reach, and event-processing's DETECT
// checkpoint is still a deferred table — but nothing is Pending any more. What remains is
// stated in every deletion record rather than missing from it, which is the whole point of
// the Pending type it replaces.
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
// # 🔴 The exemptions do NOT reach the deletion record, and that is a gap
//
// messaging.KvPurgeExemptions explains the four buckets this deliberately does not touch —
// refresh tokens, OAuth codes, locks and leases. None of them retains this tenant's data,
// so none belongs in Deferred: a deferral blocks completion, and these must not. But
// Outcome has nowhere else to put them, so today they reach a reader only through that
// function and the test that pairs it against the platform's bucket inventory. The ledger
// therefore records this store as clean without recording what it decided not to look at.
//
// That is the same shape as the telemetry store's "no database here" — a judgement the
// record cannot distinguish from an absence — and it has the same fix, a note column that
// carries text without blocking. Tracked with it rather than papered over here.
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
	return Outcome{Rows: res.Keys}, nil
}
