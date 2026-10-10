// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// colReading is the shape the column-array insert exists for: tenant-scoped, append-only,
// audit-exempt, deduplicated on a key that leads with the tenant.
type colReading struct {
	TenantScoped
	Key   string `gorm:"uniqueIndex:uq_col_reading_key,priority:2"`
	Seq   int64
	At    time.Time
	Value sql.NullFloat64
	Note  *string
	Blob  []byte
}

func (colReading) AuditExempt() bool { return true }

// The tenant column joins the unique key, so the conflict target can name it.
func (colReading) TableName() string { return "col_readings" }

func colReadingTenant(r *colReading) *string { return &r.TenantId }

var colReadings = NewColumnTable(colReadingTenant, []string{"tenant_id", "key"},
	TextColumn("key", func(r *colReading) string { return r.Key }),
	Int64Column("seq", func(r *colReading) int64 { return r.Seq }),
	TimeColumn("at", func(r *colReading) time.Time { return r.At }),
	NumericColumn("value", func(r *colReading) sql.NullFloat64 { return r.Value }),
	NullTextColumn("note", func(r *colReading) *string { return r.Note }),
	BytesColumn("blob", func(r *colReading) []byte { return r.Blob }),
)

func newColumnsDB(t *testing.T, fence bool) (*gorm.DB, *rdbtest.StatementCounter) {
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
	if fence {
		if err := RegisterTenantFence(db); err != nil {
			t.Fatalf("registering the fence: %v", err)
		}
	}
	if err := db.AutoMigrate(&PurgedTenant{}, &colReading{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	// AutoMigrate builds the unique index on key alone; the conflict target names the
	// tenant too, so the index has to.
	if err := db.Exec("DROP INDEX uq_col_reading_key").Error; err != nil {
		t.Fatalf("dropping the key index: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX uq_col_reading_key ON col_readings (tenant_id, key)").Error; err != nil {
		t.Fatalf("creating the key index: %v", err)
	}
	counter.Reset()
	return db, counter
}

func colRows(keys ...string) []*colReading {
	note := "n"
	out := make([]*colReading, 0, len(keys))
	for i, k := range keys {
		out = append(out, &colReading{Key: k, Seq: int64(i), At: time.Date(2026, 10, 1, 0, 0, i, 1000, time.UTC),
			Value: sql.NullFloat64{Float64: 1.5 * float64(i), Valid: i%2 == 0}, Note: &note, Blob: []byte{byte(i)}})
	}
	return out
}

// storedReadings returns every stored row, read under a system context so the answer is
// about the table rather than about a scoped query.
func storedReadings(t *testing.T, db *gorm.DB) []colReading {
	t.Helper()
	var out []colReading
	if err := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background())).
		Order("tenant_id, key").Find(&out).Error; err != nil {
		t.Fatalf("reading back: %v", err)
	}
	return out
}

func TestColumnarInsertStoresAndStampsUnderTheContextTenant(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	rows := colRows("a", "b", "c")
	n, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), rows)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n != 3 {
		t.Errorf("inserted %d row(s); want 3", n)
	}
	for _, r := range rows {
		if r.TenantId != "acme" {
			t.Errorf("row %q came back with tenant %q; a Create stamps the context's (acme)", r.Key, r.TenantId)
		}
	}
	got := storedReadings(t, db)
	if len(got) != 3 {
		t.Fatalf("stored %d row(s); want 3", len(got))
	}
	for _, r := range got {
		if r.TenantId != "acme" {
			t.Errorf("stored row %q has tenant %q; want acme", r.Key, r.TenantId)
		}
	}
}

