// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// infoReply scripts the device's answer to one GET.
type infoReply struct {
	code codes.Code
	body string
	cf   *message.MediaType // nil => no Content-Format option
	err  error
}

// infoConn is a device whose Object 3 resources are scripted by path. It records every request, so
// a test can say how many times the device was asked and in what order.
type infoConn struct {
	mux.Conn
	mu      sync.Mutex
	replies map[string]infoReply
	calls   []string
	posts   []string
	bodies  []string
	// delay is how long the device takes to answer each GET (bounded by the request's ctx).
	delay time.Duration
	// onGet, when set, runs as each GET arrives.
	onGet func(path string)
}

func (c *infoConn) Get(ctx context.Context, path string, _ ...message.Option) (*pool.Message, error) {
	c.mu.Lock()
	c.calls = append(c.calls, path)
	r, ok := c.replies[path]
	delay, onGet := c.delay, c.onGet
	c.mu.Unlock()
	if onGet != nil {
		onGet(path)
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !ok {
		r = infoReply{code: codes.NotFound}
	}
	if r.err != nil {
		return nil, r.err
	}
	m := pool.NewMessage(context.Background())
	m.SetCode(r.code)
	if r.cf != nil {
		m.SetContentFormat(*r.cf)
	}
	if r.body != "" {
		m.SetBody(bytes.NewReader([]byte(r.body)))
	}
	return m, nil
}

func (c *infoConn) Post(_ context.Context, path string, _ message.MediaType, body io.ReadSeeker, _ ...message.Option) (*pool.Message, error) {
	var b []byte
	if body != nil {
		b, _ = io.ReadAll(body)
	}
	c.mu.Lock()
	c.posts = append(c.posts, path)
	c.bodies = append(c.bodies, string(b))
	c.mu.Unlock()
	m := pool.NewMessage(context.Background())
	m.SetCode(codes.Changed)
	return m, nil
}

func (c *infoConn) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func textPlain() *message.MediaType { cf := message.TextPlain; return &cf }

func fullDevice() map[string]infoReply {
	return map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: "Acme", cf: textPlain()},
		"/3/0/1": {code: codes.Content, body: "AC-100", cf: textPlain()},
		"/3/0/2": {code: codes.Content, body: "SN-42", cf: textPlain()},
		"/3/0/3": {code: codes.Content, body: "1.2.3", cf: textPlain()},
		"/3/0/9": {code: codes.Content, body: "87", cf: textPlain()},
	}
}

func TestRebootIsABareExecuteOnObject3(t *testing.T) {
	conn := &infoConn{replies: fullDevice()}
	// The payload is not the operator's to steer: a path in it must change nothing.
	res := NewOps().Execute(context.Background(), conn, CommandReboot, []byte(`{"path":"/5/0/2"}`))
	require.True(t, res.Success)
	assert.Equal(t, labelReboot, res.Op)
	assert.Equal(t, []string{"/3/0/4"}, conn.posts)
	assert.Equal(t, []string{""}, conn.bodies, "the Execute carries no argument")
}

func TestRebootDeviceRefusalIsAFailure(t *testing.T) {
	conn := &opConn{code: codes.MethodNotAllowed}
	res := NewOps().Execute(context.Background(), conn, CommandReboot, nil)
	require.False(t, res.Success)
	assert.Equal(t, "/3/0/4", conn.path)
	assert.Contains(t, *res.Err, "device returned")
}

