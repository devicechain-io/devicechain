// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The raise-alarm consumer's throughput on a real PostgreSQL, driven through the real read loop
// and the real Api.ApplyAlarmContributorEdge. It measures the consumer, not the broker: the
// reader is an in-memory feed, so alarms/s here is the DATABASE side of the path.
//
//	docker run -d --name alarmtp-pg -e POSTGRES_PASSWORD=postgres -p 55432:5432 postgres:17-alpine
//	DC_IT_PGPORT=55432 go test -tags integration -count=1 ./processor/ -run TestRaiseAlarmThroughput -v
//
// Knobs (environment): DC_RA_DEVICES (per tenant), DC_RA_KEYS (alarm keys per device),
// DC_RA_TENANTS, DC_RA_PUBLISH_LATENCY (a fake alarm-event publisher's per-event delay, e.g.
// 1ms, standing in for the broker round trip), DC_RA_WORKERS, DC_RA_CPUPROFILE (a path).
package processor

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func itEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func itInt(t testing.TB, key string, fallback int) int {
	t.Helper()
	v := itEnv(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	require.NoError(t, err, key)
	return n
}

// newPostgresRaiseManager runs the real migration chain against a live PostgreSQL.
func newPostgresRaiseManager(t testing.TB) *rdb.RdbManager {
	t.Helper()
	port := itInt(t, "DC_IT_PGPORT", 5432)
	host, user, pass := itEnv("DC_IT_PGHOST", "localhost"), itEnv("DC_IT_PGUSER", "postgres"), itEnv("DC_IT_PGPASSWORD", "postgres")
	require.NoError(t, rdbtest.EnsureDatabase(context.Background(), host, port, user, pass, "dcalarmtp", ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: "dcalarmtp", FunctionalArea: "device-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres95",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": user, "password": pass,
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	return mgr
}

// feedReader hands out a fixed list of messages, then io.EOF.
type feedReader struct {
	msgs []messaging.Message
	next int
}

func (f *feedReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if f.next >= len(f.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := f.msgs[f.next]
	f.next++
	return m, nil
}

func (f *feedReader) HandleResponse(error) {}

type countAck struct{ n *atomic.Int64 }

func (a countAck) Ack() error { a.n.Add(1); return nil }

// delayPublisher stands in for the alarm-events writer: a synchronous publish costs a broker round trip.
type delayPublisher struct {
	delay time.Duration
	n     atomic.Int64
}

func (p *delayPublisher) PublishAlarmEvent(context.Context, *model.AlarmStateChangeEvent) {
	p.n.Add(1)
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
}

type raiseRig struct {
	api     *model.Api
	pub     *delayPublisher
	tenants []string
	devices map[string][]string // tenant -> device tokens
}

func seedRaiseRig(t testing.TB, nTenants, nDevices int, delay time.Duration) *raiseRig {
	t.Helper()
	mgr := newPostgresRaiseManager(t)
	api := model.NewApi(mgr)
	pub := &delayPublisher{delay: delay}
	api.AlarmPublisher = pub
	r := &raiseRig{api: api, pub: pub, devices: map[string][]string{}}
	run := time.Now().UnixNano()
	for ti := 0; ti < nTenants; ti++ {
		tenant := fmt.Sprintf("ra%d-%d", run, ti)
		r.tenants = append(r.tenants, tenant)
		ctx := core.WithTenant(context.Background(), tenant)
		_, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "pump"})
		require.NoError(t, err)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		toks := make([]string, nDevices)
		for i := 0; i < nDevices; i++ {
			toks[i] = fmt.Sprintf("dev-%d", i)
			wg.Add(1)
			sem <- struct{}{}
			go func(tok string) {
				defer wg.Done()
				defer func() { <-sem }()
				_, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: tok, DeviceTypeToken: "pump"})
				require.NoError(t, err)
			}(toks[i])
		}
		wg.Wait()
		r.devices[tenant] = toks
	}
	return r
}

// edgeSpec is one logical edge; the stream carries them round-robin across alarms, so one
// alarm's successive edges are far apart, as they are on a real firing-heavy stream.
type edgeSpec struct {
	tenant, device, key, edge string
	at                        time.Time
}

