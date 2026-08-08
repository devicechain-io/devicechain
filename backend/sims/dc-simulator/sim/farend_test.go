// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
)

// These tests gate the WIRING of the command far end, which is the half that was
// missing — not the receiver, which has its own unit tests over a real payload.
//
// 🔑 The distinction matters because the wiring is the part that cannot be checked
// by hand without a live MQTT gateway, and "I opened the console and pressed Send"
// is exactly the manual step nobody repeats. Every property below — is a far end
// attached at all, does it cover EVERY device, does a missing broker fail loudly
// rather than degrade, does a blind device fail the bootstrap — is a way the control
// channel can be quietly dead while the board still renders a Send button.

// fakeFarEnd records what was attached and can be told to fail one device, so a
// blind subscription is testable without a broker.
type fakeFarEnd struct {
	broker    string
	tlsConfig *tls.Config

	mu       sync.Mutex
	attached map[string]string // device token → credential id
	closed   int
	failOn   string // device token whose Subscribe returns an error
}

func (f *fakeFarEnd) Subscribe(_ context.Context, deviceToken, credentialId string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != "" && f.failOn == deviceToken {
		return fmt.Errorf("device %q: subscribe not acked (blind)", deviceToken)
	}
	if f.attached == nil {
		f.attached = map[string]string{}
	}
	f.attached[deviceToken] = credentialId
	return nil
}

func (f *fakeFarEnd) Report() cmdreceiver.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	rep := cmdreceiver.Report{Broker: f.broker, Devices: map[string]cmdreceiver.DeviceReport{}}
	for tok := range f.attached {
		rep.Devices[tok] = cmdreceiver.DeviceReport{Token: tok, Subscribed: true}
	}
	return rep
}

func (f *fakeFarEnd) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *fakeFarEnd) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.attached))
	for tok := range f.attached {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

func (f *fakeFarEnd) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// farEndSim is a Sim that declares a far end (and the command definition Validate
// requires alongside one), and PROVISIONS its devices in Bootstrap.
//
// 🔴 The provisioning is the load-bearing part of this fake, and it was missing.
// The real Provision assigns rt.Devices (bootstrap.go), so the far end can only see
// devices because it attaches AFTER it — an ordering the code comments call
// load-bearing. While this fake left Bootstrap a no-op and the FIXTURE pre-filled
// rt.Devices, that ordering was pinned by nothing: moving the attach ahead of
// Provision passed the entire suite, while in production it would attach to zero
// devices, report attached:true, and reinstate the original defect behind a green
// status. A fake that skips the step under test measures the fixture, not the code.
type farEndSim struct {
	mode    CommandFarEndMode
	devices []DeviceInstance
}

func (s *farEndSim) Manifest() SimManifest {
	m := SimManifest{Name: "farend-fake", CommandFarEnd: s.mode}
	// The command vocabulary belongs to BOTH far-end modes, not just the internal
	// one: Validate refuses either without it, so a fake that carried commands only
	// for internal would be a manifest Validate rejects — an invalid fixture for the
	// external tests, which are about what a VALID external scenario does.
	if m.FarEndMode() != FarEndNone {
		m.Profiles = []ProfileSpec{{
			Token:    "fe-profile",
			Commands: []CommandSpec{{Token: "fe-cmd", CommandKey: "doThing"}},
		}}
	}
	return m
}

func (s *farEndSim) Bootstrap(_ context.Context, rt *Runtime) error {
	rt.Devices = s.devices
	return nil
}
func (s *farEndSim) Tick(context.Context, *Runtime) error { return nil }

// farEndFixture builds a Lifecycle with a fake far-end factory over a scenario that
// provisions two devices, returning both so a test can assert what was attached.
func farEndFixture(t *testing.T, mode CommandFarEndMode, mutate func(*Runtime)) (*Lifecycle, *fakeFarEnd) {
	t.Helper()
	fe := &fakeFarEnd{}
	rt := &Runtime{
		Tenant:     "acme",
		InstanceId: "dc",
		MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(instanceId, tenant, broker string, tlsConfig *tls.Config) CommandFarEnd {
			fe.broker, fe.tlsConfig = broker, tlsConfig
			return fe
		},
	}
	if mutate != nil {
		mutate(rt)
	}
	driver := &farEndSim{mode: mode, devices: []DeviceInstance{
		{Token: "dev-001", CredentialId: "cred-001"},
		{Token: "dev-002", CredentialId: "cred-002"},
	}}
	return NewLifecycle(driver, rt), fe
}

// The property the whole seam exists for: after Bootstrap, EVERY device is
// listening. A far end covering only some devices is worse than none — the board's
// command widget works for the device someone happened to test with and expires
// for the rest.
func TestBootstrapAttachesTheFarEndToEveryDevice(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if got, want := fe.tokens(), []string{"dev-001", "dev-002"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("far end attached %v, want %v", got, want)
	}
	// Each device must present its OWN credential: a device's JWT grants SUB to
	// exactly one command subject, so a crossed credential subscribes the wrong
	// device — or nothing — while the count still looks right.
	fe.mu.Lock()
	defer fe.mu.Unlock()
	for tok, cred := range fe.attached {
		if want := strings.Replace(tok, "dev-", "cred-", 1); cred != want {
			t.Errorf("device %q attached with credential %q, want %q", tok, cred, want)
		}
	}
}

// A declared far end with nowhere to dial must FAIL the bootstrap.
//
// The tempting alternative — log a warning and carry on — is the exact defect being
// fixed: the scenario comes up green, the board renders a Send button, and every
// command issued from it reaches SENT and expires a week later with nothing
// anywhere reporting a problem.
func TestBootstrapRefusesADeclaredFarEndWithNoBroker(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, func(rt *Runtime) { rt.MqttBroker = "  " })
	err := lc.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("bootstrap succeeded with no MQTT broker for a scenario that declares a far end")
	}
	if !strings.Contains(err.Error(), "mqttBroker") {
		t.Errorf("error %q does not name the missing handshake field, so a reader cannot fix it", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("a far end was attached despite there being no broker")
	}
	// And the failure must be visible in the FSM, not only in the returned error:
	// a state of BOOTSTRAPPED would let Start run the scenario anyway.
	if st := lc.State(); st != StateCreated {
		t.Errorf("state is %s after a failed bootstrap, want %s", st, StateCreated)
	}
}