func TestReadDeviceInfoAnswersJSONAndRecordsMissing(t *testing.T) {
	replies := fullDevice()
	delete(replies, "/3/0/9") // a mains-powered device has no battery level
	replies["/3/0/2"] = infoReply{code: codes.Unauthorized}
	conn := &infoConn{replies: replies}

	res := NewOps().Execute(context.Background(), conn, CommandReadDeviceInfo, nil)
	require.True(t, res.Success)
	assert.Equal(t, labelDeviceInfo, res.Op)

	var got struct {
		Manufacturer    string        `json:"manufacturer"`
		ModelNumber     string        `json:"modelNumber"`
		SerialNumber    *string       `json:"serialNumber"`
		FirmwareVersion string        `json:"firmwareVersion"`
		BatteryLevel    *int          `json:"batteryLevel"`
		Missing         []InfoMissing `json:"missing"`
	}
	require.NoError(t, json.Unmarshal([]byte(*res.Payload), &got))
	assert.Equal(t, "Acme", got.Manufacturer)
	assert.Equal(t, "AC-100", got.ModelNumber)
	assert.Equal(t, "1.2.3", got.FirmwareVersion)
	assert.Nil(t, got.SerialNumber)
	assert.Nil(t, got.BatteryLevel)
	require.Len(t, got.Missing, 2)
	assert.Equal(t, "serialNumber", got.Missing[0].Resource)
	assert.Contains(t, got.Missing[0].Reason, "not readable")
	assert.Equal(t, "batteryLevel", got.Missing[1].Resource)
	assert.Contains(t, got.Missing[1].Reason, "4.04")
}

func TestReadDeviceInfoBatteryIsANumber(t *testing.T) {
	conn := &infoConn{replies: fullDevice()}
	res := NewOps().Execute(context.Background(), conn, CommandReadDeviceInfo, nil)
	require.True(t, res.Success)
	assert.Contains(t, *res.Payload, `"batteryLevel":87`)
	assert.Contains(t, *res.Payload, `"missing":[]`)
}

func TestReadDeviceInfoFailsWhenNothingIsReadable(t *testing.T) {
	conn := &infoConn{replies: map[string]infoReply{}} // every resource 4.04: no Device object
	res := NewOps().Execute(context.Background(), conn, CommandReadDeviceInfo, nil)
	require.False(t, res.Success)
	assert.Contains(t, *res.Err, "no Device-object resource could be read")
}

func TestReadDeviceInfoRejectsHostileValues(t *testing.T) {
	cases := map[string]infoReply{
		"/3/0/9": {code: codes.Content, body: "250", cf: textPlain()},                      // out of range
		"/3/0/0": {code: codes.Content, body: string([]byte{0xff, 0xfe}), cf: textPlain()}, // not UTF-8
		"/3/0/1": {code: codes.Content, body: "\x00\x07 ‮", cf: textPlain()},               // nothing printable
		"/3/0/2": {code: codes.Content, body: "AB\x00C\n", cf: textPlain()},                // controls stripped
		"/3/0/3": {code: codes.Content, body: "x", cf: func() *message.MediaType { c := message.AppOctets; return &c }()},
	}
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: cases}, time.Second)
	assert.Equal(t, "ABC", info.Values["serialNumber"])
	assert.NotContains(t, info.Values, "batteryLevel")
	assert.NotContains(t, info.Values, "manufacturer")
	assert.NotContains(t, info.Values, "modelNumber")
	assert.NotContains(t, info.Values, "firmwareVersion")
	assert.Len(t, info.Missing, 4)
}

func TestReadDeviceInfoCapsLongValues(t *testing.T) {
	long := string(bytes.Repeat([]byte("a"), 5000))
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: long, cf: textPlain()},
	}}, time.Second)
	assert.Len(t, info.Values["manufacturer"], maxInfoValueBytes)
}

// A device that stops answering is asked ONCE, not once per resource: the first transport failure
// ends the read and the rest are recorded as not attempted.
func TestReadDeviceInfoStopsAtFirstTransportFailure(t *testing.T) {
	conn := &infoConn{replies: map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: "Acme", cf: textPlain()},
		"/3/0/1": {err: context.DeadlineExceeded},
	}}
	info := ReadDeviceInfo(context.Background(), conn, time.Second)
	assert.True(t, info.Aborted)
	assert.Equal(t, []string{"/3/0/0", "/3/0/1"}, conn.asked(), "nothing after the failure is asked")
	assert.Equal(t, "Acme", info.Values["manufacturer"])
	require.Len(t, info.Missing, 4)
	assert.Contains(t, info.Missing[0].Reason, "timeout")
	assert.Contains(t, info.Missing[3].Reason, "not read")
}

