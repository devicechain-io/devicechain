// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/mqttack"
	"github.com/devicechain-io/dc-simulator/sim"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// ErrNotConnected is a publish that was not sent because the device's session was down.
var ErrNotConnected = errors.New("mqtt session is not connected")

// Fleet is one MQTT session per device, kept connected by per-session supervisors, with the
// accounting of everything published through it.
type Fleet struct {
	cfg    Config
	broker string // the address dialled, scheme resolved from cfg.TLS

	sessions []*session
	byToken  map[string]*session
	sem      chan struct{}

	// Counters is the running accounting; Lat the PUBACK latency of every acknowledged
	// publish.
	Counters Counters
	Lat      LatencyHist
	ivLat    atomic.Pointer[LatencyHist] // the current reporting interval's latencies

	tlsMu       sync.Mutex
	tlsVersions map[string]int64 // negotiated TLS versions, observed on the socket

	wg     sync.WaitGroup
	cancel context.CancelFunc
	closed atomic.Bool
}

// session is one device's connection and the state its supervisor and sender share.
type session struct {
	f     *Fleet
	dev   sim.DeviceInstance
	idx   int
	topic string

	client mqtt.Client
	store  *mqttack.Store

	lost   chan struct{} // buffered 1: the connection was lost
	everUp atomic.Bool
	up     atomic.Bool

	errMu    sync.Mutex
	firstErr error
}

// NewFleet builds the sessions for devices without connecting any. Each device's MQTT identity
// is derived exactly as the platform's auth callout expects it: client id from
// messaging.DeviceClientID, username "{tenant}:{credentialId}", an empty password, and the
// publish topic confined to the device's own events subject.
func NewFleet(cfg Config, devices []sim.DeviceInstance) (*Fleet, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no devices to connect")
	}
	broker, err := ResolveBroker(cfg.Broker, cfg.TLS)
	if err != nil {
		return nil, err
	}
	f := &Fleet{
		cfg: cfg, broker: broker,
		byToken:     make(map[string]*session, len(devices)),
		sem:         make(chan struct{}, cfg.ConnectConcurrency),
		tlsVersions: map[string]int64{},
	}
	f.ivLat.Store(&LatencyHist{})
	for i, d := range devices {
		if d.CredentialId == "" {
			return nil, fmt.Errorf("device %q has no credential id: it was not provisioned with an ACCESS_TOKEN, so it cannot authenticate to the broker", d.Token)
		}
		if _, dup := f.byToken[d.Token]; dup {
			return nil, fmt.Errorf("device %q appears twice: two sessions cannot share a client id", d.Token)
		}
		s, err := f.newSession(i, d)
		if err != nil {
			return nil, err
		}
		f.sessions = append(f.sessions, s)
		f.byToken[d.Token] = s
	}
	return f, nil
}

// Broker is the address the fleet dials, with the scheme the TLS flag selected.
func (f *Fleet) Broker() string { return f.broker }

// Config is the effective configuration, defaults filled.
func (f *Fleet) Config() Config { return f.cfg }

// Devices is how many sessions the fleet holds (one per device).
func (f *Fleet) Devices() int { return len(f.sessions) }

// TLSVersions reports the TLS versions negotiated on the connections observed so far.
func (f *Fleet) TLSVersions() map[string]int64 {
	f.tlsMu.Lock()
	defer f.tlsMu.Unlock()
	out := make(map[string]int64, len(f.tlsVersions))
	for k, v := range f.tlsVersions {
		out[k] = v
	}
	return out
}

