// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// colUp is the shape the column-array UPSERT exists for: a gorm.Model table (id from a
// sequence, caller-stamped times, soft delete), tenant-scoped, audit-exempt, keyed on
// (tenant, key), with a column of every type the primitive binds and two literal defaults.
type colUp struct {
	gorm.Model
	TenantId string `gorm:"uniqueIndex:uq_col_up_key,priority:1;not null;size:128"`
	Key      string `gorm:"uniqueIndex:uq_col_up_key,priority:2"`
	Rev      int64
	Big      uint64
	Flag     bool
	N        int
	Label    string  `gorm:"default:'dflt'"`
	Mode     int64   `gorm:"default:5"`
	Active   bool    `gorm:"default:true"`
	Anchors  Anchors `gorm:"type:text[]"`
}

func (colUp) AuditExempt() bool { return true }

func (colUp) TableName() string { return "col_ups" }

func colUpTenant(r *colUp) *string { return &r.TenantId }

var colUpCols = []Column[colUp]{
	TimeColumn("created_at", func(r *colUp) time.Time { return r.CreatedAt }),
	TimeColumn("updated_at", func(r *colUp) time.Time { return r.UpdatedAt }),
	TextColumn("key", func(r *colUp) string { return r.Key }),
	Int64Column("rev", func(r *colUp) int64 { return r.Rev }),
	Uint64Column("big", func(r *colUp) uint64 { return r.Big }),
	BoolColumn("flag", func(r *colUp) bool { return r.Flag }),
	IntColumn("n", func(r *colUp) int { return r.N }),
	TextColumn("label", func(r *colUp) string { return r.Label }),
	Int64Column("mode", func(r *colUp) int64 { return r.Mode }),
	BoolColumn("active", func(r *colUp) bool { return r.Active }),
	AnchorsColumn("anchors", func(r *colUp) Anchors { return r.Anchors }),
}

// colUpSet is every column an update refreshes: all but the key (the conflict target), the
// first-written time and the ones the terms forbid.
var colUpSet = []Assignment{
	SetTo("updated_at", "excluded.updated_at"),
	SetTo("rev", "excluded.rev"),
	SetTo("big", "excluded.big"),
	SetTo("flag", "excluded.flag"),
	SetTo("n", "excluded.n"),
	SetTo("label", "excluded.label"),
	SetTo("mode", "excluded.mode"),
	SetTo("active", "excluded.active"),
	SetTo("anchors", "excluded.anchors"),
}

// colUps updates only to a higher revision.
var colUps = NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"},
	Upsert{Set: colUpSet, Where: "t.rev < excluded.rev"}, colUpCols...)

func newColUpDB(t *testing.T, fence bool) (*gorm.DB, *rdbtest.StatementCounter) {
	t.Helper()
	counter := rdbtest.NewStatementCounter(FenceTable)
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: counter})
	require.NoError(t, RegisterTenantScoping(db))
	if fence {
		require.NoError(t, RegisterTenantFence(db))
	}
	require.NoError(t, db.AutoMigrate(&PurgedTenant{}, &colUp{}))
	counter.Reset()
	return db, counter
}

var upAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func upRow(key string, rev int64) *colUp {
	return &colUp{
		Model: gorm.Model{CreatedAt: upAt, UpdatedAt: upAt.Add(time.Duration(rev) * time.Second)},
		Key:   key, Rev: rev, Label: "l-" + key, Mode: 3, Active: true,
	}
}

func upRows(rev int64, keys ...string) []*colUp {
	out := make([]*colUp, len(keys))
	for i, k := range keys {
		out[i] = upRow(k, rev)
	}
	return out
}

func storedUps(t *testing.T, db *gorm.DB) []colUp {
	t.Helper()
	var out []colUp
	require.NoError(t, db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background())).
		Unscoped().Order("tenant_id, id").Find(&out).Error)
	return out
}

// --- the happy path, mirroring TestColumnarInsertStores… ---

