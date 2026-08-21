// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/natsauth"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"
)

// These tests run against a REAL nats-server, configured the way the deployed one is:
// an APP account holding services and devices, a SYS system account, the MQTT gateway
// on. Nothing here asserts against a hand-built advisory.
//
// 🔑 THAT IS THE POINT, AND IT IS NOT THOROUGHNESS FOR ITS OWN SAKE. Every claim this
// package makes is a claim about what the BROKER does — which subject an advisory
// lands on, which JSON key carries the client id, whether one connection's start is
// the same value in an advisory and in a monitoring reply, whether a queue group
// distributes on a $SYS subject. A test against our own fixtures would confirm that
// our idea of the broker agrees with itself. The two-key divergence this package is
// built around was found this way and is invisible any other way.

const testInstance = "inst-1"

// broker starts an embedded server and returns it with its MQTT port.
func broker(t *testing.T) (*natsserver.Server, int) {
	t.Helper()
	mqttPort := dctest.FreeTCPPort(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "presence-test"
jetstream { store_dir: %q }
mqtt { listen: "127.0.0.1:%d" }
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: svc} ] }
  SYS: { users: [ {user: dc_sys, password: sys} ] }
}
system_account: SYS
`, dir, mqttPort)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return srv, mqttPort
}

// sysConn dials the system account the way the service does.
func sysConn(t *testing.T, srv *natsserver.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo("dc_sys", "sys"))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// connectDevice opens an MQTT connection under clientID and returns it.
func connectDevice(t *testing.T, mqttPort int, clientID string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort))
	opts.SetClientID(clientID)
	opts.SetUsername("dc_service")
	opts.SetPassword("svc")
	opts.SetAutoReconnect(false)
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(20*time.Second), "mqtt connect timed out for %q", clientID)
	require.NoError(t, tok.Error(), "mqtt connect refused for %q", clientID)
	return cl
}

// recordingEmitter captures what the tap emitted.
type recordingEmitter struct {
	mu     sync.Mutex
	events []recorded
	ch     chan struct{}
}

type recorded struct {
	Tenant, Source, Device string
	Event                  adapter.PresenceEvent
	// Demotion is non-nil exactly when this emission was a custody release rather than a
	// connectivity claim. A demotion has no Connected field to inspect, so the only honest
	// way to record both on one timeline is to keep them in distinct fields.
	Demotion *adapter.DemotionEvent
}

func newRecordingEmitter() *recordingEmitter {
	return &recordingEmitter{ch: make(chan struct{}, 64)}
}

func (e *recordingEmitter) EmitPresence(_ context.Context, tenant, source, device string, ev adapter.PresenceEvent) error {
	e.mu.Lock()
	e.events = append(e.events, recorded{Tenant: tenant, Source: source, Device: device, Event: ev})
	e.mu.Unlock()
	select {
	case e.ch <- struct{}{}:
	default:
	}
	return nil
}

// EmitPresenceDemotion records a custody release on the SAME timeline as the presence
// events, so a test can assert what a source emitted in order rather than reconciling two
// independent logs. Demotion is nil on a presence record and non-nil on a demotion, which
// is what makes "this emission was not a connectivity claim" checkable.
func (e *recordingEmitter) EmitPresenceDemotion(_ context.Context, tenant, source, device string, ev adapter.DemotionEvent) error {
	e.mu.Lock()
	e.events = append(e.events, recorded{Tenant: tenant, Source: source, Device: device, Demotion: &ev})
	e.mu.Unlock()
	select {
	case e.ch <- struct{}{}:
	default:
	}
	return nil
}

func (e *recordingEmitter) all() []recorded {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recorded(nil), e.events...)
}

// await waits for a recorded event satisfying match, failing the test on timeout.
func (e *recordingEmitter) await(t *testing.T, what string, match func(recorded) bool) recorded {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		for _, r := range e.all() {
			if match(r) {
				return r
			}
		}
		select {
		case <-e.ch:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no %s within the deadline; recorded: %+v", what, e.all())
		}
	}
}

// refute asserts that nothing matching appears within a settling window. Used for the
// negative halves, where the claim is that something must NOT be emitted.
func (e *recordingEmitter) refute(t *testing.T, what string, match func(recorded) bool) {
	t.Helper()
	time.Sleep(2 * time.Second)
	for _, r := range e.all() {
		if match(r) {
			t.Fatalf("emitted %s, which must not happen: %+v", what, r)
		}
	}
}

// tapFor wires a tap over a real SYS connection with a permissive gate.
func tapFor(t *testing.T, srv *natsserver.Server, e *recordingEmitter) (*Tap, *nats.Conn) {
	t.Helper()
	nc := sysConn(t, srv)
	tap := NewTap(testInstance, "mqtt1", e, func(string, string, time.Time, bool) bool { return true }, Metrics{})
	stop, err := tap.Subscribe(nc)
	require.NoError(t, err)
	t.Cleanup(stop)
	return tap, nc
}

func isDevice(tenant, device string, connected bool) func(recorded) bool {
	return func(r recorded) bool {
		return r.Tenant == tenant && r.Device == device && r.Event.Connected == connected
	}
}

// A real device connecting over MQTT becomes an asserted CONNECTED transition.
//
// This is the claim the whole feature rests on, and it is driven end to end: a paho
// client connects to the broker's own gateway, the broker publishes into the system
// account, and the tap's real subscription decodes it.
func TestARealDeviceConnectAssertsPresence(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	tapFor(t, srv, emitter)

	before := time.Now()
	cl := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	t.Cleanup(func() { cl.Disconnect(50) })

	got := emitter.await(t, "a CONNECTED transition for acme/sensor-001", isDevice("acme", "sensor-001", true))
	if got.Source != "mqtt1" {
		t.Errorf("source = %q, want the gateway source id", got.Source)
	}
	if got.Event.SessionId == 0 {
		t.Error("no session id was derived from the connection's start")
	}
	// The session must be the connection's start, so it lands in the window around the
	// connect rather than being some other clock's value.
	at := time.Unix(0, int64(got.Event.SessionId))
	if at.Before(before.Add(-time.Minute)) || at.After(time.Now().Add(time.Minute)) {
		t.Errorf("session id decodes to %v, which is not this connection's start", at)
	}
}

// The device's disconnect becomes a DISCONNECTED transition naming the SAME session,
// which is what presence.Decide requires to apply it at all.
func TestARealDeviceDisconnectClosesTheSameSession(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	tapFor(t, srv, emitter)

	cl := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	birth := emitter.await(t, "the connect", isDevice("acme", "sensor-001", true))
	cl.Disconnect(50)
	death := emitter.await(t, "the disconnect", isDevice("acme", "sensor-001", false))

	if death.Event.SessionId != birth.Event.SessionId {
		t.Errorf("death session %d != birth session %d; the projection would reject it as unrelated",
			death.Event.SessionId, birth.Event.SessionId)
	}
	if !death.Event.OccurredAt.After(birth.Event.OccurredAt) {
		t.Errorf("death at %v does not follow birth at %v", death.Event.OccurredAt, birth.Event.OccurredAt)
	}
}

// 🔴 THE SIDE-CONNECTION CASE, DRIVEN AGAINST THE REAL BROKER. A device holds a
// long-lived connection; a second client connects beside it with a discriminator and
// hangs up. The device must NOT be reported offline — its real session is up. This is
// the documented two-terminal workflow, so it is a routine occurrence, not an edge.
func TestASideConnectionCannotKillALiveDeviceOnARealBroker(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	tapFor(t, srv, emitter)

	held := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	t.Cleanup(func() { held.Disconnect(50) })
	emitter.await(t, "the device's own connect", isDevice("acme", "sensor-001", true))

	side := connectDevice(t, mqttPort, testInstance+":acme:sensor-001:pub")
	side.Disconnect(50)

	emitter.refute(t, "a disconnect for a device whose own session is still up",
		isDevice("acme", "sensor-001", false))
	// The counterweight: the device is still connected, so its own disconnect must
	// still work afterwards. Without this the test would pass against a tap that had
	// stopped emitting disconnects entirely.
	held.Disconnect(50)
	emitter.await(t, "the device's own disconnect", isDevice("acme", "sensor-001", false))
}

// Another instance's device on the shared broker must not be spoken for.
func TestAnotherInstancesDeviceIsNotAsserted(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	tapFor(t, srv, emitter)

	other := connectDevice(t, mqttPort, "inst-9:acme:sensor-001")
	t.Cleanup(func() { other.Disconnect(50) })
	emitter.refute(t, "presence for another instance's device", func(r recorded) bool {
		return r.Device == "sensor-001"
	})

	// The counterweight, in the same broker: OUR device is asserted. Without it this
	// passes against a tap that is subscribed to nothing.
	ours := connectDevice(t, mqttPort, testInstance+":acme:sensor-002")
	t.Cleanup(func() { ours.Disconnect(50) })
	emitter.await(t, "our own device's connect", isDevice("acme", "sensor-002", true))
}

// 🔴 THE INVENTORY PATH, WHICH READS A DIFFERENT JSON KEY THAN THE ADVISORY PATH. This
// test is why the two are separate types: it fails outright against a decoder that
// reuses the advisory's `client_id`, because a CONNZ reply spells it `mqtt_client`.
func TestTheInventoryFindsARealConnectedDevice(t *testing.T) {
	srv, mqttPort := broker(t)
	nc := sysConn(t, srv)

	cl := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	t.Cleanup(func() { cl.Disconnect(50) })
	side := connectDevice(t, mqttPort, testInstance+":acme:sensor-001:pub")
	t.Cleanup(func() { side.Disconnect(50) })
	other := connectDevice(t, mqttPort, "inst-9:acme:sensor-003")
	t.Cleanup(func() { other.Disconnect(50) })

	inv, err := FetchInventory(t.Context(), NewRequester(nc), testInstance, 2*time.Second)
	require.NoError(t, err)

	if !inv.Complete {
		t.Errorf("a single-server broker reported an incomplete inventory (%d/%d servers)", inv.Servers, inv.Expected)
	}
	live, ok := inv.Devices[DeviceKey("acme", "sensor-001")]
	if !ok {
		t.Fatalf("the connected device is missing from the inventory: %+v", inv.Devices)
	}
	if live.SessionId == 0 {
		t.Error("the inventory carries no session id, so a repair could not name the session")
	}
	// The same filters as the advisory path, or the two directions of reconciliation
	// would disagree about who exists.
	if len(inv.Devices) != 1 {
		t.Errorf("inventory = %+v, want only this instance's plain device sessions", inv.Devices)
	}
}

// 🔑 THE PROPERTY THAT MAKES A REPAIR THE SAME EVENT AS THE ADVISORY IT REPLACES: one
// connection's session id must be identical whether it is read from the advisory or
// from the inventory. If they differed, a synthetic connect would open a second session
// for a device that never reconnected.
func TestTheAdvisoryAndTheInventoryAgreeOnTheSession(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	_, nc := tapFor(t, srv, emitter)

	cl := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	t.Cleanup(func() { cl.Disconnect(50) })
	fromAdvisory := emitter.await(t, "the connect", isDevice("acme", "sensor-001", true))

	inv, err := FetchInventory(t.Context(), NewRequester(nc), testInstance, 2*time.Second)
	require.NoError(t, err)
	fromInventory, ok := inv.Devices[DeviceKey("acme", "sensor-001")]
	require.True(t, ok, "the device is missing from the inventory")

	if fromAdvisory.Event.SessionId != fromInventory.SessionId {
		t.Errorf("advisory session %d != inventory session %d for one connection",
			fromAdvisory.Event.SessionId, fromInventory.SessionId)
	}
}

// 🔴🔴 THE TEST THAT SHOULD HAVE EXISTED FIRST, AND WHOSE ABSENCE LET THE FEATURE SHIP
// DEAD. Every other broker test here configures the broker WITHOUT the auth callout,
// and that one omission made all of them agree with each other while the deployed
// configuration refused the tap outright.
//
// nats-server delegates a connection to the auth callout whenever the user is not
// listed in `auth_users`, and — this is the part that is easy to get wrong — the
// delegation covers EVERY account unless `allowed_accounts` narrows it, the system
// account included (`server/auth.go`: `delegated := len(opts.AuthCallout.AllowedAccounts) == 0`).
// So a system-account login absent from auth_users is handed to the DEVICE callout
// responder, which expects a `tenant:credentialId` username and denies it.
//
// The symptom in production is the worst kind: the broker is healthy, every device
// connects, and the tap alone is refused — after which it logs once and turns itself
// off. "MQTT presence stays inferred" is indistinguishable from the feature being
// disabled on purpose.
//
// This test carries the callout block and NO responder, which is exactly the
// discriminating condition: a user in auth_users bypasses the callout and its static
// password stands, so it connects; a user not in auth_users waits on a responder that
// does not exist, so it does not.
func TestTheSystemAccountLoginIsNotDelegatedToTheDeviceCallout(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "nats.conf")
	// A throwaway account nkey: the server validates the issuer's SHAPE at config load,
	// and the callout is never actually invoked for a user in auth_users — which is the
	// whole point being asserted.
	akp, err := nkeys.CreateAccount()
	require.NoError(t, err)
	issuer, err := akp.PublicKey()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "callout-test"
jetstream { store_dir: %q }
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: svc} ] }
  SYS: { users: [ {user: dc_sys, password: sys}, {user: dc_unlisted, password: unlisted} ] }
}
system_account: SYS
authorization {
  timeout: 5
  auth_callout {
    issuer: %q
    auth_users: [ dc_service, dc_sys ]
    account: "APP"
  }
}
`, dir, issuer)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "broker not ready")
	t.Cleanup(srv.Shutdown)

	// The claim: the tap's own login authenticates against a broker with the callout on.
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo("dc_sys", "sys"),
		nats.Timeout(5*time.Second))
	require.NoError(t, err, "the system-account login was refused by a broker running the auth callout; "+
		"it is missing from auth_users, so it is being handed to the device callout responder")
	t.Cleanup(nc.Close)

	// And it can actually do the thing it exists for, not merely connect.
	sub, err := nc.SubscribeSync(natsauth.SysConnectSubject)
	require.NoError(t, err)
	require.NoError(t, nc.Flush(), "the system-account subscription was refused")
	_ = sub.Unsubscribe()

	// 🔑 THE NEGATIVE CONTROL, and without it this test proves nothing: a login that is
	// NOT in auth_users must be refused on this same broker. If it connected, then
	// auth_users is not what admits dc_sys and the assertion above would hold even
	// against the broken configuration.
	//
	// 🔴 THE CONTROL'S FIXTURE HAS TO BE A USER THIS BROKER WOULD OTHERWISE ADMIT, and
	// getting that wrong is how this test spent its first life proving nothing. It used
	// `dc_other`, defined in no account at all — which ordinary authentication refuses
	// whether a callout is configured or not. Deleting the entire auth_callout block
	// then left the test GREEN: dc_sys connected on its own static password, dc_other
	// was refused as an unknown user, and both assertions read exactly as they do here.
	// Measured, not reasoned about — the mutation survived.
	//
	// dc_unlisted is defined in SYS with a valid password, so this broker admits it on
	// the static credential alone. The ONLY thing that can refuse it is the callout
	// taking it over — which is the delegation this test exists to pin, and which the
	// account block above deliberately places in SYS, since "delegation reaches the
	// system account too" is the specific surprise that shipped the feature dead.
	other, err := nats.Connect(srv.ClientURL(), nats.UserInfo("dc_unlisted", "unlisted"),
		nats.Timeout(3*time.Second))
	if err == nil {
		other.Close()
		t.Fatal("a login absent from auth_users connected on its own static password, so this broker " +
			"is not delegating to the callout at all and the assertion above is vacuous")
	}
}
