// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// newBenchResolveRig is the counting rig on a private in-memory SQLite database, for a
// benchmark (the test fixture helper takes a *testing.T only).
//
// wrap is the rig's cache builder: nil for production's caches, kvTierOnly for caches that
// ask the key-value store on every Get.
func newBenchResolveRig(b *testing.B, extraMetrics int,
	wrap func(field string, kv *msgtest.MemoryKV) *messaging.Cache) *resolveRig {
	b.Helper()
	return buildResolveRig(b, benchResolveDB(b), false, extraMetrics, wrap)
}

// benchResolveDB is a private in-memory SQLite database holding the rig's tables.
func benchResolveDB(b *testing.B) *gorm.DB {
	b.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	sqldb, err := db.DB()
	if err != nil {
		b.Fatalf("sql db: %v", err)
	}
	// One connection: every ":memory:" connection is its own empty database.
	sqldb.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = sqldb.Close() })
	if err := rdb.RegisterTenantScoping(db); err != nil {
		b.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(resolveRigTables...); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	return db
}

// BenchmarkWarmResolve is the in-process cost of resolving one event whose every lookup is
// a warm cache hit, by event type, by the size of the device type's published profile
// (1 + extra declared metrics), and by the tier that answers: tier=kv asks the key-value
// store on every Get (the in-process tier off), tier=local is production's caches, which
// answer from process memory. The stores are in-memory, so this measures decoding and
// resolution work only; a production key-value read adds a network round trip per Get on
// top, and kvgets/op is how many of those an event makes.
//
// 🔴 A RUN LONGER THAN THE IN-PROCESS TTL (5 s) REFILLS MEMORY FROM THE STORE, so a
// -benchtime above it reads a little over 0 kvgets/op on the local rows.
func BenchmarkWarmResolve(b *testing.B) {
	events := []struct {
		name  string
		event func() *esmodel.UnresolvedEvent
	}{
		{"measurement", func() *esmodel.UnresolvedEvent { return tempEvent("21") }},
		{"location", devLocationEvent},
	}
	tiers := []struct {
		name string
		wrap func(field string, kv *msgtest.MemoryKV) *messaging.Cache
	}{
		{"kv", kvTierOnly},
		{"local", nil},
	}
	for _, tier := range tiers {
		for _, ev := range events {
			for _, extra := range []int{0, 20, 200} {
				b.Run(fmt.Sprintf("tier=%s/%s/metrics=%d", tier.name, ev.name, 1+extra), func(b *testing.B) {
					rig := newBenchResolveRig(b, extra, tier.wrap)
					rig.resolve(ev.event()) // warm every cache
					rig.resetCounters()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if _, _, err := rig.rez.ResolveEvent(rig.ctx, ev.event()); err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(rig.totalGets())/float64(b.N), "kvgets/op")
				})
			}
		}
	}
}

// BenchmarkWarmCredentialedResolve is the in-process cost of resolving one credentialed
// event under required device authentication, the default, with every key-value lookup
// warm, with the credential cache off (credcache=off: one credential statement per event,
// as before the cache) and on. metadata= is the size of the device's metadata, which a hit
// copies: 0, and 64 KiB to show what a large one costs. sqlstmts/op counts the database
// statements per event. The database is an in-memory SQLite, so the off arm's statement
// is far cheaper here than a PostgreSQL round trip; the ratio is a floor on the saving.
//
// 🔴 A RUN LONGER THAN THE CREDENTIAL CACHE'S 5 S re-reads the credential once per 5 s, so
// a -benchtime above it reads a little over 0 sqlstmts/op on the on rows.
func BenchmarkWarmCredentialedResolve(b *testing.B) {
	for _, metadata := range []int{0, 64 << 10} {
		for _, cached := range []bool{false, true} {
			name := fmt.Sprintf("credcache=%s/metadata=%d", map[bool]string{false: "off", true: "on"}[cached], metadata)
			b.Run(name, func(b *testing.B) {
				rig := newBenchResolveRig(b, 0, nil)
				if !cached {
					rig.caches.Credentials = nil
				}
				secret := "s3cret"
				if _, err := rig.api.CreateDeviceCredential(rig.ctx, &model.DeviceCredentialCreateRequest{
					Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
					CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
				}); err != nil {
					b.Fatal(err)
				}
				if metadata > 0 {
					meta := `{"blob":"` + strings.Repeat("x", metadata) + `"}`
					if _, err := rig.api.UpdateDevice(rig.ctx, "dev",
						&model.DeviceUpdateRequest{Metadata: dcgraphql.OptionalStringOf(meta)}); err != nil {
						b.Fatal(err)
					}
				}
				rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
				event := func() *esmodel.UnresolvedEvent {
					e := tempEvent("21")
					ctype, cid := string(model.CredentialMqttBasic), "cred-1"
					e.CredentialType, e.CredentialId, e.CredentialSecret = &ctype, &cid, &secret
					return e
				}
				if _, _, err := rez.ResolveEvent(rig.ctx, event()); err != nil { // warm every cache
					b.Fatal(err)
				}
				// The rig records every statement's text, which costs an allocation per
				// statement and so would be charged to the off arm alone; count instead.
				var stmts atomic.Int64
				if err := rig.db.Callback().Query().Remove("test:count-sql"); err != nil {
					b.Fatal(err)
				}
				if err := rig.db.Callback().Query().After("gorm:query").Register("bench:count-sql",
					func(*gorm.DB) { stmts.Add(1) }); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, _, err := rez.ResolveEvent(rig.ctx, event()); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(stmts.Load())/float64(b.N), "sqlstmts/op")
			})
		}
	}
}