func TestColumnarUpsertStoresAndStampsUnderTheContextTenant(t *testing.T) {
	db, _ := newColUpDB(t, true)
	rows := upRows(1, "a", "b", "c")
	n, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), rows)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	for _, r := range rows {
		require.Equal(t, "acme", r.TenantId, "a Create stamps the context's tenant on the row")
	}
	got := storedUps(t, db)
	require.Len(t, got, 3)
	for _, r := range got {
		require.Equal(t, "acme", r.TenantId)
		require.False(t, r.DeletedAt.Valid, "deleted_at is always NULL")
		require.NotZero(t, r.ID, "the id comes from the sequence")
	}
}

func TestColumnarUpsertUpdatesOnAConflictAndKeepsWhatSetDoesNotName(t *testing.T) {
	db, _ := newColUpDB(t, true)
	ctx := tenantCtx("acme")
	_, err := colUps.Upsert(db.WithContext(ctx), upRows(1, "a"))
	require.NoError(t, err)
	first := storedUps(t, db)[0]

	second := upRow("a", 2)
	second.CreatedAt = upAt.Add(time.Hour) // not in SET: must not move
	second.Label = "changed"
	n, err := colUps.Upsert(db.WithContext(ctx), []*colUp{second})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	got := storedUps(t, db)
	require.Len(t, got, 1)
	require.Equal(t, "changed", got[0].Label)
	require.EqualValues(t, 2, got[0].Rev)
	require.Equal(t, first.ID, got[0].ID, "an update keeps the row's id")
	require.True(t, first.CreatedAt.Equal(got[0].CreatedAt), "created_at is never in SET")
}

func TestColumnarUpsertHonoursTheCallersGuard(t *testing.T) {
	db, _ := newColUpDB(t, true)
	ctx := tenantCtx("acme")
	_, err := colUps.Upsert(db.WithContext(ctx), upRows(5, "a"))
	require.NoError(t, err)
	stale := upRow("a", 3)
	stale.Label = "stale"
	n, err := colUps.Upsert(db.WithContext(ctx), []*colUp{stale})
	require.NoError(t, err)
	require.Zero(t, n, "a row the guard excludes is not written")
	require.Equal(t, "l-a", storedUps(t, db)[0].Label)
}

// RowsAffected counts the rows written: inserted plus updated, not the ones the guard held
// back.
func TestColumnarUpsertCountsOnlyWrittenRows(t *testing.T) {
	db, _ := newColUpDB(t, true)
	ctx := tenantCtx("acme")
	_, err := colUps.Upsert(db.WithContext(ctx), append(upRows(10, "held", "moved"), upRow("equal", 10)))
	require.NoError(t, err)
	batch := []*colUp{upRow("held", 4), upRow("moved", 11), upRow("equal", 10), upRow("new", 1)}
	n, err := colUps.Upsert(db.WithContext(ctx), batch)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "one update and one insert; the lower and the equal revision are held back")
}

// Rows land in the order the batch names them: the ids a sequence hands out follow it.
func TestColumnarUpsertKeepsTokenOrder(t *testing.T) {
	db, _ := newColUpDB(t, true)
	keys := []string{"q", "b", "z", "a", "m", "c", "y", "d"}
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, keys...))
	require.NoError(t, err)
	got := storedUps(t, db)
	slices.SortFunc(got, func(a, b colUp) int { return int(a.ID) - int(b.ID) })
	var order []string
	for _, r := range got {
		order = append(order, r.Key)
	}
	require.Equal(t, keys, order)
}

func TestColumnarUpsertWritesNothingForNoRows(t *testing.T) {
	db, counter := newColUpDB(t, true)
	n, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), nil)
	require.NoError(t, err)
	require.Zero(t, n)
	all, _ := counter.Counts()
	require.Zero(t, all)
}

// --- tenant: the context's, and only that ---

func TestColumnarUpsertRefusesNoTenant(t *testing.T) {
	db, counter := newColUpDB(t, true)
	for name, ctx := range map[string]context.Context{
		"no tenant":            context.Background(),
		"system context":       core.WithSystemContext(context.Background()),
		"system over a tenant": core.WithSystemContext(tenantCtx("acme")),
	} {
		rows := upRows(1, "a")
		_, err := colUps.Upsert(db.WithContext(ctx), rows)
		require.ErrorIs(t, err, core.ErrNoTenant, name)
		require.Empty(t, rows[0].TenantId, name)
	}
	require.Empty(t, storedUps(t, db))
	all, _ := counter.Counts()
	require.EqualValues(t, 1, all, "only the read-back above")
}

