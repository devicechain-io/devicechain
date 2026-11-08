// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"gorm.io/gorm"
)

type mapResolver map[string]int

func (m mapResolver) ShardGroup(tenant string) (int, error) {
	g, ok := m[tenant]
	if !ok {
		return 0, errors.New("unplaced")
	}
	return g, nil
}

func migrateShardTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}, &grammarThing{}); err != nil {
		t.Fatal(err)
	}
}

// shardFixture is a manager whose home database is group 0 and which has a second database
// registered (by the test, via EnableSharding) as group 1. Both carry the tables.
func shardFixture(t *testing.T) (*RdbManager, *gorm.DB, *gorm.DB) {
	t.Helper()
	home := sqlitetest.OpenMemory(t, nil)
	if err := registerCallbacks(home); err != nil {
		t.Fatal(err)
	}
	g1 := sqlitetest.OpenMemory(t, nil)
	migrateShardTables(t, home)
	migrateShardTables(t, g1)
	return &RdbManager{Database: home}, home, g1
}

// count reads a table without a model, so the placement guard (which looks at tenant-scoped
// models) does not stand between a test and the rows it wants to see.
func count(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.WithContext(core.WithSystemContext(context.Background())).Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func tenantCtxOf(tn string) context.Context { return core.WithTenant(context.Background(), tn) }

func enable(t *testing.T, m *RdbManager, r ShardResolver, g1 *gorm.DB) {
	t.Helper()
	if err := m.EnableSharding(r, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingIsOffByDefault(t *testing.T) {
	m, home, _ := shardFixture(t)
	if err := m.DB(tenantCtxOf("a")).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatal(err)
	}
	if m.ShardingEnabled() || count(t, home, "widgets") != 1 {
		t.Fatal("a manager that never enabled sharding must write to its own Database")
	}
}

func TestTenantsLandOnTheirShardGroup(t *testing.T) {
	m, home, g1 := shardFixture(t)
	enable(t, m, mapResolver{"a": 0, "b": 1}, g1)
	for _, tn := range []string{"a", "b", "b"} {
		if err := m.DB(tenantCtxOf(tn)).Create(&widget{Name: tn}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if count(t, home, "widgets") != 1 || count(t, g1, "widgets") != 2 {
		t.Fatalf("home=%d shard1=%d, want 1 and 2", count(t, home, "widgets"), count(t, g1, "widgets"))
	}
	var got []widget
	q, pag := m.ListOf(tenantCtxOf("b"), sortableWidget{}, nil, Pagination{})
	if err := q.Find(&got).Error; err != nil || len(got) != 2 || pag.TotalRecords != 2 {
		t.Fatalf("ListOf on shard 1: err=%v rows=%d total=%d", err, len(got), pag.TotalRecords)
	}
}

func TestUnknownGroupFailsClosed(t *testing.T) {
	m, home, g1 := shardFixture(t)
	enable(t, m, mapResolver{"c": 2}, g1)
	err := m.DB(tenantCtxOf("c")).Create(&widget{Name: "x"}).Error
	if !errors.Is(err, ErrNoShard) {
		t.Fatalf("want ErrNoShard, got %v", err)
	}
	var out []widget
	if err := m.DB(tenantCtxOf("c")).Find(&out).Error; !errors.Is(err, ErrNoShard) {
		t.Fatalf("a read must fail too, got %v", err)
	}
	if count(t, home, "widgets") != 0 || count(t, g1, "widgets") != 0 {
		t.Fatal("nothing may be written anywhere")
	}
}

func TestResolverErrorFailsClosed(t *testing.T) {
	m, home, g1 := shardFixture(t)
	enable(t, m, mapResolver{}, g1)
	if err := m.DB(tenantCtxOf("nobody")).Create(&widget{Name: "x"}).Error; err == nil {
		t.Fatal("an unplaced tenant must not be written to the home database")
	}
	if count(t, home, "widgets") != 0 {
		t.Fatal("home database was written")
	}
	if _, ok := m.DB(tenantCtxOf("nobody")).Statement.ConnPool.(refusingPool); !ok {
		t.Fatal("an unplaced tenant was handed a live pool, not a refusing one")
	}
}

func TestTenantlessStaysOnHomeAndScopeStillRefuses(t *testing.T) {
	m, _, g1 := shardFixture(t)
	enable(t, m, mapResolver{}, g1)
	if err := m.DB(context.Background()).Create(&widget{Name: "x"}).Error; !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v", err)
	}
}

// F1: a refused handle must not hold or leak a real connection. gorm's Transaction begins on the
// pool before it looks at the error and never rolls back, so with one home connection a leak
// would wedge the next home query.
func TestARefusedHandleLeaksNoHomeConnection(t *testing.T) {
	m, home, g1 := shardFixture(t)
	sqlDB, err := home.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	enable(t, m, mapResolver{"a": 0, "lost": 5}, g1)

	ran := false
	err = m.DB(tenantCtxOf("lost")).Transaction(func(tx *gorm.DB) error { ran = true; return nil })
	if err == nil || ran {
		t.Fatalf("a refused Transaction must fail without running its body (err=%v ran=%v)", err, ran)
	}
	if !errors.Is(err, ErrNoShard) {
		t.Fatalf("want ErrNoShard, got %v", err)
	}
	if in := sqlDB.Stats().InUse; in != 0 {
		t.Fatalf("a refused handle left %d home connection(s) in use", in)
	}
	if err := m.DB(tenantCtxOf("a")).Create(&widget{Name: "after"}).Error; err != nil {
		t.Fatalf("the next home query must succeed: %v", err)
	}

	// A fresh session taken from the refused handle must not fall back to the home pool either.
	err = m.DB(tenantCtxOf("lost")).Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error { return nil })
	if err == nil || sqlDB.Stats().InUse != 0 {
		t.Fatalf("a session from a refused handle reached the home pool (err=%v inUse=%d)", err, sqlDB.Stats().InUse)
	}

	// Raw().Row() must not panic on Scan, and DB() must not hand back the home pool.
	var n int
	if err := m.DB(tenantCtxOf("lost")).Raw("select 1").Row().Scan(&n); err == nil {
		t.Fatal("a refused handle answered a raw row")
	}
	if got, err := m.DB(tenantCtxOf("lost")).DB(); err == nil && got == sqlDB {
		t.Fatal("a refused handle exposes the home *sql.DB")
	}
	if in := sqlDB.Stats().InUse; in != 0 {
		t.Fatalf("%d home connection(s) in use after the raw row", in)
	}
}

// F2: a statement must not escape its shard by changing its context after the handle was chosen,
// or by using a system context (which the tenant-scope callback skips).
func TestStatementsCannotEscapeTheirShard(t *testing.T) {
	m, home, g1 := shardFixture(t)
	enable(t, m, mapResolver{"a": 0, "b": 1}, g1)

	// A transaction opened with no tenant lands on home; giving it tenant b must not write b there.
	err := m.DB(context.Background()).Transaction(func(tx *gorm.DB) error {
		return tx.WithContext(tenantCtxOf("b")).Create(&widget{Name: "escape"}).Error
	})
	if !errors.Is(err, ErrWrongShard) {
		t.Fatalf("tenant-b write on the home handle: want ErrWrongShard, got %v", err)
	}
	// A system context writing a tenant-b row.
	sys := core.WithSystemContext(tenantCtxOf("b"))
	err = m.DB(sys).Create(&widget{TenantScoped: TenantScoped{TenantId: "b"}, Name: "sys"}).Error
	if !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system-context write of a b row: want ErrWrongShard, got %v", err)
	}
	err = m.DB(core.WithSystemContext(context.Background())).Create(&widget{TenantScoped: TenantScoped{TenantId: "b"}, Name: "sys"}).Error
	if !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system-context b row on home: want ErrWrongShard, got %v", err)
	}
	// The shard handle taken directly, with the wrong tenant, or under a system context.
	if err := g1.WithContext(tenantCtxOf("a")).Create(&widget{Name: "x"}).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("tenant-a write on shard 1: want ErrWrongShard, got %v", err)
	}
	if err := g1.WithContext(core.WithSystemContext(context.Background())).Create(&widget{TenantScoped: TenantScoped{TenantId: "b"}}).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system write on a shard handle: want ErrWrongShard, got %v", err)
	}
	// Reads and deletes are guarded too.
	var out []widget
	if err := home.WithContext(tenantCtxOf("b")).Find(&out).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("tenant-b read on home: want ErrWrongShard, got %v", err)
	}
	if err := home.WithContext(tenantCtxOf("b")).Where("1 = 1").Delete(&widget{}).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("tenant-b delete on home: want ErrWrongShard, got %v", err)
	}
	if err := home.WithContext(tenantCtxOf("b")).Model(&widget{}).Where("1 = 1").Update("name", "z").Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("tenant-b update on home: want ErrWrongShard, got %v", err)
	}
	if rows, err := home.WithContext(tenantCtxOf("b")).Model(&widget{}).Select("1").Rows(); !errors.Is(err, ErrWrongShard) {
		if rows != nil {
			rows.Close()
		}
		t.Fatalf("tenant-b rows on home: want ErrWrongShard, got %v", err)
	}
	if count(t, home, "widgets") != 0 || count(t, g1, "widgets") != 0 {
		t.Fatal("a refused statement wrote something")
	}
	// A system context on a shard handle is refused even for a group-0 tenant or no rows at all:
	// only home takes a tenant-scoped model under a system context.
	gs := core.WithSystemContext(context.Background())
	if err := g1.WithContext(gs).Create(&widget{TenantScoped: TenantScoped{TenantId: "a"}}).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system write of a group-0 row on a shard: want ErrWrongShard, got %v", err)
	}
	var none []widget
	if err := g1.WithContext(gs).Find(&none).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system read on a shard: want ErrWrongShard, got %v", err)
	}
	// The row a model-based update names is checked, not just the destination.
	bRow := &widget{TenantScoped: TenantScoped{TenantId: "b"}}
	if err := home.WithContext(gs).Model(bRow).Where("1 = 1").Update("name", "z").Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system update naming a b row on home: want ErrWrongShard, got %v", err)
	}
	// Re-assigning the tenant column by map is a write of that tenant.
	if err := home.WithContext(gs).Model(&widget{}).Where("1 = 1").Update("TenantId", "b").Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("system update assigning tenant b on home: want ErrWrongShard, got %v", err)
	}
	if err := home.WithContext(gs).Model(&widget{}).Where("1 = 1").Update("tenant_id", 7).Error; !errors.Is(err, ErrWrongShard) {
		t.Fatalf("an undeterminable tenant assignment must be refused, got %v", err)
	}
	// The column-array insert bypasses the create chain and asks the guard itself.
	if _, err := colReadings.Insert(home.WithContext(tenantCtxOf("b")), colRows("a")); !errors.Is(err, ErrWrongShard) {
		t.Fatalf("column-array insert of a b row on home: want ErrWrongShard, got %v", err)
	}
	// What must still work: a system context writing a group-0 row on home.
	ok := core.WithSystemContext(context.Background())
	if err := m.DB(ok).Create(&widget{TenantScoped: TenantScoped{TenantId: "a"}, Name: "bootstrap"}).Error; err != nil {
		t.Fatalf("a system write of a group-0 row on home must work: %v", err)
	}
}