// A device that does not come back subscribed fails the bootstrap, and what DID
// attach is disconnected rather than left holding broker connections across a
// Reset loop.
func TestBootstrapFailsWhenADeviceCannotSubscribe(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	fe.failOn = "dev-002"

	err := lc.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("bootstrap succeeded with a device that never subscribed: its commands would expire silently")
	}
	if !strings.Contains(err.Error(), "dev-002") {
		t.Errorf("error %q does not name the device that failed", err)
	}
	if fe.closeCount() != 1 {
		t.Errorf("the partially-attached far end was closed %d times, want 1 — a failed bootstrap "+
			"must not leave broker connections open", fe.closeCount())
	}
}

// The explicit opt-out runs the scenario without a far end and SAYS SO on /status.
// The point of the flag is that the degrade becomes a reported decision instead of
// an invisible one.
func TestFarEndDisabledSkipsAttachAndIsReported(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, func(rt *Runtime) { rt.FarEndDisabled = true })
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("a far end was attached despite --no-command-far-end")
	}
	st := lc.commandFarEndStatus()
	if !st.Declared {
		t.Error("status reports the scenario does not declare a far end; it does")
	}
	if st.Attached {
		t.Error("status reports a far end attached; none is")
	}
	if !st.Disabled {
		t.Error("status does not report the far end as disabled, so the degrade is invisible")
	}
}

// Disabling the far end must NOT rescue a scenario from the missing-broker refusal
// by accident of ordering — but it must also not be the only way to run one. The
// property under test is that the opt-out is checked BEFORE the broker, so a host
// that cannot reach the gateway has a working escape hatch.
func TestFarEndDisabledDoesNotNeedABroker(t *testing.T) {
	lc, _ := farEndFixture(t, FarEndInternal, func(rt *Runtime) {
		rt.FarEndDisabled = true
		rt.MqttBroker = ""
	})
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap with the far end disabled and no broker: %v", err)
	}
}

// A scenario that declares no far end needs no broker and gets no connection.
// Without this, adding the seam would break devicepulse and buildingpulse.
func TestAScenarioWithNoFarEndNeedsNoBroker(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndNone, func(rt *Runtime) { rt.MqttBroker = "" })
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("a far end was attached for a scenario that declares none")
	}
	if st := lc.commandFarEndStatus(); st.Declared || st.Attached {
		t.Errorf("status reports declared=%v attached=%v for a scenario with no far end", st.Declared, st.Attached)
	}
}