// ── harvest ───────────────────────────────────────────────────────────────

// attrRecorder stands in for device-management's GraphQL endpoint.
type attrRecorder struct {
	mu   sync.Mutex
	sets []map[string]any
	fail bool
	// hang, when set, makes every write wait for its ctx (a device-management that never
	// answers); entered receives once per write that started.
	hang    bool
	entered chan struct{}
	ctxErrs []error
}

func (r *attrRecorder) Query(ctx context.Context, _, tenant, _ string, vars map[string]any, _ any) error {
	r.mu.Lock()
	hang, entered := r.hang, r.entered
	r.mu.Unlock()
	if hang {
		if entered != nil {
			entered <- struct{}{}
		}
		<-ctx.Done()
		// device-management takes a moment to give up, so a Run that does not wait for the
		// write is seen returning while it is still in flight.
		time.Sleep(50 * time.Millisecond)
		r.mu.Lock()
		r.ctxErrs = append(r.ctxErrs, ctx.Err())
		r.mu.Unlock()
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("device-management unreachable")
	}
	req := map[string]any{}
	for k, v := range vars["request"].(map[string]any) {
		req[k] = v
	}
	req["tenant"] = tenant
	r.sets = append(r.sets, req)
	return nil
}

func (r *attrRecorder) byKey() map[string]map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]map[string]any{}
	for _, s := range r.sets {
		out[s["attrKey"].(string)] = s
	}
	return out
}

func (r *attrRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sets)
}

func harvestMetricsForTest() HarvestMetrics {
	return HarvestMetrics{
		Runs:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "runs"}, []string{"outcome"}),
		Retries:     prometheus.NewCounter(prometheus.CounterOpts{Name: "retries"}),
		Skipped:     prometheus.NewCounter(prometheus.CounterOpts{Name: "skipped"}),
		WriteErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "write_errors"}),
		WriteShed:   prometheus.NewCounter(prometheus.CounterOpts{Name: "write_shed"}),
	}
}

type harvestRig struct {
	d    *Dispatcher
	h    *Harvester
	rec  *attrRecorder
	conn *infoConn
	clk  *fakeClock
	exec *orderExecutor
	rdr  *chanReader
	st   *fakeStore
	m    HarvestMetrics
	// gone is the tenant-deleted answer the dispatcher sees.
	gone atomic.Bool
	// disabled is the "has an operator disabled this device" answer the harvester sees; disabledErr,
	// when set, makes the question itself fail; asked counts the questions.
	disabled    atomic.Bool
	disabledErr atomic.Bool
	asked       atomic.Int32
	// disableAfter, when positive, flips disabled on once that many questions have been asked, so a
	// device can be disabled between the harvest's start and its writes.
	disableAfter atomic.Int32
	// stop ends the term (cancels Run's ctx) and waits for Run to return.
	stop func()
}

func newHarvestRig(t *testing.T, replies map[string]infoReply, tweak ...func(*Options)) *harvestRig {
	t.Helper()
	g := &harvestRig{
		rec:  &attrRecorder{},
		conn: &infoConn{replies: replies},
		clk:  newFakeClock(),
		exec: &orderExecutor{},
		rdr:  &chanReader{ch: make(chan messaging.Message, 16)},
		st:   newFakeStore(),
		m:    harvestMetricsForTest(),
	}
	g.h = NewHarvester(g.rec, "http://dm/graphql", g.m, func(context.Context, string, string) (bool, error) {
		n := g.asked.Add(1)
		if after := g.disableAfter.Load(); after > 0 && n >= after {
			g.disabled.Store(true)
		}
		if g.disabledErr.Load() {
			return false, errors.New("device-management unreachable")
		}
		return g.disabled.Load(), nil
	})
	g.h.now = g.clk.Now
	look := &fakeLookup{conn: g.conn, reaches: map[string]Reach{"acme/dev-1": ReachLive}}
	opts := Options{
		Workers: 1, Parker: g.st, Harvester: g.h,
		ReadPacer:     core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock()),
		TenantDeleted: func(string) bool { return g.gone.Load() },
	}
	for _, f := range tweak {
		f(&opts)
	}
	g.d = NewDispatcher(g.rdr, &fakePublisher{}, look, g.exec, g.st, g.st, gateMetrics(), opts)
	g.d.clock = g.clk
	g.stop = runDispatcher(t, g.d)
	t.Cleanup(g.stop)
	return g
}

