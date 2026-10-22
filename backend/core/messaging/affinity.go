// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// Leader affinity: keep the broker from forwarding a client's traffic between its servers.
//
// A JetStream stream, and each consumer on it, is led by ONE server of the cluster. A client
// attached to another server pays a route hop for everything it does with it: a publish is
// forwarded to the stream leader and its PubAck forwarded back; a delivery is forwarded from
// the consumer leader and the ack forwarded to it. Measured on the GKE round-10 shape, three
// of the ~11.9 broker messages per event were such forwarded frames, and the broker's CPU is
// paid per frame, not per byte. Two halves remove them:
//
//   - CONSUMER LEADERS FOLLOW THEIR READERS. The leader that matters is the one on the
//     server the READER is connected to (not the stream leader's server, as round 10 first
//     read it: a durable led from its reader's server pays no hop whoever leads the stream).
//     The sampler reconciles it: after several samples with the leader elsewhere, on a
//     healthy broker, one replica asks the broker to step the consumer's leader down with
//     the readers' server as the preferred successor.
//   - WRITERS PUBLISH TO THEIR STREAM LEADER. Each pipelined writer publishes over a second
//     connection dialled to the server leading its stream, and moves to the new leader when
//     the broker elects one. It changes connection only with an empty window, so the order
//     publishes reach the stream in, and the order outcomes are reported in, is unchanged.
//
// 🔑 A ROUTE HOP IS A COST, NEVER A MEANING. Nothing here changes what is stored or acked, so
// every failure resolves to the state before this existed: a failed dial or a lost leader
// connection leaves the writer on the service's own connection, and a refused or unanswered
// step-down leaves the consumer where it was.
//
// 🔑 THE GAUGES ARE THE WATCHER AND ARE NEVER SWITCHED OFF. jetstream_consumer_leader_local
// and jetstream_publish_leader_local are exported whatever the settings say, so turning a
// half off shows what it was doing.

const (
	// affinityHysteresis is how many consecutive samples (30 s apart) a consumer's leader must
	// read away from its reader before the reconciler acts. One remote sample is what any
	// election produces for an instant; three is a placement, not a transient.
	affinityHysteresis = 3
	// affinityMoveInterval is the least time between two moves of one durable's leader by
	// this replica, whatever the samples say: the reconciler cannot flap faster than this.
	affinityMoveInterval = time.Minute
	// affinityQuietAfterReconnect is how long after this service's connection came back the
	// reconciler stays its hand. A reconnect is a broker incident seen from the client, and
	// elections are still settling for a while after one.
	affinityQuietAfterReconnect = 2 * time.Minute
	// affinityRequestTimeout bounds each broker request the affinity code makes (a stream
	// info, a step-down). A leader connection's dial is bounded by the liveness dialer.
	affinityRequestTimeout = 5 * time.Second
	// affinityCensusQuiet ends a census gather once no reply has arrived for this long, and
	// affinityCensusMax bounds it outright.
	affinityCensusQuiet = 150 * time.Millisecond
	affinityCensusMax   = time.Second
	// affinitySwapSettle bounds how long a writer's submitter waits for its window to settle
	// before changing connection: one publish ceiling and one failure backoff. A window that
	// does not settle in that time is in a failure episode, and the swap waits for a later
	// submission (affinitySwapRetry).
	affinitySwapSettle = publishWait + orderedBackoffMax
	affinitySwapRetry  = 10 * time.Second
	// affinityLogEvery bounds how often a repeated affinity condition (a leader that cannot
	// be dialled, readers split across servers) is logged per subject.
	affinityLogEvery = 10 * time.Minute

	// affinityCensusSegment is the control subject replicas of one area answer a census on;
	// see CensusSubject.
	affinityCensusSegment = ".affinity-census."

	// Results of jetstream_consumer_leader_moves_total.
	leaderMoveMoved  = "moved"
	leaderMoveFailed = "failed"
)

// pubTarget is the connection a writer on one stream should publish on: nil nc means the
// manager's own connection.
type pubTarget struct {
	nc     *nats.Conn
	server string
}

