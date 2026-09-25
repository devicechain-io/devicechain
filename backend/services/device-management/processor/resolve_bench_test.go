// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// newBenchResolveRig is the counting rig on a private in-memory SQLite database, for a
// benchmark (the test fixture helper takes a *testing.T only).
func newBenchResolveRig(b *testing.B, extraMetrics int) *resolveRig {
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
	return buildResolveRig(b, db, false, extraMetrics, nil)
}

// BenchmarkWarmResolve is the in-process cost of resolving one event whose every lookup is
// a warm cache hit, by event type and by the size of the device type's published profile
// (1 + extra declared metrics). The cache stores are in-memory, so this measures decoding
// and resolution work only; a production key-value read adds a network round trip per
// Get on top, which is what the read-count tests count.
func BenchmarkWarmResolve(b *testing.B) {
	events := []struct {
		name  string
		event func() *esmodel.UnresolvedEvent
	}{
		{"measurement", func() *esmodel.UnresolvedEvent { return tempEvent("21") }},
		{"location", devLocationEvent},
	}
	for _, ev := range events {
		for _, extra := range []int{0, 20, 200} {
			b.Run(fmt.Sprintf("%s/metrics=%d", ev.name, 1+extra), func(b *testing.B) {
				rig := newBenchResolveRig(b, extra)
				rig.resolve(ev.event()) // warm every cache
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, _, err := rig.rez.ResolveEvent(rig.ctx, ev.event()); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(rig.totalGets())/float64(b.N+1), "kvgets/op")
			})
		}
	}
}