// 🔑 THE EXTERNAL-MODE GATE. A scenario whose far end is a presentation client in
// another process must get NO in-process receiver — and the failure this refuses is
// worse than the one the whole seam was built for. A cmdreceiver attached alongside
// the real device answers SUCCESSFUL for work it did not do and cannot see: the
// command shows as completed, the machine on screen never moved, and every layer
// reports success. A command stuck at SENT at least looks broken.
//
// It must also not ERROR: external is a legal, complete scenario, and a bootstrap
// failure here would make the mode unusable rather than merely un-attached.
func TestExternalFarEndAttachesNoInProcessReceiver(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndExternal, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap of an external-far-end scenario: %v", err)
	}
	if got := fe.tokens(); len(got) != 0 {
		t.Errorf("a Go far end subscribed %v for an external-far-end scenario; it would answer "+
			"SUCCESSFUL for commands only the external client can act on", got)
	}
	if st := lc.commandFarEndStatus(); st.Attached {
		t.Error("status reports an in-process far end attached in external mode")
	}
	// The scenario is fully bootstrapped, not half-failed: BOOTSTRAPPED is what lets
	// Start run it at all.
	if st := lc.State(); st != StateBootstrapped {
		t.Errorf("state is %s after bootstrapping an external-far-end scenario, want %s",
			st, StateBootstrapped)
	}
}

// 🔴 External REQUIRES the broker, and this test replaced one asserting the exact
// opposite. The earlier reasoning — "this process dials nothing in external mode, so
// it needs no address" — is wrong about where the address goes: it is handed to the
// presentation client with the rest of the scenario's config, so an empty value here
// is an empty value in the scene.
//
// And the polarity runs the other way from the intuition. An internal far end that
// cannot dial fails right here, in the log the operator is watching. An external one
// that cannot dial fails in ANOTHER PROCESS: the scene subscribes to nothing while
// this side's bootstrap, /status and board are all green, and every command the board
// dispatches reaches SENT and expires a week later. Waiving the check is the precise
// fail-open the whole seam exists to remove, moved one process further away where
// nothing reports it.
func TestExternalFarEndRefusesToBootstrapWithNoBroker(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndExternal, func(rt *Runtime) { rt.MqttBroker = "  " })
	err := lc.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("an external-far-end scenario bootstrapped with no broker: the presentation " +
			"client would be handed an empty address, dial nothing, and every command from " +
			"its board would expire unanswered with everything here reporting green")
	}
	if !strings.Contains(err.Error(), "mqttBroker") {
		t.Errorf("error %q does not name the handshake field a reader has to fix", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("a Go far end was attached in external mode")
	}
	// The refusal must reach the FSM, not just the caller: BOOTSTRAPPED would let
	// Start run the scenario anyway.
	if st := lc.State(); st != StateCreated {
		t.Errorf("state is %s after a refused bootstrap, want %s", st, StateCreated)
	}
}

// The counterweight to the test above, and it is what keeps that one from being
// satisfied by a guard that simply always refuses: the operator's explicit opt-out
// still runs an external scenario on a host with no gateway address, exactly as it
// does an internal one. Without this the broker gate would have no escape hatch in
// the mode that gained it.
func TestFarEndDisabledLetsAnExternalScenarioRunWithNoBroker(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndExternal, func(rt *Runtime) {
		rt.FarEndDisabled = true
		rt.MqttBroker = ""
	})
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap of an external scenario with the far end disabled and no broker: %v", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("a far end was attached in external mode")
	}
}

// 🔴 The distinction /status exists to carry. attached:false has two causes once a
// far end can live elsewhere — "nothing is answering" and "something outside this
// process is answering" — and they are opposite verdicts on the same Send button.
// From inside the simulator both are the same absence, so if the report cannot tell
// them apart, nothing can.
//
// Asserted as a DIFFERENCE between the two modes rather than as a field's value, so
// a report that hard-codes a mode, or drops the field, fails here.
func TestStatusDistinguishesAnExternalFarEndFromNothingAnswering(t *testing.T) {
	external, _ := farEndFixture(t, FarEndExternal, nil)
	if err := external.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap external: %v", err)
	}
	none, _ := farEndFixture(t, FarEndNone, nil)
	if err := none.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap none: %v", err)
	}

	ext, non := external.commandFarEndStatus(), none.commandFarEndStatus()
	if ext.Attached || non.Attached {
		t.Fatalf("a far end attached in one of the un-attached modes (external=%v none=%v); "+
			"the comparison below would be about the wrong thing", ext.Attached, non.Attached)
	}
	if ext.Mode == non.Mode {
		t.Fatalf("/status reports mode %q for both external and none: a scenario answered by a "+
			"Unity player is indistinguishable from one nothing answers", ext.Mode)
	}
	if ext.Mode != FarEndExternal || non.Mode != FarEndNone {
		t.Errorf("/status reports mode external=%q none=%q, want %q and %q",
			ext.Mode, non.Mode, FarEndExternal, FarEndNone)
	}
	// Declared is the other half: external DOES expect an answer, so a reader who
	// only looks at declared/attached still sees "wanted, not attached here" rather
	// than "never wanted one".
	if !ext.Declared {
		t.Error("/status reports an external far end as undeclared, so the board's Send button " +
			"reads as decorative")
	}
	if non.Declared {
		t.Error("/status declares a far end for a scenario that has none")
	}
}