func TestColumnarInsertDoesNothingOnAConflict(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	ctx := tenantCtx("acme")
	if _, err := colReadings.Insert(db.WithContext(ctx), colRows("a", "b")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	again := colRows("a", "b", "c")
	again[0].Seq = 99 // the stored row must keep its own value: DO NOTHING, not DO UPDATE
	n, err := colReadings.Insert(db.WithContext(ctx), again)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if n != 1 {
		t.Errorf("the second insert reported %d new row(s); want 1 (only c is new)", n)
	}
	got := storedReadings(t, db)
	if len(got) != 3 || got[0].Seq != 0 {
		t.Errorf("stored %+v; want a, b, c with a's original seq 0", got)
	}
	// The same key under ANOTHER tenant is a different row: the target leads with the tenant.
	if n, err := colReadings.Insert(db.WithContext(tenantCtx("globex")), colRows("a")); err != nil || n != 1 {
		t.Errorf("another tenant's same key: inserted %d, err %v; want 1, nil", n, err)
	}
}

func TestColumnarInsertRefusesNoTenant(t *testing.T) {
	db, counter := newColumnsDB(t, true)
	for name, ctx := range map[string]context.Context{
		"no tenant":      context.Background(),
		"system context": core.WithSystemContext(context.Background()),
	} {
		rows := colRows("a")
		_, err := colReadings.Insert(db.WithContext(ctx), rows)
		if !errors.Is(err, core.ErrNoTenant) {
			t.Errorf("%s: err = %v; want core.ErrNoTenant", name, err)
		}
		if rows[0].TenantId != "" {
			t.Errorf("%s: a refused row was stamped with %q", name, rows[0].TenantId)
		}
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused insert stored %d row(s)", len(got))
	}
	if all, _ := counter.Counts(); all != 1 { // the read-back above
		t.Errorf("a refused insert issued %d statement(s) before the read-back; want none", all-1)
	}
}

func TestColumnarInsertRefusesAMismatchedRow(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	rows := colRows("a", "b", "c")
	rows[2].TenantId = "globex"
	_, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), rows)
	if !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("err = %v; want ErrTenantMismatch", err)
	}
	if rows[0].TenantId != "" || rows[2].TenantId != "globex" {
		t.Errorf("a refused batch was stamped: %q, %q", rows[0].TenantId, rows[2].TenantId)
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused insert stored %d row(s)", len(got))
	}
	// A row already naming the context's own tenant is not a mismatch.
	rows = colRows("a")
	rows[0].TenantId = "acme"
	if _, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), rows); err != nil {
		t.Errorf("a row naming the context's own tenant was refused: %v", err)
	}
}

func TestColumnarInsertRefusesAFencedTenant(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	if err := sys.Create(&PurgedTenant{Token: "acme", Epoch: time.Now(), PlantedAt: time.Now()}).Error; err != nil {
		t.Fatalf("planting the fence: %v", err)
	}
	_, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), colRows("a"))
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("err = %v; want ErrTenantPurged", err)
	}
	// In a transaction too: the refusal is the write's error, and the transaction's.
	err = db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		_, err := colReadings.Insert(tx, colRows("b"))
		return err
	})
	if !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("in a transaction: err = %v; want ErrTenantPurged", err)
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a fenced tenant's insert stored %d row(s)", len(got))
	}
	// Another tenant is not fenced.
	if _, err := colReadings.Insert(db.WithContext(tenantCtx("globex")), colRows("a")); err != nil {
		t.Errorf("an unfenced tenant was refused: %v", err)
	}
}

// The fence is read when, and only when, a Create on the same handle would read it: a
// handle with the fence callback reads it, one without does not.
func TestColumnarInsertReadsTheFenceExactlyWhenACreateWould(t *testing.T) {
	for _, fenced := range []bool{true, false} {
		t.Run(map[bool]string{true: "fenced", false: "unfenced"}[fenced], func(t *testing.T) {
			readsLikeACreate(t, fenced)
		})
	}
}

func readsLikeACreate(t *testing.T, fenced bool) {
	db, counter := newColumnsDB(t, fenced)
	if _, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), colRows("a")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	columnar := fenceReads(counter)
	counter.Reset()
	if err := db.WithContext(tenantCtx("acme")).Create(colRows("b")).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if create := fenceReads(counter); columnar != create {
		t.Errorf("the column-array insert read the fence %d time(s), a Create %d", columnar, create)
	}
	if want := map[bool]int64{true: 1, false: 0}[fenced]; columnar != want {
		t.Errorf("the column-array insert read the fence %d time(s); want %d", columnar, want)
	}
}

func TestColumnarInsertReadsTheFenceOncePerTenantPerTransaction(t *testing.T) {
	db, counter := newColumnsDB(t, true)
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		for _, k := range []string{"a", "b", "c"} {
			if _, err := colReadings.Insert(tx, colRows(k)); err != nil {
				return err
			}
		}
		// A Create in the same transaction shares the memo.
		return tx.Create(colRows("d")).Error
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if got := fenceReads(counter); got != 1 {
		t.Errorf("four writes for one tenant in one transaction read the fence %d time(s); want 1", got)
	}
}

