// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The test rig: fakes for every collaborator of the dispatcher, all writing to ONE ordered event
// log, so a test can assert the ORDER of the claim, the record and the publish (which is the
// contract) and not only that each happened.

type evlog struct {
	mu sync.Mutex
	ev []string
}

func (l *evlog) add(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = append(l.ev, fmt.Sprintf(format, a...))
}

func (l *evlog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

// count returns how many events start with prefix.
func (l *evlog) count(prefix string) int {
	n := 0
	for _, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func (l *evlog) index(prefix string) int {
	for i, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

func (l *evlog) has(prefix string) bool { return l.index(prefix) >= 0 }

// ---- the source ----

type pubRec struct {
	kind   string
	target host.CommandTarget
	ref    host.MetricRef
	value  any
	wire   host.CommandWire
}

type fakeSource struct {
	mu     sync.Mutex
	log    *evlog
	tenant string
	cfg    config.SourceCommands

	index   map[string]host.CommandTarget
	metrics map[string]host.MetricRef // "<target>|<metric>"
	// notBorn makes every metric lookup answer ErrNotBorn (a table that is not there yet).
	notBorn bool
	breaker host.BreakerState
	up      bool

	publishErr error
	// moves is how many publishes find, after the record is written, that the node's table moved.
	moves int
	// afterHook, when set, runs after the before-wire hook succeeded and before the publish: where
	// a term ends or a session drops while the record is being written.
	afterHook func()
	pubs      []pubRec
	hooks     host.CommandHooks

	// now is the clock the publish is stamped with (time.Now when nil). atValue/lastAt say the node
	// already held a written value when the command was published.
	now     func() time.Time
	atValue bool
	lastAt  time.Time
	// lastVal is what LastReported answers while atValue holds; tableGone makes it answer false (the
	// node died or re-birthed, so the table the command was addressed against is gone).
	lastVal   host.WireValue
	tableGone bool
	// forceLast makes LastReported answer true whatever atValue says (the node reported the value
	// after the command was published, with a timestamp the match rule does not take).
	forceLast bool
	// bdSeq is the bdSeq the table was read under (5 by default).
	bdSeq uint64
}

func newSource(log *evlog, tenant string) *fakeSource {
	return &fakeSource{
		log: log, tenant: tenant,
		cfg: config.SourceCommands{
			Enabled: true, HostRole: config.HostRolePrimary, WritableMetrics: []string{"Setpoint", "Mode", "Big", "Blob", "Outputs/*"},
			AllowReboot: true, ConfirmWindowSeconds: 120, RebootWindowSeconds: 600, ClockSkewSeconds: 30,
			PublishPerSecond: 1000, Burst: 1000,
		},
		index: map[string]host.CommandTarget{}, metrics: map[string]host.MetricRef{}, up: true,
	}
}

var (
	nodeT = host.CommandTarget{Group: "plant-a", Node: "node-3"}
	devT  = host.CommandTarget{Group: "plant-a", Node: "node-3", Device: "pump-1"}
)

const (
	dtInt8    uint32 = 1
	dtBoolean uint32 = 11
	dtString  uint32 = 12
	dtDataSet uint32 = 16
)

// born puts token on the source's session with the node's and device's metrics declared.
func (f *fakeSource) born(token string, target host.CommandTarget) *fakeSource {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.index[token] = target
	return f
}

func (f *fakeSource) declare(target host.CommandTarget, name string, dt uint32) *fakeSource {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics[target.String()+"|"+name] = host.MetricRef{Target: target, Name: name, Datatype: dt, Epoch: 1, Gen: 1}
	return f
}

func (f *fakeSource) SourceID() string { return "sparkplug:h1" }
func (f *fakeSource) LastReported(host.MetricRef) (host.WireValue, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastVal, f.lastAt, (f.atValue || f.forceLast) && !f.tableGone
}
func (f *fakeSource) Tenant() string                  { return f.tenant }
func (f *fakeSource) Commands() config.SourceCommands { return f.cfg }
func (f *fakeSource) SetCommandHooks(h host.CommandHooks) {
	f.mu.Lock()
	f.hooks = h
	f.mu.Unlock()
}
func (f *fakeSource) Locate(token string) (host.CommandTarget, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.index[token]
	return t, ok
}
func (f *fakeSource) LookupMetric(target host.CommandTarget, name string) (host.MetricRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notBorn {
		return host.MetricRef{}, fmt.Errorf("%w: %s", host.ErrNotBorn, target)
	}
	ref, ok := f.metrics[target.String()+"|"+name]
	if !ok {
		return host.MetricRef{}, fmt.Errorf("%w: %q on %s", host.ErrMetricUnknown, name, target)
	}
	return ref, nil
}
func (f *fakeSource) BreakerState() host.BreakerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.breaker
}
func (f *fakeSource) SessionUp() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up
}

func (f *fakeSource) put(ctx context.Context, kind string, target host.CommandTarget, ref host.MetricRef, value any, before func(host.CommandWire) error) (host.CommandWire, error) {
	f.mu.Lock()
	up, br, perr, after := f.up, f.breaker, f.publishErr, f.afterHook
	f.mu.Unlock()
	if ctx.Err() != nil {
		return host.CommandWire{}, host.ErrTermEnded
	}
	if !up {
		return host.CommandWire{}, host.ErrSessionDown
	}
	if br == host.BreakerOpen {
		return host.CommandWire{}, host.ErrBreakerOpen
	}
	alias := uint64(7)
	at := time.Now()
	if f.now != nil {
		at = f.now()
	}
	bd := f.bdSeq
	if bd == 0 {
		bd = 5
	}
	wire := host.CommandWire{Topic: target.Topic(), Alias: &alias, Datatype: ref.Datatype, Value: value, SHA256: "abc", Size: 3,
		Epoch: 1, Gen: 1, BdSeq: bd, HasBdSeq: true, PublishedAt: at}
	if kind == KindWrite {
		wire.AtRequestedValue, wire.LastReportedAt = f.atValue, f.lastAt
	}
	if kind == KindRebirth {
		wire.Alias, wire.Name = nil, "Node Control/Rebirth"
	}
	if before != nil {
		if err := before(wire); err != nil {
			return wire, &host.BeforeWireError{Err: err}
		}
	}
	if after != nil {
		after()
	}
	var ind *host.PublishIndeterminateError
	if errors.As(perr, &ind) {
		return wire, perr // the publish was attempted, whatever became of the term meanwhile
	}
	if ctx.Err() != nil {
		return wire, host.ErrTermEnded
	}
	f.mu.Lock()
	up = f.up
	moved := f.moves > 0
	if moved {
		f.moves--
	}
	f.mu.Unlock()
	if !up {
		return wire, host.ErrSessionDown
	}
	if moved {
		return wire, host.ErrTableMoved
	}
	if perr != nil {
		return wire, perr
	}
	f.mu.Lock()
	f.pubs = append(f.pubs, pubRec{kind: kind, target: target, ref: ref, value: value, wire: wire})
	f.mu.Unlock()
	f.log.add("publish:%s:%s", kind, target)
	return wire, nil
}

func (f *fakeSource) PublishMetric(ctx context.Context, ref host.MetricRef, value any, before func(host.CommandWire) error) (host.CommandWire, error) {
	kind := KindWrite
	if ref.Name == nodeControlReboot {
		kind = KindReboot
	}
	return f.put(ctx, kind, ref.Target, ref, value, before)
}

func (f *fakeSource) PublishRebirth(ctx context.Context, target host.CommandTarget, before func(host.CommandWire) error) (host.CommandWire, error) {
	return f.put(ctx, KindRebirth, host.CommandTarget{Group: target.Group, Node: target.Node}, host.MetricRef{Datatype: dtBoolean}, true, before)
}

func (f *fakeSource) published() []pubRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pubRec(nil), f.pubs...)
}

