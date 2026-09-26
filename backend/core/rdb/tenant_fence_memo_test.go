// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The erasure fence is read at most once per tenant per transaction. These tests pin that
// the memo saves what it claims to, BY VALUE (fence reads counted by the statement
// counter), and — the half that matters more — that it never admits a write the fence
// read on every statement would have refused, except inside the one window the doc
// comment on tenantFenceCheck accepts.

// newFenceMemoDB is newFenceDB with two differences. The database is shared-cache and
// named per test, because a plain ":memory:" gives each pool connection a database of its
// own, and a fence planted on the pool while a transaction holds a connection would land
// in a different database from the one the transaction reads. And every statement goes
// through a counter whose marker is the fence table.
func newFenceMemoDB(t *testing.T) (*gorm.DB, *rdbtest.StatementCounter) {
	t.Helper()
	counter := rdbtest.NewStatementCounter(FenceTable)
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := db.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("registering tenant scoping: %v", err)
	}
	if err := RegisterTenantFence(db); err != nil {
		t.Fatalf("registering the fence: %v", err)
	}
	if err := RegisterAuditJournal(db); err != nil {
		t.Fatalf("registering the audit journal: %v", err)
	}
	if err := db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}, &projection{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	counter.Reset()
	return db, counter
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

// fenceReads is the number of fence-table statements since the counter's last Reset.
func fenceReads(c *rdbtest.StatementCounter) int64 {
	_, n := c.Counts()
	return n
}

// widgetsOf counts one tenant's widgets under a system context, so the count is a fact
// about the table rather than about whether a scoped query was allowed to run.
func widgetsOf(t *testing.T, db *gorm.DB, tenant string) int64 {
	t.Helper()
	var n int64
	if err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).
		Model(&widget{}).Where("tenant_id = ?", tenant).Count(&n).Error; err != nil {
		t.Fatalf("counting widgets: %v", err)
	}
	return n
}

