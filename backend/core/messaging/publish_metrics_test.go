// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
)

// metricsManager is a started manager whose metrics go to a registry the test can gather.
func metricsManager(t *testing.T, srv *natsserver.Server) (*NatsManager, *prometheus.Registry) {
	t.Helper()
	ms := testMicroservice(t, srv, uniqueArea("publish-metrics"))
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(recordNothing)
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr, reg
}

// publishCounts gathers the publish-latency histogram as sample counts by "suffix/mode".
func publishCounts(t *testing.T, reg *prometheus.Registry) map[string]uint64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	for _, f := range families {
		if !strings.HasSuffix(f.GetName(), "_jetstream_publish_duration_seconds") {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if len(labels) != 2 {
				t.Fatalf("publish latency series carries labels %v, want exactly suffix and mode", labels)
			}
			counts[labels["suffix"]+"/"+labels["mode"]] = m.GetHistogram().GetSampleCount()
		}
	}
	return counts
}

// Every publish is observed once, by its stream suffix and the writer's mode, and those are
// the only labels: no tenant, no subject. A writer's series exists at 0 from the moment it
// is built.
func TestPublishLatencyIsObservedPerSuffix(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reg := metricsManager(t, srv)

	sync, err := nmgr.NewWriter(streams.FailedEvents)
	if err != nil {
		t.Fatal(err)
	}
	ordered := newOrdered(t, nmgr, 8)
	if _, err := nmgr.NewWriter(streams.DeadLetters); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := sync.WriteMessages(orderedCtx(), Message{Value: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		ordered.Publish(orderedCtx(), Message{Value: []byte("x")}, func(err error) {
			if err != nil {
				t.Errorf("ordered publish: %v", err)
			}
		})
	}
	ordered.Close()

	got := publishCounts(t, reg)
	want := map[string]uint64{
		streams.FailedEvents + "/sync":        3,
		streams.ResolvedEvents + "/pipelined": 5,
		streams.DeadLetters + "/sync":         0,
	}
	keys := func(m map[string]uint64) string {
		out := make([]string, 0, len(m))
		for k, v := range m {
			out = append(out, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	if keys(got) != keys(want) {
		t.Errorf("publish latency series: got %s, want %s", keys(got), keys(want))
	}
}

// A publish the expired-deadline guard skipped was never sent, so it is not a latency.
func TestAPublishSkippedForAnExpiredDeadlineIsNotObserved(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reg := metricsManager(t, srv)
	sync, err := nmgr.NewWriter(streams.FailedEvents)
	if err != nil {
		t.Fatal(err)
	}
	ordered := newOrdered(t, nmgr, 8)

	ctx, cancel := context.WithDeadline(orderedCtx(), time.Now().Add(-time.Second))
	defer cancel()
	if err := sync.WriteMessages(ctx, Message{Value: []byte("x")}); err == nil {
		t.Fatal("a publish past its deadline succeeded")
	}
	ordered.Publish(ctx, Message{Value: []byte("x")}, func(error) {})
	ordered.Close()

	got := publishCounts(t, reg)
	if got[streams.FailedEvents+"/sync"] != 0 || got[streams.ResolvedEvents+"/pipelined"] != 0 {
		t.Errorf("observed publishes that were never sent: %v", got)
	}
	if _, ok := got[streams.FailedEvents+"/sync"]; !ok {
		t.Error("the sync writer's series does not exist, so its 0 above is an absence, not a count")
	}
}
