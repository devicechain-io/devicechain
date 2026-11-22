// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"slices"
	"testing"
)

// Track each armed execution transaction, including retries. Refusal and reenable
// transactions may write after rollback, and the API reads the result after commit.
type commitPointStatements struct {
	armed      bool
	lastPool   gorm.ConnPool
	armedPools []gorm.ConnPool
	statements []string
}

func (w *commitPointStatements) arm() {
	w.armedPools = append(w.armedPools, w.lastPool)
	w.armed = true
}

func watchCommitPointStatements(t *testing.T, db *gorm.DB) *commitPointStatements {
	t.Helper()
	w := &commitPointStatements{}
	for _, cb := range []struct {
		kind     string
		register func(string, func(*gorm.DB)) error
		remove   func(string) error
	}{
		{"create", db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove},
		{"query", db.Callback().Query().Before("gorm:query").Register, db.Callback().Query().Remove},
		{"update", db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove},
		{"delete", db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove},
		{"row", db.Callback().Row().Before("gorm:row").Register, db.Callback().Row().Remove},
		{"raw", db.Callback().Raw().Before("gorm:raw").Register, db.Callback().Raw().Remove},
	} {
		kind, remove := cb.kind, cb.remove
		name := "commit_point_tail_" + kind
		require.NoError(t, cb.register(name, func(db *gorm.DB) {
			if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); !inTx {
				return
			}
			inArmedPool := slices.Contains(w.armedPools, db.Statement.ConnPool)
			if !w.armed || !inArmedPool {
				w.lastPool = db.Statement.ConnPool
			}
			if w.armed && inArmedPool {
				statement := db.Statement.Table
				if kind == "row" || kind == "raw" {
					statement = db.Statement.SQL.String()
				}
				w.statements = append(w.statements, kind+": "+statement)
			}
		}))
		t.Cleanup(func() { require.NoError(t, remove(name)) })
	}
	return w
}

func TestCommandSQLFreeCommitTail(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "refusal"}[refuse], func(t *testing.T) {
			r := newCommandRig(t)
			p := r.pending(t)
			w := watchCommitPointStatements(t, r.api.RDB.Database)
			r.dec.second = func() (actiongate.ExecutionDecision, error) {
				w.arm()
				d := r.dec.exec
				if refuse {
					d.Verdict = actiongate.VerdictRefuse
				}
				return d, nil
			}
			got, err := r.approve(p)
			w.armed = false
			require.NoError(t, err)
			require.NotNil(t, got)
			require.NotEmpty(t, w.armedPools)
			require.Empty(t, w.statements)
		})
	}
}
func TestCommandCommitTailWatcherDetectsSQL(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	w := watchCommitPointStatements(t, r.api.RDB.Database)
	r.dec.second = func() (actiongate.ExecutionDecision, error) { w.arm(); return r.dec.exec, nil }
	r.api.afterDecisionForTest = func(tx *gorm.DB) error { return tx.Exec("SELECT 1").Error }
	_, err := r.approve(p)
	w.armed = false
	require.NoError(t, err)
	require.NotEmpty(t, w.statements)
}