// The shard handle carries the full chain, not just routing: each of these fails if the
// corresponding registration is removed from EnableSharding.
func TestShardHandleCarriesTheWholeChain(t *testing.T) {
	m, _, g1 := shardFixture(t)
	enable(t, m, mapResolver{"b": 1}, g1)

	// Tenant scoping.
	if err := g1.Create(&widget{Name: "x"}).Error; !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("no tenant scoping on the shard handle: %v", err)
	}
	// Audit: one create writes exactly one journal row on THIS shard.
	if err := m.DB(tenantCtxOf("b")).Create(&widget{Name: "audited"}).Error; err != nil {
		t.Fatal(err)
	}
	if n := count(t, g1, "audit_events"); n != 1 {
		t.Fatalf("a create on the shard wrote %d audit row(s), want 1", n)
	}
	// Token grammar.
	if err := g1.WithContext(tenantCtxOf("b")).Create(&grammarThing{TokenReference: TokenReference{Token: "bad.token"}}).Error; err == nil {
		t.Fatal("an invalid token was accepted on the shard handle")
	}
	if err := g1.WithContext(tenantCtxOf("b")).Create(&grammarThing{TokenReference: TokenReference{Token: "good-1"}}).Error; err != nil {
		t.Fatalf("a valid token was refused: %v", err)
	}
	// Erasure fence: a purged tenant planted on this shard fails the write.
	sys := g1.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	if err := sys.Create(&PurgedTenant{Token: "b", Epoch: time.Now(), PlantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.DB(tenantCtxOf("b")).Create(&widget{Name: "after-purge"}).Error; !errors.Is(err, ErrTenantPurged) {
		t.Fatalf("want ErrTenantPurged, got %v", err)
	}
}

func TestEnableShardingValidation(t *testing.T) {
	m, home, g1 := shardFixture(t)
	r := mapResolver{}
	g2 := sqlitetest.OpenMemory(t, nil)
	if m.EnableSharding(nil, map[int]*gorm.DB{1: g1}) == nil {
		t.Fatal("nil resolver accepted")
	}
	if m.EnableSharding(r, nil) == nil || m.EnableSharding(r, map[int]*gorm.DB{}) == nil {
		t.Fatal("an empty group map must be refused: S = 1 never routes")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{0: g1}) == nil {
		t.Fatal("group 0 may not be replaced")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: nil}) == nil {
		t.Fatal("nil handle accepted")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: home}) == nil {
		t.Fatal("the home handle accepted as a shard")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: home.Session(&gorm.Session{})}) == nil {
		t.Fatal("a session of the home handle accepted as a shard")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: g1, 2: g1.Session(&gorm.Session{})}) == nil {
		t.Fatal("two sessions of one handle accepted for two groups")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: g1, 2: g1}) == nil {
		t.Fatal("one handle accepted for two groups")
	}
	// A good entry next to a bad one must publish nothing: no routing, no callbacks on the good handle.
	if m.EnableSharding(r, map[int]*gorm.DB{1: g2, 2: nil}) == nil {
		t.Fatal("a nil entry among good ones accepted")
	}
	if m.ShardingEnabled() {
		t.Fatal("a refused configuration must leave routing off")
	}
	if g2.Callback().Create().Get("dc:tenant_create") != nil || g2.Callback().Create().Get(shardGuardCreate) != nil ||
		home.Callback().Create().Get(shardGuardCreate) != nil {
		t.Fatal("a refused configuration registered callbacks")
	}
	// A valid one works once, and only once.
	enable(t, m, r, g1)
	if m.EnableSharding(r, map[int]*gorm.DB{2: g2}) == nil {
		t.Fatal("a second EnableSharding was accepted")
	}
	if g2.Callback().Create().Get(shardGuardCreate) != nil {
		t.Fatal("the refused second call touched a handle")
	}
}