func TestColumnarUpsertRefusesAMismatchedRow(t *testing.T) {
	db, _ := newColUpDB(t, true)
	rows := upRows(1, "a", "b")
	rows[1].TenantId = "globex"
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), rows)
	require.ErrorIs(t, err, ErrTenantMismatch)
	require.Empty(t, rows[0].TenantId, "a refused batch is not stamped")
	require.Empty(t, storedUps(t, db))
	own := upRows(1, "a")
	own[0].TenantId = "acme"
	_, err = colUps.Upsert(db.WithContext(tenantCtx("acme")), own)
	require.NoError(t, err)
}

func TestColumnarUpsertRefusesAFencedTenant(t *testing.T) {
	db, _ := newColUpDB(t, true)
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.Create(&PurgedTenant{Token: "acme", Epoch: time.Now(), PlantedAt: time.Now()}).Error)
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "a"))
	require.ErrorIs(t, err, ErrTenantPurged)
	err = db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		_, err := colUps.Upsert(tx, upRows(1, "b"))
		return err
	})
	require.ErrorIs(t, err, ErrTenantPurged)
	require.Empty(t, storedUps(t, db))
	_, err = colUps.Upsert(db.WithContext(tenantCtx("globex")), upRows(1, "a"))
	require.NoError(t, err, "another tenant is not fenced")
}

func TestColumnarUpsertReadsTheFenceExactlyWhenACreateWould(t *testing.T) {
	for _, fenced := range []bool{true, false} {
		db, counter := newColUpDB(t, fenced)
		_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "a"))
		require.NoError(t, err)
		upsert := fenceReads(counter)
		counter.Reset()
		require.NoError(t, db.WithContext(tenantCtx("acme")).Create(upRows(1, "b")).Error)
		require.Equal(t, fenceReads(counter), upsert, "fenced=%v", fenced)
		require.EqualValues(t, map[bool]int64{true: 1, false: 0}[fenced], upsert)
	}
}

func TestColumnarUpsertReadsTheFenceOncePerTenantPerTransaction(t *testing.T) {
	db, counter := newColUpDB(t, true)
	err := db.WithContext(tenantCtx("acme")).Transaction(func(tx *gorm.DB) error {
		for _, k := range []string{"a", "b", "c"} {
			if _, err := colUps.Upsert(tx, upRows(1, k)); err != nil {
				return err
			}
		}
		return tx.Create(upRows(1, "d")).Error
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, fenceReads(counter))
}

func TestColumnarUpsertRefusesAScopedCaller(t *testing.T) {
	db, _ := newColUpDB(t, true)
	_, err := colUps.Upsert(db.WithContext(scopedCtx("acme", "customer:acme-corp")), upRows(1, "a"))
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)
	require.Empty(t, storedUps(t, db))
	n, err := colUps.Upsert(db.WithContext(unscopedCtx("acme")), upRows(1, "a"))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

// Two tenants key their rows alike: each upsert touches only its own tenant's row.
func TestColumnarUpsertWritesNothingAcrossTenants(t *testing.T) {
	db, _ := newColUpDB(t, true)
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "k"))
	require.NoError(t, err)
	_, err = colUps.Upsert(db.WithContext(tenantCtx("globex")), upRows(1, "k"))
	require.NoError(t, err)
	changed := upRow("k", 9)
	changed.Label = "acme-only"
	_, err = colUps.Upsert(db.WithContext(tenantCtx("acme")), []*colUp{changed})
	require.NoError(t, err)
	for _, r := range storedUps(t, db) {
		want := map[string]string{"acme": "acme-only", "globex": "l-k"}[r.TenantId]
		require.Equal(t, want, r.Label, "tenant %s", r.TenantId)
	}
}

