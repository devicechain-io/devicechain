// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// gatedMergeApi holds every merge until gate is closed, and records how many were held at
// once — which is how many writers are running.
type gatedMergeApi struct {
	model.DeviceStateApi
	gate           chan struct{}
	inflight, peak atomic.Int32
}

func (g *gatedMergeApi) MergeDeviceState(context.Context, string, time.Time, *model.PresenceTransition,
	model.DeviceIdentity) (*model.DeviceState, error) {
	n := g.inflight.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-g.gate
	g.inflight.Add(-1)
	// An error keeps the message on the retry path, which touches nothing else.
	return nil, errors.New("gated merge")
}

// The number of projection writers is the configured one: with more messages waiting than
// writers, exactly that many merges run at once.
func TestTheProjectionRunsTheConfiguredNumberOfWriters(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []StateProcessorOption
		want int32
	}{
		{"two writers", []StateProcessorOption{WithWriters(2)}, 2},
		{"three writers", []StateProcessorOption{WithWriters(3)}, 3},
		{"the default", nil, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			api := &gatedMergeApi{gate: make(chan struct{})}
			sp := NewStateProcessor(ms, nil, core.NewNoOpLifecycleCallbacks(), api, NewStateMetrics(ms),
				ms.NewPeriodicTaskMetrics("writers_test"), tc.opts...)
			if err := sp.Initialize(context.Background()); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			if got := sp.Writers(); got != int(tc.want) {
				t.Errorf("Writers() = %d; want %d", got, tc.want)
			}
			// More messages than any row's writers, so the writers are the limit.
			for i := 0; i < 8; i++ {
				encoded, err := dmproto.MarshalResolvedEvent(&dmmodel.ResolvedEvent{
					SourceDeviceToken: fmt.Sprintf("dev-%d", i),
					EventType:         esmodel.Alert,
					OccurredTime:      time.Date(2026, 9, 20, 12, 0, i, 0, time.UTC),
					Payload:           &dmmodel.ResolvedAlertsPayload{},
				})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if !sp.handOff(context.Background(), messaging.Message{Subject: "instance1.acme.resolved-events", Value: encoded}) {
					t.Fatalf("hand-off %d refused", i)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for api.inflight.Load() < tc.want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			// Give a writer beyond the configured count time to show itself.
			time.Sleep(50 * time.Millisecond)
			close(api.gate)
			if err := sp.ExecuteStop(context.Background()); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if got := api.peak.Load(); got != tc.want {
				t.Errorf("%d merges ran at once; want %d", got, tc.want)
			}
		})
	}
}
