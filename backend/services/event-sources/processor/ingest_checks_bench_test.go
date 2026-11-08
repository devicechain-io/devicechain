// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/credcheck"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/rs/zerolog"
)

type benchFetcher struct{}

func (benchFetcher) Fetch(context.Context, string) (governance.Limits, error) {
	return governance.Limits{RatePerSecond: 1e9, Burst: 1 << 30}, nil
}

type benchBackend struct{}

func (benchBackend) Check(context.Context, string, credcheck.Presented) (bool, error) {
	return true, nil
}

// BenchmarkHttpIngestChecks is what the synchronous checks add to the per-event cost of the HTTP
// handler, with every answer WARM (the steady state of a device that posts more than once per
// verdict TTL), through the real decoder and the real tenant resolver and credential verifier:
//
//	checks=none         the handler as it was
//	checks=tenant       the tenant-existence check (the ingest ceiling's own cache)
//	checks=tenant+cred  both: the full set a production instance runs
//
// The recorder is called directly, so net/http's connection serving is not on the bill: the
// delta between the rows is the cost of the checks, and nothing else.
//
// Run with: go test ./processor -run '^$' -bench BenchmarkHttpIngestChecks -benchmem -count 6
func BenchmarkHttpIngestChecks(b *testing.B) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	resolver := governance.NewTenantLimitResolver(benchFetcher{}, governance.Limits{RatePerSecond: 1, Burst: 1}, "ingest")
	resolver.AwaitExistence(context.Background(), "acme")
	verifier, err := credcheck.New(benchBackend{}, credcheck.Config{})
	if err != nil {
		b.Fatal(err)
	}
	body := `{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC",` +
		`"credentialId":"user-1","credentialSecret":"hunter2hunter2",` +
		`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`
	verifier.Verify(context.Background(), "acme", credcheck.Presented{
		Device: "sensor-001", Type: "MQTT_BASIC", Id: "user-1", Secret: "hunter2hunter2"})

	for _, tc := range []struct {
		name   string
		checks *IngestChecks
	}{
		{"none", nil},
		{"tenant", &IngestChecks{TenantUnknown: func(ctx context.Context, t string) bool {
			return resolver.Existence(t) == governance.ExistenceNone
		}}},
		{"tenant+cred", &IngestChecks{
			TenantUnknown: func(ctx context.Context, t string) bool {
				return resolver.Existence(t) == governance.ExistenceNone
			},
			Credential: verifier,
		}},
	} {
		b.Run("checks="+tc.name, func(b *testing.B) {
			es, err := NewHttpEventSource("bench", map[string]string{}, "inst-1", config.HttpIngest{},
				NewJsonDecoder(map[string]string{}),
				func(string, []byte) {},
				func(string, string, *model.UnresolvedEvent, interface{}, string) error { return nil },
				func(string, string, []byte, error) error { return nil },
				nil, admitAllReadings, admitAll, nil)
			if err != nil {
				b.Fatal(err)
			}
			es.UseIngestChecks(tc.checks)
			h := es.handler()
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body))
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					if rec.Code != http.StatusAccepted {
						b.Fatalf("status %d", rec.Code)
					}
				}
			})
		})
	}
}