func TestTheFenceIsReadOncePerTenantPerTransaction(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		for i := 0; i < 3; i++ {
			if err := tx.Create(&widget{Name: fmt.Sprintf("w%d", i)}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if got := fenceReads(counter); got != 1 {
		t.Errorf("three writes for one tenant in one transaction read the fence %d time(s); want 1", got)
	}
	if n := widgetsOf(t, db, "acme"); n != 3 {
		t.Errorf("%d widget(s) landed; want 3", n)
	}
}

// Each standalone write is its own default transaction, so this pins that the
// per-statement memo saves nothing and carries nothing from one call to the next.
func TestAWriteOutsideATransactionReadsTheFenceEveryTime(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	for i := 0; i < 3; i++ {
		if err := db.WithContext(tenantCtx("acme")).Create(&widget{Name: fmt.Sprintf("w%d", i)}).Error; err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if got := fenceReads(counter); got != 3 {
		t.Errorf("three standalone writes read the fence %d time(s); want 3", got)
	}
}

func TestTheMemoDoesNotCarryIntoTheNextTransaction(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	for round := 0; round < 2; round++ {
		counter.Reset()
		err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
			for i := 0; i < 2; i++ {
				if err := tx.Create(&widget{Name: "w"}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("transaction %d: %v", round, err)
		}
		if got := fenceReads(counter); got != 1 {
			t.Errorf("transaction %d read the fence %d time(s); want 1 (its own)", round, got)
		}
	}
}

func TestAFencePlantedBetweenTwoTransactionsIsSeenByTheSecond(t *testing.T) {
	db, _ := newFenceMemoDB(t)
	write := func() error {
		return db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
			return tx.Create(&widget{Name: "w"}).Error
		})
	}
	if err := write(); err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	plant(t, db, "acme")
	if err := write(); !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("a transaction begun after the plant returned %v; want ErrTenantPurged", err)
	}
	if n := widgetsOf(t, db, "acme"); n != 1 {
		t.Fatalf("%d widget(s) after the refused transaction; want the first transaction's 1", n)
	}
	// Negative control: lifted, the same write lands and the same count sees it.
	lift(t, db, "acme")
	if err := write(); err != nil {
		t.Fatalf("a transaction after the lift: %v", err)
	}
	if n := widgetsOf(t, db, "acme"); n != 2 {
		t.Fatalf("%d widget(s) after the lift; want 2", n)
	}
}

func TestAProofForOneTenantIsNotAProofForAnother(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	plant(t, db, "globex")
	counter.Reset()
	var globexErr error
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(tenantCtx("acme")).Create(&widget{Name: "a"}).Error; err != nil {
			return err
		}
		globexErr = tx.WithContext(tenantCtx("globex")).Create(&widget{Name: "g"}).Error
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if !errors.Is(globexErr, ErrTenantPurged) {
		t.Fatalf("a write for a fenced tenant after another tenant was proven clear returned %v; "+
			"want ErrTenantPurged", globexErr)
	}
	if got := fenceReads(counter); got != 2 {
		t.Errorf("the transaction read the fence %d time(s); want 2 (one per tenant)", got)
	}
	if n := widgetsOf(t, db, "globex"); n != 0 {
		t.Errorf("%d globex widget(s) landed; want 0", n)
	}
}

func TestAMultiTenantBatchReadsEveryTenantNotYetProven(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	plant(t, db, "tenant-gamma")
	sys := core.WithSystemContext(context.Background())
	counter.Reset()
	counter.Record(true)

	var second error
	var secondSQL []string
	err := db.WithContext(sys).Transaction(func(tx *gorm.DB) error {
		first := []projection{{Tenant: "tenant-alpha", DeviceToken: "a1"}, {Tenant: "tenant-beta", DeviceToken: "b1"}}
		if err := tx.Create(&first).Error; err != nil {
			return err
		}
		if got := fenceReads(counter); got != 1 {
			t.Errorf("a two-tenant batch read the fence %d time(s); want 1", got)
		}
		counter.Reset()
		batch := []projection{
			{Tenant: "tenant-alpha", DeviceToken: "a2"},
			{Tenant: "tenant-beta", DeviceToken: "b2"},
			{Tenant: "tenant-gamma", DeviceToken: "c2"},
		}
		second = tx.Create(&batch).Error
		secondSQL = counter.MarkedSQL()
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if !errors.Is(second, ErrTenantPurged) {
		t.Fatalf("a batch adding a fenced tenant to two proven ones returned %v; want ErrTenantPurged", second)
	}
	// Exactly one read, and it asked about the tenant not yet proven and nothing else.
	if len(secondSQL) != 1 {
		t.Fatalf("the second batch made %d fence statement(s); want 1: %q", len(secondSQL), secondSQL)
	}
	if q := secondSQL[0]; !strings.Contains(q, "tenant-gamma") ||
		strings.Contains(q, "tenant-alpha") || strings.Contains(q, "tenant-beta") {
		t.Errorf("the second batch's fence read was %q; want it to ask about tenant-gamma alone", q)
	}

	// A fresh transaction starts with nothing proven.
	counter.Reset()
	err = db.WithContext(sys).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&[]projection{{Tenant: "tenant-alpha", DeviceToken: "a3"},
			{Tenant: "tenant-beta", DeviceToken: "b3"}}).Error
	})
	if err != nil {
		t.Fatalf("fresh transaction: %v", err)
	}
	if got := fenceReads(counter); got != 1 {
		t.Errorf("a fresh transaction's batch read the fence %d time(s); want 1", got)
	}
}

// A failed read must not be remembered as a clear one. The read is failed with a context
// cancelled before it runs, which database/sql refuses before touching the connection: the
// transaction stays healthy (on Postgres too, where a failed STATEMENT would abort it), and
// nothing about the fence table changes — so no forget can mask the result.
func TestAFailedFenceReadIsNotRemembered(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	var failed error
	err := db.Transaction(func(tx *gorm.DB) error {
		cancelled, cancel := context.WithCancel(tenantCtx("acme"))
		cancel()
		failed = tx.WithContext(cancelled).Create(&widget{Name: "unchecked"}).Error
		if ask, _ := memoOf(tx.Statement.ConnPool).unproven([]string{"acme"}); len(ask) != 1 {
			t.Errorf("after a failed read the memo reports %q unproven; want [acme]", ask)
		}
		return tx.WithContext(tenantCtx("acme")).Create(&widget{Name: "checked"}).Error
	})
	if err != nil {
		t.Fatalf("the write after the failed read: %v", err)
	}
	if failed == nil || errors.Is(failed, ErrTenantPurged) ||
		!strings.Contains(failed.Error(), "reading the erasure fence") {
		t.Fatalf("a write whose fence read failed returned %v; want a fence-read error", failed)
	}
	if got := fenceReads(counter); got != 2 {
		t.Errorf("the transaction made %d fence read(s); want 2 — the failed one and a real one", got)
	}
	if n := widgetsOf(t, db, "acme"); n != 1 {
		t.Errorf("%d widget(s) landed; want only the checked one", n)
	}
}