// The statement itself carries the tenant term, in the form each dialect renders, and
// parenthesises the caller's guard. At the data level the term is redundant (the conflict
// target pins the tenant, so a conflicting row is always the statement's own tenant's), which
// is why it is read in the SQL: a behavioural test cannot see it go.
func TestColumnarUpsertParenthesisesTheGuard(t *testing.T) {
	or := NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"},
		Upsert{Set: colUpSet, Where: "t.rev < excluded.rev OR t.flag"}, colUpCols...)
	for dialect, want := range map[string]string{
		"postgres": ` WHERE (t.rev < excluded.rev OR t.flag) AND t."tenant_id" = $1::text`,
		"sqlite":   " WHERE (t.rev < excluded.rev OR t.flag) AND t.`tenant_id` = excluded.`tenant_id`",
	} {
		sql := planSQL(t, dialect, or)
		require.True(t, strings.HasSuffix(sql, want), "%s: %s", dialect, sql)
	}
	// No caller guard: the tenant term stands alone, unparenthesised.
	bare := NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"}, Upsert{Set: colUpSet}, colUpCols...)
	require.True(t, strings.HasSuffix(planSQL(t, "postgres", bare), ` WHERE t."tenant_id" = $1::text`))
	require.True(t, strings.HasSuffix(planSQL(t, "sqlite", bare), " WHERE t.`tenant_id` = excluded.`tenant_id`"))
}

// A guard with an OR works as written: the OR is the caller's, the tenant term is not.
func TestColumnarUpsertAGuardWithAnOrStillGuards(t *testing.T) {
	db, _ := newColUpDB(t, true)
	or := NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"},
		Upsert{Set: colUpSet, Where: "t.rev < excluded.rev OR t.flag"}, colUpCols...)
	ctx := tenantCtx("acme")
	flagged := upRow("flagged", 9)
	flagged.Flag = true
	_, err := or.Upsert(db.WithContext(ctx), []*colUp{flagged, upRow("plain", 9)})
	require.NoError(t, err)
	n, err := or.Upsert(db.WithContext(ctx), []*colUp{upRow("flagged", 1), upRow("plain", 1)})
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the flagged row passes the OR; the plain one is held by the lower revision")
}

func planSQL[R any](t *testing.T, dialect string, table *ColumnTable[R]) string {
	t.Helper()
	var db *gorm.DB
	switch dialect {
	case "postgres":
		var err error
		db, err = gorm.Open(postgres.New(postgres.Config{Conn: unreachablePool{}}), &gorm.Config{})
		require.NoError(t, err)
	default:
		db = sqlitetest.OpenMemory(t, &gorm.Config{})
	}
	plan, err := table.plan(db)
	require.NoError(t, err)
	if plan.unnest != "" {
		return plan.unnest
	}
	return plan.head + plan.tail
}

func TestColumnarUpsertRendersTheDocumentedPostgresStatement(t *testing.T) {
	sql := planSQL(t, "postgres", colUps)
	require.True(t, strings.HasPrefix(sql, `INSERT INTO "col_ups" AS t ("tenant_id","created_at","updated_at","key",`), sql)
	require.Contains(t, sql, ` SELECT $1::text, unnest($2::timestamptz[]), `)
	require.Contains(t, sql, `unnest($12::text[])::text[] ON CONFLICT ("tenant_id","key") DO UPDATE SET `)
	require.NotContains(t, sql, `"id"`, "the id is left to the sequence")
	require.NotContains(t, sql, `deleted_at`, "deleted_at is never written")
	require.NotContains(t, sql, `"created_at" = `, "created_at is never in SET")
	require.NotContains(t, sql, `"tenant_id" = excluded`, "the tenant is never in SET")
}

// --- refusals ---

type colUpExpr struct {
	gorm.Model
	TenantId string `gorm:"uniqueIndex:uq_col_up_expr,priority:1;not null"`
	Key      string `gorm:"uniqueIndex:uq_col_up_expr,priority:2"`
	Ref      string `gorm:"default:gen_random_uuid()"`
}

func (colUpExpr) AuditExempt() bool { return true }

type colUpPlain struct {
	TenantId string `gorm:"primaryKey"`
	Key      string `gorm:"primaryKey"`
	Rev      int64
}

func (colUpPlain) AuditExempt() bool { return true }

func upsertRefusal[R any](t *testing.T, db *gorm.DB, table *ColumnTable[R], row *R) error {
	t.Helper()
	_, err := table.Upsert(db.WithContext(tenantCtx("acme")), []*R{row})
	return err
}

