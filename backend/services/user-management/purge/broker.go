// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// Broker erases a tenant from the message broker.
//
// Almost nothing of the erasure is here. The subject arithmetic — which stream shape puts
// the tenant where, and what filter therefore matches its messages — belongs to
// core/messaging, which owns the subjects in the first place; this type supplies the
// connection and the instance, and turns the result into what the ledger records. That is
// the same split the telemetry store has, and for the same reason: a filter rebuilt by a
// caller does not fail when a shape changes, it silently matches nothing.
//
// # 🔴 This store is NOT clean, and it is not supposed to be yet
//
// Three things survive a subject purge, all named in the ledger on every pass
// (messaging.TenantPurgeDeferrals): MQTT session state, whose streams key on a
// device-chosen client id that is bound to no tenant; durable consumers, which a purge
// does not remove and which on an interest-retention stream actively keep the deleted
// tenant's messages alive; and the retained-message cache, which serves purged payloads
// for up to two minutes after the purge. Until those are closed this store blocks
// completion — deliberately, and visibly.
type Broker struct {
	conn       messaging.Connection
	instanceId string
}

// NewBroker builds the broker store over the service's existing NATS connection.
func NewBroker(conn messaging.Connection, instanceId string) *Broker {
	return &Broker{conn: conn, instanceId: instanceId}
}

func (b *Broker) Name() string { return StoreBroker }

// Erase purges every stream whose subjects name the tenant, and reports what it could not
// reach.
//
// The epoch is unused, and that is worth saying rather than leaving as an unnamed
// parameter: a subject carries the token and nothing else, so while the tenant row exists
// in `purging` the token is unambiguous, exactly as it is for the relational sweep. What
// the epoch is load-bearing for is anything that OUTLIVES the tenant row.
func (b *Broker) Erase(ctx context.Context, tenant string, _ time.Time) (Outcome, error) {
	nc := b.conn.Conn()
	if nc == nil {
		return Outcome{}, fmt.Errorf("the broker connection is not available, so nothing about %q "+
			"can be erased from it — the ledger records this as retryable", tenant)
	}

	res, err := messaging.PurgeTenant(ctx, nc, b.instanceId, tenant)
	if err != nil {
		return Outcome{}, fmt.Errorf("purging the broker for %q: %w", tenant, err)
	}

	return Outcome{Rows: res.Messages, Deferred: messaging.TenantPurgeDeferrals()}, nil
}
