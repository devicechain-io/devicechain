// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
)

// fleetpulseFarEndRig brings a fleetpulse up through the REAL lifecycle wiring against a fake
// far end, so what a test drives is the handler the lifecycle actually handed the far end,
// not one the test constructed. A scenario that stopped offering its handler would leave the
// far end answering at once, and these tests would see a nil handler and fail.
type fleetpulseFarEndRig struct {
	fe     *handlerFarEnd
	driver *fleetpulse
}

// fpFarEndSim is the real fleetpulse with Bootstrap replaced: provisioning talks to a
// platform, and the wiring under test starts after it.
type fpFarEndSim struct {
	*fleetpulse
	devices []DeviceInstance
}

func (s *fpFarEndSim) Bootstrap(_ context.Context, rt *Runtime) error {
	rt.Devices = s.devices
	return nil
}

func newFleetpulseFarEndRig(t *testing.T) *fleetpulseFarEndRig {
	t.Helper()
	fe := &handlerFarEnd{}
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(_, _, _ string, _ *tls.Config) CommandFarEnd { return fe },
	}
	driver := NewFleetpulse(7, Load{}).(*fleetpulse)
	devices := make([]DeviceInstance, fleetpulseVehicleCount)
	for i := range devices {
		// The names fakeIngress gives its devices, so a limit set through the handler is
		// the one the next tick against that ingress applies.
		devices[i] = DeviceInstance{Token: renderPattern("dev-{n:05d}", i+1), CredentialId: "c"}
	}
	lc := NewLifecycle(&fpFarEndSim{fleetpulse: driver, devices: devices}, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return &fleetpulseFarEndRig{fe: fe, driver: driver}
}

// send delivers one command frame the way the receiver would and returns the outcome the
// vehicle answered with. It fails the test if the vehicle never answers.
func (r *fleetpulseFarEndRig) send(t *testing.T, device, key string, payload any) bool {
	t.Helper()
	if r.fe.handler == nil {
		t.Fatal("the scenario handed the far end no handler, so every command is answered " +
			"SUCCESSFUL on receipt whatever it asked and nothing about the vehicle changes")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan bool, 1)
	r.fe.handler.Receive(device, cmdreceiver.Command{Token: "cmd-1", Nonce: "n1", Name: key, Payload: raw},
		func(ok bool) { got <- ok })
	select {
	case ok := <-got:
		return ok
	case <-time.After(5 * time.Second):
		t.Fatal("the vehicle never answered the command")
		return false
	}
}

func TestFleetpulseDeclaresTheTypedSpeedLimitCommandAndAFarEnd(t *testing.T) {
	m := NewFleetpulse(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest does not validate: %v", err)
	}
	if m.FarEndMode() != FarEndInternal {
		t.Fatalf("far end = %q, want %q: without one the command sits at SENT until it times out",
			m.FarEndMode(), FarEndInternal)
	}
	cmds := m.Profiles[0].Commands
	if len(cmds) != 1 || cmds[0].CommandKey != FleetpulseSpeedLimitKey {
		t.Fatalf("want exactly the %q command, got %+v", FleetpulseSpeedLimitKey, cmds)
	}
	var params []struct {
		Name     string   `json:"name"`
		DataType string   `json:"dataType"`
		Required bool     `json:"required"`
		Min      *float64 `json:"minValue"`
		Max      *float64 `json:"maxValue"`
	}
	if err := json.Unmarshal([]byte(cmds[0].ParameterSchema), &params); err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 || params[0].Name != FleetpulseSpeedLimitParameter || !params[0].Required ||
		params[0].DataType != "DOUBLE" || params[0].Min == nil || *params[0].Min != 5 ||
		params[0].Max == nil || *params[0].Max != 30 {
		t.Fatalf("parameter schema is not one required DOUBLE limit_mps in [5, 30]: %+v", params)
	}
}

// The command completes SUCCESSFULLY, through the handler the lifecycle gave the far end.
func TestFleetpulseSetSpeedLimitCompletesSuccessfully(t *testing.T) {
	r := newFleetpulseFarEndRig(t)
	if ok := r.send(t, "dev-00003", FleetpulseSpeedLimitKey, map[string]any{"limit_mps": 15}); !ok {
		t.Fatal("set-speed-limit with an in-range value was answered FAILED")
	}
}

