// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// sequenceOrphanSource queries the authority afresh after the sweep scans the
// projection. The scratch table has no created_at; stamped rows only compare IDs.
type sequenceOrphanSource struct {
	db      *sql.DB
	lookups int
}

func (s *sequenceOrphanSource) Tenants(context.Context) ([]string, error) {
	return []string{"a"}, nil
}

func (s *sequenceOrphanSource) Devices(ctx context.Context, tenant string, tokens []string) (map[string]LiveDevice, error) {
	s.lookups++
	live := make(map[string]LiveDevice)
	for _, token := range tokens {
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM orphan_sequence.devices
			WHERE tenant_id = $1 AND token = $2 AND deleted_at IS NULL`, tenant, token).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		live[token] = LiveDevice{Token: token, ID: id, CreatedAt: time.Unix(1, 0).UTC()}
	}
	return live, nil
}

// A obtains its sequence ID before B but commits after B has been deleted. The
// partial unique index permits this schedule: IDs do not describe commit order.
// Run with -tags integration and DC_IT_PGHOST/DC_IT_PGPORT/DC_IT_PGUSER/DC_IT_PGPASSWORD.
func TestOrphanSweepRepairsOutOfOrderSequenceCommitsOnPostgres(t *testing.T) {
	mgr, _ := newFenceBenchManager(t, fmt.Sprintf("dsorphanseq%d", time.Now().UnixNano()), true)
	api := model.NewApi(mgr)
	db, err := mgr.Database.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	_, err = db.ExecContext(ctx, `CREATE SCHEMA orphan_sequence`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE orphan_sequence.devices (
		id bigserial PRIMARY KEY, tenant_id text, token text,
		deleted_at timestamptz, stall boolean DEFAULT false)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE UNIQUE INDEX devices_live_token
		ON orphan_sequence.devices (tenant_id, token) WHERE deleted_at IS NULL`)
	require.NoError(t, err)
	const lockKey = 28620017
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION orphan_sequence.stall_insert()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.stall THEN
				PERFORM pg_advisory_xact_lock(%d);
			END IF;
			RETURN NEW;
		END;
		$$`, lockKey))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TRIGGER stall_before_index BEFORE INSERT
		ON orphan_sequence.devices FOR EACH ROW EXECUTE FUNCTION orphan_sequence.stall_insert()`)
	require.NoError(t, err)

	// The trigger runs after nextval but before the partial unique index insert.
	keeper, err := db.Conn(ctx)
	require.NoError(t, err)
	defer keeper.Close()
	hold, err := keeper.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer hold.Rollback()
	_, err = hold.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey)
	require.NoError(t, err)
	var keeperPID int
	require.NoError(t, hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&keeperPID))

	aConn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() {
		// Also release A on assertion failure before closing its connection.
		_ = hold.Rollback()
		cancel()
		_ = aConn.Close()
	}()
	var aPID int
	require.NoError(t, aConn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&aPID))
	type insertResult struct {
		id  int64
		err error
	}
	aDone := make(chan insertResult, 1)
	go func() {
		var result insertResult
		result.err = aConn.QueryRowContext(ctx, `INSERT INTO orphan_sequence.devices
			(tenant_id, token, stall) VALUES ('a', 'd', true) RETURNING id`).Scan(&result.id)
		aDone <- result
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := db.QueryRowContext(ctx, `SELECT $1 = ANY(pg_blocking_pids($2))`, keeperPID, aPID).Scan(&blocked)
		require.NoError(t, err)
		return blocked
	}, 10*time.Second, 5*time.Millisecond, "A must obtain its ID and stall before B inserts")

	// These autocommit statements finish while A is still inside its trigger.
	var bID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO orphan_sequence.devices
		(tenant_id, token) VALUES ('a', 'd') RETURNING id`).Scan(&bID))
	deleted, err := db.ExecContext(ctx, `UPDATE orphan_sequence.devices SET deleted_at = now() WHERE id = $1`, bID)
	require.NoError(t, err)
	n, err := deleted.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.NoError(t, hold.Commit())
	var a insertResult
	select {
	case a = <-aDone:
	case <-ctx.Done():
		t.Fatal("A did not finish after releasing the advisory lock:", ctx.Err())
	}
	require.NoError(t, a.err)
	require.Less(t, a.id, bID)
	var liveID int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM orphan_sequence.devices
		WHERE tenant_id = 'a' AND token = 'd' AND deleted_at IS NULL`).Scan(&liveID))
	require.Equal(t, a.id, liveID)

	tenantCtx := core.WithTenant(ctx, "a")
	at := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	require.NoError(t, api.MergeProjectionBatch(tenantCtx, []model.ProjectionUpdate{raceUpdate("d", uint64(bID), at, float64(bID))}))
	requireProjection(t, api, tenantCtx, "d", 1)
	var before model.DeviceState
	require.NoError(t, api.RDB.DB(tenantCtx).First(&before).Error)
	require.Equal(t, sql.NullInt64{Int64: bID, Valid: true}, before.Incarnation)

	source := &sequenceOrphanSource{db: db}
	require.NoError(t, (&OrphanSweep{Api: api, Source: source}).Sweep(ctx))
	require.Equal(t, 1, source.lookups, "the sweep must query the live authority after scanning")
	requireProjection(t, api, tenantCtx, "d", 0)
	require.NoError(t, api.MergeProjectionBatch(tenantCtx, []model.ProjectionUpdate{raceUpdate("d", uint64(a.id), at.Add(time.Minute), float64(a.id))}))
	requireProjection(t, api, tenantCtx, "d", 1)
	var rebuilt model.DeviceState
	require.NoError(t, api.RDB.DB(tenantCtx).First(&rebuilt).Error)
	require.Equal(t, sql.NullInt64{Int64: a.id, Valid: true}, rebuilt.Incarnation)
	var reading model.LatestMeasurement
	require.NoError(t, api.RDB.DB(tenantCtx).First(&reading).Error)
	require.Equal(t, float64(a.id), reading.Value.Float64)
}
