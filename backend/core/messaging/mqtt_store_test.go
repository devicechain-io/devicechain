// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	nats "github.com/nats-io/nats.go"
)

// mqttGatewayStreams is the set ReconcileMqttStores bounds. core/config carries a
// COUNT of these for its budget arithmetic and cannot import this package
// (messaging depends on config, not the reverse), so the two are pinned to each
// other by TestMqttGatewayStreamCountMatchesConfig below. Without that, adding a
// third gateway stream here would silently understate the reservation — the exact
// failure mode the stream declaration was built to end.
var mqttGatewayStreams = []string{MqttMessageStore, MqttPubRelStore, MqttQoS2InStore}

// A ceiling of zero means UNLIMITED to JetStream, so writing one would silently
// undo the bound rather than fail. Refusing it is the ADR-023 never-unlimited
// posture applied to a stream we do not create.
func TestReconcileMqttStoresRefusesUnlimited(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	for _, bad := range []int64{0, -1} {
		if err := nmgr.ReconcileMqttStores(context.Background(), bad, bad); err == nil {
			t.Errorf("ceiling %d must be refused; 0 means UNLIMITED to JetStream", bad)
		}
	}
}

// The gateway creates its streams lazily, on the first MQTT connection. An
// instance nobody has connected to over MQTT has none, and that is ordinary —
// not a startup failure. The test server runs without the MQTT gateway, so this
// exercises exactly that state.
func TestReconcileMqttStoresToleratesAbsentStreams(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// Skip the backoff: the streams are definitively absent here, and waiting the
	// real schedule would just make the test slow.
	restore := mqttStoreLookupBackoff
	mqttStoreLookupBackoff = []time.Duration{0}
	defer func() { mqttStoreLookupBackoff = restore }()

	if err := nmgr.ReconcileMqttStores(context.Background(), 256<<20, 64<<20); err != nil {
		t.Fatalf("absent gateway streams must not fail startup: %v", err)
	}
}

// The bound must actually be applied to a stream that DOES exist, and be
// idempotent — every service restart runs this, and an UpdateStream per boot per
// stream for no reason is noise at best.
func TestReconcileMqttStoresAppliesAndIsIdempotent(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	// Stand in for what the gateway would have created: unbounded, interest
	// retention. If JetStream refuses the reserved "$" name to a normal client,
	// there is nothing to assert here and the guarantee is left to the live
	// cluster.
	const maxBytes = 256 << 20
	const qos2MaxBytes = 64 << 20
	for _, name := range mqttGatewayStreams {
		if _, err := nmgr.js.AddStream(&nats.StreamConfig{
			Name:      name,
			Subjects:  []string{"$MQTT.test." + name + ".>"},
			Storage:   nats.FileStorage,
			Retention: nats.InterestPolicy,
		}); err != nil {
			t.Skipf("cannot create a %q stream in-process (%v); "+
				"the applied-bound path is covered on a live cluster", name, err)
		}
	}

	if err := nmgr.ReconcileMqttStores(context.Background(), maxBytes, qos2MaxBytes); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, b := range MqttStoreBounds(maxBytes, qos2MaxBytes) {
		info, err := nmgr.js.StreamInfo(b.Name)
		if err != nil {
			t.Fatalf("stream info %q: %v", b.Name, err)
		}
		if info.Config.MaxBytes != b.MaxBytes {
			t.Errorf("%s MaxBytes = %d, want %d — an unbounded gateway stream can "+
				"consume the whole budget's headroom", b.Name, info.Config.MaxBytes, b.MaxBytes)
		}
	}

	// Idempotency, asserted by observable effect rather than by "no error". A
	// regression that calls UpdateStream unconditionally every boot would return
	// nil too, so mark the streams out-of-band first: if the second pass rewrote
	// the config, the marker is gone.
	for _, b := range MqttStoreBounds(maxBytes, qos2MaxBytes) {
		info, err := nmgr.js.StreamInfo(b.Name)
		if err != nil {
			t.Fatalf("stream info %q: %v", b.Name, err)
		}
		cfg := info.Config
		cfg.Description = "idempotency-marker"
		if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
			t.Fatalf("marking %q: %v", b.Name, err)
		}
	}
	if err := nmgr.ReconcileMqttStores(context.Background(), maxBytes, qos2MaxBytes); err != nil {
		t.Fatalf("reconcile must be idempotent across restarts: %v", err)
	}
	for _, b := range MqttStoreBounds(maxBytes, qos2MaxBytes) {
		info, err := nmgr.js.StreamInfo(b.Name)
		if err != nil {
			t.Fatalf("stream info %q: %v", b.Name, err)
		}
		if info.Config.Description != "idempotency-marker" {
			t.Errorf("%s was rewritten on a second reconcile; every service restart "+
				"would issue a pointless UpdateStream against it", b.Name)
		}
	}
}

// core/config budgets MqttGatewayStreamCount x MqttStoreMaxBytes of reservation
// but cannot see the stream names, because messaging depends on config and not
// the reverse. This is the seam where the two could disagree: adding a third
// gateway stream above without raising the count would reserve room for two and
// bound three, understating the disk floor by a whole ceiling — which is exactly
// how the platform's own budget was wrong before core/streams.
func TestMqttGatewayStreamCountMatchesReconciler(t *testing.T) {
	ic := &config.InstanceConfiguration{}
	ic.ApplyDefaults()
	cfg := ic.Infrastructure.Nats

	bounds := MqttStoreBounds(cfg.MqttStoreMaxBytes, cfg.MqttQoS2StoreMaxBytes)

	if got, want := len(bounds), config.MqttGatewayStreamCount; got != want {
		t.Fatalf("ReconcileMqttStores bounds %d streams but the disk budget reserves for %d",
			got, want)
	}

	// The stronger check: the budget must reserve exactly the bytes actually
	// applied, not merely count the same number of streams. Giving one stream a
	// different ceiling would keep the counts equal while understating the floor.
	var applied int64
	for _, b := range bounds {
		applied += b.MaxBytes
	}
	if got := cfg.MqttStoreReservation(); got != applied {
		t.Fatalf("the disk budget reserves %d B for the MQTT gateway streams but "+
			"ReconcileMqttStores actually applies %d B of ceilings; the budget "+
			"understates the disk floor by %d B", got, applied, applied-got)
	}
}