func (g *harvestRig) waitWrites(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { g.h.WaitForWrites(); return g.rec.count() >= n }, 3*time.Second, 5*time.Millisecond,
		"attribute writes: have %d, want %d", g.rec.count(), n)
}

func TestHarvestWritesClientAttributesForANewlyLiveDevice(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 6)

	got := g.rec.byKey()
	for key, want := range map[string]struct{ typ, val string }{
		"manufacturer":    {"STRING", "Acme"},
		"modelNumber":     {"STRING", "AC-100"},
		"serialNumber":    {"STRING", "SN-42"},
		"firmwareVersion": {"STRING", "1.2.3"},
		"batteryLevel":    {"LONG", "87"},
		missingAttrKey:    {"JSON", "[]"},
	} {
		s, ok := got[key]
		require.True(t, ok, "attribute %s written", key)
		assert.Equal(t, "CLIENT", s["scope"], key)
		assert.Equal(t, "device", s["entityType"], key)
		assert.Equal(t, "dev-1", s["entity"], key)
		assert.Equal(t, "acme", s["tenant"], key)
		assert.Equal(t, want.typ, s["valueType"], key)
		assert.Equal(t, want.val, s["value"], key)
	}
	assert.Equal(t, []string{"/3/0/0", "/3/0/1", "/3/0/2", "/3/0/3", "/3/0/9"}, g.conn.asked())
}

func TestHarvestRecordsMissingResourcesInAnAttribute(t *testing.T) {
	replies := fullDevice()
	delete(replies, "/3/0/2")
	delete(replies, "/3/0/9")
	g := newHarvestRig(t, replies)
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 4)

	got := g.rec.byKey()
	assert.NotContains(t, got, "serialNumber")
	assert.NotContains(t, got, "batteryLevel")
	var missing []InfoMissing
	require.NoError(t, json.Unmarshal([]byte(got[missingAttrKey]["value"].(string)), &missing))
	require.Len(t, missing, 2)
	assert.Equal(t, "serialNumber", missing[0].Resource)
	assert.Equal(t, "batteryLevel", missing[1].Resource)
}

// A device that wakes every few minutes must not be re-read, and what it already gave must not be
// rewritten, however often it becomes live.
func TestHarvestIsThrottledAndUnchangedValuesAreNotRewritten(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 6)

	g.d.Harvest("acme", "dev-1") // within the interval
	never(t, func() bool { return len(g.conn.asked()) > 5 }, "a throttled device was read again")

	g.clk.Advance(harvestMinInterval + time.Second)
	g.d.Harvest("acme", "dev-1") // due again, values identical
	require.Eventually(t, func() bool { return len(g.conn.asked()) == 10 }, 3*time.Second, 5*time.Millisecond)
	g.h.WaitForWrites()
	assert.Equal(t, 6, g.rec.count(), "unchanged values are not rewritten")

	g.conn.mu.Lock()
	g.conn.replies["/3/0/3"] = infoReply{code: codes.Content, body: "2.0.0", cf: textPlain()}
	g.conn.mu.Unlock()
	g.clk.Advance(harvestMinInterval + time.Second)
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 7)
	assert.Equal(t, "2.0.0", g.rec.byKey()["firmwareVersion"]["value"], "a changed value is written")
	assert.Equal(t, 7, g.rec.count(), "only the changed attribute is written")
}

