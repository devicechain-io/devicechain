// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// BenchmarkAuthenticateDeviceConcurrency measures the one relational read inbound-event
// resolution makes for every event that carries a credential: AuthenticateDevice, which is
// deliberately uncached (a revoked or expired credential takes effect on the next event), so
// each call is one SELECT holding one pooled connection.
//
// The resolver pool runs that call from as many goroutines as it has resolvers, over the
// service's connection pool at its default size. The arms run it the same way at several
// widths and report what widening costs on the database side: calls a second, the mean call,
// and how often and how long a call WAITED FOR A CONNECTION (sql.DBStats). A width that
// raises the wait count is trading resolver waits for pool waits.
//
// Over loopback to one local server, so the absolute numbers are a floor; what matters is
// whether calls a second scale with the width and whether any call waits for a connection.
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model -run '^$' -bench BenchmarkAuthenticateDeviceConcurrency -benchtime 1x
func BenchmarkAuthenticateDeviceConcurrency(b *testing.B) {
	const devices, calls = 200, 20000
	mgr := newPostgresRdbManager(b)
	api := NewApi(mgr)
	sys := mgr.Database.WithContext(core.WithSystemContext(context.Background()))
	for _, table := range []string{"device_credentials", "devices", "device_types"} {
		if err := sys.Exec(fmt.Sprintf(`TRUNCATE TABLE "device-management".%q CASCADE`, table)).Error; err != nil {
			b.Fatalf("truncate %s: %v", table, err)
		}
	}
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		b.Fatalf("seed device type: %v", err)
	}
	for i := 0; i < devices; i++ {
		tok := fmt.Sprintf("dev-%d", i)
		if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: tok, DeviceTypeToken: "dt"}); err != nil {
			b.Fatalf("seed device: %v", err)
		}
		if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
			Token: "c-" + tok, DeviceToken: tok, CredentialType: string(CredentialAccessToken),
			CredentialId: "cred-" + tok, Enabled: true,
		}); err != nil {
			b.Fatalf("seed credential: %v", err)
		}
	}
	sqldb, err := mgr.Database.DB()
	if err != nil {
		b.Fatal(err)
	}
	pool := sqldb.Stats().MaxOpenConnections

	for _, width := range []int{1, 5, 10, 16} {
		b.Run(fmt.Sprintf("resolvers=%d/pool=%d", width, pool), func(b *testing.B) {
			for iter := 0; iter < b.N; iter++ {
				before := sqldb.Stats()
				var next, busyNanos atomic.Int64
				var failed atomic.Int64
				var wg sync.WaitGroup
				start := time.Now()
				for w := 0; w < width; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							n := next.Add(1)
							if n > calls {
								return
							}
							cid := fmt.Sprintf("cred-dev-%d", n%devices)
							t0 := time.Now()
							d, err := api.AuthenticateDevice(ctx, accessToken(cid), time.Now())
							busyNanos.Add(int64(time.Since(t0)))
							if err != nil || d == nil {
								failed.Add(1)
							}
						}
					}()
				}
				wg.Wait()
				elapsed := time.Since(start)
				after := sqldb.Stats()
				if f := failed.Load(); f > 0 {
					b.Fatalf("%d of %d authentications failed", f, calls)
				}
				rate := float64(calls) / elapsed.Seconds()
				mean := float64(busyNanos.Load()) / calls / 1e6
				waits := after.WaitCount - before.WaitCount
				waitMs := float64(after.WaitDuration-before.WaitDuration) / 1e6
				b.ReportMetric(rate, "calls/s")
				b.ReportMetric(mean, "call-mean-ms")
				b.ReportMetric(float64(waits), "pool-waits")
				b.ReportMetric(waitMs, "pool-wait-ms")
				b.Logf("resolvers=%d pool=%d: %.0f calls/s, mean call %.3f ms, %d waits for a connection totalling %.1f ms, %d connections open after",
					width, pool, rate, mean, waits, waitMs, after.OpenConnections)
			}
		})
	}
}
