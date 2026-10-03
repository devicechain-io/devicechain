// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgperturb is the privileged out-of-band stimulus for the ADR-064 oracle
// self-test: it deletes and duplicates persisted rows DIRECTLY in event-management's
// Postgres, beneath the tenant GraphQL API the oracle reads through. It is the one thing
// the load test proper is forbidden — the harness is an untrusted client with no DB
// access — and is deliberately isolated in its own package so the pgx driver links ONLY
// into the self-test binary, never into dc-loadtest or the sim.
package pgperturb

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/devicechain-io/dc-simulator/loadtest"
)

// eventsTable is schema-qualified because the functional-area schema name carries
// a hyphen, so it must be quoted; a bare psql session defaults search_path to
// public, where this table does not exist.
const eventsTable = `"event-management"."events"`

// Perturber deletes and duplicates persisted base events in the event store
// out-of-band. It holds a single pgx connection scoped to one tenant.
type Perturber struct {
	conn   *pgx.Conn
	tenant string
}

// New opens a pgx connection with dsn (e.g.
// postgres://devicechain:devicechain@127.0.0.1:5432/devicechain) and scopes every
// statement to tenant (the slug/token stored verbatim in events.tenant_id). The caller
// must Close it.
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

// deleteOneSQL removes exactly one base Measurement event in the window and returns
// its identity. Postgres DELETE has no LIMIT, so a CTE picks one row — the earliest by
// (occurred_time, event_id), a total order — and the DELETE matches it on the table's
// primary key, (tenant_id, event_id, occurred_time): a single row even if several
// devices emitted at the same instant. The base row alone is enough: the oracle counts
// and reads base events, and there is no FK/cascade to measurement_events (an app-level
// join, ADR-026), so no child cleanup is needed for the count to drop by exactly 1.
const deleteOneSQL = `
WITH victim AS (
  SELECT tenant_id, event_id, occurred_time
  FROM ` + eventsTable + `
  WHERE tenant_id = $1
    AND event_type = $2
    AND occurred_time >= $3
    AND occurred_time <= $4
  ORDER BY occurred_time, event_id
  LIMIT 1
)
DELETE FROM ` + eventsTable + ` e
USING victim v
WHERE e.tenant_id = v.tenant_id
  AND e.event_id = v.event_id
  AND e.occurred_time = v.occurred_time
RETURNING e.device_token, e.occurred_time;`

// duplicateOneSQL inserts one extra base Measurement row with the same device and
// occurred_time as the LATEST row in the window, under a different event_id — the shape
// of a duplicate that escaped the store's content dedup (a redelivery that re-resolved
// to different bytes gets a different event_id). The new event_id differs, so the
// primary key accepts it; the simulator sends no alt_id, so the partial alt-id index is
// not involved. The columns are the base event table's.
const duplicateOneSQL = `
WITH src AS (
  SELECT tenant_id, event_id, device_token, event_type, occurred_time, source, alt_id, processed_time
  FROM ` + eventsTable + `
  WHERE tenant_id = $1
    AND event_type = $2
    AND occurred_time >= $3
    AND occurred_time <= $4
  ORDER BY occurred_time DESC, event_id DESC
  LIMIT 1
)
INSERT INTO ` + eventsTable + `
  (tenant_id, event_id, device_token, event_type, occurred_time, source, alt_id, processed_time)
SELECT tenant_id, sha256(event_id || '\x01'::bytea), device_token, event_type, occurred_time,
       source, alt_id, processed_time
FROM src
RETURNING device_token, occurred_time;`

// DeleteOneMeasurement implements loadtest.Perturber: it removes one persisted base
// Measurement event whose occurred_time falls in w and returns its identity and the rows
// removed (the self-test requires exactly 1).
func (p *Perturber) DeleteOneMeasurement(ctx context.Context, w loadtest.Window) (loadtest.IdentityKey, int, error) {
	return p.returning(ctx, "delete one measurement event", deleteOneSQL, w)
}

// DuplicateOneMeasurement implements loadtest.Perturber: it inserts one extra base
// Measurement row duplicating the latest one in w and returns its identity and the rows
// inserted (the self-test requires exactly 1).
func (p *Perturber) DuplicateOneMeasurement(ctx context.Context, w loadtest.Window) (loadtest.IdentityKey, int, error) {
	return p.returning(ctx, "duplicate one measurement event", duplicateOneSQL, w)
}

// returning runs a statement that RETURNs (device_token, occurred_time) for each row it
// touched, and reports the first row's identity and how many rows came back. It reads
// the rows rather than a command tag because the identity is the point: the self-test
// asserts the oracle names exactly this event.
func (p *Perturber) returning(ctx context.Context, what, sql string, w loadtest.Window) (loadtest.IdentityKey, int, error) {
	rows, err := p.conn.Query(ctx, sql, p.tenant, loadtest.MeasurementEventType, w.Start, w.End)
	if err != nil {
		return loadtest.IdentityKey{}, 0, fmt.Errorf("pgperturb: %s: %w", what, err)
	}
	defer rows.Close()
	var (
		key loadtest.IdentityKey
		n   int
	)
	for rows.Next() {
		var (
			device string
			at     time.Time
		)
		if err := rows.Scan(&device, &at); err != nil {
			return loadtest.IdentityKey{}, n, fmt.Errorf("pgperturb: %s: %w", what, err)
		}
		if n == 0 {
			key = loadtest.IdentityKey{Device: device, OccurredMicros: at.UnixMicro()}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return loadtest.IdentityKey{}, n, fmt.Errorf("pgperturb: %s: %w", what, err)
	}
	return key, n, nil
}

// compile-time proof the concrete type satisfies the harness interface.
var _ loadtest.Perturber = (*Perturber)(nil)