// A device that does not answer is retried exactly once, later, and a retry that also fails gives
// the reservation back so the next registration can try again.
func TestHarvestRetriesOnceThenReleasesTheDevice(t *testing.T) {
	g := newHarvestRig(t, map[string]infoReply{"/3/0/0": {err: context.DeadlineExceeded}})
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return len(g.clk.pending()) == 1 }, 3*time.Second, 5*time.Millisecond, "a retry is scheduled")
	assert.Equal(t, 1, len(g.conn.asked()), "the dead device is asked once, not per resource")

	g.clk.Advance(harvestRetryDelay)
	require.Eventually(t, func() bool { return len(g.conn.asked()) == 2 }, 3*time.Second, 5*time.Millisecond)
	never(t, func() bool { return len(g.clk.pending()) > 0 }, "a second retry was scheduled")

	// The failed harvest still leaves an honest record of what it could not read...
	g.waitWrites(t, 1)
	var missing []InfoMissing
	require.NoError(t, json.Unmarshal([]byte(g.rec.byKey()[missingAttrKey]["value"].(string)), &missing))
	assert.Len(t, missing, 5)
	// ...and does not lock the device out for an hour.
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return len(g.conn.asked()) == 3 }, 3*time.Second, 5*time.Millisecond)
}

// A harvest never runs while the device's worker is busy with a command: it is a task on the same
// serial worker, so the device is never asked two things at once.
func TestHarvestWaitsForTheDevicesCommandToFinish(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	started := make(chan string, 4)
	release := make(chan struct{})
	g.exec.mu.Lock()
	g.exec.onOp = func(token string) { started <- token; <-release }
	g.exec.mu.Unlock()

	nonce := g.st.add("c1", "dev-1", "SENT")
	g.rdr.ch <- cmdMsgNonce("acme", "dev-1", "c1", CommandRead, `{"path":"/c1"}`, nonce, newAck())
	recv(t, started, "the command to start")

	g.d.Harvest("acme", "dev-1")
	never(t, func() bool { return len(g.conn.asked()) > 0 }, "the harvest read the device while its command was in flight")

	close(release)
	require.Eventually(t, func() bool { return len(g.conn.asked()) == 5 }, 3*time.Second, 5*time.Millisecond)
}

// An attribute write that fails is neither remembered nor fatal: the next harvest sends it again.
func TestHarvestRetriesFailedWritesNextTime(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.rec.mu.Lock()
	g.rec.fail = true
	g.rec.mu.Unlock()
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { g.h.WaitForWrites(); return len(g.conn.asked()) == 5 }, 3*time.Second, 5*time.Millisecond)
	g.h.WaitForWrites()
	assert.Equal(t, 0, g.rec.count())

	g.rec.mu.Lock()
	g.rec.fail = false
	g.rec.mu.Unlock()
	g.clk.Advance(harvestMinInterval + time.Second)
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 6)
}

// A device that went away before its queued harvest ran is skipped, not read.
func TestHarvestSkipsADeviceThatIsNoLongerLive(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.d.Harvest("acme", "ghost") // not served: the lookup answers ReachNotServed
	never(t, func() bool { return len(g.conn.asked()) > 0 }, "an unreachable device was read")
}

// The byte cap must not cut a multi-byte character in half: the stored value stays valid UTF-8 and
// as long as whole characters allow, never longer than the cap.
func TestReadDeviceInfoCapDoesNotSplitACharacter(t *testing.T) {
	long := strings.Repeat("€", 200) // 3 bytes each: 600 bytes
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: long, cf: textPlain()},
	}}, time.Second)
	got := info.Values["manufacturer"]
	assert.True(t, utf8.ValidString(got), "the capped value is valid UTF-8")
	assert.Equal(t, strings.Repeat("€", maxInfoValueBytes/3), got, "as many whole characters as fit")
}

// Bidirectional overrides and isolates are format characters a device can use to make one value
// read as another in the console; they are stripped like control characters.
func TestReadDeviceInfoStripsBidiOverrides(t *testing.T) {
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: "Ac‮me⁦x⁩", cf: textPlain()},
	}}, time.Second)
	assert.Equal(t, "Acmex", info.Values["manufacturer"])
}

