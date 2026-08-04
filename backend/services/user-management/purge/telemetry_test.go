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
	"gorm.io/gorm"
)

// stubConnector stands in for the guest connection, and only for the connecting half.
//
// 🔴 IT MUST NEVER BE ASKED FOR A HANDLE. Every test here covers a path that ends at
// Connect, so DB is a deliberate failure: if a change makes Erase reach the sweep with a
// connection that was never opened, that must surface as a panic naming this line rather
// than as a nil dereference deep in gorm — or, worse, as a test that quietly measures the
// stub instead of the store.
type stubConnector struct {
	err   error
	calls int
}

func (s *stubConnector) Connect(context.Context) error { s.calls++; return s.err }

func (s *stubConnector) DB(context.Context) *gorm.DB {
	panic("the telemetry store asked for a database handle after a failed connect")
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
