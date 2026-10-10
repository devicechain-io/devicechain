// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/rs/zerolog"
)

// BenchmarkHttpIngestPath is the per-event cost of the HTTP ingest path the GKE benchmark
// drives (devicepulse: one measurement, an ACCESS_TOKEN credential in the body), through
// the real HttpEventSource handler, the real JSON decoder, the real message and reading
// gates (both stages, untrusted origin) and the real message build, by:
//
//	transport=recorder  the handler called directly with an httptest recorder: everything
//	                    this service does per event, without net/http's connection serving
//	transport=loopback  a real listener and a keep-alive client in the same process, so
//	                    the net/http server (and the client) are on the bill
//	publish=none        the event is built (marshal) and handed nowhere
//	publish=sync        the shipped publish: a synchronous natsWriter.WriteMessages to an
//	                    embedded single-server JetStream (R1), waiting for the PubAck
//
// Requests run in parallel (-cpu x SetParallelism goroutines) so the synchronous publish
// is measured with concurrent requests in flight, as it is under load. It reports
// cpu-us/ev, the WHOLE process's CPU per event (the embedded server and, on loopback, the
// client included): a profile separates them (-cpuprofile, then focus on
// net/http.(*conn).serve for the server side).
//
// In-process over loopback on whatever box runs it: absolute numbers are not a GKE node's,
// the shares and the ratios are the result.
//
// Run with: go test ./processor -run '^$' -bench BenchmarkHttpIngestPath -benchmem -count 6 -p 1
func BenchmarkHttpIngestPath(b *testing.B) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	// The server belongs to the parent: a sub-benchmark's function runs once per b.N
	// probe, and its cleanup would shut a server it had started under the next probe.
	parent := b
	var srv *natsserver.Server
	n := 0
	for _, transport := range []string{"recorder", "loopback"} {
		for _, publish := range []string{"none", "sync"} {
			n++
			instance := fmt.Sprintf("httpbench%d", n)
			b.Run(fmt.Sprintf("transport=%s/publish=%s", transport, publish), func(b *testing.B) {
				var decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
				switch publish {
				case "none":
					decoded = func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
						seq uint64) error {
						_, _, ok := benchInboundMessage(source, tenant, event, payload, seq)
						if !ok {
							return fmt.Errorf("event not built")
						}
						return nil
					}
				case "sync":
					if srv == nil {
						srv = captureBenchServer(parent)
					}
					w := httpBenchWriter(b, srv, fmt.Sprintf("%s-%d", instance, time.Now().UnixNano()))
					decoded = func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
						seq uint64) error {
						ctx, msg, ok := benchInboundMessage(source, tenant, event, payload, seq)
						if !ok {
							return fmt.Errorf("event not built")
						}
						return w.WriteMessages(ctx, msg)
					}
				}
				es := httpBenchSource(b, decoded)
				runHttpIngest(b, es, transport)
			})
		}
	}
}

// httpBenchSource is the HTTP source as production builds it, with ceilings no bench reaches.
func httpBenchSource(b *testing.B,
	decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error) *HttpEventSource {
	b.Helper()
	flat := core.StaticCeiling(1e9, 1<<30)
	msgs := func() *core.TenantRateLimiter { return core.NewTenantRateLimiter(flat) }
	reads := func() *core.TenantRateLimiter {
		return core.NewTenantRateLimiter(governance.ReadingCeiling(flat))
	}
	es, err := NewHttpEventSource("http", map[string]string{}, "inst", config.HttpIngest{}, NewJsonDecoder(nil),
		func(string, []byte) {}, decoded,
		func(string, string, []byte, error) error { return nil },
		NewRateGate(msgs(), msgs(), msgs(), nil),
		NewReadingGate(reads(), reads(), reads(), nil),
		func(string) error { return nil }, nil)
	if err != nil {
		b.Fatal(err)
	}
	return es
}

// httpBenchWriter is an inbound-events writer on srv under its own instance.
func httpBenchWriter(b *testing.B, srv *natsserver.Server, instance string) messaging.MessageWriter {
	b.Helper()
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		b.Fatal(err)
	}
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	ms.Readiness.MarkReadyWithoutAuthSurface()
	var writer messaging.MessageWriter
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		w, err := n.NewWriter(streams.InboundEvents)
		writer = w
		return err
	})
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	if err := nmgr.Initialize(context.Background()); err != nil {
		b.Fatal(err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return writer
}

// httpBenchBody is the devicepulse body: one measurement, the sim's field order, an
// ACCESS_TOKEN credential. Dated from the run so the age check accepts it.
func httpBenchBody(device int) []byte {
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond).Format(time.RFC3339Nano)
	return []byte(fmt.Sprintf(`{"device":"dp-%05d","occurredTime":%q,"eventType":"Measurement",`+
		`"payload":{"entries":[{"measurements":{"cpu":"42.17"},"occurredTime":%q}]},`+
		`"credentialType":"ACCESS_TOKEN","credentialId":"cred-dp-%05d-0123456789abcdef"}`, device, at, at, device))
}

func runHttpIngest(b *testing.B, es *HttpEventSource, transport string) {
	const devices = 1024
	bodies := make([][]byte, devices)
	for i := range bodies {
		bodies[i] = httpBenchBody(i)
	}
	const path = "/inst/acme/events"
	var seq atomic.Int64
	var failed atomic.Int64
	b.SetParallelism(16) // x GOMAXPROCS goroutines with a request in flight
	b.ReportAllocs()

	switch transport {
	case "recorder":
		h := es.handler()
		cpu0 := benchProcessCPU()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := seq.Add(1)
				r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(bodies[i%devices]))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusAccepted {
					failed.Add(1)
				}
			}
		})
		b.StopTimer()
		reportCPU(b, cpu0)
	case "loopback":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Fatal(err)
		}
		server := &http.Server{Handler: es.handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = server.Serve(ln) }()
		b.Cleanup(func() { _ = server.Close() })
		client := &http.Client{Transport: &http.Transport{
			MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096, IdleConnTimeout: time.Minute}}
		target := "http://" + ln.Addr().String() + path
		cpu0 := benchProcessCPU()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := seq.Add(1)
				resp, err := client.Post(target, "application/json", bytes.NewReader(bodies[i%devices]))
				if err != nil {
					failed.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusAccepted {
					failed.Add(1)
				}
			}
		})
		b.StopTimer()
		reportCPU(b, cpu0)
	}
	if f := failed.Load(); f > 0 {
		b.Fatalf("%d of %d requests were not accepted", f, b.N)
	}
}

func reportCPU(b *testing.B, cpu0 time.Duration) {
	b.ReportMetric(float64(benchProcessCPU()-cpu0)/float64(time.Microsecond)/float64(b.N), "cpu-us/ev")
}