// COMMANDS FIRST: a device that still has a drain turn (or a live task, or a park in flight) waiting
// on its shard is not harvested ahead of it, while a device with no command work is.
func TestHarvestIsNotTakenAheadOfTheDevicesOwnCommandWork(t *testing.T) {
	s := newShardState()
	busy := deviceKey{"acme", "dev-busy"}
	idle := deviceKey{"acme", "dev-idle"}
	s.wake(busy) // gated, and wanting a drain turn
	require.True(t, s.addHarvest(harvestJob{tenant: busy.tenant, deviceToken: busy.deviceToken}))
	require.True(t, s.addHarvest(harvestJob{tenant: idle.tenant, deviceToken: idle.deviceToken}))

	j, ok := s.takeHarvest()
	require.True(t, ok)
	assert.Equal(t, idle.deviceToken, j.deviceToken, "the device with no command work goes first")
	_, ok = s.takeHarvest()
	assert.False(t, ok, "a device whose drain turn is still waiting is not harvested")

	for _, block := range []func(g *gate){
		func(g *gate) { g.wanted = false; g.queuedLive = 1 },
		func(g *gate) { g.queuedLive = 0; g.parksInFlight = 1 },
	} {
		s.mu.Lock()
		block(s.devs[busy])
		s.mu.Unlock()
		_, ok = s.takeHarvest()
		assert.False(t, ok, "command work is still pending for the device")
	}

	s.mu.Lock()
	s.devs[busy].parksInFlight = 0
	s.mu.Unlock()
	j, ok = s.takeHarvest()
	require.True(t, ok, "once its command work is done the device is harvested")
	assert.Equal(t, busy.deviceToken, j.deviceToken)
}

// The whole harvest is bounded by ONE op timeout, as the readDeviceInfo command is: a device that
// answers each read just inside the timeout cannot hold its shard's worker for five of them.
func TestHarvestIsBoundedByOneOpTimeoutOverall(t *testing.T) {
	const opTimeout = 200 * time.Millisecond
	g := newHarvestRig(t, fullDevice(), func(o *Options) { o.OpTimeout = opTimeout })
	g.conn.mu.Lock()
	g.conn.delay = 90 * time.Millisecond // each read inside opTimeout; all five would take 450ms
	g.conn.mu.Unlock()

	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 1)
	assert.Less(t, len(g.conn.asked()), 5, "the read stopped at the overall bound")
	got := g.rec.byKey()
	assert.Equal(t, "Acme", got["manufacturer"]["value"], "what was read in time is still recorded")
}

// When the term ends, the attribute writes stop and Run does not return until they have: a replica
// that has lost leadership is not left writing device attributes.
func TestHarvestWritesEndWithTheTerm(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	entered := make(chan struct{}, 8)
	g.rec.mu.Lock()
	g.rec.hang, g.rec.entered = true, entered
	g.rec.mu.Unlock()

	g.d.Harvest("acme", "dev-1")
	recv(t, entered, "the first attribute write to start")

	g.stop() // fails the test if Run does not return
	assert.Equal(t, int32(0), g.h.inflight.Load(), "Run returned only after the writes ended")
	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	require.Len(t, g.rec.ctxErrs, 1, "the write in flight was cancelled and no further write started")
	assert.ErrorIs(t, g.rec.ctxErrs[0], context.Canceled, "cancelled by the term, not by its own timeout")
}

// A tenant deleted while its device was being read gets none of the attributes written.
func TestHarvestWritesNothingForATenantDeletedDuringTheRead(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.conn.mu.Lock()
	g.conn.onGet = func(path string) {
		if path == "/3/0/9" { // the last read
			g.gone.Store(true)
		}
	}
	g.conn.mu.Unlock()

	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return len(g.conn.asked()) == 5 }, 3*time.Second, 5*time.Millisecond)
	never(t, func() bool { g.h.WaitForWrites(); return g.rec.count() > 0 }, "an attribute was written for a deleted tenant")
}

