// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubConnector stands in for the guest connection.
//
// Connect is dictated by the test. DB hands back a real gorm handle over in-memory SQLite
// carrying a stand-in `pg_namespace`, which is enough to drive the schema probe's branch —
// the probe's SQL is portable, and whether it names the right CATALOG is the one thing a
// unit test cannot check, so that is asserted against real Postgres in the purge drill
// instead. With no handle configured, DB fails loudly rather than returning nil: a change
// that made Erase reach the sweep on an unopened connection must name this line rather
// than surface as a nil dereference deep in gorm.
type stubConnector struct {
	err   error
	calls int
	db    *gorm.DB
}

func (s *stubConnector) Connect(context.Context) error { s.calls++; return s.err }

func (s *stubConnector) DB(context.Context) *gorm.DB {
	if s.db == nil {
		panic("the telemetry store asked for a database handle it was never given")
	}
	return s.db
}

// withSchemas builds the stub's handle with a stand-in catalog listing the given schemas.
func withSchemas(t *testing.T, schemas ...string) *stubConnector {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE pg_namespace (nspname text)`).Error)
	for _, s := range schemas {
		require.NoError(t, db.Exec(`INSERT INTO pg_namespace (nspname) VALUES (?)`, s).Error)
	}
	return &stubConnector{db: db}
}

func telemetryWith(c connector) *Telemetry {
	return &Telemetry{guest: c, rel: NewRelational(StoreTelemetry, c, nil)}
}

// TestAnAbsentTelemetryDatabaseIsCleanRatherThanStuck covers the one case where this
// store concludes there is nothing to erase without erasing anything.
//
// It is a real deployment, not a hypothetical: the `ingest-only` profile ships no
// event-management, the telemetry cluster is provisioned unconditionally anyway, and the
// instance database on it is created by whichever service owns it — so on such an
// instance there is nothing there at all. Without this branch every purge on that profile
// blocks forever and no token is ever released.
func TestAnAbsentTelemetryDatabaseIsCleanRatherThanStuck(t *testing.T) {
	// Wrapped exactly as Connect wraps it, because IsMissingDatabase uses errors.As and
	// a test on a bare error would not notice a wrapper that formatted instead.
	stub := &stubConnector{err: fmt.Errorf("connecting to the %q database: %w",
		"tsdb", &pgconn.PgError{Code: "3D000"})}

	out, err := telemetryWith(stub).Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err, "a database that does not exist is an answer, not a failure to retry")
	assert.True(t, out.Clean(), "an absent database holds no rows, so it cannot be holding this "+
		"tenant's — a store that stayed dirty here would block completion forever")
	assert.Zero(t, out.Rows, "nothing was erased and the ledger must not imply otherwise")
	assert.Equal(t, 1, stub.calls)
}

// TestATelemetryClusterThatCannotBeReachedBlocksThePurge is the assertion that keeps the
// one above from being a hole.
//
// The dangerous version of this store treats every connect failure as "nothing there".
// That completes a purge, writes a deletion record and releases the token over a cluster
// that was merely down — an erasure claim that is simply false. Only the database
// ANSWERING that it does not exist may be read that way; everything else is retryable.
func TestATelemetryClusterThatCannotBeReachedBlocksThePurge(t *testing.T) {
	for name, cause := range map[string]error{
		"refused": errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"),
		"bad password": fmt.Errorf("connecting to the %q database: %w", "tsdb",
			&pgconn.PgError{Code: "28P01"}),
		"no privilege": fmt.Errorf("connecting to the %q database: %w", "tsdb",
			&pgconn.PgError{Code: "42501"}),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := telemetryWith(&stubConnector{err: cause}).
				Erase(context.Background(), "acme", time.Now())

			require.Errorf(t, err, "%q is not the cluster telling us the database is absent; "+
				"reading it that way completes a purge over a store nobody reached", name)
			assert.Zero(t, out.Rows)
		})
	}
}

// TestTheTelemetryStoreKeepsItsLedgerName pins the string, because it is PERSISTED: the
// per-store ledger keys on it, so renaming one orphans the history of every purge that
// used the old name — including the passes recorded while this store was still Pending.
func TestTheTelemetryStoreKeepsItsLedgerName(t *testing.T) {
	assert.Equal(t, "tsdb", NewTelemetry(nil).Name())
	assert.Equal(t, StoreTelemetry, NewTelemetry(nil).Name())
}

// TestATelemetryDatabaseWithoutTheOwningSchemaIsCleanRatherThanStuck covers the second of
// the two ways an instance says it has no telemetry store, and it exists because the first
// one was wrong on its own.
//
// 🔴 THE ORIGINAL REASONING WAS: an instance running no event-management never creates the
// instance database on the telemetry cluster, so 3D000 is the whole signal. It is false.
// CNPG's initdb creates a database named by `timescale_database`, whose default is the same
// as the instance id's — so on the `ingest-only` profile the database is present, empty,
// and the connect succeeds. With only the 3D000 branch, core's empty-classification guard
// then fails the store on every pass forever: no completion, no token release, permanently,
// on a shipped profile.
func TestATelemetryDatabaseWithoutTheOwningSchemaIsCleanRatherThanStuck(t *testing.T) {
	// A database that exists and has never had event-management run against it — the
	// public schema is there, the area's is not.
	stub := withSchemas(t, "public", "information_schema")

	out, err := telemetryWith(stub).Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err, "the area was never deployed here; there is nothing to retry")
	assert.True(t, out.Clean())
	assert.Zero(t, out.Rows)
	assert.Equal(t, 1, stub.calls)
}

// TestATelemetryDatabaseWithTheOwningSchemaIsActuallySwept is the control, and without it
// the test above is satisfied by a store that reports clean unconditionally.
//
// The sweep is reached and fails on the stand-in catalog, which is the proof that matters
// here: what must not happen is Erase returning a clean outcome without going near the
// database. Where the sweep GOES once reached is the purge drill's question, against a real
// schema with real rows in it.
func TestATelemetryDatabaseWithTheOwningSchemaIsActuallySwept(t *testing.T) {
	stub := withSchemas(t, "public", telemetrySchema)

	out, err := telemetryWith(stub).Erase(context.Background(), "acme", time.Now())

	require.Error(t, err,
		"the owning schema is present, so this store must sweep rather than conclude anything")
	assert.Zero(t, out.Rows)
}
