// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"gorm.io/gorm"
)

func cfgWith(m map[string]interface{}) config.DatastoreConfiguration {
	base := map[string]interface{}{"hostname": "home", "port": 5432, "username": "u", "password": "p"}
	for k, v := range m {
		base[k] = v
	}
	return config.DatastoreConfiguration{Type: "postgres", Configuration: base}
}

func shardEntry(group int, host string) map[string]interface{} {
	return map[string]interface{}{"group": group, "hostname": host, "port": 5432, "username": "u", "password": "p"}
}

func shardsOf(entries ...map[string]interface{}) map[string]interface{} {
	l := make([]interface{}, 0, len(entries))
	for _, e := range entries {
		l = append(l, e)
	}
	return map[string]interface{}{"shards": l}
}

func TestUnconfiguredShardsChangeNothing(t *testing.T) {
	pg, err := convertToPostgresConfig(cfgWith(nil))
	if err != nil {
		t.Fatal(err)
	}
	if pg.Shards != nil {
		t.Fatalf("shards = %v, want nil", pg.Shards)
	}
	// An explicitly empty list is the same as none.
	pg, err = convertToPostgresConfig(cfgWith(map[string]interface{}{"shards": []interface{}{}}))
	if err != nil || pg.Shards != nil {
		t.Fatalf("empty list: %v %v", pg.Shards, err)
	}
	m := &RdbManager{Microservice: &core.Microservice{InstanceId: "i", FunctionalArea: "a"}}
	called := false
	m.openShard = func(string, string) (*gorm.DB, error) { called = true; return nil, errors.New("must not open") }
	got, err := m.openShards(context.Background(), pg)
	if err != nil || got != nil || called {
		t.Fatalf("unconfigured openShards = %v, %v, opened=%v", got, err, called)
	}
	if len(m.ConfiguredShardGroups()) != 0 {
		t.Fatal("groups listed without configuration")
	}
	if err := m.EnableConfiguredSharding(mapResolver{}); err == nil || !strings.Contains(err.Error(), "no shard databases are configured") {
		t.Fatalf("EnableConfiguredSharding with nothing configured must fail loudly: %v", err)
	}
	if m.CheckShards(context.Background()) != nil {
		t.Fatal("CheckShards with nothing configured must be nil")
	}
	if m.ShardingEnabled() {
		t.Fatal("routing on without configuration")
	}
}