func TestColumnarUpsertRefusesAModelItCannotWriteLikeACreate(t *testing.T) {
	db, _ := newColUpDB(t, false)
	key := []string{"tenant_id", "key"}
	set := func(c string) Upsert { return Upsert{Set: []Assignment{SetTo(c, Fragment("excluded."+c))}} }
	with := func(mutate func(c []Column[colUp]) []Column[colUp], up Upsert, conflict []string) error {
		cols := mutate(slices.Clone(colUpCols))
		return upsertRefusal(t, db, NewColumnUpsert(colUpTenant, conflict, up, cols...), upRow("a", 1))
	}
	same := func(c []Column[colUp]) []Column[colUp] { return c }
	without := func(name string) func([]Column[colUp]) []Column[colUp] {
		return func(c []Column[colUp]) []Column[colUp] {
			return slices.DeleteFunc(c, func(x Column[colUp]) bool { return x.name == name })
		}
	}
	cases := map[string]error{
		"a database-expression default": upsertRefusal(t, db, NewColumnUpsert(
			func(r *colUpExpr) *string { return &r.TenantId }, key,
			Upsert{Set: []Assignment{SetTo("ref", "excluded.ref")}},
			TimeColumn("created_at", func(r *colUpExpr) time.Time { return r.CreatedAt }),
			TimeColumn("updated_at", func(r *colUpExpr) time.Time { return r.UpdatedAt }),
			TextColumn("key", func(r *colUpExpr) string { return r.Key }),
			TextColumn("ref", func(r *colUpExpr) string { return r.Ref })),
			&colUpExpr{Model: gorm.Model{CreatedAt: upAt, UpdatedAt: upAt}}),
		"the tenantless conflict target": with(same, set("rev"), []string{"key"}),
		"no conflict target":             with(same, set("rev"), nil),
		"SET of the tenant":              with(same, set("tenant_id"), key),
		"SET of the id":                  with(same, set("id"), key),
		"SET of created_at":              with(same, set("created_at"), key),
		"SET of deleted_at":              with(same, set("deleted_at"), key),
		"SET of an unknown column":       with(same, set("nope"), key),
		"an empty SET":                   with(same, Upsert{}, key),
		"SET of the same column twice": with(same, Upsert{Set: []Assignment{
			SetTo("rev", "excluded.rev"), SetTo("rev", "t.rev")}}, key),
		"id as a column": with(func(c []Column[colUp]) []Column[colUp] {
			return append(c, Int64Column("id", func(r *colUp) int64 { return int64(r.ID) }))
		}, set("rev"), key),
		"deleted_at as a column": with(func(c []Column[colUp]) []Column[colUp] {
			return append(c, TimeColumn("deleted_at", func(r *colUp) time.Time { return r.DeletedAt.Time }))
		}, set("rev"), key),
		"the tenant as a column": with(func(c []Column[colUp]) []Column[colUp] {
			return append(c, TextColumn("tenant_id", func(r *colUp) string { return "" }))
		}, set("rev"), key),
		"a column twice":                    with(func(c []Column[colUp]) []Column[colUp] { return append(c, c[0]) }, set("rev"), key),
		"an uncovered column":               with(without("flag"), set("rev"), key),
		"created_at the caller can't stamp": with(without("created_at"), set("rev"), key),
		"a default no column can apply": with(func(c []Column[colUp]) []Column[colUp] {
			i := slices.IndexFunc(c, func(x Column[colUp]) bool { return x.name == "label" })
			c[i] = NullTextColumn("label", func(r *colUp) *string { return &r.Label })
			return c
		}, set("rev"), key),
	}
	for name, err := range cases {
		require.ErrorIs(t, err, ErrColumnarModel, name)
	}
	require.ErrorContains(t, cases["a database-expression default"], "database expression",
		"refused for the default itself, not for a column type that cannot hold it")
	// Each forbidden assignment is refused for ITS OWN reason, not by whichever later check
	// happens to catch it: the tenant column is not one of the table's columns either, so a
	// bare ErrColumnarModel would still pass with the tenant rule itself gone.
	for column, reason := range map[string]string{
		"tenant_id":  "tenant column",
		"id":         "generated id",
		"created_at": "first written",
		"deleted_at": "never writes",
	} {
		err := with(same, set(column), key)
		require.ErrorContains(t, err, reason, "SET of %s", column)
	}
	// SET of a column that exists but the table does not write is refused too.
	require.ErrorIs(t, upsertRefusal(t, db, NewColumnUpsert(func(r *colUpPlain) *string { return &r.TenantId },
		[]string{"tenant_id", "key"}, Upsert{Set: []Assignment{SetTo("rev", "excluded.rev")}},
		TextColumn("key", func(r *colUpPlain) string { return r.Key })), &colUpPlain{}), ErrColumnarModel)
	require.Empty(t, storedUps(t, db), "a refused upsert stores nothing")
}