// durableAffinity is the reconciler's memory of one durable, touched only by the sampler.
type durableAffinity struct {
	remote   int
	lastMove time.Time
}

// affinity is one manager's leader-affinity state. Built on first use (NatsManager.aff), so a
// manager assembled as a struct literal in a test has one too.
type affinity struct {
	nmgr *NatsManager
	// id names this replica in a census.
	id string
	// main is the sentinel target for the manager's own connection. Writers compare targets
	// by pointer, so there is exactly one.
	main *pubTarget

	mu sync.Mutex
	// conns are the leader connections, by the name of the server each is attached to.
	conns map[string]*nats.Conn
	// urls remembers the URL each server was last reached at.
	urls map[string]string
	// targets is each written stream's current target, which its writers read lock-free.
	targets map[string]*atomic.Pointer[pubTarget]
	writers map[string][]*orderedWriter
	// subs are this manager's advisory and census subscriptions.
	subs []*nats.Subscription
	// dirty is the streams whose target is to be worked out again; poke wakes the loop.
	dirty map[string]bool
	poke  chan struct{}
	// logged is when each repeated condition was last logged (affinityLogEvery).
	logged map[string]time.Time

	// leaderURLs, when set, replaces the default candidate URLs for a server (tests).
	leaderURLs func(server string) []string

	// durables is the reconciler's memory; touched only by the sampler goroutine.
	durables map[durableRef]*durableAffinity
	// lastReconnect is when the manager's connection last came back (Unix nanos; 0 never).
	lastReconnect atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// aff returns this manager's affinity state, building it on first use.
func (nmgr *NatsManager) aff() *affinity {
	nmgr.affOnce.Do(func() {
		nmgr.affState = &affinity{
			nmgr:     nmgr,
			id:       uuid.NewString(),
			main:     &pubTarget{},
			conns:    map[string]*nats.Conn{},
			urls:     map[string]string{},
			targets:  map[string]*atomic.Pointer[pubTarget]{},
			writers:  map[string][]*orderedWriter{},
			dirty:    map[string]bool{},
			poke:     make(chan struct{}, 1),
			logged:   map[string]time.Time{},
			durables: map[durableRef]*durableAffinity{},
		}
	})
	return nmgr.affState
}

func (a *affinity) consumersOn() bool {
	return a.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Affinity.ConsumerLeadersEnabled()
}

func (a *affinity) publishOn() bool {
	return a.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Affinity.PublishToLeaderEnabled()
}

// ownServer is the name of the server the manager's connection is attached to, "" while it
// is not connected.
func (a *affinity) ownServer() string {
	if a.nmgr.nc == nil || !a.nmgr.nc.IsConnected() {
		return ""
	}
	return a.nmgr.nc.ConnectedServerName()
}

func (a *affinity) noteReconnect() { a.lastReconnect.Store(time.Now().UnixNano()) }

// logEvery reports whether the condition named key is due to be logged again.
func (a *affinity) logEvery(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.logged[key]; ok && time.Since(t) < affinityLogEvery {
		return false
	}
	a.logged[key] = time.Now()
	return true
}

// ---------------------------------------------------------------------------------------
// Writers follow the stream leader.
// ---------------------------------------------------------------------------------------

// registerWriter enrols w and returns the target its stream's writers read. The first writer
// on a stream subscribes to the stream's leader-elected advisory, so a new leader is followed
// in milliseconds rather than at the next sample. A failed subscription is logged and
// otherwise ignored: the sampler still finds the new leader, 30 s later.
func (a *affinity) registerWriter(w *orderedWriter) *atomic.Pointer[pubTarget] {
	a.mu.Lock()
	defer a.mu.Unlock()
	ptr, ok := a.targets[w.stream]
	if !ok {
		ptr = &atomic.Pointer[pubTarget]{}
		ptr.Store(a.main)
		a.targets[w.stream] = ptr
		if a.publishOn() && a.nmgr.nc != nil {
			stream := w.stream
			subject := "$JS.EVENT.ADVISORY.STREAM.LEADER_ELECTED." + stream
			sub, err := SubscribeSynced(a.nmgr.nc, subject, func(*nats.Msg) { a.markDirty(stream) })
			if err != nil {
				log.Warn().Err(err).Str("stream", stream).
					Msg("Could not watch the stream's leader elections; its writers follow a new leader at the next 30 s sample")
			} else {
				a.subs = append(a.subs, sub)
			}
		}
	}
	a.writers[w.stream] = append(a.writers[w.stream], w)
	if a.publishOn() {
		a.dirty[w.stream] = true
		a.wake()
	}
	return ptr
}

// markDirty asks the loop to work out stream's target again.
func (a *affinity) markDirty(stream string) {
	a.mu.Lock()
	a.dirty[stream] = true
	a.mu.Unlock()
	a.wake()
}

func (a *affinity) wake() {
	select {
	case a.poke <- struct{}{}:
	default:
	}
}

// start runs the loop that dials leader connections, and answers this replica's part of every
// census. Called from ExecuteStart, after oncreate has built the readers and writers.
func (a *affinity) start(ctx context.Context) {
	if a.cancel != nil {
		return
	}
	if a.nmgr.nc != nil && len(a.nmgr.trackedDurables()) > 0 {
		sub, err := SubscribeSynced(a.nmgr.nc, CensusSubject(a.nmgr.Microservice.InstanceId,
			a.nmgr.Microservice.FunctionalArea), a.answerCensus)
		if err != nil {
			// Without an answer from this replica a census never finds it, so no replica of
			// this area moves a leader while this one is up: the fail-safe direction.
			log.Warn().Err(err).Msg("Could not answer consumer-leader censuses; leaders of this area's " +
				"durables will not be moved while this replica runs")
		} else {
			a.mu.Lock()
			a.subs = append(a.subs, sub)
			a.mu.Unlock()
		}
	}
	lctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.wg.Add(1)
	go a.run(lctx)
}

// stop ends the loop (bounded by ctx) and releases the subscriptions. The leader connections
// are closed by closeConns, after the manager's own connection has drained.
func (a *affinity) stop(ctx context.Context) {
	if a.cancel == nil {
		return
	}
	a.cancel()
	joined := make(chan struct{})
	go func() { a.wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
	}
	a.cancel = nil
	a.mu.Lock()
	subs := a.subs
	a.subs = nil
	a.mu.Unlock()
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
}

// closeConns drains every leader connection, bounded by ctx. Called after the manager's own
// connection has drained, so whatever a writer still had in flight has had the same chance
// to be acknowledged as it would have had on that one.
func (a *affinity) closeConns(ctx context.Context) {
	a.mu.Lock()
	conns := make([]*nats.Conn, 0, len(a.conns))
	for _, nc := range a.conns {
		conns = append(conns, nc)
	}
	a.conns = map[string]*nats.Conn{}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, nc := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			drainLeaderConn(ctx, nc)
		}()
	}
	wg.Wait()
}

