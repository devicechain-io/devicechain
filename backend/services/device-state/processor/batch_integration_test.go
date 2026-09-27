// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The batch path against a REAL PostgreSQL, where a refused statement aborts the whole
// transaction — which is what makes "a poison write must not fail its batch-mates" a claim
// worth proving here and not only against a double.
//
// Run it as fence_cost_bench_integration_test.go describes (without -bench).
package processor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// poisonMeasurement is a consumed measurement whose data type is longer than the column
// holds (varchar(32)), so the database refuses its latest-value write.
func poisonMeasurement(tb testing.TB, tenant, device string, at time.Time, ack messaging.Acknowledger) messaging.Message {
	tb.Helper()
	dataType := strings.Repeat("X", 40)
	event := &dmmodel.ResolvedEvent{
		Source: mqttTestSource, SourceDeviceToken: device, EventType: esmodel.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "21.5", DataType: &dataType}},
		}}},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		tb.Fatalf("marshal: %v", err)
	}
	return messaging.NewConsumedMessage("instance1."+tenant+".resolved-events", encoded, 1, nil, ack)
}

// A real refused write in a batch — a data type too long for its column — takes the
// transaction down on PostgreSQL. The poison is left for redelivery; its five tenant-mates
// land one at a time, and the other tenant's five commit together: one fallback.
func TestAPoisonWriteDoesNotFailItsBatchMatesOnPostgres(t *testing.T) {
	mgr, _ := newFenceBenchManager(t, "dsbatchpoison", true)
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	metrics := NewStateMetrics(ms)
	sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), newBenchProcessor(mgr).Api, metrics,
		ms.NewPeriodicTaskMetrics("batch_poison"), WithProjection(config.ProjectionConfiguration{Writers: 1, MaxBatch: 32}))
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	a, b := "pa"+run, "pb"+run
	t0 := time.Now().UTC().Truncate(time.Second)

	var msgs []messaging.Message
	acks := map[string]*recordingAck{}
	add := func(tenant, device string, poison bool) {
		ack := &recordingAck{}
		acks[tenant+"/"+device] = ack
		if poison {
			msgs = append(msgs, poisonMeasurement(t, tenant, device, t0, ack))
			return
		}
		msgs = append(msgs, benchMeasurement(t, tenant, device, t0, ack))
	}
	for i := 0; i < 5; i++ {
		add(a, fmt.Sprintf("good-%d", i), false)
		if i == 2 {
			add(a, "poison", true)
		}
		add(b, fmt.Sprintf("good-%d", i), false)
	}
	ch := make(chan messaging.Message, len(msgs))
	for _, m := range msgs {
		ch <- m
	}
	close(ch)
	sp.messages = ch
	sp.processMessages(context.Background())

	for key, ack := range acks {
		want := int32(1)
		if strings.HasSuffix(key, "/poison") {
			want = 0
		}
		if got := ack.n.Load(); got != want {
			t.Errorf("%s acknowledged %d times; want %d", key, got, want)
		}
	}
	for _, tenant := range []string{a, b} {
		for i := 0; i < 5; i++ {
			var ds model.DeviceState
			if err := mgr.DB(core.WithTenant(context.Background(), tenant)).
				Where("device_token = ?", fmt.Sprintf("good-%d", i)).First(&ds).Error; err != nil {
				t.Errorf("%s/good-%d has no live state: %v", tenant, i, err)
				continue
			}
			if !ds.LastActivityTime.Time.Equal(t0) {
				t.Errorf("%s/good-%d LastActivityTime = %v; want %v", tenant, i, ds.LastActivityTime.Time, t0)
			}
		}
	}
	if got := testutil.ToFloat64(metrics.fallbacks); got != 1 {
		t.Errorf("state_batch_fallbacks_total = %v; want 1", got)
	}
}

// The inactivity sweep updates many rows in one statement, in scan order, and a batch holds
// several rows' locks at once, so the two can deadlock — PostgreSQL then aborts one side.
// Whichever loses, nothing is lost: every event is merged and acknowledged (a batch that
// loses is merged again one event at a time), and every device ends active at its event's
// time, since each event is newer than the sweep's deadline.
func TestTheSweepAndBatchesContendWithoutLosingAnything(t *testing.T) {
	mgr, _ := newFenceBenchManager(t, "dsbatchsweep", true)
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	metrics := NewStateMetrics(ms)
	sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), newBenchProcessor(mgr).Api, metrics,
		ms.NewPeriodicTaskMetrics("batch_sweep"), WithProjection(config.ProjectionConfiguration{Writers: 5, MaxBatch: 32}))
	if err := sp.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	tenant := fmt.Sprintf("sw%d", time.Now().UnixNano())
	const devices = 120
	now := time.Now().UTC().Truncate(time.Second)
	silent := now.Add(-2 * time.Hour) // far past the 600 s inactivity timeout
	fresh := now.Add(-time.Minute)    // inside it

	var acked atomic.Int64
	for i := 0; i < devices; i++ {
		sp.handOff(context.Background(), benchMeasurement(t, tenant, fmt.Sprintf("sw-%03d", i), silent, countingAck{&acked}))
	}
	waitAcked(t, &acked, devices)
	acked.Store(0)

	// Sweeps and batches over the same rows, at once.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var sweepErrs atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := sp.Api.SweepInactive(core.WithSystemContext(context.Background()), now); err != nil {
				sweepErrs.Add(1)
			}
		}
	}()
	for round := 0; round < 4; round++ {
		for i := 0; i < devices; i++ {
			// Descending token order within a round, so batches and the sweep's scan meet
			// their rows in different orders.
			sp.handOff(context.Background(), benchMeasurement(t, tenant, fmt.Sprintf("sw-%03d", devices-1-i),
				fresh.Add(time.Duration(round)*time.Second), countingAck{&acked}))
		}
	}
	waitAcked(t, &acked, 4*devices)
	close(stop)
	wg.Wait()
	if err := sp.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// A final pass must run cleanly, and flip nothing: every device's activity is fresh.
	flipped, err := sp.Api.SweepInactive(core.WithSystemContext(context.Background()), now)
	if err != nil {
		t.Fatalf("a sweep after the contention failed: %v", err)
	}
	var states []model.DeviceState
	if err := mgr.DB(core.WithTenant(context.Background(), tenant)).Find(&states).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(states) != devices {
		t.Fatalf("%d device rows; want %d", len(states), devices)
	}
	want := fresh.Add(3 * time.Second)
	for _, ds := range states {
		if !ds.Active || !ds.LastActivityTime.Time.Equal(want) {
			t.Errorf("%s active=%v activity=%v; want active at %v", ds.DeviceToken, ds.Active, ds.LastActivityTime.Time, want)
		}
	}
	t.Logf("contended sweeps failed %d time(s); batch fallbacks %v; final sweep flipped %d",
		sweepErrs.Load(), testutil.ToFloat64(metrics.fallbacks), flipped)
}

func waitAcked(t *testing.T, acked *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for acked.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d events acknowledged after 2 minutes", acked.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