// The two modes do not stand in for each other: a table built for DO NOTHING cannot upsert
// and one built for DO UPDATE cannot insert.
func TestColumnarUpsertAndInsertAreNotInterchangeable(t *testing.T) {
	db, _ := newColUpDB(t, false)
	_, err := colUps.Insert(db.WithContext(tenantCtx("acme")), upRows(1, "a"))
	require.ErrorIs(t, err, ErrColumnarModel)
	_, err = colReadings.Upsert(db.WithContext(tenantCtx("acme")), nil)
	require.ErrorIs(t, err, ErrColumnarModel)
}

func TestColumnarUpsertRefusesAZeroStampedTime(t *testing.T) {
	db, _ := newColUpDB(t, true)
	for _, mutate := range []func(*colUp){
		func(r *colUp) { r.CreatedAt = time.Time{} },
		func(r *colUp) { r.UpdatedAt = time.Time{} },
	} {
		rows := upRows(1, "a", "b")
		mutate(rows[1])
		_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), rows)
		require.ErrorIs(t, err, ErrColumnValue)
	}
	require.Empty(t, storedUps(t, db))
}

func TestColumnarUpsertRefusesAUint64AboveBigint(t *testing.T) {
	db, _ := newColUpDB(t, true)
	ok := upRow("ok", 1)
	ok.Big = math.MaxInt64
	over := upRow("over", 1)
	over.Big = math.MaxInt64 + 1
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), []*colUp{ok, over})
	require.ErrorIs(t, err, ErrColumnValue)
	require.Empty(t, storedUps(t, db), "nothing is written when one value is refused")
	n, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), []*colUp{ok})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.EqualValues(t, uint64(math.MaxInt64), storedUps(t, db)[0].Big, "the bigint ceiling itself is accepted")
}

func TestColumnarUpsertRefusesAColumnArrayOfTheWrongLength(t *testing.T) {
	db, _ := newColUpDB(t, true)
	cols := slices.Clone(colUpCols)
	i := slices.IndexFunc(cols, func(c Column[colUp]) bool { return c.name == "rev" })
	cols[i] = Column[colUp]{name: "rev", pgType: "int8",
		array: func(rows []*colUp) (any, error) { return make([]int64, len(rows)-1), nil },
		value: func(r *colUp) any { return r.Rev }}
	table := NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"}, Upsert{Set: colUpSet}, cols...)
	_, err := table.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "a", "b"))
	require.ErrorIs(t, err, ErrColumnValue)
	require.Empty(t, storedUps(t, db))
}

func TestColumnarUpsertRefusesATenantAccessorOnAnotherField(t *testing.T) {
	db, _ := newColUpDB(t, true)
	table := NewColumnUpsert(func(r *colUp) *string { return &r.Key }, []string{"tenant_id", "key"},
		Upsert{Set: colUpSet}, colUpCols...)
	_, err := table.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "a"))
	require.ErrorIs(t, err, ErrColumnarModel)
}

func TestColumnarUpsertRefusesARefusedModelEveryTime(t *testing.T) {
	db, _ := newColUpDB(t, true)
	table := NewColumnUpsert(colUpTenant, []string{"key"}, Upsert{Set: colUpSet}, colUpCols...)
	for call := 1; call <= 3; call++ {
		_, err := table.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, "a"))
		require.ErrorIs(t, err, ErrColumnarModel, "call %d", call)
	}
}

// --- fragments are constants, and cannot change the statement's shape ---