// ---- the collaborators ----

type fakeClaimer struct {
	log *evlog
	mu  sync.Mutex
	// live claims
	liveNonce string
	liveWon   bool
	liveErr   error
	// drain claims
	drainNonce string
	drainWon   bool
	drainErr   error
	calls      atomic.Int32
	// onClaim runs when a drain claim succeeds (the backlog loses the row).
	onClaim func(token string)
}

func newClaimer(log *evlog) *fakeClaimer {
	return &fakeClaimer{log: log, liveNonce: "claim-nonce", liveWon: true, drainNonce: "drain-nonce", drainWon: true}
}

func (c *fakeClaimer) ClaimDispatch(_ context.Context, _, token, nonce string) (string, bool, error) {
	c.calls.Add(1)
	c.log.add("claim:%s:%s", token, nonce)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.liveNonce, c.liveWon, c.liveErr
}

func (c *fakeClaimer) Claim(_ context.Context, _, token string) (string, bool, error) {
	c.calls.Add(1)
	c.log.add("drainclaim:%s", token)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.drainWon && c.drainErr == nil && c.onClaim != nil {
		c.onClaim(token)
	}
	return c.drainNonce, c.drainWon, c.drainErr
}

type fakeParker struct {
	log    *evlog
	mu     sync.Mutex
	parked bool
	err    error
	// onPark runs when a park succeeds (the backlog gets the row back).
	onPark func(token string)
}

