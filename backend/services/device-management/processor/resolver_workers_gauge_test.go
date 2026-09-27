// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
)

// The resolve_workers gauge reports the width the pool runs, as the operator configured it,
// under its exported name. 3 rather than the default, so a gauge that reported the default
// whatever it was given fails here.
func TestTheResolverWorkersGaugeReportsThePoolItRuns(t *testing.T) {
	nmgr, _ := startGateNats(t)
	ms := &core.Microservice{InstanceId: "gate", FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	metrics := NewResolveMetrics(ms)

	resolved, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, PUBLISH_WINDOW)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := nmgr.NewOrderedWriter(streams.FailedEvents, PUBLISH_WINDOW)
	if err != nil {
		t.Fatal(err)
	}
	iproc := NewInboundEventsProcessor(nmgr.Microservice, &sliceReader{}, resolved, failed,
		core.NewNoOpLifecycleCallbacks(), instantApi{}, config.AuthModeOptional, time.Minute, metrics,
		WithResolvers(3))
	if got := iproc.Resolvers(); got != 3 {
		t.Fatalf("Resolvers() = %d before Initialize, want the configured 3", got)
	}
	if err := iproc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := iproc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stopWithin(t, iproc, 10*time.Second) })

	if got := gaugeValue(reg, "devicechain_devicemanagement_resolve_workers"); got != 3 {
		t.Errorf("resolve_workers = %v, want 3", got)
	}
	if got := len(iproc.resolvers); got != 3 {
		t.Errorf("the pool runs %d resolvers, want 3", got)
	}
}

// A pool width the configuration would refuse is refused by the processor too, rather than
// started as a pool that resolves nothing.
func TestAResolverPoolBelowOneIsRefused(t *testing.T) {
	nmgr, _ := startGateNats(t)
	iproc := newGateProcessor(t, nmgr, &sliceReader{}, instantApi{})
	iproc.resolverCount = -1
	err := iproc.Initialize(context.Background())
	if err == nil {
		t.Fatal("a pool of -1 resolvers initialized; want it refused")
	}
	if want := "at least one resolver, got -1"; !strings.Contains(err.Error(), want) {
		t.Errorf("refusal %q does not say %q", err, want)
	}
}