func TestARefusalIsNotRemembered(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	plant(t, db, "acme")
	counter.Reset()
	var errs [2]error
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		errs[0] = tx.Create(&widget{Name: "first"}).Error
		if ask, _ := memoOf(tx.Statement.ConnPool).unproven([]string{"acme"}); len(ask) != 1 {
			t.Errorf("after a refusal the memo reports %q unproven; want [acme]", ask)
		}
		errs[1] = tx.Create(&widget{Name: "second"}).Error
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	for i, e := range errs {
		if !errors.Is(e, ErrTenantPurged) {
			t.Errorf("write %d for a fenced tenant returned %v; want ErrTenantPurged", i, e)
		}
	}
	if got := fenceReads(counter); got != 2 {
		t.Errorf("two refused writes read the fence %d time(s); want 2", got)
	}
	if n := widgetsOf(t, db, "acme"); n != 0 {
		t.Errorf("%d widget(s) landed; want 0", n)
	}
}

// A nested Transaction is a SAVEPOINT on the same connection pool, so it shares the outer
// memo; rolling the savepoint back un-proves nothing, because a proof is a read.
func TestANestedTransactionSharesTheOuterMemo(t *testing.T) {
	db, counter := newFenceMemoDB(t)
	errNested := errors.New("roll the savepoint back")
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&widget{Name: "outer-1"}).Error; err != nil {
			return err
		}
		if got := fenceReads(counter); got != 1 {
			t.Errorf("the outer write read the fence %d time(s); want 1", got)
		}
		nested := tx.Transaction(func(inner *gorm.DB) error {
			if err := inner.Create(&widget{Name: "nested"}).Error; err != nil {
				return err
			}
			return errNested
		})
		if !errors.Is(nested, errNested) {
			t.Errorf("nested transaction returned %v; want its own error", nested)
		}
		if err := tx.Create(&widget{Name: "outer-2"}).Error; err != nil {
			return err
		}
		if got := fenceReads(counter); got != 1 {
			t.Errorf("after a nested write and a second outer write the fence was read %d time(s); want 1", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	var names []string
	if err := db.WithContext(tenantCtx("acme")).Model(&widget{}).Order("name").Pluck("name", &names).Error; err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if strings.Join(names, ",") != "outer-1,outer-2" {
		t.Errorf("rows after commit = %q; want the two outer rows and not the rolled-back nested one", names)
	}
}

// A transaction that writes the fence table forgets what it had proven, whatever shape the
// write takes on its way to the connection.
func TestAFenceWrittenInsideTheTransactionIsHonoured(t *testing.T) {
	insert := `INSERT INTO purged_tenants (token, epoch, planted_at) VALUES (?, ?, ?)`
	now := time.Now().UTC()
	sys := core.WithSystemContext(context.Background())
	for _, tc := range []struct {
		name  string
		plant func(tx *gorm.DB) error
	}{
		{"tx.Exec", func(tx *gorm.DB) error { return tx.Exec(insert, "acme", now, now).Error }},
		{"tx.Exec, upper case", func(tx *gorm.DB) error {
			return tx.Exec(strings.ToUpper(insert[:24])+insert[24:], "acme", now, now).Error
		}},
		{"tx.Create", func(tx *gorm.DB) error {
			return tx.WithContext(sys).Create(&PurgedTenant{Token: "acme", Epoch: now, PlantedAt: now}).Error
		}},
		{"ConnPool.ExecContext", func(tx *gorm.DB) error {
			_, err := tx.Statement.ConnPool.ExecContext(context.Background(), insert, "acme", now, now)
			return err
		}},
		{"Raw RETURNING through the row processor", func(tx *gorm.DB) error {
			var tok string
			return tx.Raw(insert+` RETURNING token`, "acme", now, now).Scan(&tok).Error
		}},
		{"Raw RETURNING through Row()", func(tx *gorm.DB) error {
			// Row() goes through QueryRowContext, not QueryContext.
			var tok string
			return tx.Raw(insert+` RETURNING token`, "acme", now, now).Row().Scan(&tok)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := newFenceMemoDB(t)
			var after error
			err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&widget{Name: "before"}).Error; err != nil {
					return err
				}
				if err := tc.plant(tx); err != nil {
					return fmt.Errorf("planting inside the transaction: %w", err)
				}
				after = tx.Create(&widget{Name: "after"}).Error
				return nil
			})
			if err != nil {
				t.Fatalf("transaction: %v", err)
			}
			if !errors.Is(after, ErrTenantPurged) {
				t.Fatalf("a write after the transaction fenced its own tenant returned %v; want ErrTenantPurged", after)
			}
		})
	}
}