// The column-array insert and a gorm Create of the same rows store the same rows. SQLite
// renders the VALUES statement, so this pins the checks and the column mapping; the
// Postgres statement is pinned the same way by event-management's integration test.
func TestColumnarInsertStoresWhatACreateStores(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	build := func() []*colReading {
		rows := colRows("a", "b", "c", "d")
		rows[1].Note = nil
		rows[2].Value = sql.NullFloat64{Float64: math.Pi * 1e6, Valid: true}
		rows[3].Blob = nil
		return rows
	}
	if err := db.WithContext(tenantCtx("viacreate")).Create(build()).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := colReadings.Insert(db.WithContext(tenantCtx("viacolumns")), build()); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var created, columnar []colReading
	for _, r := range storedReadings(t, db) {
		tenant := r.TenantId
		r.TenantId = ""
		switch tenant {
		case "viacreate":
			created = append(created, r)
		case "viacolumns":
			columnar = append(columnar, r)
		}
	}
	if len(created) != 4 || len(columnar) != 4 {
		t.Fatalf("stored %d by Create and %d by the column-array insert; want 4 and 4", len(created), len(columnar))
	}
	for i := range created {
		a, b := created[i], columnar[i]
		if a.Key != b.Key || a.Seq != b.Seq || !a.At.Equal(b.At) || a.Value != b.Value ||
			(a.Note == nil) != (b.Note == nil) || (a.Note != nil && *a.Note != *b.Note) || string(a.Blob) != string(b.Blob) {
			t.Errorf("row %d: Create stored %+v, the column-array insert %+v", i, a, b)
		}
	}
}

func TestColumnarInsertWritesNothingForNoRows(t *testing.T) {
	db, counter := newColumnsDB(t, true)
	n, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), nil)
	if err != nil || n != 0 {
		t.Errorf("no rows: inserted %d, err %v; want 0, nil", n, err)
	}
	if all, _ := counter.Counts(); all != 0 {
		t.Errorf("no rows issued %d statement(s); want none", all)
	}
}

// --- models the insert must refuse, because a Create would write them differently ---

type colNoTenant struct {
	Key string `gorm:"primaryKey"`
}

func (colNoTenant) AuditExempt() bool { return true }

type colTokened struct {
	TenantScoped
	Token string
}

func (colTokened) AuditExempt() bool { return true }

type colAudited struct {
	TenantScoped
	Key string
}

type colHooked struct {
	TenantScoped
	Key string
}

func (colHooked) AuditExempt() bool               { return true }
func (*colHooked) BeforeCreate(tx *gorm.DB) error { return nil }

type colDefaulted struct {
	TenantScoped
	Key string
	N   int64 `gorm:"default:7"`
}

func (colDefaulted) AuditExempt() bool { return true }

type colStamped struct {
	TenantScoped
	Key       string
	CreatedAt time.Time
}

func (colStamped) AuditExempt() bool { return true }

type colFenceExempt struct {
	TenantScoped
	Key string
}

func (colFenceExempt) AuditExempt() bool { return true }
func (colFenceExempt) FenceExempt() bool { return true }

func refusal[R any](t *testing.T, db *gorm.DB, table *ColumnTable[R], row *R) error {
	t.Helper()
	_, err := table.Insert(db.WithContext(tenantCtx("acme")), []*R{row})
	return err
}

