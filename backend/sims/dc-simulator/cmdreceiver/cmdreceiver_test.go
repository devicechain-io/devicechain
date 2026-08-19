// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDevice registers a device state on the receiver without opening a
// connection, so the pure accounting (recordFrame/Report) is exercisable with no
// broker. The paho wiring (connect/subscribe/respond) is proven live.
func (r *Receiver) newTestDevice(token string) *deviceState {
	ds := &deviceState{
		token:         token,
		commandTopic:  r.commandTopic(token),
		responseTopic: r.responseTopic(),
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
	assert.Equal(t, "inst-1/acme/command-responses", r.responseTopic())
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