// buildEdges makes `rounds` passes over every (tenant, device, key): raise, resolve, raise, ...
func (r *raiseRig) buildEdges(nKeys, rounds int, t0 time.Time) []edgeSpec {
	var out []edgeSpec
	for round := 0; round < rounds; round++ {
		edge := model.AlarmEdgeRaised
		if round%2 == 1 {
			edge = model.AlarmEdgeResolved
		}
		for _, tn := range r.tenants {
			for _, d := range r.devices[tn] {
				for k := 0; k < nKeys; k++ {
					out = append(out, edgeSpec{tn, d, fmt.Sprintf("key-%d", k), edge, t0.Add(time.Duration(round+1) * time.Minute)})
				}
			}
		}
	}
	return out
}

func edgeMessages(t testing.TB, edges []edgeSpec, acked *atomic.Int64) []messaging.Message {
	t.Helper()
	msgs := make([]messaging.Message, len(edges))
	for i, e := range edges {
		req := model.RaiseAlarmRequest{DeviceToken: e.device, AlarmKey: e.key, RuleID: "rule-" + e.key,
			MetricKey: "temp", Edge: e.edge, OccurredTime: e.at}
		if e.edge == model.AlarmEdgeRaised {
			v := 90.0 + float64(i%10)
			req.Value, req.Severity = &v, "major"
		}
		b, err := model.MarshalRaiseAlarmRequest(&req)
		require.NoError(t, err)
		msgs[i] = messaging.NewConsumedMessage("dc."+e.tenant+".raise-alarm", b, 1, nil, countAck{acked}).
			WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "dc_raise-alarm",
				Consumer: "dc_device-management_raise-alarm", Seq: uint64(i + 1)})
	}
	return msgs
}

// runConsumer drives msgs through the real read loop and returns the wall time to drain them.
func (r *raiseRig) runConsumer(t testing.TB, msgs []messaging.Message, workers int) time.Duration {
	t.Helper()
	rc := newThroughputConsumer(r.api, &feedReader{msgs: msgs}, workers)
	start := time.Now()
	rc.readLoop(context.Background())
	return time.Since(start)
}

// TestRaiseAlarmThroughput reports alarms/s through the consumer on a real database.
func TestRaiseAlarmThroughput(t *testing.T) {
	nTenants := itInt(t, "DC_RA_TENANTS", 2)
	nDevices := itInt(t, "DC_RA_DEVICES", 300)
	nKeys := itInt(t, "DC_RA_KEYS", 2)
	workers := itInt(t, "DC_RA_WORKERS", 1)
	delay, err := time.ParseDuration(itEnv("DC_RA_PUBLISH_LATENCY", "0s"))
	require.NoError(t, err)

	r := seedRaiseRig(t, nTenants, nDevices, delay)
	// Three rounds: round 0 creates every row, round 1 clears them, round 2 reactivates them.
	edges := r.buildEdges(nKeys, 3, time.Now().UTC().Truncate(time.Second))
	var acked atomic.Int64
	msgs := edgeMessages(t, edges, &acked)

	if p := os.Getenv("DC_RA_CPUPROFILE"); p != "" {
		f, err := os.Create(p)
		require.NoError(t, err)
		require.NoError(t, pprof.StartCPUProfile(f))
		defer func() { pprof.StopCPUProfile(); _ = f.Close() }()
	}
	took := r.runConsumer(t, msgs, workers)
	require.EqualValues(t, len(msgs), acked.Load(), "every edge must be acked")
	t.Logf("RESULT workers=%d publishLatency=%s edges=%d took=%s alarmsPerSec=%.0f events=%d",
		workers, delay, len(msgs), took.Round(time.Millisecond), float64(len(msgs))/took.Seconds(), r.pub.n.Load())

	// Final state: every alarm was raised, cleared, raised again.
	for _, tn := range r.tenants {
		ctx := core.WithSystemContext(core.WithTenant(context.Background(), tn))
		var active, total int64
		db := r.api.RDB.DB(ctx)
		require.NoError(t, db.Model(&model.Alarm{}).Where("tenant_id = ?", tn).Count(&total).Error)
		require.NoError(t, db.Model(&model.Alarm{}).Where("tenant_id = ? AND state = ?", tn, string(model.AlarmStateActive)).Count(&active).Error)
		require.EqualValues(t, nDevices*nKeys, total)
		require.EqualValues(t, nDevices*nKeys, active)
	}
}

// alarmStateTuple is what two runs of the same stream must agree on.
type alarmStateTuple struct {
	Key, State, Severity string
	Version              uint
}