func TestShardListIsValidatedFailClosed(t *testing.T) {
	good := shardsOf(shardEntry(1, "s1"), shardEntry(2, "s2"))
	pg, err := convertToPostgresConfig(cfgWith(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Shards) != 2 || pg.Shards[0].SslMode != DefaultSslMode {
		t.Fatalf("good list = %+v", pg.Shards)
	}
	// Out-of-order input is sorted by group.
	pg, err = convertToPostgresConfig(cfgWith(shardsOf(shardEntry(2, "s2"), shardEntry(1, "s1"))))
	if err != nil || pg.Shards[0].Group != 1 {
		t.Fatalf("unsorted list: %+v %v", pg, err)
	}
	with := func(f func(e map[string]interface{})) map[string]interface{} {
		e := shardEntry(1, "s1")
		f(e)
		return shardsOf(e)
	}
	cases := map[string]struct {
		cfg  map[string]interface{}
		want string
	}{
		"group zero":      {with(func(e map[string]interface{}) { e["group"] = 0 }), "group 0"},
		"negative group":  {with(func(e map[string]interface{}) { e["group"] = -1 }), "not allowed"},
		"duplicate group": {shardsOf(shardEntry(1, "a"), shardEntry(1, "b")), "twice"},
		"gap":             {shardsOf(shardEntry(1, "a"), shardEntry(3, "b")), "group 2 is missing"},
		"starts at two":   {shardsOf(shardEntry(2, "a")), "group 1 is missing"},
		"no host":         {with(func(e map[string]interface{}) { e["hostname"] = " " }), "hostname is required"},
		"no user":         {with(func(e map[string]interface{}) { e["username"] = "" }), "username is required"},
		"port zero":       {with(func(e map[string]interface{}) { e["port"] = 0 }), "port"},
		"port high":       {with(func(e map[string]interface{}) { e["port"] = 70000 }), "port"},
		"bad ssl":         {with(func(e map[string]interface{}) { e["sslMode"] = "requrie" }), "invalid sslMode"},
		"ssl injection":   {with(func(e map[string]interface{}) { e["sslMode"] = "disable sslrootcert=/x" }), "invalid sslMode"},
		"unknown key":     {with(func(e map[string]interface{}) { e["databse"] = "x" }), "unknown field"},
		"same as home":    {with(func(e map[string]interface{}) { e["hostname"] = "HOME" }), "home database"},
		"same as other":   {shardsOf(shardEntry(1, "a"), shardEntry(2, "A")), "own database"},
		"wrong type":      {map[string]interface{}{"shards": "s1"}, "invalid database configuration"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := convertToPostgresConfig(cfgWith(c.cfg))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func shardManager(t *testing.T, open openShardFunc) *RdbManager {
	t.Helper()
	m := rdbFixture(t, nil)
	m.openShard = open
	return m
}

func TestOpenShardsOpensPingsAndKeysByGroup(t *testing.T) {
	var dsns []string
	m := shardManager(t, func(dsn, area string) (*gorm.DB, error) {
		dsns = append(dsns, dsn)
		if area != "device-management" {
			t.Errorf("area = %q", area)
		}
		return sqlitetest.OpenMemory(t, nil), nil
	})
	pg, err := convertToPostgresConfig(cfgWith(shardsOf(shardEntry(1, "s1"), shardEntry(2, "s2"))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.openShards(context.Background(), pg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] == nil || got[2] == nil || got[1] == got[2] {
		t.Fatalf("handles = %v", got)
	}
	if len(dsns) != 2 || !strings.Contains(dsns[0], "host='s1'") || !strings.Contains(dsns[1], "host='s2'") ||
		!strings.Contains(dsns[0], "dbname='prod'") || !strings.Contains(dsns[0], "search_path='device-management,public'") {
		t.Fatalf("dsns = %v", dsns)
	}
	// Freshly opened: no callback chain yet, so EnableSharding can wire them.
	if got[1].Callback().Create().Get("dc:tenant_create") != nil {
		t.Fatal("handle already carries callbacks")
	}
	m.shardHandles = got
	if g := m.ConfiguredShardGroups(); len(g) != 2 || g[0] != 1 || g[1] != 2 {
		t.Fatalf("groups = %v", g)
	}
	if err := m.CheckShards(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Closing one shard makes the health check name exactly that group.
	if err := closeHandle(got[2]); err != nil {
		t.Fatal(err)
	}
	err = m.CheckShards(context.Background())
	if err == nil || !strings.Contains(err.Error(), "shard group 2") || strings.Contains(err.Error(), "shard group 1") {
		t.Fatalf("CheckShards = %v", err)
	}
}

func TestOpenShardsClosesEarlierHandlesWhenALaterOneFails(t *testing.T) {
	var first *gorm.DB
	ctx, cancel := context.WithCancel(context.Background())
	m := shardManager(t, func(dsn, _ string) (*gorm.DB, error) {
		if strings.Contains(dsn, "host='s2'") {
			cancel() // the retry of the failing shard gives up at once
			return nil, errors.New("unreachable")
		}
		first = sqlitetest.OpenMemory(t, nil)
		return first, nil
	})
	pg, err := convertToPostgresConfig(cfgWith(shardsOf(shardEntry(1, "s1"), shardEntry(2, "s2"))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.openShards(ctx, pg)
	if err == nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	sqldb, _ := first.DB()
	if sqldb.Ping() == nil {
		t.Fatal("the first shard was left open after a later one failed")
	}
}

func TestConfiguredShardingRoutesAndTerminateClosesEveryHandle(t *testing.T) {
	m, home, _ := shardFixture(t)
	m.openShard = func(string, string) (*gorm.DB, error) { return sqlitetest.OpenMemory(t, nil), nil }
	m.Microservice = &core.Microservice{InstanceId: "prod", FunctionalArea: "device-management"}
	pg, err := convertToPostgresConfig(cfgWith(shardsOf(shardEntry(1, "s1"))))
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.openShards(context.Background(), pg)
	if err != nil {
		t.Fatal(err)
	}
	m.shardHandles = h
	migrateShardTables(t, h[1])
	if err := m.EnableConfiguredSharding(mapResolver{"b": 1}); err != nil {
		t.Fatal(err)
	}
	if !m.ShardingEnabled() {
		t.Fatal("routing not enabled")
	}
	if got := m.routed(tenantCtxOf("b")); got != h[1] {
		t.Fatal("tenant b did not route to the configured shard handle")
	}
	if err := m.ExecuteTerminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]*gorm.DB{"home": home, "shard 1": h[1]} {
		sqldb, _ := db.DB()
		if sqldb.Ping() == nil {
			t.Fatalf("%s still open after terminate", name)
		}
	}
}

// A handle that opens but does not answer is a failed attempt, not an opened shard, and is
// closed rather than leaked.
func TestOpenShardsHealthChecksEachHandleAndClosesOneThatFails(t *testing.T) {
	var opened *gorm.DB
	ctx, cancel := context.WithCancel(context.Background())
	m := shardManager(t, func(string, string) (*gorm.DB, error) {
		opened = sqlitetest.OpenMemory(t, nil)
		cancel() // the open succeeds but the health probe that follows runs on a dead context
		return opened, nil
	})
	pg, err := convertToPostgresConfig(cfgWith(shardsOf(shardEntry(1, "s1"))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.openShards(ctx, pg)
	if err == nil || got != nil || !strings.Contains(err.Error(), "shard group 1") {
		t.Fatalf("got %v, %v: an unhealthy shard must fail startup naming its group", got, err)
	}
	sqldb, _ := opened.DB()
	if sqldb.Ping() == nil {
		t.Fatal("the handle that failed its health probe was left open")
	}
}

func TestSqlDebugAppliesToShardHandlesAsToHome(t *testing.T) {
	var raw *gorm.DB
	m := shardManager(t, func(string, string) (*gorm.DB, error) {
		raw = sqlitetest.OpenMemory(t, nil)
		return raw, nil
	})
	m.MicroserviceConfig.SqlDebug = true
	pg, err := convertToPostgresConfig(cfgWith(shardsOf(shardEntry(1, "s1"))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.openShards(context.Background(), pg)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] == raw {
		t.Fatal("SqlDebug did not reach the shard handle")
	}
}