// A statement prepared on the transaction can run the fence write at any later moment, and
// running it never passes this wrapper again. Prepared BEFORE a proof and executed AFTER
// it, it must still stop the next write: preparing it disables the memo for the rest of
// the transaction.
func TestAFenceStatementPreparedBeforeAProofIsHonouredWhenRunAfterIt(t *testing.T) {
	insert := `INSERT INTO purged_tenants (token, epoch, planted_at) VALUES (?, ?, ?)`
	now := time.Now().UTC()
	db, counter := newFenceMemoDB(t)
	var after error
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		stmt, err := tx.Statement.ConnPool.PrepareContext(context.Background(), insert)
		if err != nil {
			return fmt.Errorf("preparing the plant: %w", err)
		}
		defer stmt.Close()
		for i := 0; i < 2; i++ {
			if err := tx.Create(&widget{Name: fmt.Sprintf("proof-%d", i)}).Error; err != nil {
				return err
			}
		}
		if _, err := stmt.Exec("acme", now, now); err != nil {
			return fmt.Errorf("running the plant: %w", err)
		}
		after = tx.Create(&widget{Name: "after"}).Error
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if !errors.Is(after, ErrTenantPurged) {
		t.Fatalf("a write after a fence statement prepared earlier in the transaction ran returned %v; "+
			"want ErrTenantPurged", after)
	}
	// Every write read the fence: the two before the plant as well, since nothing is
	// proven once a fence statement is prepared. (The counter's marker also counts the
	// prepare; the executed statement does not pass the logger.)
	if got := fenceReads(counter); got < 3 {
		t.Errorf("the transaction made %d fence statement(s); want every write to read it (>= 3)", got)
	}
}

// A statement prepared on the POOL and bound into the transaction carries no text the
// wrapper can see, so binding one disables the memo whatever it holds.
func TestAPoolStatementBoundIntoTheTransactionIsHonoured(t *testing.T) {
	insert := `INSERT INTO purged_tenants (token, epoch, planted_at) VALUES (?, ?, ?)`
	now := time.Now().UTC()
	db, _ := newFenceMemoDB(t)
	stmt, err := db.Statement.ConnPool.PrepareContext(context.Background(), insert)
	if err != nil {
		t.Fatalf("preparing on the pool: %v", err)
	}
	defer stmt.Close()
	var after error
	err = db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&widget{Name: "proof"}).Error; err != nil {
			return err
		}
		bound := tx.Statement.ConnPool.(gorm.Tx).StmtContext(context.Background(), stmt)
		if _, err := bound.Exec("acme", now, now); err != nil {
			return fmt.Errorf("running the bound plant: %w", err)
		}
		after = tx.Create(&widget{Name: "after"}).Error
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if !errors.Is(after, ErrTenantPurged) {
		t.Fatalf("a write after a pool statement bound into the transaction planted the fence returned %v; "+
			"want ErrTenantPurged", after)
	}
}

