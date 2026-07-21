// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgperturb is the privileged out-of-band stimulus for the ADR-064 oracle
// self-test: it deletes persisted rows DIRECTLY in event-management's Postgres,
// beneath the tenant GraphQL API the oracle reads through. It is the one thing the
// load test proper is forbidden — the harness is an untrusted client with no DB
// access — and is deliberately isolated in its own package so the pgx driver
// links ONLY into the self-test binary, never into dc-loadtest or the sim.
package pgperturb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/devicechain-io/dc-simulator/loadtest"
)

// eventsTable is schema-qualified because the functional-area schema name carries
// a hyphen, so it must be quoted; a bare psql session defaults search_path to
// public, where this table does not exist.
const eventsTable = `"event-management"."events"`

// Perturber removes persisted base events from the event store out-of-band. It
// holds a single pgx connection scoped to one tenant.
type Perturber struct {
	conn   *pgx.Conn
	tenant string
}

// New opens a pgx connection with dsn (e.g.
// postgres://postgres:devicechain@127.0.0.1:5432/devicechain) and scopes deletes
// to tenant (the slug/token stored verbatim in events.tenant_id). The caller must
// Close it.
func New(ctx context.Context, dsn, tenant string) (*Perturber, error) {
	if tenant == "" {
		return nil, fmt.Errorf("pgperturb: tenant is required")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		// Deliberately not echoing dsn — it carries the password.
		return nil, fmt.Errorf("pgperturb: connect to event store: %w", err)
	}
	return &Perturber{conn: conn, tenant: tenant}, nil
}

// Close releases the connection.
func (p *Perturber) Close(ctx context.Context) error {
	if p.conn == nil {
		return nil
	}
	return p.conn.Close(ctx)
}

// deleteOneSQL removes exactly one base Measurement event in the window. Postgres
// DELETE has no LIMIT, so a CTE picks one row by the earliest occurred_time and the
// DELETE matches it on the full composite PK — a single row even if several devices
// emitted at the same instant. The base row alone is enough: the oracle counts base
// events, and there is no FK/cascade to measurement_events (an app-level natural-key
// join, ADR-026), so no child cleanup is needed for the count to drop by exactly 1.
const deleteOneSQL = `
WITH victim AS (
  SELECT tenant_id, device_token, event_type, occurred_time
  FROM "event-management"."events"
  WHERE tenant_id = $1
    AND event_type = $2
    AND occurred_time >= $3
    AND occurred_time <= $4
  ORDER BY occurred_time
  LIMIT 1
)
DELETE FROM "event-management"."events" e
USING victim v
WHERE e.tenant_id = v.tenant_id
  AND e.device_token = v.device_token
  AND e.event_type = v.event_type
  AND e.occurred_time = v.occurred_time;`

// DeleteOneMeasurement implements loadtest.Perturber: it removes one persisted base
// Measurement event whose occurred_time falls in w and returns the rows removed
// (the self-test requires exactly 1).
func (p *Perturber) DeleteOneMeasurement(ctx context.Context, w loadtest.Window) (int, error) {
	tag, err := p.conn.Exec(ctx, deleteOneSQL, p.tenant, loadtest.MeasurementEventType, w.Start, w.End)
	if err != nil {
		return 0, fmt.Errorf("pgperturb: delete one measurement event: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// compile-time proof the concrete type satisfies the harness interface.
var _ loadtest.Perturber = (*Perturber)(nil)