func drainLeaderConn(ctx context.Context, nc *nats.Conn) {
	if nc.IsClosed() {
		return
	}
	closed := nc.StatusChanged(nats.CLOSED)
	defer nc.RemoveStatusListener(closed)
	if err := nc.Drain(); err != nil {
		nc.Close()
		return
	}
	select {
	case <-closed:
	case <-ctx.Done():
		nc.Close()
	}
}

// run works out the target of every stream marked dirty, then closes leader connections no
// writer uses any more.
func (a *affinity) run(ctx context.Context) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.poke:
		}
		a.mu.Lock()
		streams := make([]string, 0, len(a.dirty))
		for s := range a.dirty {
			streams = append(streams, s)
		}
		a.dirty = map[string]bool{}
		a.mu.Unlock()
		slices.Sort(streams)
		for _, s := range streams {
			if ctx.Err() != nil {
				return
			}
			a.retarget(ctx, s)
		}
		a.closeIdle()
	}
}

// retarget points stream's writers at a connection to its current leader: the manager's own
// connection when that is where the leader is, or when anything goes wrong, and a dialled one
// otherwise. A stream with no leader (an election in progress) keeps its target: the next
// leader-elected advisory, or the next sample, comes back here.
func (a *affinity) retarget(ctx context.Context, stream string) {
	a.mu.Lock()
	ptr := a.targets[stream]
	a.mu.Unlock()
	if ptr == nil {
		return
	}
	if !a.publishOn() {
		ptr.Store(a.main)
		return
	}
	rctx, cancel := context.WithTimeout(ctx, affinityRequestTimeout)
	info, err := a.nmgr.js.StreamInfo(stream, nats.Context(rctx))
	cancel()
	if err != nil {
		return
	}
	leader := leaderOfStream(info)
	if leader == "" {
		return
	}
	if leader == a.ownServer() {
		ptr.Store(a.main)
		return
	}
	if cur := ptr.Load(); cur.server == leader && cur.nc != nil && !cur.nc.IsClosed() {
		return
	}
	nc, err := a.connTo(leader)
	if err != nil {
		ptr.Store(a.main)
		if a.logEvery("dial:" + leader) {
			log.Warn().Err(err).Str("stream", stream).Str("leader", leader).
				Msg("Could not connect to the server leading the stream; its writers publish through this " +
					"service's own connection, and every publish crosses a route between servers")
		}
		return
	}
	ptr.Store(&pubTarget{nc: nc, server: leader})
	log.Info().Str("stream", stream).Str("leader", leader).
		Msg("Writers on the stream now publish on a connection to its leader")
}