func TestColumnarUpsertRefusesAFragmentThatCouldChangeTheStatement(t *testing.T) {
	build := func(set, where Fragment) {
		NewColumnUpsert(colUpTenant, []string{"tenant_id", "key"},
			Upsert{Set: []Assignment{SetTo("rev", set)}, Where: where}, colUpCols...)
	}
	for name, f := range map[string]func(){
		"SET with a sub-select":              func() { build("(SELECT 1)", "") },
		"SET with from":                      func() { build("excluded.rev FROM other", "") },
		"WHERE with a lower-case select":     func() { build("excluded.rev", "t.rev IN (select 1)") },
		"SET with a second statement":        func() { build("excluded.rev; DROP TABLE x", "") },
		"WHERE with a line comment":          func() { build("excluded.rev", "t.rev < 1 --") },
		"WHERE with a block comment":         func() { build("excluded.rev", "t.rev < 1 /*") },
		"WHERE with a parameter":             func() { build("excluded.rev", "t.rev < $1") },
		"WHERE with a placeholder":           func() { build("excluded.rev", "t.rev < ?") },
		"WHERE with an @name":                func() { build("excluded.rev", "t.rev < @x") },
		"WHERE closing the guard early":      func() { build("excluded.rev", "t.rev < 1) OR (true") },
		"WHERE with an unclosed parenthesis": func() { build("excluded.rev", "(t.rev < 1") },
		"WHERE with an unterminated quote":   func() { build("excluded.rev", "t.label = 'x") },
		"SET starting a second assignment":   func() { build("excluded.rev, tenant_id = 'other'", "") },
		"an empty SET expression":            func() { build(" ", "") },
	} {
		func() {
			defer func() {
				r := recover()
				err, _ := r.(error)
				require.True(t, errors.Is(err, ErrColumnarModel), "%s: recovered %v", name, r)
			}()
			f()
		}()
	}
	// The counterweights: a comma inside parentheses or a quote is fine, and so is a word
	// that merely contains one of the blocked ones.
	build("greatest(t.rev, excluded.rev)", "t.label <> 'a, b' AND t.rev > 0 AND t.label <> 'selected'")
	build("coalesce(excluded.label, 'it''s')", "")
}

// --- equivalence with the gorm upsert ---

