// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"testing"

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

// shardFixture is a manager whose home database is group 0 and which has a second database
// registered as group 1. Both carry the widget table.
func shardFixture(t *testing.T) (*RdbManager, *gorm.DB, *gorm.DB) {
	t.Helper()
	home := sqlitetest.OpenMemory(t, nil)
	if err := registerCallbacks(home); err != nil {
		t.Fatal(err)
	}
	g1 := sqlitetest.OpenMemory(t, nil)
	for _, db := range []*gorm.DB{home, g1} {
		if err := db.AutoMigrate(&PurgedTenant{}, &AuditEvent{}, &widget{}); err != nil {
			t.Fatal(err)
		}
	}
	m := &RdbManager{Database: home}
	return m, home, g1
}

func count(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.WithContext(core.WithSystemContext(context.Background())).Model(&widget{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRoutingIsOffByDefault(t *testing.T) {
	m, home, _ := shardFixture(t)
	ctx := core.WithTenant(context.Background(), "a")
	if err := m.DB(ctx).Create(&widget{Name: "w"}).Error; err != nil {
		t.Fatal(err)
	}
	if m.ShardingEnabled() || count(t, home) != 1 {
		t.Fatal("a manager that never enabled sharding must write to its own Database")
	}
}

func TestTenantsLandOnTheirShardGroup(t *testing.T) {
	m, home, g1 := shardFixture(t)
	if err := m.EnableSharding(mapResolver{"a": 0, "b": 1}, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []string{"a", "b", "b"} {
		if err := m.DB(core.WithTenant(context.Background(), tn)).Create(&widget{Name: tn}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if count(t, home) != 1 || count(t, g1) != 2 {
		t.Fatalf("home=%d shard1=%d, want 1 and 2", count(t, home), count(t, g1))
	}
	// Reads follow, through the list API's COUNT and data queries too.
	var got []widget
	q, pag := m.ListOf(core.WithTenant(context.Background(), "b"), sortableWidget{}, nil, Pagination{})
	if err := q.Find(&got).Error; err != nil || len(got) != 2 || pag.TotalRecords != 2 {
		t.Fatalf("ListOf on shard 1: err=%v rows=%d total=%d", err, len(got), pag.TotalRecords)
	}
}

func TestUnknownGroupFailsClosed(t *testing.T) {
	m, home, g1 := shardFixture(t)
	if err := m.EnableSharding(mapResolver{"c": 2}, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
	err := m.DB(core.WithTenant(context.Background(), "c")).Create(&widget{Name: "x"}).Error
	if !errors.Is(err, ErrNoShard) {
		t.Fatalf("want ErrNoShard, got %v", err)
	}
	var out []widget
	if err := m.DB(core.WithTenant(context.Background(), "c")).Find(&out).Error; !errors.Is(err, ErrNoShard) {
		t.Fatalf("a read must fail too, got %v", err)
	}
	if count(t, home) != 0 || count(t, g1) != 0 {
		t.Fatal("nothing may be written anywhere")
	}
}

func TestResolverErrorFailsClosed(t *testing.T) {
	m, home, g1 := shardFixture(t)
	if err := m.EnableSharding(mapResolver{}, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
	if err := m.DB(core.WithTenant(context.Background(), "nobody")).Create(&widget{Name: "x"}).Error; err == nil {
		t.Fatal("an unplaced tenant must not be written to the home database")
	}
	if count(t, home) != 0 {
		t.Fatal("home database was written")
	}
}

func TestTenantlessStaysOnHomeAndScopeStillRefuses(t *testing.T) {
	m, _, g1 := shardFixture(t)
	if err := m.EnableSharding(mapResolver{}, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
	if err := m.DB(context.Background()).Create(&widget{Name: "x"}).Error; !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("want ErrNoTenant, got %v", err)
	}
}

func TestShardHandleGetsTheTenantScopeChain(t *testing.T) {
	m, _, g1 := shardFixture(t)
	if err := m.EnableSharding(mapResolver{}, map[int]*gorm.DB{1: g1}); err != nil {
		t.Fatal(err)
	}
	// Bypass routing: the handle itself must refuse a tenant-less scoped write.
	if err := g1.Create(&widget{Name: "x"}).Error; !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("shard handle has no tenant scoping: %v", err)
	}
}

func TestEnableShardingValidation(t *testing.T) {
	m, home, g1 := shardFixture(t)
	r := mapResolver{}
	if m.EnableSharding(nil, nil) == nil {
		t.Fatal("nil resolver accepted")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{0: g1}) == nil {
		t.Fatal("group 0 may not be replaced")
	}
	if m.EnableSharding(r, map[int]*gorm.DB{1: nil}) == nil {
		t.Fatal("nil handle accepted")
	}
	_ = home
	if m.ShardingEnabled() {
		t.Fatal("a refused configuration must leave routing off")
	}
}

// sortableWidget is widget with the ordering ListOf requires, on the same table.
type sortableWidget struct{ widget }

func (sortableWidget) TableName() string    { return "widgets" }
func (sortableWidget) DefaultOrder() string { return "widgets.id" }