// connTo returns a connection attached to server, dialling one if none is open. A candidate
// URL is kept only when the server that answers it is the one asked for, so a load-balanced
// name that lands elsewhere is never mistaken for the leader.
func (a *affinity) connTo(server string) (*nats.Conn, error) {
	a.mu.Lock()
	if nc, ok := a.conns[server]; ok && !nc.IsClosed() {
		a.mu.Unlock()
		return nc, nil
	}
	known := a.urls[server]
	a.mu.Unlock()

	var candidates []string
	if a.leaderURLs != nil {
		candidates = a.leaderURLs(server)
	} else {
		candidates = a.candidateURLs(server, known)
	}
	var errs []error
	for _, url := range candidates {
		nc, err := a.dial(server, url)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", url, err))
			continue
		}
		if got := nc.ConnectedServerName(); got != server {
			nc.Close()
			errs = append(errs, fmt.Errorf("%s: answered by %q", url, got))
			continue
		}
		a.mu.Lock()
		a.conns[server] = nc
		a.urls[server] = url
		a.mu.Unlock()
		return nc, nil
	}
	if len(candidates) == 0 {
		return nil, errors.New("no address is known for the server")
	}
	return nil, errors.Join(errs...)
}

// candidateURLs is where server might be reached: where it was reached before, the URLs the
// cluster advertises to clients, and, for a broker that advertises none (the deployed chart
// sets no_advertise), the per-pod name its StatefulSet's headless Service gives it.
func (a *affinity) candidateURLs(server, known string) []string {
	var out []string
	add := func(u string) {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	add(known)
	if a.nmgr.nc != nil {
		for _, u := range a.nmgr.nc.DiscoveredServers() {
			add(u)
		}
	}
	cfg := a.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	add(headlessURL(server, cfg.Hostname, cfg.Port))
	return out
}

// headlessURL is the address a broker server named like a StatefulSet pod has under the
// headless Service beside the client Service: server "dc-nats-2" behind client host
// "dc-nats.dc-infra" is "dc-nats-2.dc-nats-headless.dc-infra". It returns "" for a host that
// is an address or a single label that the server name does not extend, where there is no
// such name to guess. The answer is only a candidate: connTo keeps it only if the server
// answering it is server.
func headlessURL(server, host string, port uint32) string {
	if server == "" || host == "" || net.ParseIP(host) != nil || host == "localhost" {
		return ""
	}
	svc, rest, _ := strings.Cut(host, ".")
	if !strings.HasPrefix(server, svc+"-") {
		return ""
	}
	name := server + "." + svc + "-headless"
	if rest != "" {
		name += "." + rest
	}
	return fmt.Sprintf("nats://%s:%d", name, port)
}

// dial opens a leader connection to url with the manager's credential and TLS. It never
// reconnects: a leader connection that drops closes, and its writers go back to the
// manager's own connection, which does reconnect, until the loop dials the new leader.
func (a *affinity) dial(server, url string) (*nats.Conn, error) {
	natscfg := a.nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	opts := []nats.Option{
		nats.Name(a.nmgr.Microservice.FunctionalArea),
		nats.NoReconnect(),
		nats.DontRandomize(),
		nats.CustomInboxPrefix(InboxPrefix(a.nmgr.Microservice.FunctionalArea)),
	}
	// 🔴 NO nats.Timeout HERE. BrokerLivenessOptions' dialer carries a fixed dial timeout and
	// its stall detector tells nats.go's whole-connect deadline from a traffic write deadline
	// by how far ahead each was armed; a caller-set connect timeout near half the write
	// timeout would be read as traffic (see brokerDialTimeout). The default 2 s connect bound
	// is the one the detector was written against.
	opts = append(opts, BrokerLivenessOptions(a.nmgr.Microservice.FunctionalArea+" (stream leader)", func() {})...)
	tlsConfig, err := natscfg.TLSConfig(natscfg.Hostname)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		opts = append(opts, nats.Secure(tlsConfig))
	}
	if natscfg.Auth.User != "" {
		opts = append(opts, nats.UserInfo(natscfg.Auth.User, natscfg.Auth.Password))
	}
	opts = append(opts, nats.ClosedHandler(func(nc *nats.Conn) { a.leaderClosed(server, nc) }))
	return nats.Connect(url, opts...)
}