func TestTheTransactionPoolIsTheMemoisingOne(t *testing.T) {
	db, _ := newFenceMemoDB(t)
	outer, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() outside a transaction: %v", err)
	}
	if _, ok := db.Statement.ConnPool.(*fencePool); !ok {
		t.Errorf("outside a transaction the statement pool is %T; want *fencePool", db.Statement.ConnPool)
	}
	if _, ok := db.Config.ConnPool.(*fencePool); !ok {
		t.Errorf("the configured pool is %T; want *fencePool", db.Config.ConnPool)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, ok := tx.Statement.ConnPool.(*fenceTx); !ok {
			t.Errorf("inside a transaction the pool is %T; want *fenceTx", tx.Statement.ConnPool)
		}
		inner, err := tx.DB()
		if err != nil {
			return err
		}
		if inner != outer {
			t.Errorf("db.DB() inside a transaction is a different *sql.DB from outside")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	// The embedding trap: a pool that is ALSO a TxBeginner is begun through that method
	// first, and its transactions are bare *sql.Tx with no memo.
	if _, ok := any(&fencePool{}).(gorm.TxBeginner); ok {
		t.Error("fencePool implements gorm.TxBeginner, so gorm would begin bare *sql.Tx transactions")
	}
	// A transaction must look like one to gorm's nesting check and to nothing that begins.
	if _, ok := any(&fenceTx{}).(gorm.TxBeginner); ok {
		t.Error("fenceTx implements gorm.TxBeginner")
	}
	if _, ok := any(&fenceTx{}).(gorm.ConnPoolBeginner); ok {
		t.Error("fenceTx implements gorm.ConnPoolBeginner")
	}
}

func TestRegisterTenantFenceRefusesAPoolItCannotMemoise(t *testing.T) {
	t.Run("a prepared-statement pool", func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{PrepareStmt: true})
		if err != nil {
			t.Fatalf("opening sqlite: %v", err)
		}
		t.Cleanup(func() {
			if sqldb, err := db.DB(); err == nil {
				_ = sqldb.Close()
			}
		})
		err = RegisterTenantFence(db)
		if err == nil || !strings.Contains(err.Error(), "PreparedStmtDB") {
			t.Fatalf("registering the fence over a prepared-statement pool returned %v; want a refusal naming it", err)
		}
		if db.Callback().Create().Get("dc:tenant_fence_create") != nil {
			t.Error("the fence's callbacks were registered despite the refusal")
		}
	})
	t.Run("a handle inside a transaction", func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatalf("opening sqlite: %v", err)
		}
		t.Cleanup(func() {
			if sqldb, err := db.DB(); err == nil {
				_ = sqldb.Close()
			}
		})
		tx := db.Begin()
		defer tx.Rollback()
		if err := RegisterTenantFence(tx); err == nil || !strings.Contains(err.Error(), "root gorm handle") {
			t.Fatalf("registering the fence on a transaction handle returned %v; want a refusal", err)
		}
		if _, ok := db.Config.ConnPool.(*sql.DB); !ok {
			t.Errorf("the refused registration still swapped the root's pool to %T", db.Config.ConnPool)
		}
	})
	// gorm keeps one callback per name, so a second registration could not run the check
	// twice; what it would do is log a "duplicated callback" warning per callback. This
	// pins that it is silent, and still one read per write.
	t.Run("a second registration", func(t *testing.T) {
		warns := &warnRecorder{Interface: logger.Discard}
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: warns})
		if err != nil {
			t.Fatalf("opening sqlite: %v", err)
		}
		t.Cleanup(func() {
			if sqldb, err := db.DB(); err == nil {
				_ = sqldb.Close()
			}
		})
		for i := 0; i < 2; i++ {
			if err := RegisterTenantFence(db); err != nil {
				t.Fatalf("registration %d: %v", i+1, err)
			}
		}
		if got := warns.messages(); len(got) != 0 {
			t.Errorf("a second registration logged %q; want nothing", got)
		}
	})
}

// warnRecorder keeps every Warn gorm logs.
type warnRecorder struct {
	logger.Interface
	mu   sync.Mutex
	seen []string
}

