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
// # 🔑 This store defers nothing, and that is a claim rather than an omission
//
// Three things used to survive a subject purge and were named in the ledger on every pass,
// which is what kept this store from ever reporting clean and therefore kept any purge from
// ever completing. Two are now erased — MQTT session state and the durable consumers that
// outlive it, both reached by READING the gateway's own records rather than by filtering
// subjects that never carried the tenant. The third, the retained-message cache, cannot be
// erased at all (it is in-process in nats-server with no API) and is instead covered by the
// settle window; messaging.RetainedCacheWindow spells out that argument and names what it
// rests on.
//
// One thing the sweep can still find and not erase: a session record whose client id is not
// device-shaped, which no tenant can be named for. It is COUNTED AND LOGGED rather than
// deferred, and MqttGatewayPurgeResult.UnownedSessions carries the reasoning — the short
// version is that such a record cannot be inherited by any device, so it is not the hazard
// a deferral exists to hold a purge open for, and deferring on it would deadlock every
// purge on any instance running an edge agent.
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

	return Outcome{Rows: res.Rows()}, nil
}