// leaderClosed sends every stream whose target was nc back to the manager's own connection,
// at once and without waiting for the loop: a writer on a closed connection can only fail.
func (a *affinity) leaderClosed(server string, nc *nats.Conn) {
	a.mu.Lock()
	if a.conns[server] == nc {
		delete(a.conns, server)
	}
	for stream, ptr := range a.targets {
		if ptr.Load().nc == nc {
			ptr.Store(a.main)
			a.dirty[stream] = true
		}
	}
	a.mu.Unlock()
	a.wake()
}

// closeIdle closes the leader connections no stream targets and no writer still publishes on.
func (a *affinity) closeIdle() {
	a.mu.Lock()
	inUse := map[*nats.Conn]bool{}
	for _, ptr := range a.targets {
		if nc := ptr.Load().nc; nc != nil {
			inUse[nc] = true
		}
	}
	for _, ws := range a.writers {
		for _, w := range ws {
			if nc := w.conn.Load(); nc != nil {
				inUse[nc] = true
			}
		}
	}
	var idle []*nats.Conn
	for server, nc := range a.conns {
		if !inUse[nc] {
			idle = append(idle, nc)
			delete(a.conns, server)
		}
	}
	a.mu.Unlock()
	for _, nc := range idle {
		nc.Close()
	}
}

// leaderOfStream is the name of the server leading the stream, "" when none does.
func leaderOfStream(info *nats.StreamInfo) string {
	if info == nil || info.Cluster == nil {
		return ""
	}
	return info.Cluster.Leader
}

// ---------------------------------------------------------------------------------------
// The sampler's half: the gauges, and the consumer-leader reconciler.
// ---------------------------------------------------------------------------------------

// sampleSeen is what one sampler pass read from the broker, for the affinity code to use
// without asking again. Its methods are nil-safe so the sampler can be driven without it.
type sampleSeen struct {
	streams   map[string]*nats.StreamInfo
	consumers map[durableRef]*nats.ConsumerInfo
}

func newSampleSeen() *sampleSeen {
	return &sampleSeen{streams: map[string]*nats.StreamInfo{}, consumers: map[durableRef]*nats.ConsumerInfo{}}
}

func (s *sampleSeen) stream(name string, info *nats.StreamInfo) {
	if s != nil {
		s.streams[name] = info
	}
}

