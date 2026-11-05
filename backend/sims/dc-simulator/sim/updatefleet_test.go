// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-simulator/internal/fakemqtt"
	"github.com/devicechain-io/dc-simulator/otafleet"
)

// fakeUpdateFleet records what was attached, with the position each device took, so the wiring is
// gated without a broker.
type fakeUpdateFleet struct {
	broker string
	opts   otafleet.Options

	mu       sync.Mutex
	attached map[string]int // device token -> index
	creds    map[string]string
	closed   int
	failOn   string
}

func (f *fakeUpdateFleet) Attach(_ context.Context, token, cred string, index int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn == token {
		return fmt.Errorf("device %q: subscribe not acked", token)
	}
	if f.attached == nil {
		f.attached, f.creds = map[string]int{}, map[string]string{}
	}
	f.attached[token], f.creds[token] = index, cred
	return nil
}

func (f *fakeUpdateFleet) Report() otafleet.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return otafleet.Report{Devices: len(f.attached)}
}

func (f *fakeUpdateFleet) Close() { f.mu.Lock(); f.closed++; f.mu.Unlock() }

func updateFixture(t *testing.T, opts *otafleet.Options, mutate func(*Runtime)) (*Lifecycle, *fakeUpdateFleet) {
	t.Helper()
	uf := &fakeUpdateFleet{}
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883", UpdateFleet: opts,
		NewUpdateFleet: func(_, _, broker string, _ *tls.Config, o otafleet.Options) (UpdateFleet, error) {
			uf.broker, uf.opts = broker, o
			return uf, nil
		},
	}
	if mutate != nil {
		mutate(rt)
	}
	driver := &farEndSim{mode: FarEndNone, devices: []DeviceInstance{
		{Token: "dev-001", CredentialId: "cred-001"}, {Token: "dev-002", CredentialId: "cred-002"},
	}}
	return NewLifecycle(driver, rt), uf
}

var unsignedFleet = &otafleet.Options{InitialVersion: "1.0.0", AllowUnsigned: true}

// Every provisioned device is on the update protocol after Bootstrap, each with its own credential
// and its position in the fleet (which is what picks its fault).
func TestBootstrapAttachesTheUpdateFleetToEveryDevice(t *testing.T) {
	lc, uf := updateFixture(t, unsignedFleet, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(uf.attached) != 2 || uf.attached["dev-001"] != 0 || uf.attached["dev-002"] != 1 {
		t.Errorf("attached %v, want both devices at positions 0 and 1", uf.attached)
	}
	if uf.creds["dev-002"] != "cred-002" {
		t.Errorf("device dev-002 attached with %q", uf.creds["dev-002"])
	}
	if got := lc.updateFleetStatus(); got == nil || got.Devices != 2 {
		t.Errorf("/status reports %+v", got)
	}
	// A Reset re-runs Bootstrap and must not attach a second connection per device.
	if err := lc.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	lc.Close()
	if uf.closed != 1 {
		t.Errorf("the fleet was closed %d times, want 1", uf.closed)
	}
	if lc.updateFleetStatus() != nil {
		t.Error("a closed fleet is still reported")
	}
}

func TestNoUpdateFleetUnlessAsked(t *testing.T) {
	lc, uf := updateFixture(t, nil, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(uf.attached) != 0 || lc.updateFleetStatus() != nil {
		t.Error("update devices were attached to a sim that did not ask for them")
	}
}

// Asked for update devices with nowhere to dial, a device that cannot attach, more faults than
// devices, or an outside device plane: each fails the bootstrap instead of running a sim whose
// rollout would wait out its deadlines.
func TestUpdateFleetFailsLoudly(t *testing.T) {
	cases := []struct {
		name   string
		opts   *otafleet.Options
		mutate func(*Runtime)
		want   string
		fail   string
	}{
		{"no broker", unsignedFleet, func(rt *Runtime) { rt.MqttBroker = " " }, "mqttBroker", ""},
		{"too many faults", &otafleet.Options{InitialVersion: "1", AllowUnsigned: true, Faults: otafleet.Faults{BadHash: 3}}, nil, "3 are to misbehave", ""},
		{"external plane", unsignedFleet, func(rt *Runtime) { rt.ExternalDevicePlane = true }, "external-device-plane", ""},
		{"device cannot attach", unsignedFleet, nil, "dev-002", "dev-002"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lc, uf := updateFixture(t, c.opts, c.mutate)
			uf.failOn = c.fail
			err := lc.Bootstrap(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one naming %q", err, c.want)
			}
			if st := lc.State(); st != StateCreated {
				t.Errorf("state is %s after a failed bootstrap", st)
			}
			if c.fail != "" && uf.closed != 1 {
				t.Errorf("the half-attached fleet was closed %d times, want 1", uf.closed)
			}
		})
	}
}

// A sim reached through an address downloads through that address, presenting the host the
// capability URL names; one reached by its hostname dials the URL as it is.
func TestDownloadDialAddr(t *testing.T) {
	rt := &Runtime{}
	if a, err := downloadDialAddr(rt); err != nil || a != "" {
		t.Errorf("no host header: %q, %v", a, err)
	}
	rt = &Runtime{HostHeader: "x.devicechain.test"}
	rt.Endpoints.UserGraphQL = "https://127.0.0.1:8443/api/user-management/graphql"
	if a, err := downloadDialAddr(rt); err != nil || a != "127.0.0.1:8443" {
		t.Errorf("port-forward: %q, %v", a, err)
	}
	rt.Endpoints.UserGraphQL = "https://10.0.0.5/graphql"
	if a, _ := downloadDialAddr(rt); a != "10.0.0.5:443" {
		t.Errorf("default https port: %q", a)
	}
	rt.Endpoints.UserGraphQL = ""
	if _, err := downloadDialAddr(rt); err == nil {
		t.Error("a host header with no address to dial must be an error")
	}
}

// The real fleet, through the real Bootstrap, against a broker: the online device is subscribed to
// its own device-desired topic, the injected-offline one is not, and /status says so.
func TestBootstrapWithTheRealUpdateFleet(t *testing.T) {
	b := fakemqtt.New(t)
	lc, _ := updateFixture(t, &otafleet.Options{InitialVersion: "1.0.0", AllowUnsigned: true,
		Faults: otafleet.Faults{Offline: 1}}, func(rt *Runtime) {
		rt.MqttBroker, rt.NewUpdateFleet = b.URL(), nil
	})
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lc.Close)
	if n := b.Subscribers("dc/acme/device-desired/dev-001"); n != 0 {
		t.Errorf("dev-001 is the injected-offline device but has %d subscriptions", n)
	}
	if n := b.Subscribers("dc/acme/device-desired/dev-002"); n != 1 {
		t.Errorf("dev-002 has %d subscriptions, want 1", n)
	}
	st := lc.updateFleetStatus()
	if st == nil || st.Devices != 2 || st.Connected != 1 || st.Modes[otafleet.Offline] != 1 {
		t.Errorf("/status reports %+v", st)
	}
}
