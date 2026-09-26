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

// PrepareContext does not forget, it DISABLES: the statement it returns can run the
// fence write at any later moment, any number of times, and nothing about running it
// reaches this wrapper again. So once a statement naming the fence table is prepared on
// this transaction, no proof is recorded for the rest of it and every later write reads
// the fence, exactly as it did before the memo.
func (t *fenceTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if namesFence(ctx, query) {
		t.memo.disable()
	}
	return t.tx.PrepareContext(ctx, query)
}

// The three statement methods that carry their text forget every proof when the
// statement names the fence table, so a transaction that writes the fence itself cannot
// then be admitted on an answer it has just overturned.

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

// StmtContext binds a statement prepared elsewhere into this transaction. Its text is not
// visible here (*sql.Stmt does not expose it), so it may be a fence write, and it may run
// any number of times: the memo is disabled for the rest of the transaction, as for a
// fence statement prepared on it.
func (t *fenceTx) StmtContext(ctx context.Context, stmt *sql.Stmt) *sql.Stmt {
	t.memo.disable()
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
// Between them, the statement methods above cover every way a statement reaches this
// transaction. One that carries its text (a gorm Create or Exec, a Raw(...).Scan or
// Raw(...).Row(), a direct ConnPool.ExecContext, an INSERT ... RETURNING) forgets when the
// text names the fence and runs at once. One that can run later is PREPARED here, and
// disables the memo when it names the fence. One whose text cannot be seen (StmtContext)
// disables it regardless. The text test is deliberately a case-insensitive substring
// match, and it over-forgets — a SELECT of the fence by anything but the fence check also
// clears the memo — because over-forgetting costs one more read, never a wrong answer.
// What no wrapper can see is a statement that never reaches this transaction's
// connection at all; a write on another connection is another transaction, which is the
// window the doc comment on tenantFenceCheck accepts. The purge plants and lifts in
// transactions of its own.
func (t *fenceTx) forgetIfFence(ctx context.Context, query string) {
	if namesFence(ctx, query) {
		t.memo.forget()
	}
}

// namesFence reports whether query names the fence table and is not the fence's own read.
func namesFence(ctx context.Context, query string) bool {
	if !containsFold(query, FenceTable) {
		return false
	}
	return ctx == nil || ctx.Value(fenceReadKey{}) == nil
}

// containsFold reports whether s contains substr ignoring ASCII case, without allocating:
// it runs on every statement of every transaction. substr must begin with an ASCII
// letter (FenceTable does); a byte compare on that letter's two cases skips every offset
// where EqualFold could not match, so a long batch INSERT costs one pass of byte compares
// rather than an EqualFold call per byte.
func containsFold(s, substr string) bool {
	n := len(substr)
	if n == 0 {
		return true
	}
	first := substr[0] | 0x20 // ASCII lower case of a letter
	for i := 0; i+n <= len(s); i++ {
		if s[i]|0x20 == first && strings.EqualFold(s[i:i+n], substr) {
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
	// off is set, and never cleared, once a statement that may write the fence can run
	// out of this wrapper's sight (see fenceTx.PrepareContext and StmtContext). From then
	// on prove records nothing, so clear stays nil and every write reads the fence.
	off bool
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
	if gen != m.gen || m.off {
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

// disable drops every proof and records none for the rest of the transaction.
func (m *fenceMemo) disable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gen++
	m.clear = nil
	m.off = true
}

// memoOf returns the memo of the transaction pool belongs to, or nil outside one — and
// nil means "read the fence every time".
func memoOf(pool gorm.ConnPool) *fenceMemo {
	switch p := pool.(type) {
	case *fenceTx:
		return &p.memo
	}
	// The fencePool itself, a *sql.Conn from db.Connection, a bare *sql.Tx begun on one,
	// a guest's *sql.DB: no memo, so the fence is read on every statement. So is a
	// session opened with PrepareStmt (a *gorm.PreparedStmtTX): it binds cached statements
	// into the transaction through StmtContext, which carries no text, so a memo there
	// could not see a fence write and would be disabled at its first statement anyway.
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
// It also refuses a handle whose Statement pool is not its Config pool, which is what a
// handle INSIDE A TRANSACTION looks like. That is the only misuse it detects. It cannot
// tell a root handle from a session derived from one: a WithContext session passes, the
// swap lands on that session's own copy of the Config, and the root it came from — and
// every transaction begun on the root — keeps the raw pool, unmemoised, with nothing
// failing. A Session without a Context (Debug() is one) passes too and shares its
// parent's Statement, so the swap changes the parent's Statement pool while the parent's
// Config keeps the raw one. Neither is detectable through gorm's exported API, so the
// rule is the caller's: register on the handle every later session derives from, before
// any is derived. postgres.go does, on rdb.Database — the root, or the Debug() session
// that replaces it, whose parent is never used again — and so does every test fixture.
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