// --no-command-far-end means ONE thing wherever there is a far end to give up: the
// operator accepts that this scenario's commands go unanswered. So it is reported
// identically for internal and external, and withheld for none — where the flag
// accepts nothing, because that scenario never had a command channel and reporting
// it as "disabled" would invent a control surface it does not have.
//
// All three are asserted together. Each half is trivially satisfiable alone: a report
// that always sets disabled passes the first two, and one that never sets it passes
// the third.
func TestFarEndDisabledIsReportedForEveryDeclaredFarEndAndOnlyThose(t *testing.T) {
	for _, mode := range []CommandFarEndMode{FarEndInternal, FarEndExternal} {
		lc, _ := farEndFixture(t, mode, func(rt *Runtime) { rt.FarEndDisabled = true })
		if err := lc.Bootstrap(context.Background()); err != nil {
			t.Fatalf("bootstrap %q with the far end disabled: %v", mode, err)
		}
		st := lc.commandFarEndStatus()
		if !st.Disabled {
			t.Errorf("mode %q: /status does not report the far end as disabled, so an operator "+
				"reading it cannot tell a dead Send button from a live one", mode)
		}
		if !st.Declared {
			t.Errorf("mode %q: /status reports the scenario as declaring no far end; it does", mode)
		}
		if st.Attached {
			t.Errorf("mode %q: /status reports a far end attached despite the opt-out", mode)
		}
	}

	none, _ := farEndFixture(t, FarEndNone, func(rt *Runtime) { rt.FarEndDisabled = true })
	if err := none.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap none with the far end disabled: %v", err)
	}
	if st := none.commandFarEndStatus(); st.Disabled {
		t.Error("/status reports a disabled far end for a scenario that declares none, which " +
			"describes a control channel it never had")
	}

	// The other direction: the flag must not be reported when it was never passed, or
	// "disabled" says nothing about what the operator chose.
	on, _ := farEndFixture(t, FarEndInternal, nil)
	if err := on.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap internal: %v", err)
	}
	if st := on.commandFarEndStatus(); st.Disabled {
		t.Error("/status reports the far end as disabled without --no-command-far-end")
	}
}

// Reset re-runs Bootstrap, and Bootstrap attaches the far end — so a Reset loop
// must not open a second connection per device. Left unguarded this is unbounded:
// dcctl's reset verb is the documented "start over".
func TestResetDoesNotReattachTheFarEnd(t *testing.T) {
	built := 0
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	inner := lc.rt.NewFarEnd
	lc.rt.NewFarEnd = func(instanceId, tenant, broker string, tlsConfig *tls.Config) CommandFarEnd {
		built++
		return inner(instanceId, tenant, broker, tlsConfig)
	}

	ctx := context.Background()
	if err := lc.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := lc.Reset(ctx); err != nil {
			t.Fatalf("reset %d: %v", i, err)
		}
	}
	if built != 1 {
		t.Errorf("built %d far ends across a bootstrap + 3 resets, want 1", built)
	}
	if fe.closeCount() != 0 {
		t.Errorf("the far end was closed %d times by Reset; a reset must not drop the "+
			"device's command subscription", fe.closeCount())
	}
}

// Stop halts telemetry; it does NOT end the far end. A real device keeps listening
// for commands while it is not reporting, and a far end that dropped off on Stop
// would make a command issued against a stopped sim expire — the same failure this
// seam removes, in a narrower window. Close is what ends it.
func TestStopKeepsTheFarEndAndCloseReleasesIt(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	ctx := context.Background()
	if err := lc.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := lc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := lc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if fe.closeCount() != 0 {
		t.Error("Stop closed the far end; a stopped sim's devices must still answer commands")
	}
	if !lc.commandFarEndStatus().Attached {
		t.Error("status reports no far end after Stop")
	}

	lc.Close()
	if fe.closeCount() != 1 {
		t.Errorf("Close closed the far end %d times, want 1", fe.closeCount())
	}
	if lc.commandFarEndStatus().Attached {
		t.Error("status still reports a far end after Close")
	}
	// Close must be safe to call twice — main's shutdown path and a test both do.
	lc.Close()
	if fe.closeCount() != 1 {
		t.Errorf("a second Close re-closed the far end (%d closes)", fe.closeCount())
	}
}

