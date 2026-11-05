// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package pgperturb

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/devicechain-io/dc-simulator/loadtest"
)

// Oracle is the aggregate identity oracle's reader: it asks event-management's Postgres,
// directly and read-only, for each device's (count, hash sum) over one tenant's Measurement
// rows in a window, so the answer costs one grouped query instead of one paged read per row.
// Like the Perturber it lives apart from the harness: the load test proper is an untrusted
// client with no database access, and the pgx driver links only into the binaries that are
// allowed to reach under it.
type Oracle struct {
	// mu serialises the connection: a pgx connection is not safe for concurrent use, and the
	// exact drill-down is driven by a pool of workers.
	mu     sync.Mutex
	conn   *pgx.Conn
	tenant string
}

// NewOracle opens a pgx connection with dsn, scoped to tenant. The caller must Close it.
func NewOracle(ctx context.Context, dsn, tenant string) (*Oracle, error) {
	if tenant == "" {
		return nil, fmt.Errorf("pgperturb: tenant is required")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgperturb: connect to event store: %w", err)
	}
	return &Oracle{conn: conn, tenant: tenant}, nil
}

// Close releases the connection.
func (o *Oracle) Close(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return nil
	}
	return o.conn.Close(ctx)
}

// aggregateSQL is the grouped query. It must stay bit-for-bit equal to loadtest.MicrosHash:
//
//   - the instant is taken in Unix MICROSECONDS, rounded (extract(epoch) is double precision
//     before PG 14 and numeric from 14; round() makes both exact at the microsecond);
//   - int8send gives the 8-byte big-endian two's-complement encoding Go hashes;
//   - sha256 is built in since PG 11; its first 8 bytes read as a signed bigint are the Go
//     uint64 modulo 2^64;
//   - the sum is taken in numeric, so it cannot overflow, then reduced modulo 2^64 into
//     [0, 2^64) (the double % makes a negative remainder positive), and returned as text
//     because a uint64 does not fit any integer type PG has.
//
// The window bounds are inclusive, as in the perturber and the GraphQL read.
const aggregateSQL = `
SELECT device_token,
       count(*)::bigint,
       (((sum(h::numeric) % 18446744073709551616) + 18446744073709551616) % 18446744073709551616)::text
FROM (
  SELECT device_token,
         ('x' || encode(substr(sha256(int8send(round(extract(epoch FROM occurred_time) * 1000000)::bigint)), 1, 8), 'hex'))::bit(64)::bigint AS h
  FROM ` + eventsTable + `
  WHERE tenant_id = $1
    AND event_type = $2
    AND occurred_time >= $3
    AND occurred_time <= $4
) x
GROUP BY device_token`

// DeviceAggregates implements loadtest.AggregateReader.
func (o *Oracle) DeviceAggregates(ctx context.Context, w loadtest.Window) (map[string]loadtest.DeviceAggregate, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows, err := o.conn.Query(ctx, aggregateSQL, o.tenant, loadtest.MeasurementEventType, w.Start, w.End)
	if err != nil {
		return nil, fmt.Errorf("pgperturb: aggregate query: %w", err)
	}
	defer rows.Close()
	out := map[string]loadtest.DeviceAggregate{}
	for rows.Next() {
		var (
			dev  string
			n    int64
			hash string
		)
		if err := rows.Scan(&dev, &n, &hash); err != nil {
			return nil, fmt.Errorf("pgperturb: aggregate row: %w", err)
		}
		h, err := strconv.ParseUint(hash, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("pgperturb: aggregate hash %q for %q: %w", hash, dev, err)
		}
		out[dev] = loadtest.DeviceAggregate{Count: n, Hash: h}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgperturb: aggregate rows: %w", err)
	}
	return out, nil
}

const deviceTimesSQL = `
SELECT round(extract(epoch FROM occurred_time) * 1000000)::bigint
FROM ` + eventsTable + `
WHERE tenant_id = $1
  AND device_token = $2
  AND event_type = $3
  AND occurred_time >= $4
  AND occurred_time <= $5`

// DeviceIdentities reads one device's stored instants exactly (Unix microseconds, with
// repeats): the aggregate oracle's drill-down for a device whose pair disagrees. It does not
// page; one device's rows in one run's window are small.
func (o *Oracle) DeviceIdentities(ctx context.Context, device string, w loadtest.Window) ([]int64, int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows, err := o.conn.Query(ctx, deviceTimesSQL, o.tenant, device, loadtest.MeasurementEventType, w.Start, w.End)
	if err != nil {
		return nil, 0, fmt.Errorf("pgperturb: read %q: %w: %w", device, loadtest.ErrReadTransport, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var us int64
		if err := rows.Scan(&us); err != nil {
			return nil, 1, fmt.Errorf("pgperturb: read %q: %w", device, err)
		}
		out = append(out, us)
	}
	if err := rows.Err(); err != nil {
		return nil, 1, fmt.Errorf("pgperturb: read %q: %w: %w", device, loadtest.ErrReadTransport, err)
	}
	return out, 1, nil
}
