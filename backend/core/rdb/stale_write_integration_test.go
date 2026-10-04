// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The stale-write guard, asked of a real PostgreSQL server.
//
// The SQLite tests in stale_write_test.go drive the guarded write to zero rows by moving
// the row BEFORE the write starts. They cannot show the case the guard exists for on the
// production database: a concurrent writer whose transaction is still OPEN when ours
// arrives. At READ COMMITTED our UPDATE then waits on the row lock and, once the other
// transaction commits, re-evaluates `updated_at = <what we read>` against the committed
// row. That re-evaluation is the whole guarantee, and only a real server has it.
//
// They also cannot show why a caller must reload before handing a version back. SQLite
// stores nanoseconds, so the value gorm leaves in memory after a write equals the stored
// one there; PostgreSQL stores microseconds, so it does not.
//
// Run it against a throwaway server (hack/integration-tests.sh does this in CI):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run StaleWrite -v
package rdb

import (
	"context"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

type staleItWidget struct {
	gorm.Model
	Name string
}

var staleItWidgets = NewStaleWriteError("widget")

// staleItDB initializes a functional area the way a service does and hands back a
// system-context session on it, with the table emptied.
func staleItDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbstalewrite"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "stale-write"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20260927000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&staleItWidget{}) },
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres",
				"sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	db := mgr.DB(core.WithSystemContext(ctx))
	// The instance id is fixed, so a server that ran this before still holds its rows.
	require.NoError(t, db.Exec("TRUNCATE TABLE stale_it_widgets").Error)
	return db
}

// A writer whose transaction is open when the guarded write arrives. The guarded write
// must WAIT for it, and then refuse — not overwrite what it committed.
func TestStaleWriteWaitsForAnOpenWriterAndThenRefuses(t *testing.T) {
	db := staleItDB(t)
	row := &staleItWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	var loaded staleItWidget
	require.NoError(t, db.First(&loaded, row.ID).Error)
	readAt := loaded.UpdatedAt

	other := db.Begin()
	require.NoError(t, other.Error)
	defer other.Rollback()
	require.NoError(t, other.Exec("UPDATE stale_it_widgets SET name = ?, updated_at = now() WHERE id = ?",
		"other writer", row.ID).Error)

	done := make(chan error, 1)
	go func() {
		done <- UpdateIfUnmoved(db, &loaded, readAt, map[string]any{"name": "mine"}, staleItWidgets)
	}()

	// The interleave, proved by the server rather than assumed from a sleep: our UPDATE is
	// a backend waiting on a lock. Without this, a guarded write that ran before the other
	// writer's UPDATE (and so matched its row) would pass the assertions below for the
	// wrong reason.
	deadline := time.Now().Add(10 * time.Second)
	var waiting int64
	for time.Now().Before(deadline) {
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND pid <> pg_backend_pid() AND query LIKE 'UPDATE %stale_it_widgets%'`).Scan(&waiting).Error)
		if waiting == 1 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the guarded write returned (%v) while another writer held the row", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	require.Equal(t, int64(1), waiting, "the guarded write never waited on the other writer's row lock")

	require.NoError(t, other.Commit().Error)

	select {
	case err := <-done:
		assert.Same(t, staleItWidgets, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the guarded write did not return after the other writer committed")
	}
	var after staleItWidget
	require.NoError(t, db.First(&after, row.ID).Error)
	assert.Equal(t, "other writer", after.Name)
}

// The version a caller may hand back is the STORED one. gorm leaves the value it sent on
// the record, at nanoseconds; PostgreSQL keeps microseconds. Handing back the in-memory
// value makes the caller's next save stale before anyone else has written.
func TestStaleWriteVersionMustBeReadBackNotTakenFromMemory(t *testing.T) {
	db := staleItDB(t)
	row := &staleItWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	var loaded staleItWidget
	require.NoError(t, db.First(&loaded, row.ID).Error)

	// A clock with sub-microsecond digits, so the lost digits are certain rather than likely.
	// It is past the version just read, so the write stores it rather than the floor.
	sent := loaded.UpdatedAt.Truncate(time.Second).Add(time.Hour + 123456789*time.Nanosecond)
	session := db.Session(&gorm.Session{NowFunc: func() time.Time { return sent }})
	require.NoError(t, UpdateIfUnmoved(session, &loaded, loaded.UpdatedAt, map[string]any{"name": "mine"}, staleItWidgets))
	require.True(t, loaded.UpdatedAt.Equal(sent), "gorm left %v on the record, want the %v it sent", loaded.UpdatedAt, sent)

	var stored staleItWidget
	require.NoError(t, db.First(&stored, row.ID).Error)
	// Whether the driver truncates or the server rounds, what is kept is whole microseconds.
	require.Zero(t, stored.UpdatedAt.Nanosecond()%1000, "the stored version has sub-microsecond digits")
	require.NotEqual(t, sent.Nanosecond(), stored.UpdatedAt.Nanosecond(),
		"the stored version kept every digit gorm sent; this server cannot show the defect")

	assert.Same(t, staleItWidgets, RefuseIfMoved(stored.UpdatedAt, gqlcore.FormatTime(loaded.UpdatedAt), staleItWidgets),
		"the in-memory version was accepted; the reload this test argues for would be unnecessary")
	assert.NoError(t, RefuseIfMoved(stored.UpdatedAt, gqlcore.FormatTime(stored.UpdatedAt), staleItWidgets))
}

// A clock less than a microsecond past the version just read would be stored AS that version
// by PostgreSQL, which keeps whole microseconds. The write must store a version strictly past
// it at that precision, or a writer holding the old one is still accepted.
func TestStaleWriteMovesTheStoredVersionAtMicrosecondPrecision(t *testing.T) {
	db := staleItDB(t)
	row := &staleItWidget{Name: "original"}
	require.NoError(t, db.Create(row).Error)
	var loaded staleItWidget
	require.NoError(t, db.First(&loaded, row.ID).Error)
	readAt := loaded.UpdatedAt
	require.Zero(t, readAt.Nanosecond()%1000)

	nudged := readAt.Add(400 * time.Nanosecond)
	session := db.Session(&gorm.Session{NowFunc: func() time.Time { return nudged }})
	require.NoError(t, UpdateIfUnmoved(session, &loaded, readAt, map[string]any{"name": "mine"}, staleItWidgets))

	var stored staleItWidget
	require.NoError(t, db.First(&stored, row.ID).Error)
	assert.True(t, stored.UpdatedAt.Equal(readAt.Add(time.Microsecond)), "stored %v, want %v", stored.UpdatedAt, readAt.Add(time.Microsecond))
	assert.Same(t, staleItWidgets, RefuseIfMoved(stored.UpdatedAt, gqlcore.FormatTime(readAt), staleItWidgets))
}