// /status must carry the control channel's state. This is the surface a human or a
// script reads to answer "does the Send button on this board do anything", and
// until now there was no such surface at all.
func TestStatusReportsTheCommandFarEnd(t *testing.T) {
	lc, _ := farEndFixture(t, FarEndInternal, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	mux := http.NewServeMux()
	NewControlServer(lc, lc.rt).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var body struct {
		CommandFarEnd struct {
			Declared bool   `json:"declared"`
			Attached bool   `json:"attached"`
			Broker   string `json:"broker"`
			Evidence *struct {
				Devices map[string]any `json:"devices"`
			} `json:"evidence"`
		} `json:"commandFarEnd"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	if !body.CommandFarEnd.Declared || !body.CommandFarEnd.Attached {
		t.Errorf("/status reports declared=%v attached=%v, want both true",
			body.CommandFarEnd.Declared, body.CommandFarEnd.Attached)
	}
	if body.CommandFarEnd.Broker == "" {
		t.Error("/status does not name the broker the far end dialed")
	}
	if body.CommandFarEnd.Evidence == nil || len(body.CommandFarEnd.Evidence.Devices) != 2 {
		t.Error("/status carries no per-device far-end evidence, so a blind device is invisible")
	}
}

// statusFarEndMap does what a real reader of /status does: it takes the response
// bytes and looks up KEYS, with no Go type in between to quietly translate them.
func statusFarEndMap(t *testing.T, lc *Lifecycle) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	NewControlServer(lc, lc.rt).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	fe, ok := body["commandFarEnd"].(map[string]any)
	if !ok {
		t.Fatalf("/status has no object under the key %q; it carries %v", "commandFarEnd", keysOf(body))
	}
	return fe
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 🔴 THE WIRE NAMES ARE THE CONTRACT, and nothing else in this file pins them. Every
// other assertion here goes through commandFarEndStatus, i.e. through the Go struct,
// where a renamed json tag is invisible — the README sells `mode` as the field an
// outside reader uses to tell "answered elsewhere" from "answered by nobody", and
// renaming it breaks dcctl and every curl script while compiling cleanly.
//
// It decodes into a map rather than a struct with tags DELIBERATELY. encoding/json
// matches field names case-insensitively and falls back to the Go field name, so a
// tagged struct still decodes a body emitting "Mode" — a test written that way stays
// green with the tag deleted outright, which is most of the ways this can break.
func TestStatusWireNamesAreTheContract(t *testing.T) {
	lc, _ := farEndFixture(t, FarEndInternal, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	fe := statusFarEndMap(t, lc)

	for key, want := range map[string]any{
		"mode":     string(FarEndInternal),
		"declared": true,
		"attached": true,
	} {
		got, ok := fe[key]
		if !ok {
			t.Errorf("/status carries no %q key; commandFarEnd has %v", key, keysOf(fe))
			continue
		}
		if got != want {
			t.Errorf("/status %q is %v, want %v", key, got, want)
		}
	}
	if broker, _ := fe["broker"].(string); broker == "" {
		t.Errorf("/status carries no %q key naming the broker; commandFarEnd has %v",
			"broker", keysOf(fe))
	}
	if _, ok := fe["evidence"].(map[string]any); !ok {
		t.Errorf("/status carries no %q object, so per-device far-end evidence is unreadable "+
			"to anything outside this process; commandFarEnd has %v", "evidence", keysOf(fe))
	}
	// disabled is omitempty, so its ABSENCE is the report for a far end nobody
	// suppressed — and asserting that is what stops "always emit disabled:false" from
	// passing for the disabled case below.
	if _, ok := fe["disabled"]; ok {
		t.Errorf("/status carries %q for a far end that was never disabled, so the key stops "+
			"meaning the operator chose something", "disabled")
	}

	// The external + opt-out shape, on the wire, is the one a reader most needs to
	// read correctly: a mode that says who was supposed to answer and a flag that says
	// the operator gave that up.
	ext, _ := farEndFixture(t, FarEndExternal, func(rt *Runtime) { rt.FarEndDisabled = true })
	if err := ext.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap external: %v", err)
	}
	extFE := statusFarEndMap(t, ext)
	if got := extFE["mode"]; got != string(FarEndExternal) {
		t.Errorf("/status %q is %v for an external far end, want %q", "mode", got, FarEndExternal)
	}
	if got, ok := extFE["disabled"]; !ok || got != true {
		t.Errorf("/status %q is %v (present=%v) for a disabled external far end, want true",
			"disabled", got, ok)
	}
}

// brokerTLSConfig decides whether the far end verifies anything at all, so it is
// checked in both directions rather than only on the path a local run takes.
// 🔴 The scheme list must be PAHO'S, not a plausible subset. paho dials mqtts://
// over TLS and, handed a nil config, verifies against the system roots — so a short
// list leaves the operator's recorded "do not verify" unapplied and the connection
// fails with an x509 error contradicting both the record and dcctl's own message.
// The non-TLS cases are asserted too: a config where paho establishes no TLS would
// read as applied while doing nothing.
func TestBrokerTLSConfigFollowsTheScheme(t *testing.T) {
	for _, broker := range []string{"tcp://127.0.0.1:1883", "mqtt://host:1883", "ws://host:80", "unix://sock"} {
		if cfg := brokerTLSConfig(broker, true); cfg != nil {
			t.Errorf("%s got a TLS config; paho establishes no TLS there, so the insecure "+
				"flag reads as applied while doing nothing", broker)
		}
	}
	for _, broker := range []string{
		"ssl://host:1883", "tls://host:1883", "mqtts://host:1883",
		"mqtt+ssl://host:1883", "tcps://host:1883", "wss://host:443",
	} {
		cfg := brokerTLSConfig(broker, false)
		if cfg == nil {
			t.Fatalf("%s got no TLS config; paho verifies against system roots with none", broker)
		}
		if cfg.InsecureSkipVerify {
			t.Errorf("%s skips verification without being asked to", broker)
		}
		if cfg := brokerTLSConfig(broker, true); !cfg.InsecureSkipVerify {
			t.Errorf("%s verifies despite mqttTLSInsecure", broker)
		}
	}
}

// A far end over no devices attaches nothing and would still report attached:true.
// It is the visible shape of a mis-ordered attach, so it is refused outright.
func TestBootstrapRefusesAFarEndOverNoDevices(t *testing.T) {
	fe := &fakeFarEnd{}
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(_, _, _ string, _ *tls.Config) CommandFarEnd { return fe },
	}
	// A scenario that declares a far end and provisions nothing.
	lc := NewLifecycle(&farEndSim{mode: FarEndInternal}, rt)

	err := lc.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("a far end over zero devices was accepted; it subscribes nothing and reports attached")
	}
	if !strings.Contains(err.Error(), "no devices") {
		t.Errorf("error %q does not say what is missing", err)
	}
	if lc.commandFarEndStatus().Attached {
		t.Error("status reports a far end attached over zero devices")
	}
}

// Bootstrap is reachable concurrently — nothing serializes POST /reset — and the
// attach is the first step in it that is not idempotent by construction. Two
// concurrent attaches would each build a far end, and the loser's would be
// overwritten while still connected: never Closed, and fighting the winner for the
// same MQTT client ids through session takeover, forever.
func TestConcurrentBootstrapsAttachExactlyOneFarEnd(t *testing.T) {
	var built atomic.Int64
	fes := make(chan *fakeFarEnd, 8)
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(_, _, _ string, _ *tls.Config) CommandFarEnd {
			built.Add(1)
			// A slow build widens the check-then-act window that the serialization
			// closes; without it the race is real but rarely observed.
			time.Sleep(20 * time.Millisecond)
			fe := &fakeFarEnd{}
			fes <- fe
			return fe
		},
	}
	driver := &farEndSim{mode: FarEndInternal, devices: []DeviceInstance{
		{Token: "dev-001", CredentialId: "cred-001"},
	}}
	lc := NewLifecycle(driver, rt)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := lc.Bootstrap(context.Background()); err != nil {
				t.Errorf("concurrent bootstrap: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := built.Load(); n != 1 {
		t.Errorf("4 concurrent bootstraps built %d far ends, want 1 — the extras hold broker "+
			"connections nothing will ever Close, under client ids that evict each other", n)
	}
	close(fes)
	for fe := range fes {
		if fe.closeCount() != 0 {
			t.Error("a far end was closed during a successful bootstrap")
		}
	}
}