func (f *Fleet) newSession(idx int, d sim.DeviceInstance) (*session, error) {
	clientID, err := messaging.DeviceClientID(f.cfg.InstanceId, f.cfg.Tenant, d.Token)
	if err != nil {
		return nil, fmt.Errorf("device %q: %w", d.Token, err)
	}
	s := &session{
		f: f, dev: d, idx: idx,
		topic: messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(f.cfg.InstanceId, f.cfg.Tenant, d.Token)),
		store: mqttack.NewStore(),
		lost:  make(chan struct{}, 1),
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(f.broker)
	opts.SetClientID(clientID)
	opts.SetUsername(fmt.Sprintf("%s:%s", f.cfg.Tenant, d.CredentialId))
	opts.SetPassword("")
	opts.SetCleanSession(f.cfg.CleanSession)
	opts.SetStore(s.store)
	opts.SetKeepAlive(f.cfg.KeepAlive)
	opts.SetConnectTimeout(f.cfg.ConnectTimeout)
	opts.SetWriteTimeout(f.cfg.AckTimeout)
	// The fleet reconnects itself, with jittered backoff and a bound on simultaneous CONNECTs.
	// paho's own reconnect doubles without jitter and has no such bound, so a broker restart
	// would bring 3,000 devices back in the same instant.
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetOrderMatters(false)
	opts.SetCustomOpenConnectionFn(f.dial)
	if f.cfg.TLS {
		opts.SetTLSConfig(f.tlsConfig(s.dev.Token))
	}
	opts.SetOnConnectHandler(func(mqtt.Client) {
		s.up.Store(true)
		s.everUp.Store(true)
		f.Counters.Connects.Add(1)
		f.Counters.Up.Add(1)
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		if s.up.Swap(false) {
			f.Counters.Up.Add(-1)
		}
		f.Counters.ConnectionLosses.Add(1)
		s.noteErr(fmt.Errorf("connection lost: %w", err))
		select {
		case s.lost <- struct{}{}:
		default:
		}
	})
	s.client = mqtt.NewClient(opts)
	return s, nil
}

func (f *Fleet) tlsConfig(deviceToken string) *tls.Config {
	host := ""
	if u, err := url.Parse(f.broker); err == nil {
		host = u.Hostname()
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: f.cfg.TLSInsecure, //nolint:gosec // the operator's recorded trust decision for a self-signed gateway
		ServerName:         host,
	}
}

// dial opens the socket itself, so the harness can see what it actually got: a TLS handshake
// with a version, or a plain TCP connection. The flag chose the scheme; this records the
// result, and the report gates on the two agreeing.
func (f *Fleet) dial(u *url.URL, o mqtt.ClientOptions) (net.Conn, error) {
	d := &net.Dialer{Timeout: f.cfg.ConnectTimeout}
	switch u.Scheme {
	case "tcp":
		c, err := d.Dial("tcp", u.Host)
		if err != nil {
			return nil, err
		}
		f.Counters.PlainConns.Add(1)
		return c, nil
	case "ssl", "tls", "tcps", "mqtts":
		cfg := o.TLSConfig
		if cfg == nil {
			cfg = f.tlsConfig("")
		}
		c, err := tls.DialWithDialer(d, "tcp", u.Host, cfg)
		if err != nil {
			return nil, err
		}
		f.Counters.TLSConns.Add(1)
		f.tlsMu.Lock()
		f.tlsVersions[tls.VersionName(c.ConnectionState().Version)]++
		f.tlsMu.Unlock()
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported scheme %q: the harness dials tcp and ssl brokers (websocket addresses are not observed)", u.Scheme)
	}
}

// Start launches one supervisor per session; it returns at once. Sessions connect as fast as
// ConnectConcurrency allows and reconnect with backoff whenever they drop. AwaitConnected
// waits for the first connections.
func (f *Fleet) Start(ctx context.Context) {
	ctx, f.cancel = context.WithCancel(ctx)
	for _, s := range f.sessions {
		f.wg.Add(1)
		go func(s *session) {
			defer f.wg.Done()
			s.supervise(ctx)
		}(s)
	}
}

// AwaitConnected blocks until every session has connected at least once, the timeout passes,
// or ctx ends, and returns how many have.
func (f *Fleet) AwaitConnected(ctx context.Context, timeout time.Duration) (int, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		n := f.everConnected()
		if n == len(f.sessions) {
			return n, nil
		}
		select {
		case <-ctx.Done():
			return n, ctx.Err()
		case <-deadline.C:
			return n, fmt.Errorf("%d of %d devices connected within %s (first failure: %v)", n, len(f.sessions), timeout, f.firstError())
		case <-tick.C:
		}
	}
}

func (f *Fleet) everConnected() int {
	n := 0
	for _, s := range f.sessions {
		if s.everUp.Load() {
			n++
		}
	}
	return n
}

// NeverConnected is how many sessions have not connected once.
func (f *Fleet) NeverConnected() int { return len(f.sessions) - f.everConnected() }

