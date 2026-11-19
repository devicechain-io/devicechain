// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The three detection fact writers count every fact they could not put on the wire. The change
// behind each one has committed and the detection engine's reconcile will repair it, so the loss
// is not an error anyone is told about — which is why it has to be COUNTED: without the counter,
// a broker that refuses every announcement is indistinguishable from a quiet instance.
func TestDetectionFactWritersCountEveryRefusedPublish(t *testing.T) {
	ctx := context.Background()
	writers := []struct {
		name    string
		publish func(w *captureFactWriter, failures prometheus.Counter)
	}{
		{"detection rules", func(w *captureFactWriter, f prometheus.Counter) {
			NewDetectionRulesPublishedWriter(w, f).PublishDetectionRulesPublished(ctx, &model.DetectionRulesPublishedEvent{
				ProfileVersionToken: "prof@1", PublishedAt: time.Now()})
		}},
		{"device roster", func(w *captureFactWriter, f prometheus.Counter) {
			NewDeviceRosterWriter(w, f).PublishDeviceRoster(ctx, &model.DeviceRosterEvent{
				DeviceToken: "d1", ExpectedSince: time.Now()})
		}},
		{"device attribute", func(w *captureFactWriter, f prometheus.Counter) {
			NewDeviceAttributeWriter(w, f).PublishDeviceAttribute(ctx, &model.DeviceAttributeEvent{
				DeviceToken: "d1", AttrKey: "limit", Scope: "SHARED", Value: 1, UpdatedAt: time.Now()})
		}},
	}
	for _, c := range writers {
		ok := failureCounter()
		c.publish(&captureFactWriter{}, ok)
		if got := testutil.ToFloat64(ok); got != 0 {
			t.Errorf("%s: a delivered publish counted %v failures; want 0", c.name, got)
		}
		refused := failureCounter()
		c.publish(&captureFactWriter{refuse: errors.New("nats: timeout")}, refused)
		c.publish(&captureFactWriter{refuse: errors.New("nats: timeout")}, refused)
		if got := testutil.ToFloat64(refused); got != 2 {
			t.Errorf("%s: two refused publishes counted %v failures; want 2", c.name, got)
		}
		// A nil counter is the unmeasured path (tests, pre-wiring), never a panic.
		c.publish(&captureFactWriter{refuse: errors.New("nats: timeout")}, nil)
	}
}

// The roster writer hands back a refused publish so a per-device fan-out can stop at it
// (model.emitRosterEntries), and answers nil for a delivered one.
func TestTheRosterWriterReturnsARefusedPublish(t *testing.T) {
	ctx := context.Background()
	ev := &model.DeviceRosterEvent{DeviceToken: "d1", ExpectedSince: time.Now()}
	refusal := errors.New("nats: timeout")
	if err := NewDeviceRosterWriter(&captureFactWriter{refuse: refusal}, nil).PublishDeviceRoster(ctx, ev); !errors.Is(err, refusal) {
		t.Fatalf("a refused publish answered %v; want the refusal", err)
	}
	if errors.Is(NewDeviceRosterWriter(&captureFactWriter{refuse: refusal}, nil).PublishDeviceRoster(ctx, ev), model.ErrRosterBrokerUnreachable) {
		t.Fatal("a writer that cannot tell the connection state reported the broker unreachable")
	}
	up := NewDeviceRosterWriter(&captureFactWriter{refuse: refusal}, nil).WithConnected(func() bool { return true })
	if err := up.PublishDeviceRoster(ctx, ev); !errors.Is(err, refusal) || errors.Is(err, model.ErrRosterBrokerUnreachable) {
		t.Fatalf("a refusal on a live connection answered %v; want the plain refusal", err)
	}
	down := NewDeviceRosterWriter(&captureFactWriter{refuse: refusal}, nil).WithConnected(func() bool { return false })
	if err := down.PublishDeviceRoster(ctx, ev); !errors.Is(err, refusal) || !errors.Is(err, model.ErrRosterBrokerUnreachable) {
		t.Fatalf("a refusal with the connection down answered %v; want it marked unreachable", err)
	}
	if err := NewDeviceRosterWriter(&captureFactWriter{}, nil).WithConnected(func() bool { return false }).PublishDeviceRoster(ctx, ev); err != nil {
		t.Fatalf("a delivered publish answered %v; want nil", err)
	}
}
