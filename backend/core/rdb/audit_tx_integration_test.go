// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The audit journal's row commits with the mutation it describes, asked of a real
// PostgreSQL server. SQLite shows the rollback (audit_test.go); only a server shows what
// the extra in-transaction insert does to LOCKING, which is where an unreviewed hook has
// deadlocked before: a row an uncommitted statement has inserted is a lock like any other.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -run AuditTx -v
package rdb

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

// errDeliberateRollback is what a writer returns to roll its transaction back on purpose.
var errDeliberateRollback = errors.New("deliberate rollback")

type auditTxWidget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func auditTxDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbaudittx"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "audit-tx"},
		Migrations: []*gormigrate.Migration{{
			ID: "20261022000000",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&auditTxWidget{}, &AuditEvent{})
			},
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
	require.NoError(t, db.Exec("TRUNCATE TABLE audit_tx_widgets").Error)
	require.NoError(t, db.Exec("DELETE FROM audit_events WHERE table_name = 'audit_tx_widgets'").Error)
	return db
}

func auditTxRows(t *testing.T, db *gorm.DB, op string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events
		WHERE table_name = 'audit_tx_widgets' AND operation = ?`, op).Scan(&n).Error)
	return n
}

// A journal insert the SERVER refuses rolls back the change on the real driver, and a
// change that succeeds leaves both rows.
func TestAuditTxServerRefusalRollsBackTheChange(t *testing.T) {
	db := auditTxDB(t)

	ok := &auditTxWidget{Name: "kept"}
	require.NoError(t, db.Create(ok).Error)
	assert.Equal(t, int64(1), auditTxRows(t, db, "create"))

	// A trigger is the server-side analogue of a full disk or a lost grant: the INSERT
	// is well-formed and the server says no.
	require.NoError(t, db.Exec(`CREATE OR REPLACE FUNCTION audit_tx_refuse() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'audit refused'; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, db.Exec(`DROP TRIGGER IF EXISTS audit_tx_refuse ON audit_events`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER audit_tx_refuse BEFORE INSERT ON audit_events
		FOR EACH ROW WHEN (NEW.table_name = 'audit_tx_widgets' AND NEW.operation <> 'create')
		EXECUTE FUNCTION audit_tx_refuse()`).Error)
	t.Cleanup(func() { _ = db.Exec("DROP TRIGGER IF EXISTS audit_tx_refuse ON audit_events").Error })

	err := db.Model(&auditTxWidget{}).Where("id = ?", ok.ID).Update("name", "changed").Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit refused")
	var name string
	require.NoError(t, db.Raw("SELECT name FROM audit_tx_widgets WHERE id = ?", ok.ID).Scan(&name).Error)
	assert.Equal(t, "kept", name, "the update was saved although its journal row was refused")

	require.Error(t, db.Delete(ok).Error)
	var n int64
	require.NoError(t, db.Raw("SELECT count(*) FROM audit_tx_widgets WHERE id = ?", ok.ID).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "the delete was saved although its journal row was refused")
	assert.Equal(t, int64(0), auditTxRows(t, db, "update")+auditTxRows(t, db, "delete"))
}

// The journal insert is a plain append: it waits for no other transaction's journal row.
// A transaction that has written (and so inserted a journal row it has not committed)
// must not stop an unrelated auto-commit mutation, whose own journal row sits beside it.
func TestAuditTxUncommittedJournalRowBlocksNoOtherWriter(t *testing.T) {
	db := auditTxDB(t)
	a, b := &auditTxWidget{Name: "a"}, &auditTxWidget{Name: "b"}
	require.NoError(t, db.Create(a).Error)
	require.NoError(t, db.Create(b).Error)

	held := db.Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	require.NoError(t, held.Model(&auditTxWidget{}).Where("id = ?", a.ID).Update("name", "a2").Error)

	done := make(chan error, 1)
	go func() { done <- db.Model(&auditTxWidget{}).Where("id = ?", b.ID).Update("name", "b2").Error }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("an unrelated mutation waited on another transaction's uncommitted journal row")
	}
	require.NoError(t, held.Commit().Error)
	assert.Equal(t, int64(2), auditTxRows(t, db, "update"))
}

// Many writers mutate overlapping rows, each transaction taking its rows in ascending id
// (the order the services observe) and journaling each statement in the transaction,
// alongside auto-commit writers. No deadlock may surface, every committed change must
// have exactly one journal row, and every rolled-back one none.
func TestAuditTxConcurrentWritersDoNotDeadlockAndEveryChangeIsJournaled(t *testing.T) {
	db := auditTxDB(t)
	const rows, workers, iterations = 12, 8, 40
	ids := make([]uint, rows)
	for i := range ids {
		w := &auditTxWidget{Name: "seed"}
		require.NoError(t, db.Create(w).Error)
		ids[i] = w.ID
	}
	base := auditTxRows(t, db, "update")

	var committedUpdates int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, workers*iterations)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 1))
			for i := 0; i < iterations; i++ {
				pick := []uint{ids[rng.Intn(rows)], ids[rng.Intn(rows)], ids[rng.Intn(rows)]}
				sort.Slice(pick, func(x, y int) bool { return pick[x] < pick[y] })
				var distinct []uint
				for _, id := range pick {
					if len(distinct) == 0 || distinct[len(distinct)-1] != id {
						distinct = append(distinct, id)
					}
				}
				if w%2 == 0 {
					// Auto-commit: each statement is its own transaction with its journal row.
					for _, id := range distinct {
						if err := db.Model(&auditTxWidget{}).Where("id = ?", id).
							Update("name", fmt.Sprintf("w%d-%d", w, i)).Error; err != nil {
							errs <- err
							return
						}
						mu.Lock()
						committedUpdates++
						mu.Unlock()
					}
					continue
				}
				rollback := i%5 == 0
				err := db.Transaction(func(tx *gorm.DB) error {
					for _, id := range distinct {
						if err := tx.Model(&auditTxWidget{}).Where("id = ?", id).
							Update("name", fmt.Sprintf("w%d-%d", w, i)).Error; err != nil {
							return err
						}
					}
					if rollback {
						return errDeliberateRollback
					}
					return nil
				})
				if rollback {
					// Only the deliberate error is expected here: a deadlock (or any other
					// failure) in a transaction that was going to roll back anyway is still
					// a failure, and must not be discarded with it.
					if !errors.Is(err, errDeliberateRollback) {
						errs <- err
						return
					}
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				committedUpdates += int64(len(distinct))
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a writer failed (a 40P01 here is a deadlock the journal introduced): %v", err)
	}
	assert.Equal(t, committedUpdates, auditTxRows(t, db, "update")-base,
		"journal rows must equal committed updates: a rolled-back change leaves none, a committed one exactly one")
}