func (w *warnRecorder) LogMode(logger.LogLevel) logger.Interface { return w }

func (w *warnRecorder) Warn(_ context.Context, msg string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, fmt.Sprintf(msg, args...))
}

func (w *warnRecorder) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

func TestContainsFold(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want bool
	}{
		{"INSERT INTO purged_tenants (token) VALUES (?)", true},
		{`insert into "PURGED_TENANTS" values (?)`, true},
		{"UPDATE Purged_Tenants SET completed_at = ?", true},
		{"purged_tenants", true},
		{"ppurged_tenants", true},
		{"purged_tenant", false},
		{"INSERT INTO widgets (name) VALUES ('purged tenants')", false},
		{"", false},
	} {
		if got := containsFold(tc.s, FenceTable); got != tc.want {
			t.Errorf("containsFold(%q) = %v; want %v", tc.s, got, tc.want)
		}
	}
}

// The text test runs on every statement of every transaction, including a large batch
// INSERT that never names the fence: the case where it has to look at every byte.
func BenchmarkContainsFoldOnABatchInsert(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO "device_states" ("tenant_id","device_token","payload") VALUES `)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "('tenant-%d','device-%d','{\"temperature\":%d,\"pressure\":%d}')", i%7, i, i, i*3)
	}
	q := sb.String()
	b.SetBytes(int64(len(q)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if containsFold(q, FenceTable) {
			b.Fatal("matched")
		}
	}
}

// A forget that lands between a read and its proof must win: the answer was read before
// the fence changed under the transaction.
func TestAProofReadBeforeAForgetIsNotRecorded(t *testing.T) {
	var m fenceMemo
	ask, gen := m.unproven([]string{"acme"})
	m.forget()
	m.prove(ask, gen)
	if got, _ := m.unproven([]string{"acme"}); len(got) != 1 {
		t.Fatalf("a proof read before a forget was recorded after it: unproven = %q", got)
	}
	// The counterweight: with no forget in between, the same proof is recorded.
	ask, gen = m.unproven([]string{"acme"})
	m.prove(ask, gen)
	if got, _ := m.unproven([]string{"acme"}); len(got) != 0 {
		t.Fatalf("a proof with no forget in between was not recorded: unproven = %q", got)
	}
}

// A DATA-RACE check, run under -race, not a supported usage. database/sql allows one
// *sql.Tx to be shared across goroutines, so the memo is guarded; but pgx refuses a second
// query on a connection with rows still open, and nothing in the repository shares a
// transaction this way. The gorm half therefore runs on sqlite only.
func TestTheMemoIsSafeAcrossGoroutines(t *testing.T) {
	t.Run("the memo", func(t *testing.T) {
		var m fenceMemo
		var wg sync.WaitGroup
		for g := 0; g < 16; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				tok := []string{fmt.Sprintf("t%d", g%4)}
				for i := 0; i < 1000; i++ {
					ask, gen := m.unproven(tok)
					if len(ask) > 0 {
						m.prove(ask, gen)
					}
					if i%97 == 0 {
						m.forget()
					}
				}
			}(g)
		}
		wg.Wait()
	})
	t.Run("one transaction, eight goroutines", func(t *testing.T) {
		db, counter := newFenceMemoDB(t)
		err := db.Transaction(func(tx *gorm.DB) error {
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					tenant := fmt.Sprintf("tenant-%d", g%4)
					errs <- tx.WithContext(tenantCtx(tenant)).Create(&widget{Name: fmt.Sprintf("w%d", g)}).Error
				}(g)
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("transaction: %v", err)
		}
		for i := 0; i < 4; i++ {
			if n := widgetsOf(t, db, fmt.Sprintf("tenant-%d", i)); n != 2 {
				t.Errorf("tenant-%d has %d widget(s); want 2", i, n)
			}
		}
		// Each tenant is read at least once, and at most once per goroutine: two goroutines
		// may both read before either proves, which is allowed.
		if got := fenceReads(counter); got < 4 || got > 8 {
			t.Errorf("eight writes over four tenants read the fence %d time(s); want between 4 and 8", got)
		}
	})
}