// sortableWidget is widget with the ordering ListOf requires, on the same table.
type sortableWidget struct{ widget }

func (sortableWidget) TableName() string    { return "widgets" }
func (sortableWidget) DefaultOrder() string { return "widgets.id" }

// The callback-level guard (a live handle carrying the wrong tenant) must refuse a Row() read
// with an error, not hand back a nil *sql.Row whose Scan panics. The refusal is the specific
// ErrWrongShard, not a generic one.
func TestAWrongShardRowReadReturnsAnErrorNotAPanic(t *testing.T) {
	m, home, g1 := shardFixture(t)
	enable(t, m, mapResolver{"a": 0, "b": 1}, g1)

	for name, h := range map[string]*gorm.DB{
		"shard handle, tenant of group 0": g1.WithContext(tenantCtxOf("a")),
		"home handle, tenant of group 1":  home.WithContext(tenantCtxOf("b")),
	} {
		t.Run(name, func(t *testing.T) {
			var n int
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Row().Scan panicked: %v", r)
					}
				}()
				err = h.Model(&widget{}).Select("count(*)").Row().Scan(&n)
			}()
			if !errors.Is(err, ErrWrongShard) {
				t.Fatalf("want ErrWrongShard, got %v", err)
			}
		})
	}
}

// A refused Row() read on a handle inside a transaction must leave the transaction's pool in
// place, so the transaction can still be rolled back and its connection released.
func TestARefusedRowReadLeavesTheTransactionRollbackable(t *testing.T) {
	m, home, g1 := shardFixture(t)
	sqlDB, err := home.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	enable(t, m, mapResolver{"a": 0, "b": 1}, g1)

	tx := home.WithContext(tenantCtxOf("b")).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	q := tx.Model(&widget{}).Select("count(*)")
	var n int
	if err := q.Row().Scan(&n); !errors.Is(err, ErrWrongShard) {
		t.Fatalf("want ErrWrongShard, got %v", err)
	}
	if err := q.Rollback().Error; err != nil {
		t.Fatalf("rollback after a refused row read: %v", err)
	}
	if in := sqlDB.Stats().InUse; in != 0 {
		t.Fatalf("%d home connection(s) still in use after the rollback", in)
	}
}