func TestColumnarInsertRefusesAModelACreateWouldWriteDifferently(t *testing.T) {
	db, _ := newColumnsDB(t, false)
	key := func(name string) []string { return []string{"tenant_id", name} }
	cases := map[string]error{
		"no tenant field": refusal(t, db, NewColumnTable(func(r *colNoTenant) *string { return &r.Key },
			[]string{"key"}, TextColumn("key", func(r *colNoTenant) string { return r.Key })), &colNoTenant{}),
		"token field": refusal(t, db, NewColumnTable(func(r *colTokened) *string { return &r.TenantId },
			key("token"), TextColumn("token", func(r *colTokened) string { return r.Token })), &colTokened{}),
		"audited": refusal(t, db, NewColumnTable(func(r *colAudited) *string { return &r.TenantId },
			key("key"), TextColumn("key", func(r *colAudited) string { return r.Key })), &colAudited{}),
		"create hook": refusal(t, db, NewColumnTable(func(r *colHooked) *string { return &r.TenantId },
			key("key"), TextColumn("key", func(r *colHooked) string { return r.Key })), &colHooked{}),
		"column default": refusal(t, db, NewColumnTable(func(r *colDefaulted) *string { return &r.TenantId },
			key("key"), TextColumn("key", func(r *colDefaulted) string { return r.Key }),
			Int64Column("n", func(r *colDefaulted) int64 { return r.N })), &colDefaulted{}),
		"auto create time": refusal(t, db, NewColumnTable(func(r *colStamped) *string { return &r.TenantId },
			key("key"), TextColumn("key", func(r *colStamped) string { return r.Key }),
			TimeColumn("created_at", func(r *colStamped) time.Time { return r.CreatedAt })), &colStamped{}),
		"fence exempt": refusal(t, db, NewColumnTable(func(r *colFenceExempt) *string { return &r.TenantId },
			key("key"), TextColumn("key", func(r *colFenceExempt) string { return r.Key })), &colFenceExempt{}),
		"uncovered column": refusal(t, db, NewColumnTable(colReadingTenant, key("key"),
			TextColumn("key", func(r *colReading) string { return r.Key })), &colReading{}),
		"unknown column": refusal(t, db, NewColumnTable(colReadingTenant, key("key"),
			append(colReadings.columns, TextColumn("nope", func(r *colReading) string { return "" }))...), &colReading{}),
		"tenant as a column": refusal(t, db, NewColumnTable(colReadingTenant, key("key"),
			append(colReadings.columns, TextColumn("tenant_id", func(r *colReading) string { return "" }))...), &colReading{}),
		"column twice": refusal(t, db, NewColumnTable(colReadingTenant, key("key"),
			append(colReadings.columns, colReadings.columns[0])...), &colReading{}),
		"tenantless conflict target": refusal(t, db, NewColumnTable(colReadingTenant, []string{"key"},
			colReadings.columns...), &colReading{}),
		"no conflict target": refusal(t, db, NewColumnTable(colReadingTenant, nil,
			colReadings.columns...), &colReading{}),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrColumnarModel) {
			t.Errorf("%s: err = %v; want ErrColumnarModel", name, err)
		}
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused model stored %d row(s)", len(got))
	}
}

// The VALUES rendering splits a batch wider than SQLite's parameter limit, and writes it
// all.
func TestColumnarInsertSplitsAWideBatchOnSQLite(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	per := sqliteMaxVariables / (len(colReadings.columns) + 1)
	keys := make([]string, per+5)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}
	n, err := colReadings.Insert(db.WithContext(tenantCtx("acme")), colRows(keys...))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n != int64(len(keys)) {
		t.Errorf("inserted %d; want %d", n, len(keys))
	}
}

func TestNumericTextIsTheDriversDecimal(t *testing.T) {
	for v, want := range map[float64]string{
		0:                   "0",
		-1.25:               "-1.25",
		1e15 + 0.123456789:  "1000000000000000.1",
		5e-324:              "0." + strings.Repeat("0", 323) + "5",
		math.Inf(1):         "Infinity",
		math.Inf(-1):        "-Infinity",
		123456789.123456789: "123456789.12345679",
		0.000000015:         "0.000000015",
	} {
		if got := numericText(v); got != want {
			t.Errorf("numericText(%v) = %q; want %q", v, got, want)
		}
	}
	if got := numericText(math.NaN()); got != "NaN" {
		t.Errorf("numericText(NaN) = %q; want NaN", got)
	}
}