// A full attribute-write pool sheds the batch rather than making the shard's worker wait for a
// slot: the harvest finishes and the worker moves on.
func TestHarvestShedsWritesWhenThePoolIsFull(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	for range harvestWriters {
		g.h.sem <- struct{}{}
	}
	t.Cleanup(func() { // before the rig's stop (cleanups run last-first)
		for range harvestWriters {
			<-g.h.sem
		}
	})
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return testutil.ToFloat64(g.m.WriteShed) == 1 }, 3*time.Second, 5*time.Millisecond,
		"the batch was shed")
	assert.Equal(t, 0, g.rec.count())

	// The worker is free: a command for the device is dispatched.
	nonce := g.st.add("c1", "dev-1", "SENT")
	g.rdr.ch <- cmdMsgNonce("acme", "dev-1", "c1", CommandRead, `{"path":"/c1"}`, nonce, newAck())
	require.Eventually(t, func() bool { return len(g.exec.order()) == 1 }, 3*time.Second, 5*time.Millisecond,
		"the worker went on to the next command")
}

// A tenant already deleted when its queued harvest comes up is not read at all.
func TestHarvestDoesNotReadADeletedTenantsDevice(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.gone.Store(true)
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return testutil.ToFloat64(g.m.Skipped) == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Empty(t, g.conn.asked(), "a deleted tenant's device was read")
}

// On a standby (no term running) a live device is not harvested, and the request does not use up
// the device's throttle: the leader that does serve it must still harvest it.
func TestHarvestOnAStandbyReservesNothing(t *testing.T) {
	h := NewHarvester(&attrRecorder{}, "http://dm/graphql", HarvestMetrics{}, neverDisabled)
	d := NewDispatcher(&chanReader{ch: make(chan messaging.Message)}, &fakePublisher{}, &fakeLookup{}, &orderExecutor{},
		newFakeStore(), newFakeStore(), gateMetrics(), Options{Workers: 1, Harvester: h,
			ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock())})
	d.Harvest("acme", "dev-1")
	assert.True(t, h.due(deviceKey{"acme", "dev-1"}), "the standby's request reserved the device")
}

func neverDisabled(context.Context, string, string) (bool, error) { return false, nil }

// 🔴 A DISABLED DEVICE IS NEITHER READ NOR WRITTEN. The harvest is a read toward the device and
// a set of attribute writes about it; a quarantined device gets neither. The answer is asked of
// device-management at harvest time (the device may have been disabled after it registered), and
// enabling the device lets the next harvest run, because the refusal reserved nothing.
func TestHarvestSkipsADisabledDeviceAndReleasesItOnEnable(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.disabled.Store(true)
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return testutil.ToFloat64(g.m.Skipped) == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Empty(t, g.conn.asked(), "a disabled device was read")
	g.h.WaitForWrites()
	assert.Zero(t, g.rec.count(), "a disabled device had attributes written")

	g.disabled.Store(false)
	g.d.Harvest("acme", "dev-1") // no clock advance: the refusal must not have used the throttle
	g.waitWrites(t, 6)
}

// A device disabled WHILE it is being read (the read takes up to an op timeout) is not written.
func TestHarvestRechecksBeforeWritingWhatItRead(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.disableAfter.Store(2) // enabled when the harvest starts, disabled by the time it would write
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return g.asked.Load() >= 2 }, 3*time.Second, 5*time.Millisecond)
	g.h.WaitForWrites()
	assert.Len(t, g.conn.asked(), 5, "the device was read while still enabled")
	assert.Zero(t, g.rec.count(), "attributes were written for a device disabled mid-harvest")
}

// A disabled-state question that cannot be answered is not "enabled": no read, no write.
func TestHarvestFailsClosedWhenDisabledStateCannotBeRead(t *testing.T) {
	g := newHarvestRig(t, fullDevice())
	g.disabledErr.Store(true)
	g.d.Harvest("acme", "dev-1")
	require.Eventually(t, func() bool { return testutil.ToFloat64(g.m.Skipped) == 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Empty(t, g.conn.asked())
	assert.Zero(t, g.rec.count())
}
