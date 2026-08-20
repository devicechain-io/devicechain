// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// newTestDevice registers a device state on the receiver without opening a
// connection, so the pure accounting (recordFrame/Report) is exercisable with no
// broker. The paho wiring (connect/subscribe/respond) is proven live.
func (r *Receiver) newTestDevice(token string) *deviceState {
	ds := &deviceState{
		token:         token,
		commandTopic:  r.commandTopic(token),
		responseTopic: r.responseTopic(token),
		ready:         make(chan error, 1),
		distinct:      make(map[string]int),
		subscribed:    true, // a live subscription is assumed for accounting tests
	}
	r.mu.Lock()
	r.devices[token] = ds
	r.mu.Unlock()
	return ds
}

func frame(t *testing.T, token, device, name string) []byte {
	t.Helper()
	raw, err := json.Marshal(deliveryEnvelope{Token: token, DeviceToken: device, Name: name})
	require.NoError(t, err)
	return raw
}

// The topics are the subject↔MQTT map (dots→slashes) the gateway expects: a wrong
// topic means the device subscribes to nothing and every command "drops".
func TestReceiverTopics(t *testing.T) {
	r := New("inst-1", "acme", "tcp://127.0.0.1:1883", nil)
	assert.Equal(t, "inst-1/acme/device-commands/sensor-1", r.commandTopic("sensor-1"))
	assert.Equal(t, "inst-1/acme/command-responses/sensor-1", r.responseTopic("sensor-1"))

	// 🔴 THE TWO TOPICS MUST BE DEVICE-SCOPED IN THE SAME WAY. The asymmetry between them
	// — commands confined to a device, responses tenant-wide — is what let one device
	// settle another's command, so a response topic that stopped naming the device would
	// reopen it while every delivery test kept passing.
	assert.NotEqual(t, r.responseTopic("sensor-1"), r.responseTopic("sensor-2"),
		"two devices must not share a response topic")
}