// What the vehicle cannot act on is FAILED, never a plausible success. Range is the
// platform's job (see device-management's TestFleetpulseSpeedLimitIsRefusedByThePlatform).
func TestFleetpulseFailsWhatItCannotActOn(t *testing.T) {
	r := newFleetpulseFarEndRig(t)
	for name, c := range map[string]struct {
		key     string
		payload any
	}{
		"another command": {"reboot", map[string]any{"limit_mps": 15}},
		"no limit":        {FleetpulseSpeedLimitKey, map[string]any{}},
		"not a number":    {FleetpulseSpeedLimitKey, map[string]any{"limit_mps": "fast"}},
		"non-positive":    {FleetpulseSpeedLimitKey, map[string]any{"limit_mps": 0}},
	} {
		if r.send(t, "dev-00003", c.key, c.payload) {
			t.Errorf("%s: answered SUCCESSFUL", name)
		}
	}
	if _, ok := r.driver.limits.get("dev-00003"); ok {
		t.Error("a refused command left a limit in force")
	}
}

// fleetpulseSpeeds returns each device's [location speed, speed_mps] from one tick.
func fleetpulseSpeeds(t *testing.T, seen []emittedEvent) map[string][2]float64 {
	t.Helper()
	out := map[string][2]float64{}
	for _, e := range seen {
		var idx int
		var raw string
		switch e.eventType {
		case "Location":
			idx, raw = 0, e.entry["speed"]
		case "Measurement":
			idx, raw = 1, e.entry[FleetpulseSpeedKey]
		default:
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("device %s %s speed %q: %v", e.device, e.eventType, raw, err)
		}
		pair := out[e.device]
		pair[idx] = v
		out[e.device] = pair
	}
	return out
}

// After the command the commanded vehicle never reports above its limit, on the location AND
// the metric (which must stay one value), and the others are untouched.
func TestFleetpulseSpeedRespectsTheLimitAfterTheCommand(t *testing.T) {
	const count = fleetpulseVehicleCount
	base := fleetpulseSpeeds(t, runFleetpulseTick(t, count))
	// A limit below every uncapped speed on this tick, so the cap is certain to bite.
	const limit = 6.0
	for dev, sp := range base {
		if sp[0] <= limit {
			t.Fatalf("setup: %s uncapped speed %v is not above the %v limit, so the test proves nothing",
				dev, sp[0], limit)
		}
	}
	d := NewFleetpulse(7, Load{}).(*fleetpulse)
	d.limits.set("dev-00002", limit)
	capped := fleetpulseSpeeds(t, runFleetpulseTickWith(t, count, d))
	for dev, sp := range capped {
		if dev == "dev-00002" {
			if sp[0] != limit || sp[1] != limit {
				t.Errorf("limited vehicle reports location %v / metric %v, want both %v", sp[0], sp[1], limit)
			}
			continue
		}
		if sp != base[dev] {
			t.Errorf("%s changed from %v to %v though it was never commanded", dev, base[dev], sp)
		}
	}
}

// A limit above the vehicle's natural speed changes nothing: it is a ceiling, not a target.
func TestFleetpulseALimitAboveTheCurveIsNotATarget(t *testing.T) {
	base := fleetpulseSpeeds(t, runFleetpulseTick(t, fleetpulseVehicleCount))
	d := NewFleetpulse(7, Load{}).(*fleetpulse)
	d.limits.set("dev-00001", 30)
	got := fleetpulseSpeeds(t, runFleetpulseTickWith(t, fleetpulseVehicleCount, d))
	if got["dev-00001"] != base["dev-00001"] {
		t.Errorf("a 30 m/s ceiling moved a slower vehicle from %v to %v", base["dev-00001"], got["dev-00001"])
	}
}

// End to end through the handler: the limit the command sets is the limit the next tick caps at.
func TestFleetpulseTheCommandSetsTheLimitTheNextTickUses(t *testing.T) {
	r := newFleetpulseFarEndRig(t)
	if !r.send(t, "dev-00003", FleetpulseSpeedLimitKey, map[string]any{"limit_mps": 7}) {
		t.Fatal("command failed")
	}
	got := fleetpulseSpeeds(t, runFleetpulseTickWith(t, fleetpulseVehicleCount, r.driver))
	if sp := got["dev-00003"]; sp[0] != 7 || sp[1] != 7 {
		t.Errorf("after set-speed-limit 7 the vehicle reports %v", sp)
	}
}