func (s *sampleSeen) consumer(d durableRef, ci *nats.ConsumerInfo) {
	if s != nil {
		s.consumers[d] = ci
	}
}

// afterSample sets both gauges from what the pass read, sends writers whose stream leader has
// moved to the loop, and runs the consumer-leader reconciler.
func (a *affinity) afterSample(ctx context.Context, seen *sampleSeen) {
	a.samplePublish(seen)
	own := a.ownServer()
	for _, d := range a.nmgr.trackedDurables() {
		ci, ok := seen.consumers[d]
		if !ok {
			a.nmgr.metrics.consumerLeaderLocal.DeleteLabelValues(d.stream, d.durable)
			continue
		}
		leader := consumerLeader(ci)
		local := leader != "" && leader == own
		a.nmgr.metrics.consumerLeaderLocal.WithLabelValues(d.stream, d.durable).Set(boolGauge(local))
		a.reconcile(ctx, d, seen.streams[d.stream], ci, local)
	}
}

// samplePublish sets jetstream_publish_leader_local for each written (suffix, writer) pair: 1
// only while every writer of that name on the suffix publishes on a connection attached to the
// stream's leader. Writers share a name (one external-mqtt writer per source), so the series
// folds them; it is labelled like the window metrics so a remote leader names its writer.
func (a *affinity) samplePublish(seen *sampleSeen) {
	a.mu.Lock()
	writers := make(map[string][]*orderedWriter, len(a.writers))
	for s, ws := range a.writers {
		writers[s] = slices.Clone(ws)
	}
	a.mu.Unlock()
	type series struct{ suffix, writer string }
	local := map[series]bool{}
	for stream, ws := range writers {
		info, ok := seen.streams[stream]
		if !ok {
			for _, w := range ws {
				a.nmgr.metrics.publishLeaderLocal.DeleteLabelValues(w.suffix, string(w.writer))
			}
			continue
		}
		leader := leaderOfStream(info)
		for _, w := range ws {
			nc := w.conn.Load()
			if nc == nil {
				nc = a.nmgr.nc
			}
			here := leader != "" && nc != nil && nc.IsConnected() && nc.ConnectedServerName() == leader
			k := series{w.suffix, string(w.writer)}
			if prev, seen := local[k]; !seen || prev {
				local[k] = here
			}
			if !here && a.publishOn() {
				a.markDirty(stream)
			}
		}
	}
	for k, here := range local {
		a.nmgr.metrics.publishLeaderLocal.WithLabelValues(k.suffix, k.writer).Set(boolGauge(here))
	}
}

// consumerLeader is the name of the server leading the consumer, "" when none does.
func consumerLeader(ci *nats.ConsumerInfo) string {
	if ci == nil || ci.Cluster == nil {
		return ""
	}
	return ci.Cluster.Leader
}

// reconcile is the consumer-leader reconciler for one durable, run once per sample. It acts
// only when ALL of these hold, and each failure of one leaves the leader where it is:
//
//   - the setting is on;
//   - the leader has been away from this replica's server for affinityHysteresis samples in
//     a row (an election leaves it away for a moment; three samples is a placement);
//   - this replica has not moved this durable's leader within affinityMoveInterval;
//   - the broker is healthy (incident): clustered, this connection up and not recently
//     back from a reconnect, the stream and the consumer each led and with every peer
//     current;
//   - a census of this area's replicas finds a server that a STRICT MAJORITY of the
//     durable's readers are connected to (two replicas on two servers have none, and
//     moving the leader between them would only flap), the leader is not already there,
//     and THIS replica is the census's actor (the lowest id among the durable's readers
//     that are away from its leader; see decideMove), so that across replicas one asks.
func (a *affinity) reconcile(ctx context.Context, d durableRef, si *nats.StreamInfo, ci *nats.ConsumerInfo, local bool) {
	st := a.durables[d]
	if st == nil {
		st = &durableAffinity{}
		a.durables[d] = st
	}
	if local {
		st.remote = 0
		return
	}
	st.remote++
	if !a.consumersOn() || st.remote < affinityHysteresis {
		return
	}
	if !st.lastMove.IsZero() && time.Since(st.lastMove) < affinityMoveInterval {
		return
	}
	if why := a.incident(si, ci); why != "" {
		log.Debug().Str("durable", d.durable).Str("reason", why).
			Msg("Consumer leader is away from its reader; not moving it during a broker incident")
		return
	}
	st.remote = 0
	st.lastMove = time.Now()
	a.move(ctx, d, ci)
}