func (f *Fleet) firstError() error {
	for _, s := range f.sessions {
		if err := s.err(); err != nil {
			return fmt.Errorf("%s: %w", s.dev.Token, err)
		}
	}
	return nil
}

// Close stops the supervisors and disconnects every session, letting in-flight work finish for
// a moment first. It is safe to call more than once.
func (f *Fleet) Close() {
	if f.closed.Swap(true) {
		return
	}
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for _, s := range f.sessions {
		wg.Add(1)
		sem <- struct{}{}
		go func(s *session) {
			defer wg.Done()
			defer func() { <-sem }()
			if s.client.IsConnectionOpen() {
				s.client.Disconnect(250)
			}
		}(s)
	}
	wg.Wait()
}

func (s *session) noteErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.firstErr == nil {
		s.firstErr = err
	}
}

func (s *session) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.firstErr
}

// backoff is the delay before reconnect attempt n (0-based): "equal jitter" exponential, half
// fixed and half random, capped at BackoffMax, so a fleet that lost its broker together does
// not come back together.
func (f *Fleet) backoff(n int) time.Duration {
	d := f.cfg.BackoffBase
	for i := 0; i < n && d < f.cfg.BackoffMax; i++ {
		d *= 2
	}
	if d > f.cfg.BackoffMax {
		d = f.cfg.BackoffMax
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// supervise keeps one session connected until ctx ends: connect (bounded by the fleet's
// connect semaphore), wait for the connection to be lost, back off, and connect again.
func (s *session) supervise(ctx context.Context) {
	attempt := 0
	first := true
	for ctx.Err() == nil {
		if !first {
			if !sleepCtx(ctx, s.f.backoff(attempt)) {
				return
			}
		}
		first = false
		if err := s.connect(ctx); err != nil {
			s.f.Counters.ConnectFailures.Add(1)
			s.noteErr(err)
			attempt++
			continue
		}
		upSince := time.Now()
		select {
		case <-s.lost:
		case <-ctx.Done():
			return
		}
		// A connection that held for a while earns a fresh backoff; one that dropped at once
		// keeps escalating, so a broker that accepts and immediately drops is not hammered.
		if time.Since(upSince) >= s.f.cfg.BackoffMax {
			attempt = 0
		} else {
			attempt++
		}
	}
}

// connect makes one CONNECT attempt, holding a slot of the fleet's connect semaphore.
func (s *session) connect(ctx context.Context) error {
	select {
	case s.f.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.f.sem }()
	s.f.Counters.ConnectAttempts.Add(1)
	tok := s.client.Connect()
	select {
	case <-tok.Done():
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2*s.f.cfg.ConnectTimeout + 5*time.Second):
		return fmt.Errorf("connect to %s: no answer", s.f.broker)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connect to %s: %w", s.f.broker, err)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// pending is one publish waiting for its acknowledgement.
type pending struct {
	tok      mqtt.Token
	mid      uint16
	stamp    string // the occurredTime sent: the event's identity
	sentAt   time.Time
	deadline time.Time
	// resent: the token closed with no error and no PUBACK, so a resumed session re-sent the
	// publish (counted once, in Counters.Resent).
	resent bool
}

// result is how a publish ended.
type result struct {
	// acked: QoS 1 and the session store saw the PUBACK. written: QoS 0 and the client wrote
	// it. Neither: the outcome is unknown (ambiguous).
	acked, written bool
	err            error
	latency        time.Duration
}

// send publishes body on the session's topic. It does not wait. A session that is down returns
// ErrNotConnected and sends nothing.
func (s *session) send(body []byte, stamp string, now time.Time) (*pending, error) {
	if !s.up.Load() || !s.client.IsConnectionOpen() {
		return nil, ErrNotConnected
	}
	tok := s.client.Publish(s.topic, s.f.cfg.QoS, false, body)
	p := &pending{tok: tok, stamp: stamp, sentAt: now, deadline: now.Add(s.f.cfg.AckTimeout)}
	if pt, ok := tok.(*mqtt.PublishToken); ok {
		p.mid = pt.MessageID()
	}
	s.f.Counters.Sent.Add(1)
	s.f.Counters.Inflight.Add(1)
	return p, nil
}

// check decides whether p has ended, and how.
//
// A QoS 1 publish is acknowledged only when the session store saw its PUBACK. A token that
// completes without an error and without one is a re-send that took over its message id (a
// persistent session resuming): it is NOT an acknowledgement, so the publish stays pending
// until the re-send's PUBACK arrives or the deadline passes. unconfirmedDone reports that
// state, so a caller waits on the clock rather than on a token that is already closed.
func (s *session) check(p *pending, now time.Time) (done bool, r result, unconfirmedDone bool) {
	select {
	case <-p.tok.Done():
		if err := p.tok.Error(); err != nil {
			return true, result{err: err, latency: now.Sub(p.sentAt)}, false
		}
		if s.f.cfg.QoS == 0 {
			return true, result{written: true, latency: now.Sub(p.sentAt)}, false
		}
		if s.store.PubAcked(p.mid) {
			return true, result{acked: true, latency: now.Sub(p.sentAt)}, false
		}
		unconfirmedDone = true
		if !p.resent {
			p.resent = true
			s.f.Counters.Resent.Add(1)
		}
	default:
	}
	if now.After(p.deadline) {
		return true, result{err: fmt.Errorf("no PUBACK within %s", s.f.cfg.AckTimeout), latency: now.Sub(p.sentAt)}, false
	}
	return false, result{}, unconfirmedDone
}

// settle books a finished publish in the fleet's counters and returns the outcome the identity
// ledger files for it: Accepted only for a real PUBACK, Ambiguous otherwise (a QoS 0 write
// promises nothing, so it is ambiguous too).
func (s *session) settle(r result) sim.EmitOutcome {
	s.f.Counters.Inflight.Add(-1)
	switch {
	case r.acked:
		s.f.Counters.Acked.Add(1)
		s.f.Lat.Observe(r.latency)
		s.f.ivLat.Load().Observe(r.latency)
		return sim.OutcomeAccepted
	case r.written:
		s.f.Counters.Written.Add(1)
		return sim.OutcomeAmbiguous
	default:
		s.f.Counters.Unconfirmed.Add(1)
		if r.err != nil {
			s.noteErr(r.err)
		}
		return sim.OutcomeAmbiguous
	}
}

// unconfirmedPoll is how often a publish whose token closed without a PUBACK is re-checked.
const unconfirmedPoll = 10 * time.Millisecond

// Post implements sim.EventWire: it publishes one event for d and waits for the broker's
// answer, so a scenario that emits through sim.EmitAll runs over MQTT unchanged. It returns nil
// only for an acknowledged publish. Every outcome is filed in rt.Identity: Accepted for a real
// PUBACK, Ambiguous when the answer is unknown, and Refused only when nothing was sent (the
// session was down), which is the one case the event is certainly absent.
func (f *Fleet) Post(ctx context.Context, rt *sim.Runtime, d sim.DeviceInstance, eventType, occurredTime string, payload map[string]interface{}) error {
	s := f.byToken[d.Token]
	if s == nil {
		return fmt.Errorf("device %q is not in the MQTT fleet", d.Token)
	}
	body, err := sim.MQTTEventBody(d, eventType, occurredTime, payload)
	if err != nil {
		return err
	}
	p, err := s.send(body, occurredTime, time.Now())
	if err != nil {
		f.Counters.NotSentDisconnected.Add(1)
		rt.Identity.Record(sim.OutcomeRefused, d.Token, eventType, occurredTime)
		return fmt.Errorf("device %q: %w", d.Token, err)
	}
	for {
		done, r, polling := s.check(p, time.Now())
		if done {
			outcome := s.settle(r)
			rt.Identity.Record(outcome, d.Token, eventType, occurredTime)
			if r.acked {
				return nil
			}
			if r.written {
				return nil
			}
			return fmt.Errorf("device %q: publish not acknowledged: %w", d.Token, r.err)
		}
		wait := time.Until(p.deadline)
		var tokDone <-chan struct{}
		if !polling {
			tokDone = p.tok.Done()
		} else if wait > unconfirmedPoll {
			wait = unconfirmedPoll
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			// The publish was handed to the transport and its fate is unknown.
			s.settle(result{err: ctx.Err(), latency: time.Since(p.sentAt)})
			rt.Identity.Record(sim.OutcomeAmbiguous, d.Token, eventType, occurredTime)
			return ctx.Err()
		case <-tokDone:
		case <-t.C:
		}
		t.Stop()
	}
}

var _ sim.EventWire = (*Fleet)(nil)