// TestColumnarInsertAccountsForEveryCreateCallback lists the create callbacks the column-
// array insert stands in for, against the callbacks a production handle actually has. A
// create callback added to core — a new check every Create gets — fails this until the
// insert is taught it (or refuses the models it applies to), because the insert bypasses
// the chain and would otherwise skip the new check silently.
func TestColumnarInsertAccountsForEveryCreateCallback(t *testing.T) {
	accounted := map[string]string{
		// gorm's own: hooks, associations and the default transaction. The insert refuses
		// models with hooks or associations, and opens a transaction when not in one.
		"gorm:begin_transaction":              "Insert opens a transaction when not in one",
		"gorm:before_create":                  "models with hooks are refused",
		"gorm:save_before_associations":       "models with associations are refused",
		"gorm:create":                         "the statement itself",
		"gorm:save_after_associations":        "models with associations are refused",
		"gorm:after_create":                   "models with hooks are refused",
		"gorm:commit_or_rollback_transaction": "Insert opens a transaction when not in one",
		"dc:tenant_create":                    "context tenant required, mismatch refused, rows stamped",
		"dc:tenant_upsert_check":              "DO UPDATE only, which the insert does not offer",
		"dc:token_grammar_create":             "models with a Token field are refused",
		"dc:audit_create":                     "audited models are refused",
		fenceCreateCallback:                   "readFence, when the handle has this callback",
	}
	db, err := gorm.Open(sqlite.Open("file:callbacks?mode=memory"), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	// The same registration every service's handle gets (postgres.go), so a chain added
	// there is a chain this test sees.
	if err := registerCallbacks(db); err != nil {
		t.Fatalf("registering: %v", err)
	}
	// gorm keeps its processor's callbacks unexported; their names are read, not called.
	cbs := reflect.ValueOf(db.Callback().Create()).Elem().FieldByName("callbacks")
	if !cbs.IsValid() || cbs.Len() == 0 {
		t.Fatal("could not read the create processor's callbacks: gorm's internals changed, so this " +
			"test can no longer see what a Create runs")
	}
	registered := map[string]bool{}
	for i := 0; i < cbs.Len(); i++ {
		name := cbs.Index(i).Elem().FieldByName("name").String()
		registered[name] = true
		if _, ok := accounted[name]; !ok {
			t.Errorf("create callback %q is not accounted for by the column-array insert "+
				"(insert_columns.go), which bypasses the create chain", name)
		}
	}
	for name := range accounted {
		if !registered[name] {
			t.Errorf("the column-array insert accounts for create callback %q, which is not registered: "+
				"renamed or removed — update the list and what the insert does for it", name)
		}
	}
}

// A column whose array does not carry one element per row is refused before anything is
// written: unnest pads the shorter arrays with NULL, so a short array would store rows with
// NULLs in place of values instead of failing.
func TestColumnarInsertRefusesAColumnArrayOfTheWrongLength(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	short := Column[colReading]{name: "seq", pgType: "int8",
		array: func(rows []*colReading) (any, error) { return make([]int64, len(rows)-1), nil },
		value: func(r *colReading) any { return r.Seq }}
	cols := append([]Column[colReading](nil), colReadings.columns...)
	cols[1] = short
	table := NewColumnTable(colReadingTenant, []string{"tenant_id", "key"}, cols...)
	_, err := table.Insert(db.WithContext(tenantCtx("acme")), colRows("a", "b", "c"))
	if !errors.Is(err, ErrColumnValue) {
		t.Errorf("err = %v; want ErrColumnValue", err)
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused insert stored %d row(s)", len(got))
	}
}

// The tenant accessor must point at the model's tenant field: one pointing anywhere else
// would check and stamp the tenant into that other column.
func TestColumnarInsertRefusesATenantAccessorOnAnotherField(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	table := NewColumnTable(func(r *colReading) *string { return &r.Key },
		[]string{"tenant_id", "key"}, colReadings.columns...)
	rows := colRows("a")
	_, err := table.Insert(db.WithContext(tenantCtx("acme")), rows)
	if !errors.Is(err, ErrColumnarModel) {
		t.Errorf("err = %v; want ErrColumnarModel", err)
	}
	if rows[0].Key != "a" {
		t.Errorf("the accessor's field was stamped: key = %q", rows[0].Key)
	}
}

// A system context refuses the write even when a tenant context lies beneath it: the
// system marker is what a Create would honour, and this write has no system mode.
func TestColumnarInsertRefusesASystemContextOverATenant(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	ctx := core.WithSystemContext(tenantCtx("acme"))
	_, err := colReadings.Insert(db.WithContext(ctx), colRows("a"))
	if !errors.Is(err, core.ErrNoTenant) {
		t.Errorf("err = %v; want core.ErrNoTenant", err)
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused insert stored %d row(s)", len(got))
	}
}

// A refused model stays refused: the refusal is not cached away as a usable plan.
func TestColumnarInsertRefusesARefusedModelEveryTime(t *testing.T) {
	db, _ := newColumnsDB(t, true)
	table := NewColumnTable(colReadingTenant, []string{"key"}, colReadings.columns...)
	for call := 1; call <= 3; call++ {
		if _, err := table.Insert(db.WithContext(tenantCtx("acme")), colRows("a")); !errors.Is(err, ErrColumnarModel) {
			t.Errorf("call %d: err = %v; want ErrColumnarModel", call, err)
		}
	}
	if got := storedReadings(t, db); len(got) != 0 {
		t.Errorf("a refused model stored %d row(s)", len(got))
	}
}