// incident names why the broker is not in a state to move a leader in, or "" when it is.
func (a *affinity) incident(si *nats.StreamInfo, ci *nats.ConsumerInfo) string {
	switch {
	case !a.nmgr.brokerIsClustered():
		return "the broker is not clustered"
	case time.Since(time.Unix(0, a.lastReconnect.Load())) < affinityQuietAfterReconnect:
		return "this service reconnected to the broker recently"
	case si == nil:
		return "the stream was not sampled"
	case leaderOfStream(si) == "" || currentPeers(si) < si.Config.Replicas:
		return "the stream has no leader, or a peer that is not current"
	case consumerLeader(ci) == "":
		return "the consumer has no leader"
	}
	for _, p := range ci.Cluster.Replicas {
		if p == nil || !p.Current || p.Offline {
			return "a consumer peer is not current"
		}
	}
	return ""
}

// move runs the census and, when this replica is the actor and a majority server exists,
// asks the broker to step the consumer's leader down in its favour.
func (a *affinity) move(ctx context.Context, d durableRef, ci *nats.ConsumerInfo) {
	replies, err := a.census(ctx)
	if err != nil {
		log.Debug().Err(err).Str("durable", d.durable).Msg("Consumer-leader census failed; leaving the leader where it is")
		return
	}
	target, actor, ok := decideMove(replies, d.durable, a.id, consumerLeader(ci))
	switch {
	case !actor:
		return
	case !ok:
		if a.logEvery("split:" + d.durable) {
			log.Info().Str("durable", d.durable).Int("readers", len(readersOf(replies, d.durable))).
				Msg("The durable's readers are spread across broker servers with no majority on one; " +
					"its consumer leader is left where it is")
		}
		return
	case target == consumerLeader(ci):
		return
	}
	if !isPeer(ci, target) {
		if a.logEvery("peer:" + d.durable) {
			log.Info().Str("durable", d.durable).Str("server", target).
				Msg("The server the durable's readers share holds no replica of the consumer; its leader is left where it is")
		}
		return
	}
	err = a.stepDown(ctx, d, target)
	result := leaderMoveMoved
	if err != nil {
		result = leaderMoveFailed
		log.Warn().Err(err).Str("durable", d.durable).Str("server", target).
			Msg("The broker did not move the consumer leader to its readers' server; nothing changed")
	} else {
		log.Info().Str("durable", d.durable).Str("from", consumerLeader(ci)).Str("to", target).
			Msg("Moved the consumer leader to the server its readers are connected to")
	}
	a.nmgr.metrics.leaderMoves.WithLabelValues(d.stream, d.durable, result).Inc()
}

// isPeer reports whether server holds a replica of the consumer (only a peer can lead it).
func isPeer(ci *nats.ConsumerInfo, server string) bool {
	if consumerLeader(ci) == server {
		return true
	}
	for _, p := range ci.Cluster.Replicas {
		if p != nil && p.Name == server {
			return true
		}
	}
	return false
}