// TestRaiseAlarmConcurrencyKeepsOrderingIdempotencyAndTenantScope drives one adversarial stream
// through one worker and through eight, on a real database, and requires the same alarms.
//
// Per alarm the stream carries two rules: rule A raises (major), rule B raises (critical), rule A
// resolves — then every one of those edges AGAIN, as a broker redelivery would repeat them, and a
// resolve of rule C that never raised (a resolve ahead of its raise). The first pass is spread
// round-robin over all alarms. The same devices and alarm keys exist under two tenants, so a
// lane that keyed on device and key alone, or a worker that lost its tenant, would fold one
// tenant's edges into the other's. The expected end state is the same for every alarm: ACTIVE at
// CRITICAL (rule B still raising), and the duplicates must not have moved the version of any row
// past what the first pass wrote.
func TestRaiseAlarmConcurrencyKeepsOrderingIdempotencyAndTenantScope(t *testing.T) {
	const nDevices, nKeys = 60, 2
	run := func(workers int) (map[string][]alarmStateTuple, int64) {
		r := seedRaiseRig(t, 2, nDevices, 0)
		t0 := time.Now().UTC().Truncate(time.Second)
		type edge struct {
			rule, kind, sev string
			at              time.Time
		}
		first := []edge{
			{"rule-a", model.AlarmEdgeRaised, "major", t0.Add(1 * time.Minute)},
			{"rule-b", model.AlarmEdgeRaised, "critical", t0.Add(2 * time.Minute)},
			{"rule-a", model.AlarmEdgeResolved, "", t0.Add(3 * time.Minute)},
			{"rule-c", model.AlarmEdgeResolved, "", t0.Add(4 * time.Minute)},
		}
		var acked atomic.Int64
		var msgs []messaging.Message
		add := func(tn, dev, key string, e edge, seq int) {
			req := model.RaiseAlarmRequest{DeviceToken: dev, AlarmKey: key, RuleID: e.rule, MetricKey: "temp",
				Edge: e.kind, OccurredTime: e.at}
			if e.kind == model.AlarmEdgeRaised {
				v := 91.0
				req.Value, req.Severity = &v, e.sev
			}
			b, err := model.MarshalRaiseAlarmRequest(&req)
			require.NoError(t, err)
			msgs = append(msgs, messaging.NewConsumedMessage("dc."+tn+".raise-alarm", b, 1, nil, countAck{&acked}).
				WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "dc_raise-alarm",
					Consumer: "dc_device-management_raise-alarm", Seq: uint64(seq)}))
		}
		seq := 0
		for pass := 0; pass < 2; pass++ { // pass 1 repeats pass 0: redelivery
			for _, e := range first {
				for _, tn := range r.tenants {
					for _, d := range r.devices[tn] {
						for k := 0; k < nKeys; k++ {
							seq++
							add(tn, d, fmt.Sprintf("key-%d", k), e, seq)
						}
					}
				}
			}
		}
		r.runConsumer(t, msgs, workers)
		require.EqualValues(t, len(msgs), acked.Load(), "every edge is acked, duplicates included")

		out := map[string][]alarmStateTuple{}
		for i, tn := range r.tenants {
			var rows []model.Alarm
			ctx := core.WithSystemContext(core.WithTenant(context.Background(), tn))
			require.NoError(t, r.api.RDB.DB(ctx).Where("tenant_id = ?", tn).Order("originator_id, alarm_key").Find(&rows).Error)
			require.Len(t, rows, nDevices*nKeys, "tenant %d must hold exactly its own alarms", i)
			for _, a := range rows {
				require.Equal(t, string(model.AlarmStateActive), a.State, "alarm %s", a.AlarmKey)
				require.Equal(t, string(model.AlarmSeverityCritical), a.Severity, "alarm %s", a.AlarmKey)
				out[fmt.Sprintf("tenant%d", i)] = append(out[fmt.Sprintf("tenant%d", i)],
					alarmStateTuple{a.AlarmKey, a.State, a.Severity, a.ContributorVersion})
			}
		}
		return out, r.pub.n.Load()
	}

	serial, serialEvents := run(1)
	conc, concEvents := run(8)
	require.Equal(t, serialEvents, concEvents, "the number of alarm events emitted must not depend on the worker count")
	for tn, rows := range serial {
		require.Equal(t, rows, conc[tn], "%s: eight workers produced different alarm rows than one", tn)
	}
	// Idempotency: the duplicates changed nothing. The first pass writes version 1 on create and
	// bumps it once per edge that changed the set (raise B, resolve A, resolve C = 3 more,
	// rule A's first raise being the create), so 4; the repeats are stale and add none.
	for _, rows := range conc {
		for _, a := range rows {
			require.Equal(t, uint(4), a.Version, "alarm %s: a redelivered edge moved the contributor version", a.Key)
		}
	}
}
