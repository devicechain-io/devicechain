// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// provisionedTowerpulse is the real towerpulse Sim (its Tick, its command handler, its manifest)
// with Bootstrap reduced to what Provision leaves behind -- the device set -- so a Lifecycle can run
// over it with no GraphQL server.
type provisionedTowerpulse struct{ *towerpulse }

func (p provisionedTowerpulse) Bootstrap(_ context.Context, rt *Runtime) error {
	m := p.Manifest()
	rt.Devices = m.Expand(m.Seed)
	return nil
}

// externalPlaneRig wires a towerpulse Lifecycle to a counting ingress and a counting far end.
func externalPlaneRig(t *testing.T, external bool) (lc *Lifecycle, publishes, farEnds *atomic.Int64, fe *handlerFarEnd) {
	t.Helper()
	publishes, farEnds = new(atomic.Int64), new(atomic.Int64)
	rt := fakeIngress(t, 0, func(w http.ResponseWriter, _ *http.Request) {
		publishes.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	rt.Tenant, rt.InstanceId = "sim-tp-ext", "dc"
	rt.MqttBroker = "tcp://127.0.0.1:1883"
	rt.Load = Load{EmitInterval: 20 * time.Millisecond, Concurrency: 4}
	rt.ExternalDevicePlane = external
	fe = &handlerFarEnd{}
	rt.NewFarEnd = func(_, _, broker string, _ *tls.Config) CommandFarEnd {
		farEnds.Add(1)
		fe.broker = broker
		return fe
	}
	return NewLifecycle(provisionedTowerpulse{NewTowerpulse(1, Load{}).(*towerpulse)}, rt), publishes, farEnds, fe
}

// 🔴 With --external-device-plane the sim provisions and then does NOTHING a device does: zero
// publishes and zero subscriptions, however long it idles, and it refuses to be started. Disabling
// the command replies alone does not give this -- towerpulse's Tick publishes the whole campus.
func TestAnExternalDevicePlanePublishesNothingAndSubscribesAsNoDevice(t *testing.T) {
	lc, publishes, farEnds, fe := externalPlaneRig(t, true)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded under an external device plane: a tick loop is the second writer")
	}
	time.Sleep(150 * time.Millisecond) // several tick intervals, had one been running
	if n := publishes.Load(); n != 0 {
		t.Errorf("%d telemetry publishes from an external-device-plane sim", n)
	}
	if n := farEnds.Load(); n != 0 || len(fe.tokens()) != 0 {
		t.Errorf("%d far ends built, %d device subscriptions: the sim must not hold any device's session", n, len(fe.tokens()))
	}
	if st := lc.State(); st != StateBootstrapped {
		t.Errorf("state = %s, want BOOTSTRAPPED (provisioned, not running)", st)
	}
	if mode := lc.commandFarEndStatus().Mode; mode != FarEndExternal {
		t.Errorf("far-end mode = %s, want external", mode)
	}
	if err := lc.Reset(context.Background()); err != nil {
		t.Fatalf("re-converge: %v", err)
	}
	if publishes.Load() != 0 || farEnds.Load() != 0 {
		t.Error("a Reset (re-converge) made the sim publish or subscribe")
	}
}

// The negative control: the same rig without the flag DOES publish and DOES subscribe all 31
// devices, so the zeros above are the flag's doing and not a dead rig.
func TestWithoutTheFlagTheSameRigPublishesAndSubscribes(t *testing.T) {
	lc, publishes, farEnds, fe := externalPlaneRig(t, false)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for publishes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := lc.Stop(); err != nil {
		t.Fatal(err)
	}
	if publishes.Load() == 0 {
		t.Error("the control rig never published, so the zero-publish result above proves nothing")
	}
	if farEnds.Load() != 1 || len(fe.tokens()) != 31 {
		t.Errorf("%d far ends, %d subscriptions, want 1 and 31", farEnds.Load(), len(fe.tokens()))
	}
}

// The flag is not a license to skip the broker address: an external plane learns it from the same
// handshake, and an empty one is a plane that can never receive a command.
func TestAnExternalDevicePlaneStillRequiresTheBrokerAddress(t *testing.T) {
	lc, _, _, _ := externalPlaneRig(t, true)
	lc.rt.MqttBroker = ""
	if err := lc.Bootstrap(context.Background()); err == nil {
		t.Fatal("bootstrap passed with no broker address for the external device plane")
	}
}

// The Unity plane reads its operator config and token from the runner's /config.json (the only
// endpoint its RunnerConfigClient and TokenBroker call). Under an external device plane that endpoint
// serves exactly as normal -- while the same sim publishes nothing and subscribes as no device.
func TestAnExternalDevicePlaneStillServesConfigJSONWithZeroDeviceTraffic(t *testing.T) {
	lc, publishes, farEnds, fe := externalPlaneRig(t, true)
	mux, prt := presentationFixture(t, &fakeAuthServer{accessToken: "operator-token"}, func(rt *Runtime) {
		rt.ExternalDevicePlane = true
		rt.Tenant, rt.InstanceId = "sim-tp-ext", "dc"
	})
	_ = prt
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	cfg := configMap(t, mux)
	if cfg["token"] != "operator-token" || cfg["tenant"] != "sim-tp-ext" || cfg["instanceId"] != "dc" {
		t.Errorf("/config.json under an external device plane = %v", cfg)
	}
	// A second fetch: the broker re-asks on every token expiry, so it must keep working.
	if cfg2 := configMap(t, mux); cfg2["token"] != "operator-token" {
		t.Errorf("second /config.json = %v", cfg2)
	}
	time.Sleep(100 * time.Millisecond)
	if publishes.Load() != 0 || farEnds.Load() != 0 || len(fe.tokens()) != 0 {
		t.Errorf("serving config produced %d publishes, %d far ends, %d subscriptions", publishes.Load(), farEnds.Load(), len(fe.tokens()))
	}
}
