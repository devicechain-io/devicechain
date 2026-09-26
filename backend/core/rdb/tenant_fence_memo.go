// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"gorm.io/gorm"
)

// fencePool is the connection pool the erasure fence installs under gorm (see
// installFenceMemo). It does one thing the *sql.DB beneath it does not: every transaction
// it begins carries a memo of the tenants already read clear in that transaction, so the
// fence is read at most once per tenant per transaction rather than once per statement.
//
// 🔴 IT DELEGATES EXPLICITLY AND DOES NOT EMBED *sql.DB, and that is load-bearing. gorm's
// Begin asks for TxBeginner — BeginTx(ctx, opts) (*sql.Tx, error) — BEFORE it asks for
// ConnPoolBeginner. Embedding *sql.DB would promote the first signature, and the method
// below would then shadow it only by an accident of Go's method-set rules; delete or
// rename it and every transaction would silently be a bare *sql.Tx with no memo, reading
// the fence on every statement again with nothing failing. Delegating means this type
// implements ConnPoolBeginner and nothing else.
type fencePool struct{ db *sql.DB }

// fenceTx is one transaction begun through a fencePool. Its memo is a FIELD, not an entry
// in a registry, so it cannot outlive the transaction, cannot be found by another one,
// and needs no eviction: a handle kept past Commit or Rollback can only reach a dead
// *sql.Tx, whose every method returns sql.ErrTxDone, so a proof it still holds can admit
// nothing.
//
// 🔴 IT MUST NEVER GAIN A BeginTx METHOD. gorm treats a ConnPool that is a TxCommitter as
// "already in a transaction": a nested Transaction becomes a SAVEPOINT on this same value,
// and the default per-statement transaction inside an explicit one finds no beginner and
// gets ErrInvalidTransaction, which it swallows. A BeginTx here would change both, and
// silently.
type fenceTx struct {
	tx   *sql.Tx
	db   *sql.DB
	memo fenceMemo
}

var (
	_ gorm.ConnPoolBeginner = (*fencePool)(nil)
	_ gorm.GetDBConnector   = (*fencePool)(nil)
	_ gorm.Tx               = (*fenceTx)(nil)
	_ gorm.GetDBConnector   = (*fenceTx)(nil)
)

func (p *fencePool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *fencePool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *fencePool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

func (p *fencePool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

// Ping keeps parity with the *sql.DB it replaces; gorm pings a pool that offers it.
func (p *fencePool) Ping() error { return p.db.Ping() }

// GetDBConn is how db.DB() reaches the real pool through this wrapper, which pool sizing,
// Close and the migration advisory lock all depend on.
func (p *fencePool) GetDBConn() (*sql.DB, error) { return p.db, nil }

// BeginTx begins a transaction that carries its own, empty, fence memo.
func (p *fencePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		// An UNTYPED nil: a (*fenceTx)(nil) in the interface would pass gorm's
		// "is this a committer" checks as a transaction that is not there.
		return nil, err
	}
	return &fenceTx{tx: tx, db: p.db}, nil
}

// The four statement methods forget every proof when the statement names the fence table
// (see forgetsFence), so a transaction that writes the fence itself cannot then be
// admitted on an answer it has just overturned.
func (t *fenceTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	t.forgetIfFence(ctx, query)
	return t.tx.PrepareContext(ctx, query)
}

func (t *fenceTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.forgetIfFence(ctx, query)
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *fenceTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	t.forgetIfFence(ctx, query)
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *fenceTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	t.forgetIfFence(ctx, query)
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *fenceTx) StmtContext(ctx context.Context, stmt *sql.Stmt) *sql.Stmt {
	return t.tx.StmtContext(ctx, stmt)
}

func (t *fenceTx) Commit() error   { return t.tx.Commit() }
func (t *fenceTx) Rollback() error { return t.tx.Rollback() }

// GetDBConn lets db.DB() inside a transaction return the pool the transaction came from,
// as it does for a bare *sql.Tx.
func (t *fenceTx) GetDBConn() (*sql.DB, error) { return t.db, nil }

// fenceReadKey marks the context of the fence's OWN read, so the statement that reads
// purged_tenants does not count as one that might have written it.
type fenceReadKey struct{}

// forgetIfFence drops every proof this transaction holds when query names the fence table
// and is not the fence's own read.
//
// It is deliberately a substring test, case-insensitive, on anything that reaches the
// transaction: a gorm Create or Exec, a Raw(...).Scan through the Row processor, a direct
// ConnPool.ExecContext, an INSERT ... RETURNING. It over-forgets — a SELECT of the fence
// by anything but the fence check also clears the memo — and over-forgetting costs one
// more read, never a wrong answer, so that is the direction to err in. What it cannot see
// is a statement that never reaches this wrapper: a *sql.Stmt prepared on the pool and
// bound into the transaction with StmtContext carries no text. Nothing in the repository
// writes the fence that way, or writes it inside a transaction that also writes tenant
// rows at all; the purge plants and lifts in transactions of their own.
func (t *fenceTx) forgetIfFence(ctx context.Context, query string) {
	if !containsFold(query, FenceTable) {
		return
	}
	if ctx != nil && ctx.Value(fenceReadKey{}) != nil {
		return
	}
	t.memo.forget()
}

// containsFold reports whether s contains substr ignoring ASCII case, without allocating:
// it runs on every statement of every transaction.
func containsFold(s, substr string) bool {
	n := len(substr)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], substr) {
			return true
		}
	}
	return false
}