func (p *fakeParker) Park(_ context.Context, _, token, nonce string) (bool, error) {
	p.log.add("park:%s:%s", token, nonce)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil && p.onPark != nil {
		p.onPark(token)
	}
	return p.parked || p.err == nil, p.err
}

type response struct {
	device string
	env    cmddownlink.ResponseEnvelope
}

type fakeResponses struct {
	log *evlog
	mu  sync.Mutex
	got []response
	err error
	// delay slows every write, to show a caller waits for it.
	delay time.Duration
}

func (r *fakeResponses) WriteToDevice(_ context.Context, device string, msgs ...messaging.Message) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	for _, m := range msgs {
		var env cmddownlink.ResponseEnvelope
		if err := json.Unmarshal(m.Value, &env); err != nil {
			return err
		}
		r.got = append(r.got, response{device: device, env: env})
		errText := ""
		if env.Error != nil {
			errText = *env.Error
		}
		r.log.add("respond:%s:%s:%s:%s", env.CommandToken, env.Reach, env.DispatchNonce, errText)
	}
	return nil
}

func (r *fakeResponses) all() []response {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]response(nil), r.got...)
}

func (r *fakeResponses) of(token string) []response {
	var out []response
	for _, x := range r.all() {
		if x.env.CommandToken == token {
			out = append(out, x)
		}
	}
	return out
}

type fakeStore struct {
	log     *evlog
	mu      sync.Mutex
	entries map[string]Awaiting
	reboots map[rebootKey]time.Time
	putErr  error
	// putRebootErr and listErr fail the reboot mark's write and the term-start listing.
	putRebootErr error
	listErr      error
	// staleList, when set, is what List answers: a listing taken before later settlements.
	staleList *Stored
}

func (s *fakeStore) Put(_ context.Context, key string, a Awaiting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	if s.entries == nil {
		s.entries = map[string]Awaiting{}
	}
	s.entries[key] = a
	s.log.add("kvput:%s", key)
	return nil
}

func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	s.log.add("kvdel:%s", key)
	return nil
}

func (s *fakeStore) List(_ context.Context) (Stored, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return Stored{}, s.listErr
	}
	if s.staleList != nil {
		return *s.staleList, nil
	}
	out := Stored{Entries: map[string]Awaiting{}}
	for k, a := range s.entries {
		out.Entries[k] = a
	}
	for k, at := range s.reboots {
		out.Reboots = append(out.Reboots, RebootMark{Tenant: k.tenant, Group: k.group, Node: k.node, At: at})
	}
	return out, nil
}

type rebootKey struct{ tenant, group, node string }

func (s *fakeStore) PutReboot(_ context.Context, tenant, group, node string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putRebootErr != nil {
		return s.putRebootErr
	}
	if s.reboots == nil {
		s.reboots = map[rebootKey]time.Time{}
	}
	s.reboots[rebootKey{tenant, group, node}] = at
	s.log.add("kvreboot:%s/%s", group, node)
	return nil
}

func (s *fakeStore) DeleteReboot(_ context.Context, tenant, group, node string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reboots, rebootKey{tenant, group, node})
	s.log.add("kvunreboot:%s/%s", group, node)
	return nil
}

func (s *fakeStore) get(key string) (Awaiting, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.entries[key]
	return a, ok
}