// A well-formed command is recorded once; a REDELIVERY of the same token bumps raw
// but NOT distinct — the at-least-once de-dup that keeps a redelivered command from
// looking like two commands.
func TestRecordFrameDedupsByToken(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	ds := r.newTestDevice("harness-cmd-probe-001")

	tok, ok := r.recordFrame(ds, frame(t, "cmd-A", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)
	assert.Equal(t, "cmd-A", tok)

	// Redelivery of cmd-A, then a genuinely new command cmd-B.
	_, ok = r.recordFrame(ds, frame(t, "cmd-A", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)
	_, ok = r.recordFrame(ds, frame(t, "cmd-B", "harness-cmd-probe-001", "harness-reset"))
	require.True(t, ok)

	assert.Equal(t, 2, r.Distinct("harness-cmd-probe-001"), "two DISTINCT command tokens")

	rep := r.Report()
	dr := rep.Devices["harness-cmd-probe-001"]
	assert.Equal(t, 3, dr.Raw, "three frames received (incl. the redelivery)")
	assert.Equal(t, 2, dr.Distinct, "two distinct commands")
	assert.Equal(t, 3, rep.TotalRaw)
	assert.Equal(t, 2, rep.TotalDistinct)
}

// A frame that does not decode as a command envelope is counted malformed and
// advances NEITHER the raw nor the distinct tally — miscounting it as a command
// would corrupt the redelivery measurement.
func TestRecordFrameMalformed(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	ds := r.newTestDevice("harness-cmd-probe-001")

	tok, ok := r.recordFrame(ds, []byte("{not json"))
	assert.False(t, ok)
	assert.Empty(t, tok)

	rep := r.Report()
	dr := rep.Devices["harness-cmd-probe-001"]
	assert.Equal(t, 0, dr.Raw)
	assert.Equal(t, 0, dr.Distinct)
	assert.Equal(t, 1, dr.Malformed)
}

// A decodable frame with an EMPTY token is malformed, not a command: answering it
// with CommandToken:"" would drive command-delivery's response consumer through a
// retry-to-poison on a row that never matches.
func TestRecordFrameEmptyTokenIsMalformed(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	ds := r.newTestDevice("harness-cmd-probe-001")

	tok, ok := r.recordFrame(ds, frame(t, "", "harness-cmd-probe-001", "harness-reset"))
	assert.False(t, ok)
	assert.Empty(t, tok)
	rep := r.Report()
	dr := rep.Devices["harness-cmd-probe-001"]
	assert.Equal(t, 0, dr.Raw)
	assert.Equal(t, 1, dr.Malformed)
}

// A device that never got a confirmed SUBACK is reported un-subscribed and listed
// Blind — its silence is never read as a clean "no command arrived" (the L2c
// fail-closed-about-own-blindness discipline). Distinct on an unknown device is 0.
func TestReceiverBlindDeviceSurfaced(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	// A device that connected but never acked its subscription: subscribed stays false.
	ds := r.newTestDevice("harness-cmd-probe-001")
	ds.subscribed = false

	rep := r.Report()
	assert.Equal(t, []string{"harness-cmd-probe-001"}, rep.Blind)
	assert.False(t, rep.Devices["harness-cmd-probe-001"].Subscribed)
	assert.Equal(t, 0, r.Distinct("unknown-device"), "an unknown device has received nothing")
}

// `responded` must mean "the broker ACKED this response", not "we tried".
//
// 🔴 A count that included attempts would report a healthy far end on a device whose
// every publish failed — precisely the reading these counters exist to make
// impossible. respond() itself needs a live broker connection, so while the decision
// was inlined among its publish steps only a comment pinned it: incrementing the
// counter BEFORE the ack check passed every test in this package, in sim, and in
// loadtest. recordResponse is the same pure heart recordFrame is, for the same reason.
func TestRespondedCountsOnlyAckedResponses(t *testing.T) {
	r := New("dc", "acme", "tcp://127.0.0.1:1883", nil)
	ds := &deviceState{token: "dev-1", distinct: map[string]int{}}

	r.recordResponse(ds, nil)
	r.recordResponse(ds, nil)
	r.recordResponse(ds, errors.New("response publish timed out for command \"cmd-3\""))
	r.recordResponse(ds, errors.New("second failure, which must not replace the first"))

	ds.mu.Lock()
	defer ds.mu.Unlock()
	if ds.responded != 2 {
		t.Errorf("responded is %d after 2 acks and 2 failures, want 2", ds.responded)
	}
	if ds.respondErr == nil {
		t.Fatal("a failed response publish recorded no error, so a device that never answered " +
			"anything reports clean")
	}
	// The FIRST failure is kept: it is the one closest to the cause, and a later
	// cascade would otherwise overwrite it.
	if !strings.Contains(ds.respondErr.Error(), "cmd-3") {
		t.Errorf("respondErr is %q, want the first failure", ds.respondErr)
	}
}

// --- deliberate departure (Disconnect) ------------------------------------------

// An unknown token is an error, never a silent no-op. The presence oracle asks for a
// departure and then WAITS to observe it, so a token typo has to fail here — if it
// does not, it resurfaces much later as "the departed device never went offline",
// which reads as a platform defect rather than a harness mistake.
func TestDisconnectUnknownDeviceIsAnError(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	r.newTestDevice("present-1")

	err := r.Disconnect("never-registered")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never-registered")
}

// Disconnect marks the departure and is idempotent — a second call is the same
// departure, not a new one and not a failure.
func TestDisconnectMarksDepartureAndIsIdempotent(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	ds := r.newTestDevice("leaving-1")

	require.NoError(t, r.Disconnect("leaving-1"))
	ds.mu.Lock()
	assert.True(t, ds.disconnected)
	ds.mu.Unlock()

	require.NoError(t, r.Disconnect("leaving-1"))
}

// A deliberate departure must NOT be reported as blind. Blind means "never attached,
// so its silence proves nothing" — a device that attached and was then disconnected
// on purpose is the opposite of that, and conflating the two would make every
// presence run report its own departed cohort as a receiver failure.
func TestDisconnectedDeviceIsNotReportedBlind(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	r.newTestDevice("leaving-1")
	r.newTestDevice("staying-1")
	require.NoError(t, r.Disconnect("leaving-1"))

	rep := r.Report()
	assert.Empty(t, rep.Blind)
	assert.True(t, rep.Devices["leaving-1"].Disconnected)
	assert.False(t, rep.Devices["staying-1"].Disconnected)
}

// Two live connections cannot share one client id, so claiming a token that is
// already connected is refused rather than silently overwriting it — the broker
// would resolve the collision by kicking a session, and the loser stops receiving
// with no error raised anywhere.
func TestClaimRefusesASecondLiveSession(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	first := r.newTestDevice("probe-1")

	err := r.claimDevice("probe-1", &deviceState{token: "probe-1", distinct: map[string]int{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already connected")

	// The refusal left the LIVE connection in place; it did not half-replace it.
	r.mu.Lock()
	assert.Same(t, first, r.devices["probe-1"])
	r.mu.Unlock()
}

// After a deliberate departure the same token may reconnect, and the reconnect count
// carries FORWARD across the replacement — otherwise each re-attach would erase the
// evidence of the previous one and a churning device would look freshly connected
// every time.
func TestClaimAfterDisconnectCountsTheReconnect(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	r.newTestDevice("churn-1")

	for want := 1; want <= 3; want++ {
		require.NoError(t, r.Disconnect("churn-1"))
		next := &deviceState{token: "churn-1", distinct: map[string]int{}, subscribed: true}
		require.NoError(t, r.claimDevice("churn-1", next))
		assert.Equal(t, want, next.reconnects)
		assert.Equal(t, want, r.Report().Devices["churn-1"].Reconnects)
	}
}

// --- the live-client half of Disconnect -----------------------------------------
//
// The tests above register devices with no client, so they cover the ACCOUNTING and
// leave the disconnect itself unmeasured. These drive a fake mqtt.Client so the part
// that actually ends the session is exercised too — without it, a Disconnect that
// issued no DISCONNECT at all would pass every test in this file.

// withFakeClient registers a device whose connection is a fake, and returns both.
func (r *Receiver) withFakeClient(token string) (*deviceState, *fakeClient) {
	ds := r.newTestDevice(token)
	fc := newFakeClient()
	ds.client = fc
	return ds, fc
}

// The DISCONNECT is really issued, once, with the configured quiesce.
func TestDisconnectIssuesTheDisconnect(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	_, fc := r.withFakeClient("leaving-1")

	require.NoError(t, r.Disconnect("leaving-1"))

	n, quiesce := fc.calls()
	assert.Equal(t, 1, n)
	assert.Equal(t, uint(disconnectQuiesceMS), quiesce)
	assert.False(t, fc.IsConnected())
}

// A second departure does not re-issue a DISCONNECT — the idempotency the accounting
// test could not see, because a nil client short-circuits before this point.
func TestDisconnectDoesNotReIssueOnASecondCall(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	_, fc := r.withFakeClient("leaving-1")

	require.NoError(t, r.Disconnect("leaving-1"))
	require.NoError(t, r.Disconnect("leaving-1"))

	n, _ := fc.calls()
	assert.Equal(t, 1, n)
}

// A client whose teardown never completes is reported as an error rather than
// waited on forever — and the error names the device, since the caller is about to
// wait on an observable that will now never arrive.
func TestDisconnectFailsWhenTheClientNeverCloses(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	r.disconnectWait = 60 * time.Millisecond
	r.disconnectPoll = 5 * time.Millisecond
	_, fc := r.withFakeClient("stuck-1")
	fc.staysConnected = true

	err := r.Disconnect("stuck-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stuck-1")
	assert.Contains(t, err.Error(), "still reports itself connected")
}

// The wait is a WAIT: a client that closes only after a few polls is tolerated, not
// failed. Without this, shrinking the loop to a single look would pass everything.
func TestDisconnectWaitsForALateClose(t *testing.T) {
	r := New("inst-1", "acme", "tcp://x:1883", nil)
	r.disconnectWait = 2 * time.Second
	r.disconnectPoll = 5 * time.Millisecond
	_, fc := r.withFakeClient("slow-1")
	fc.staysConnected = true

	go func() {
		time.Sleep(40 * time.Millisecond)
		fc.mu.Lock()
		fc.staysConnected = false
		fc.connected = false
		fc.mu.Unlock()
	}()

	require.NoError(t, r.Disconnect("slow-1"))
}

// --- the misrouted frame ------------------------------------------------------

// 🔴 A COMMAND RESPONSE CARRIES NO DEVICE IDENTITY. A device's JWT scopes SUBSCRIBE
// to its own command topic but grants PUBLISH on the tenant-wide responses subject,
// and command-delivery marks whatever command token it is handed. So a receiver that
// answers a frame addressed to somebody else closes another device's command — and
// nothing downstream can tell that the wrong device answered.
//
// That is precisely the fan-out mis-route a fleet-write oracle exists to catch: if
// device A's envelope lands on device B's topic, B answering it drives A's row to
// SUCCESSFUL while A never actuates, and every durable-state check reads green.
func TestAFrameAddressedToAnotherDeviceIsNotAnswered(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	ds := &deviceState{token: "probe-1", distinct: map[string]int{}}

	payload := []byte(`{"token":"cmd-9","deviceToken":"probe-2","name":"reset"}`)
	token, ok := r.recordFrame(ds, payload)

	assert.False(t, ok, "a misrouted frame must not be answered")
	assert.Empty(t, token)
	assert.Equal(t, 1, ds.misrouted)
	assert.Equal(t, "probe-2", ds.firstMisroutedTo)
	assert.Equal(t, 0, ds.raw, "a misrouted frame is not this device's traffic")
	assert.Empty(t, ds.distinct)
	assert.Equal(t, 0, ds.malformed, "misrouted and malformed are opposite diagnoses")
}

func TestAFrameAddressedToThisDeviceIsAnswered(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	ds := &deviceState{token: "probe-1", distinct: map[string]int{}}

	token, ok := r.recordFrame(ds, []byte(`{"token":"cmd-9","deviceToken":"probe-1","name":"reset"}`))
	assert.True(t, ok)
	assert.Equal(t, "cmd-9", token)
	assert.Equal(t, 1, ds.raw)
	assert.Equal(t, 0, ds.misrouted)
}

// An ABSENT addressee is not a WRONG one. The dispatcher always sets deviceToken, but
// refusing on absence would make this receiver unusable against any transport that
// omits it — and would convert a missing field into silent non-delivery.
func TestAFrameWithNoAddresseeIsStillAnswered(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	ds := &deviceState{token: "probe-1", distinct: map[string]int{}}

	token, ok := r.recordFrame(ds, []byte(`{"token":"cmd-9","name":"reset"}`))
	assert.True(t, ok)
	assert.Equal(t, "cmd-9", token)
	assert.Equal(t, 0, ds.misrouted)
}

func TestMisroutedFramesSurfaceInTheReport(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	ds := &deviceState{token: "probe-1", subscribed: true, distinct: map[string]int{}}
	require.NoError(t, r.claimDevice("probe-1", ds))

	_, _ = r.recordFrame(ds, []byte(`{"token":"cmd-1","deviceToken":"probe-2"}`))
	_, _ = r.recordFrame(ds, []byte(`{"token":"cmd-2","deviceToken":"probe-3"}`))

	rep := r.Report()
	assert.Equal(t, 2, rep.TotalMisrouted)
	assert.Equal(t, 2, rep.Devices["probe-1"].Misrouted)
	assert.Equal(t, "probe-2", rep.Devices["probe-1"].MisroutedTo, "the FIRST addressee is kept")
}

// --- the claim is released when the attach fails ------------------------------

// 🔴 The claim is taken before Connect, so a failed attach must give it back. A claim
// left behind turns the NEXT Subscribe into "device is already connected" — false,
// and a diagnosis pointing at a session collision that never happened. The presence
// harness reconnects its churn cohort R times per run, so one transient failure would
// abort the whole run naming the wrong cause.
func TestAFailedAttachDoesNotStrandTheClaim(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	first := &deviceState{token: "probe-1", distinct: map[string]int{}}
	require.NoError(t, r.claimDevice("probe-1", first))

	r.releaseDevice("probe-1", first)

	_, exists := r.devices["probe-1"]
	assert.False(t, exists, "a released claim leaves no entry behind")

	second := &deviceState{token: "probe-1", distinct: map[string]int{}}
	assert.NoError(t, r.claimDevice("probe-1", second), "a retry after a failed attach must be allowed")
}

// The loser of a race must not delete the winner's registration.
func TestReleaseOnlyRemovesItsOwnClaim(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	loser := &deviceState{token: "probe-1", distinct: map[string]int{}}
	winner := &deviceState{token: "probe-1", distinct: map[string]int{}}

	require.NoError(t, r.claimDevice("probe-1", loser))
	r.devices["probe-1"] = winner // the winner took the slot after the loser gave up

	r.releaseDevice("probe-1", loser)
	assert.Same(t, winner, r.devices["probe-1"], "the winner's registration survives the loser's cleanup")
}

// 🔴 THE ATTACH PATH, WHICH IS WHERE THE CLAIM ACCOUNTING LIVES. Calling releaseDevice
// directly proves the function works; it says nothing about whether Subscribe calls
// it. A mutant that stranded the claim on a failed connect survived every test until
// this one existed — and a stranded claim turns the NEXT Subscribe into "device is
// already connected", which is false and points at a session collision that never
// happened. The presence harness reconnects its churn cohort R times per run.
func TestSubscribeGivesTheClaimBackWhenTheConnectFails(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	fake := &fakeClient{connectResult: &fakeToken{completes: true, err: fmt.Errorf("connection refused")}}
	r.newClient = func(*mqtt.ClientOptions) mqtt.Client { return fake }

	err := r.Subscribe(context.Background(), "probe-1", "cred-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")

	r.mu.Lock()
	_, stranded := r.devices["probe-1"]
	r.mu.Unlock()
	assert.False(t, stranded, "a failed attach must leave no claim behind")

	// And the retry a real harness would make must be allowed.
	err2 := r.Subscribe(context.Background(), "probe-1", "cred-1")
	require.Error(t, err2)
	assert.NotContains(t, err2.Error(), "already connected",
		"the retry must fail on the CONNECT, not on a claim the first attempt stranded")
}

// A connect that never completes within the timeout is the same class: nothing is
// attached, so nothing may be claimed.
func TestSubscribeGivesTheClaimBackWhenTheConnectTimesOut(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	r.newClient = func(*mqtt.ClientOptions) mqtt.Client {
		return &fakeClient{connectResult: &fakeToken{completes: false}}
	}

	err := r.Subscribe(context.Background(), "probe-1", "cred-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	r.mu.Lock()
	_, stranded := r.devices["probe-1"]
	r.mu.Unlock()
	assert.False(t, stranded)
}