// stepDown asks the broker to elect server as the consumer's leader.
func (a *affinity) stepDown(ctx context.Context, d durableRef, server string) error {
	body, err := json.Marshal(map[string]any{"placement": map[string]string{"preferred": server}})
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, affinityRequestTimeout)
	defer cancel()
	msg, err := a.nmgr.nc.RequestWithContext(rctx,
		fmt.Sprintf("$JS.API.CONSUMER.LEADER.STEPDOWN.%s.%s", d.stream, d.durable), body)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool `json:"success"`
		Error   *struct {
			Code        int    `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return fmt.Errorf("unreadable step-down response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("step-down refused (%d): %s", resp.Error.Code, resp.Error.Description)
	}
	if !resp.Success {
		return errors.New("step-down not acknowledged")
	}
	return nil
}

// censusReply is one replica's answer to a census: who it is, which server its connection is
// attached to, and the durables it reads.
type censusReply struct {
	ID       string   `json:"id"`
	Server   string   `json:"server"`
	Durables []string `json:"durables"`
}

// CensusSubject is the control subject one area's replicas answer a census on. It sits under
// controlSubjectRoot, outside every stream's capture space, so no stream stores a census.
func CensusSubject(instanceId, area string) string {
	return controlSubjectRoot + instanceId + affinityCensusSegment + sanitizeName(area)
}

// answerCensus is this replica's reply.
func (a *affinity) answerCensus(msg *nats.Msg) {
	if msg.Reply == "" {
		return
	}
	reply := censusReply{ID: a.id, Server: a.ownServer()}
	for _, d := range a.nmgr.trackedDurables() {
		reply.Durables = append(reply.Durables, d.durable)
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return
	}
	_ = a.nmgr.nc.Publish(msg.Reply, body)
}

// census asks every replica of this area which server it is attached to and which durables
// it reads, and gathers the answers until none has arrived for affinityCensusQuiet. This
// replica answers its own census, so an empty result is a failure, never "no readers".
func (a *affinity) census(ctx context.Context) ([]censusReply, error) {
	nc := a.nmgr.nc
	inbox := nc.NewInbox()
	//subconfirm:ok the request below goes out on this same connection, so the server reads
	// SUB before PUB (the reasoning is PurgeTenantDetect's)
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.PublishRequest(CensusSubject(a.nmgr.Microservice.InstanceId,
		a.nmgr.Microservice.FunctionalArea), inbox, nil); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(affinityCensusMax)
	var out []censusReply
	for {
		wait := time.Until(deadline)
		if len(out) > 0 {
			wait = min(wait, affinityCensusQuiet)
		}
		if wait <= 0 || ctx.Err() != nil {
			break
		}
		msg, err := sub.NextMsg(wait)
		if err != nil {
			break
		}
		var r censusReply
		if json.Unmarshal(msg.Data, &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	for _, r := range out {
		if r.ID == a.id {
			return out, nil
		}
	}
	return nil, errors.New("this replica's own census reply did not arrive")
}

// readersOf is the census replies from replicas that read durable.
func readersOf(replies []censusReply, durable string) []censusReply {
	var out []censusReply
	for _, r := range replies {
		if slices.Contains(r.Durables, durable) {
			out = append(out, r)
		}
	}
	return out
}

// decideMove reads a census for one durable: whether self is the actor, and the server a
// strict majority of the durable's readers are attached to (ok false when there is none).
//
// 🔴 THE ACTOR IS THE LOWEST ID AMONG THE READERS THAT ARE AWAY FROM THE LEADER, not among
// all readers. Only a replica whose own server does not lead the consumer ever samples it as
// remote and reaches the census (reconcile returns early on a local leader), so an actor
// elected from every reader can be one that never asks: three readers on A, A and B with
// the leader on B and the B reader holding the lowest id would leave the leader on B for
// good, the two A readers each deferring to a replica that has nothing to do. Every reader
// that does run the census sees the same leader, so all of them elect the same actor.
func decideMove(replies []censusReply, durable, self, leader string) (target string, actor, ok bool) {
	readers := readersOf(replies, durable)
	if len(readers) == 0 {
		return "", false, false
	}
	lowest := ""
	counts := map[string]int{}
	for _, r := range readers {
		if r.Server != leader || leader == "" {
			if lowest == "" || r.ID < lowest {
				lowest = r.ID
			}
		}
		if r.Server != "" {
			counts[r.Server]++
		}
	}
	actor = lowest != "" && lowest == self
	for server, n := range counts {
		if 2*n > len(readers) {
			return server, actor, true
		}
	}
	return "", actor, false
}