func (s *fakeStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// fakeFetcher is command-delivery's backlog for one device: rows stay until a claim takes them and
// come back when one is parked, so a drain that claims a row it just handed back would find it again.
type fakeFetcher struct {
	log   *evlog
	mu    sync.Mutex
	rows  []cmddownlink.DrainCommand
	known map[string]cmddownlink.DrainCommand
	err   error
}

func (f *fakeFetcher) Pending(_ context.Context, _, device string, limit int) ([]cmddownlink.DrainCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("fetch:%s", device)
	if f.err != nil {
		return nil, f.err
	}
	if f.known == nil {
		f.known = map[string]cmddownlink.DrainCommand{}
	}
	n := min(len(f.rows), limit)
	out := append([]cmddownlink.DrainCommand(nil), f.rows[:n]...)
	for _, r := range out {
		f.known[r.Token] = r
	}
	return out, nil
}

// remove takes a claimed row out of the backlog.
func (f *fakeFetcher) remove(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rows {
		if r.Token == token {
			f.rows = append(f.rows[:i:i], f.rows[i+1:]...)
			return
		}
	}
}

// requeue puts a parked row back at the end of the backlog.
func (f *fakeFetcher) requeue(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.known[token]; ok {
		f.rows = append(f.rows, r)
	}
}

// ackSpy is a message's acknowledger: it records acks and in-progress marks.
type ackSpy struct {
	log        *evlog
	token      string
	acks       atomic.Int32
	inProgress atomic.Int32
}

func (a *ackSpy) Ack() error {
	a.acks.Add(1)
	a.log.add("ack:%s", a.token)
	return nil
}

func (a *ackSpy) InProgress() error {
	a.inProgress.Add(1)
	a.log.add("inprogress:%s", a.token)
	return nil
}

// ---- the rig ----

type rig struct {
	t         *testing.T
	log       *evlog
	src       *fakeSource
	claimer   *fakeClaimer
	parker    *fakeParker
	responses *fakeResponses
	store     *fakeStore
	fetcher   *fakeFetcher
	commands  *prometheus.CounterVec
	awaiting  prometheus.Gauge
	d         *Dispatcher
	clk       *testClock
}

// testClock is a clock a test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// useClock puts the rig, its source and its dispatcher on a clock the test moves.
func (r *rig) useClock() *testClock {
	r.clk = &testClock{t: time.Now()}
	r.src.now = r.clk.now
	r.build(nil)
	return r.clk
}

// newRig builds a dispatcher over one source for tenant "acme" with pump-1 born on it (Setpoint is
// an Int8, Mode a String, both on the device; the node declares Reboot).
func newRig(t *testing.T, opts ...func(*rig)) *rig {
	t.Helper()
	log := &evlog{}
	r := &rig{
		t: t, log: log, src: newSource(log, "acme"), claimer: newClaimer(log), parker: &fakeParker{log: log},
		responses: &fakeResponses{log: log}, store: &fakeStore{log: log}, fetcher: &fakeFetcher{log: log},
		commands: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "c", Help: "c"}, []string{"result"}),
		awaiting: prometheus.NewGauge(prometheus.GaugeOpts{Name: "a", Help: "a"}),
	}
	r.src.born("pump-1", devT).born("node-3", nodeT).
		declare(devT, "Setpoint", dtInt8).declare(devT, "Mode", dtString).declare(devT, "Big", dtInt8).
		declare(devT, "Blob", dtDataSet).declare(devT, "Outputs/Valve", dtBoolean).declare(devT, "Hidden", dtInt8).
		declare(nodeT, nodeControlReboot, dtBoolean)
	for _, o := range opts {
		o(r)
	}
	r.claimer.onClaim = r.fetcher.remove
	r.parker.onPark = r.fetcher.requeue
	r.build(nil)
	return r
}

func (r *rig) build(mutate func(*Options)) {
	opts := Options{Workers: 2, ProgressEvery: 10 * time.Millisecond}
	if r.clk != nil {
		opts.Now = r.clk.now
	}
	if mutate != nil {
		mutate(&opts)
	}
	r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting}, opts)
}

func (r *rig) result(res string) int {
	return int(testutil.ToFloat64(r.commands.WithLabelValues(res)))
}

// msg builds a live device-commands message.
func (r *rig) msg(tenant, device, token, name, payload string, headers map[string]string) (work, *ackSpy) {
	r.t.Helper()
	env := cmddownlink.DeliveryEnvelope{Token: token, DeviceToken: device, Name: name, DispatchNonce: "env-nonce"}
	if payload != "" {
		raw := json.RawMessage(payload)
		env.Payload = &raw
	}
	b, err := json.Marshal(env)
	require.NoError(r.t, err)
	spy := &ackSpy{log: r.log, token: token}
	if headers == nil {
		headers = map[string]string{cmddownlink.HeaderTransport: "sparkplug"}
	}
	m := messaging.NewConsumedMessage("inst."+tenant+".device-commands."+device, b, 1, headers, spy)
	return work{msg: m, tenant: tenant, env: env}, spy
}

func (r *rig) live(tenant, device, token, name, payload string, headers map[string]string) *ackSpy {
	r.t.Helper()
	w, spy := r.msg(tenant, device, token, name, payload, headers)
	r.d.live(context.Background(), w)
	return spy
}

func writeCmd(metric string, value any) string {
	b, _ := json.Marshal(map[string]any{"metric": metric, "value": value})
	return string(b)
}

func errorOf(res response) string {
	if res.env.Error == nil {
		return ""
	}
	return *res.env.Error
}

var errBoom = errors.New("boom")