// upsertEquivalence is TestColumnarUpsertStoresWhatTheGormUpsertStores' body, for either
// database: the same two batches written through a gorm upsert under one tenant and through
// the column-array upsert under another leave the same rows.
//
// Its rows cover: anchors that are NULL, '{}' and elements holding a quote, a backslash, a
// comma, a brace and nothing at all; a zero under each literal default (a string, an
// integer and a bool whose default is true, which a zero false must NOT survive); the
// largest bigint; a row the guard holds back; one it admits; a new one; and deleted_at.
func upsertEquivalence(t *testing.T, db *gorm.DB) {
	t.Helper()
	nasty := Anchors{`a"b`, `c\d`, "e,f", "{g}", "h}", "", "customer:x y"}
	first := func() []*colUp {
		rows := upRows(10, "a", "b", "c", "d", "e", "f")
		rows[0].Anchors = nil
		rows[1].Anchors = Anchors{}
		rows[2].Anchors = nasty
		rows[3].Big, rows[3].Flag, rows[3].N = math.MaxInt64, true, -5
		rows[4].Label, rows[4].Mode, rows[4].Active = "", 0, false // all three defaults apply
		rows[5].Anchors = Anchors{"only"}
		return rows
	}
	second := func() []*colUp {
		rows := []*colUp{upRow("a", 20), upRow("b", 5), upRow("c", 10), upRow("d", 11), upRow("g", 1)}
		rows[0].Label, rows[0].Anchors = "", nasty
		rows[1].Label = "held"
		rows[2].Label = "equal-held"
		rows[3].Anchors, rows[3].Mode, rows[3].Big = Anchors{}, 0, math.MaxInt64-1
		return rows
	}
	set := make([]string, len(colUpSet))
	for i, a := range colUpSet {
		set[i] = a.Column
	}
	gormUpsert := func(tenant string, rows []*colUp) int64 {
		res := db.WithContext(tenantCtx(tenant)).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns(set),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "col_ups.rev < excluded.rev"}}},
		}).Create(rows)
		require.NoError(t, res.Error)
		return res.RowsAffected
	}
	var gormN, colN []int64
	gormN = append(gormN, gormUpsert("viagorm", first()), gormUpsert("viagorm", second()))
	for _, rows := range [][]*colUp{first(), second()} {
		n, err := colUps.Upsert(db.WithContext(tenantCtx("viacolumns")), rows)
		require.NoError(t, err)
		colN = append(colN, n)
	}
	require.Equal(t, gormN, colN, "rows written, per batch")

	byTenant := map[string][]colUp{}
	for _, r := range storedUps(t, db) {
		byTenant[r.TenantId] = append(byTenant[r.TenantId], r)
	}
	g, c := byTenant["viagorm"], byTenant["viacolumns"]
	require.Len(t, g, 7)
	require.Len(t, c, len(g))
	slices.SortFunc(g, func(a, b colUp) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(c, func(a, b colUp) int { return strings.Compare(a.Key, b.Key) })
	for i := range g {
		a, b := g[i], c[i]
		require.True(t, a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt), "%s: times", a.Key)
		require.False(t, a.DeletedAt.Valid || b.DeletedAt.Valid, "%s: deleted_at", a.Key)
		a.ID, b.ID, a.TenantId, b.TenantId = 0, 0, "", ""
		a.CreatedAt, b.CreatedAt, a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		require.Truef(t, reflect.DeepEqual(a, b), "row %q: gorm stored %+v, the column-array upsert %+v", a.Key, a, b)
	}
	// Spot-check the cases the test exists for, so a bug that hits both sides equally cannot
	// pass: the substituted defaults, and anchors that kept their shape.
	byKey := map[string]colUp{}
	for _, r := range c {
		byKey[r.Key] = r
	}
	require.Equal(t, "dflt", byKey["e"].Label)
	require.EqualValues(t, 5, byKey["e"].Mode)
	require.True(t, byKey["e"].Active, "a zero bool under default:true is the default, as a Create writes")
	require.Equal(t, Anchors{}, byKey["b"].Anchors)
	require.Equal(t, nasty, byKey["a"].Anchors, "the update wrote the awkward elements back intact")
	require.Equal(t, "l-b", byKey["b"].Label, "a revision below the stored one is held back")
	require.Equal(t, "l-c", byKey["c"].Label, "an equal revision is held back")
	require.EqualValues(t, uint64(math.MaxInt64-1), byKey["d"].Big)
}

func TestColumnarUpsertStoresWhatTheGormUpsertStores(t *testing.T) {
	db, _ := newColUpDB(t, true)
	upsertEquivalence(t, db)
}

// Anchors cross the wire as themselves: the same list, nil or empty, comes back.
func TestColumnarUpsertAnchorsEdgeCases(t *testing.T) {
	db, _ := newColUpDB(t, true)
	lists := map[string]Anchors{
		"nil": nil, "empty": {}, "quote": {`"`}, "backslash": {`\`}, "comma": {","},
		"brace": {"{", "}"}, "blank element": {""}, "mixed": {`a"\,{}b`, "plain"}, "NULL word": {"NULL"},
	}
	var rows []*colUp
	for name, l := range lists {
		r := upRow(name, 1)
		r.Anchors = l
		rows = append(rows, r)
	}
	_, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), rows)
	require.NoError(t, err)
	for _, r := range storedUps(t, db) {
		want := lists[r.Key]
		require.Equal(t, want == nil, r.Anchors == nil, "%s: NULL stays NULL, and only NULL", r.Key)
		if want != nil {
			require.Equal(t, want, r.Anchors, r.Key)
		}
	}
}

func TestColumnarUpsertSplitsAWideBatchOnSQLite(t *testing.T) {
	db, _ := newColUpDB(t, true)
	per := sqliteMaxVariables / (len(colUps.columns) + 1)
	keys := make([]string, per+5)
	for i := range keys {
		keys[i] = "k" + time.Duration(i).String()
	}
	n, err := colUps.Upsert(db.WithContext(tenantCtx("acme")), upRows(1, keys...))
	require.NoError(t, err)
	require.EqualValues(t, len(keys), n)
	require.Len(t, storedUps(t, db), len(keys))
}