// fenceMemo is the set of tenant tokens this transaction has READ the fence for and found
// no standing fence. Only that answer is recorded: a refusal is an error the statement
// already carries, and a failed read is never an answer (fail closed, unchanged).
type fenceMemo struct {
	// mu guards both fields. database/sql lets one *sql.Tx be used from several
	// goroutines, so the memo does too, cheaply.
	mu sync.Mutex
	// gen counts forgets. A read that began before a forget must not record its answer
	// after it: see unproven and prove.
	gen uint64
	// clear is allocated on the first proof; nil for a transaction that never wrote.
	clear map[string]struct{}
}

// unproven returns the tokens not yet proven clear in this transaction, and the
// generation the caller must hand back to prove. It returns its input unchanged, without
// allocating, while nothing is proven.
func (m *fenceMemo) unproven(tokens []string) ([]string, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.clear) == 0 {
		return tokens, m.gen
	}
	var ask []string
	for _, tok := range tokens {
		if _, ok := m.clear[tok]; !ok {
			ask = append(ask, tok)
		}
	}
	return ask, m.gen
}

// prove records tokens as read clear, unless the memo was forgotten since the read began
// (gen moved): the answer was read before the fence changed under this transaction, and
// recording it now would be exactly the stale proof forget exists to prevent.
func (m *fenceMemo) prove(tokens []string, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		return
	}
	if m.clear == nil {
		m.clear = make(map[string]struct{}, len(tokens))
	}
	for _, tok := range tokens {
		m.clear[tok] = struct{}{}
	}
}

// forget drops every proof.
func (m *fenceMemo) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.clear = nil
}

// memoOf returns the memo of the transaction pool belongs to, or nil outside one — and
// nil means "read the fence every time".
func memoOf(pool gorm.ConnPool) *fenceMemo {
	switch p := pool.(type) {
	case *fenceTx:
		return &p.memo
	case *gorm.PreparedStmtTX:
		// A session opened with PrepareStmt wraps the transaction it runs in.
		return memoOf(p.Tx)
	}
	// The fencePool itself, a *sql.Conn from db.Connection, a bare *sql.Tx begun on one,
	// a guest's *sql.DB: no memo, so the fence is read on every statement.
	return nil
}

// installFenceMemo swaps the *sql.DB under this gorm handle for a fencePool.
//
// 🔴 IT REFUSES A POOL IT CANNOT WRAP rather than leaving the fence unmemoised. The fence
// would still be CORRECT without the memo, only slower, and a silent performance cliff —
// every statement paying the read again, nothing failing — is exactly the plausible value
// a stub returns. The one realistic cause is gorm's PrepareStmt at Open, which makes the
// pool a *gorm.PreparedStmtDB; nothing here enables it.
//
// It also refuses a handle whose Statement pool is not its Config pool. A gorm Session
// without a Context shares its PARENT's Statement, so swapping the Statement's pool there
// would change the parent's while the parent's Config — what its default transactions
// restore to — kept the raw pool. Every caller passes a root handle or its Debug()
// session, where the two agree.
func installFenceMemo(db *gorm.DB) error {
	switch pool := db.Config.ConnPool.(type) {
	case *fencePool:
		if db.Statement.ConnPool != gorm.ConnPool(pool) {
			return fmt.Errorf("the erasure fence's pool is installed on this handle's configuration " +
				"but not on its statement; register the fence on the root handle")
		}
		return nil
	case *sql.DB:
		if db.Statement.ConnPool != gorm.ConnPool(pool) {
			return fmt.Errorf("the erasure fence must be registered on a root gorm handle: this one's "+
				"statement runs on %T, not on its configured pool", db.Statement.ConnPool)
		}
		fp := &fencePool{db: pool}
		// Config.ConnPool is what every Session copies and what a default transaction
		// restores the statement to; Statement.ConnPool is what the next getInstance and
		// Begin read. Both, or a default transaction would end on the raw pool.
		db.Config.ConnPool = fp
		db.Statement.ConnPool = fp
		return nil
	default:
		return fmt.Errorf("the erasure fence needs a *sql.DB connection pool to read the fence once "+
			"per tenant per transaction; this handle's pool is %T", pool)
	}
}
