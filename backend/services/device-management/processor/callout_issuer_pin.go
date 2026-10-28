// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// The issuer pin (ADR-025, G16 S4b).
//
// nats-server signs every authorization request with its own server nkey, generated at NewServer
// and neither seedable nor readable from configuration, and the server's ID IS that key's public
// half (server.go: kp, _ := nkeys.CreateServer(); the request is claim.Encode(s.kp)). So a request
// JWT's "iss" names the server that sent it, and the set of live servers is knowable from the
// system account: every server answers $SYS.REQ.SERVER.PING with a statsz document carrying its
// server.id. The pin is "iss is one of those, and is the server the request says it came from".
//
// WHAT IT BUYS. A certificate grant needs no secret from the device, so a request forged by
// anything that can publish on the callout subject (and seal to the responder's public xkey) would
// otherwise be a grant. The broker's publish-deny on that subject is the first defence; this is
// the second, and it does not depend on the deny staying in place. A forger would need a server's
// PRIVATE key, which never leaves the process that generated it.
//
// WHAT IT DOES NOT COVER. A password or token grant is not pinned: those still need a secret the
// forger would have to guess, so they are not made worse by an unpinned issuer than they already
// were, and putting them behind the system connection would make every device connect depend on
// it. A compromised broker process is out of scope (it holds the key).

// Defaults for the pin. The gather window sits well under the callout's lookup timeout (3 s) and
// the broker's authorization timeout (5 s): an on-demand refresh happens INSIDE a connect.
const (
	DefaultIssuerPinRefreshInterval    = 30 * time.Second
	DefaultIssuerPinMinRefreshInterval = 2 * time.Second
	DefaultIssuerPinGatherWindow       = 750 * time.Millisecond
	// issuerPinMaxAgeIntervals is how many refresh intervals a membership snapshot is believed
	// without a successful refresh. Past it the snapshot answers nothing (fail closed).
	issuerPinMaxAgeIntervals = 3
	// issuerPinSysPing is the fan-out statsz request every server answers.
	issuerPinSysPing = "$SYS.REQ.SERVER.PING"
)

// Pin refresh results (callout_issuer_pin_refresh_total), a closed list.
const (
	pinRefreshOK          = "ok"           // at least one server answered; the ids that did were (re)stamped as seen
	pinRefreshError       = "error"        // nobody answered, or the request failed; the set is unchanged
	pinRefreshRateLimited = "rate_limited" // an unknown issuer arrived inside the minimum interval; no ping sent
)

var pinRefreshResults = []string{pinRefreshOK, pinRefreshError, pinRefreshRateLimited}

// pinVerdict is the pin's answer for one request.
type pinVerdict int

const (
	// pinPinned: the issuer is a live server and is the server the request names.
	pinPinned pinVerdict = iota
	// pinUnknown: the membership was current and the issuer is not in it (after a re-ping when one was allowed).
	pinUnknown
	// pinPending: the issuer is not (yet) known and the re-ping that could have learned it was not
	// allowed or did not finish (inside the minimum interval, or the wait timed out). Refused, like
	// pinUnknown, but counted apart so that pinUnknown stays a clean forgery signal: it means a
	// re-ping WAS made and found nothing. A server that joined during a roll lands here.
	pinPending
	// pinUnavailable: the membership cannot be vouched for (system connection down, no refresh has
	// succeeded recently). Refused, like pinUnknown, but counted apart: it is the operator's problem,
	// not an attack.
	pinUnavailable
)

// IssuerPinMetrics are the pin's series. Optional.
type IssuerPinMetrics struct {
	// Refresh counts membership refreshes by result.
	Refresh *prometheus.CounterVec
	// Servers is the size of the believed membership.
	Servers prometheus.Gauge
	// Connected is 1 while the system connection is up.
	Connected prometheus.Gauge
}

// IssuerPinGather asks the system account which servers are live: the server ids that answered
// within its window. It returns an error when none did.
type IssuerPinGather func(ctx context.Context) (ids []string, err error)

// IssuerPinConfig assembles an IssuerPin.
type IssuerPinConfig struct {
	Gather IssuerPinGather
	// Connected reports whether the system connection is up. A pin over a down connection refuses.
	Connected func() bool
	// RefreshInterval is the periodic refresh; MinRefreshInterval is the least time between two
	// pings, however many unknown issuers arrive.
	RefreshInterval    time.Duration
	MinRefreshInterval time.Duration
	Metrics            *IssuerPinMetrics
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// IssuerPin is the believed set of live nats-server ids.
type IssuerPin struct {
	gather    IssuerPinGather
	connected func() bool
	interval  time.Duration
	minGap    time.Duration
	maxAge    time.Duration
	metrics   *IssuerPinMetrics
	now       func() time.Time

	// slot serializes refreshes (a one-token channel, so a waiter can give up with its context).
	slot chan struct{}

	mu          sync.Mutex
	ids         map[string]time.Time // server id -> when a ping last heard it
	lastOK      time.Time            // last refresh that produced a usable answer
	lastAttempt time.Time            // last time a ping was sent

	downLog rateLimitedLog
	started bool
	stop    chan struct{}
	done    chan struct{}
}

// NewIssuerPin builds a pin. It refuses a missing gather or connection probe: a pin with no way to
// learn the membership would refuse every certificate connect, quietly.
func NewIssuerPin(cfg IssuerPinConfig) (*IssuerPin, error) {
	if cfg.Gather == nil || cfg.Connected == nil {
		return nil, errors.New("the issuer pin needs a way to gather the live servers and to tell whether the system connection is up")
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultIssuerPinRefreshInterval
	}
	if cfg.MinRefreshInterval <= 0 {
		cfg.MinRefreshInterval = DefaultIssuerPinMinRefreshInterval
	}
	if cfg.MinRefreshInterval > cfg.RefreshInterval {
		return nil, fmt.Errorf("the issuer pin's minimum refresh interval (%s) exceeds its refresh interval (%s)",
			cfg.MinRefreshInterval, cfg.RefreshInterval)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &IssuerPin{
		gather: cfg.Gather, connected: cfg.Connected,
		interval: cfg.RefreshInterval, minGap: cfg.MinRefreshInterval,
		maxAge:  issuerPinMaxAgeIntervals * cfg.RefreshInterval,
		metrics: cfg.Metrics, now: cfg.Now,
		slot: make(chan struct{}, 1),
		ids:  make(map[string]time.Time),
		stop: make(chan struct{}), done: make(chan struct{}),
	}, nil
}

// Check decides whether a request signed by issuer, naming claimedServerID as its sender, came
// from a live server. It may send ONE statsz ping (rate limited) when the issuer is not in the
// believed membership, which is what lets a server that joined since the last refresh (a rolling
// restart) be learned inside the connect that first meets it.
func (p *IssuerPin) Check(ctx context.Context, issuer, claimedServerID string) pinVerdict {
	if !p.isConnected() {
		p.logDown("the system-account connection is down")
		return pinUnavailable
	}
	// The sender is named twice (iss, signed; server.id, a plain field). A real server writes the
	// same key to both. A mismatch is a forgery or a bug, and either way is not granted.
	if issuer == "" || issuer != claimedServerID {
		return pinUnknown
	}
	if p.lookup(issuer) == pinPinned {
		return pinPinned
	}
	// Not in the snapshot (or the snapshot is too old to believe): one re-ping, if the minimum
	// interval allows one.
	outcome := p.refresh(ctx, false)
	if !p.isConnected() {
		return pinUnavailable
	}
	switch v := p.lookup(issuer); v {
	case pinUnknown:
		// The id is absent from a current snapshot. That is a verdict only if a ping of OUR OWN
		// just found nothing; a refusal for want of a ping (rate limit, timeout, failed ping) is
		// "not learned yet", which a rolling restart produces and a forger does not need to.
		if outcome != refreshDone {
			return pinPending
		}
		return pinUnknown
	default:
		return v
	}
}

// lookup answers from the snapshot alone: pinned, unknown (a current snapshot without the
// issuer), or unavailable (no snapshot, or one past maxAge).
func (p *IssuerPin) lookup(issuer string) pinVerdict {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastOK.IsZero() || p.now().Sub(p.lastOK) > p.maxAge {
		return pinUnavailable
	}
	// Per id, not per snapshot: an answer that heard only some servers refreshes only those, so a
	// server that stopped answering ages out on its own clock instead of riding on its neighbours.
	if seen, ok := p.ids[issuer]; ok && p.now().Sub(seen) <= p.maxAge {
		return pinPinned
	}
	return pinUnknown
}

func (p *IssuerPin) isConnected() bool {
	up := p.connected()
	if p.metrics != nil && p.metrics.Connected != nil {
		if up {
			p.metrics.Connected.Set(1)
		} else {
			p.metrics.Connected.Set(0)
		}
	}
	return up
}

func (p *IssuerPin) logDown(why string) {
	if allow, held := p.downLog.allow(p.now()); allow {
		log.Warn().Int("alsoHeld", held).Str("why", why).
			Msg("Certificate connects are being refused: the issuer pin cannot vouch for the broker's server ids.")
	}
}

// refreshOutcome is what refresh did, for Check.
type refreshOutcome int

const (
	// refreshDone: a ping was sent and answered; the snapshot now reflects it.
	refreshDone refreshOutcome = iota
	// refreshSkipped: inside the minimum interval, or the wait for a refresh in flight ran out.
	refreshSkipped
	// refreshFailed: a ping was sent and nobody answered, or it could not be sent.
	refreshFailed
)

// refresh pings the system account and updates the membership. force skips the rate limit (the
// periodic refresh and the first one). A caller that finds a refresh in flight waits for it
// (bounded by ctx) rather than sending a second ping: its answer is the one both want.
//
// 🔴 AN ANSWER NEVER SHRINKS THE SET. The count a server reports of its cluster is not a promise
// that the others have been heard: a server that has just booted reports a small one for its first
// seconds, and the connection this ping rides on reconnects to exactly such a server during a
// rolling restart. Treating "this many answered" as "this is everyone" would have replaced the set
// with that one pod and refused the rest of the fleet. So the ids that answered are stamped with the
// time, the rest are left alone, and an id leaves only by not being heard for maxAge.
func (p *IssuerPin) refresh(ctx context.Context, force bool) refreshOutcome {
	select {
	case p.slot <- struct{}{}:
	case <-ctx.Done():
		p.countRefresh(pinRefreshRateLimited)
		return refreshSkipped
	}
	defer func() { <-p.slot }()

	p.mu.Lock()
	now := p.now()
	if !force && !p.lastAttempt.IsZero() && now.Sub(p.lastAttempt) < p.minGap {
		p.mu.Unlock()
		p.countRefresh(pinRefreshRateLimited)
		return refreshSkipped
	}
	p.lastAttempt = now
	p.mu.Unlock()

	ids, err := p.gather(ctx)
	if err != nil || len(ids) == 0 {
		p.countRefresh(pinRefreshError)
		if err != nil {
			p.logDown("the server ping failed: " + err.Error())
		} else {
			p.logDown("no server answered the ping")
		}
		return refreshFailed
	}
	p.countRefresh(pinRefreshOK)
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.now()
	for _, id := range ids {
		p.ids[id] = at
	}
	for id, seen := range p.ids {
		if at.Sub(seen) > p.maxAge {
			delete(p.ids, id)
		}
	}
	p.lastOK = at
	if p.metrics != nil && p.metrics.Servers != nil {
		p.metrics.Servers.Set(float64(len(p.ids)))
	}
	return refreshDone
}

func (p *IssuerPin) countRefresh(result string) {
	if p.metrics != nil && p.metrics.Refresh != nil {
		p.metrics.Refresh.WithLabelValues(result).Inc()
	}
}

// Start learns the membership once, then keeps it fresh every RefreshInterval until Stop. The
// first refresh failing is not an error: the system connection may still be dialling, and every
// certificate connect is refused (and counted) until a refresh succeeds.
func (p *IssuerPin) Start(ctx context.Context) {
	p.started = true
	if p.isConnected() {
		p.refresh(ctx, true)
	}
	go func() {
		defer close(p.done)
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				if !p.isConnected() {
					p.logDown("the system-account connection is down")
					continue
				}
				rctx, cancel := context.WithTimeout(context.Background(), p.interval)
				p.refresh(rctx, true)
				cancel()
			}
		}
	}()
}

// Stop ends the periodic refresh.
func (p *IssuerPin) Stop() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	if p.started {
		<-p.done
	}
}

// Members is the believed membership, for tests and diagnostics.
func (p *IssuerPin) Members() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.ids))
	for id := range p.ids {
		out = append(out, id)
	}
	return out
}

// issuerPinRegistry is what NewIssuerPinMetrics needs of the microservice.
type issuerPinRegistry interface {
	NewCounterVec(name, help string, labels []string) *prometheus.CounterVec
	NewGauge(name, help string) prometheus.Gauge
}

// NewIssuerPinMetrics registers the pin's series with every refresh result at zero.
func NewIssuerPinMetrics(ms issuerPinRegistry) *IssuerPinMetrics {
	m := &IssuerPinMetrics{
		Refresh: ms.NewCounterVec("callout_issuer_pin_refresh_total",
			"Refreshes of the set of live broker server ids the auth callout pins certificate requests to, by result. "+
				"ok heard at least one server (and stamped those ids as seen; an id leaves only by not being heard for three refresh periods); "+
				"error left the set unchanged (nobody answered); rate_limited is an unknown issuer that arrived inside the minimum interval, or whose wait for a ping in flight ran out, and sent no ping.",
			[]string{"result"}),
		Servers: ms.NewGauge("callout_issuer_pin_servers",
			"Live broker server ids the auth callout currently believes."),
		Connected: ms.NewGauge("callout_issuer_pin_connected",
			"1 while the auth callout's system-account connection is up. While it is 0 every certificate connect is refused."),
	}
	for _, r := range pinRefreshResults {
		m.Refresh.WithLabelValues(r)
	}
	return m
}

// statszServer is the subset of a $SYS.REQ.SERVER.PING reply the pin reads.
type statszServer struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
	Statsz struct {
		ActiveServers int `json:"active_servers"`
	} `json:"statsz"`
}

// NewSystemGather is the IssuerPinGather over a system-account connection. It collects replies
// until the window closes, or earlier ONLY when the replies agree: every reply so far claims the
// same cluster size and that many distinct servers have answered. A fan-out request must not be a
// plain Request, which returns the FIRST reply and would learn one server of three.
//
// 🔴 THE CLAIM IS NOT TRUSTED TO END THE WAIT ALONE. active_servers is each server's own count of
// the cluster, and a server that has just booted counts few. "The largest claim so far has been
// met" therefore ended the wait on the first reply of a freshly started pod (claiming one) and
// returned one server of three. Disagreeing claims mean somebody has not seen the cluster yet, so
// the wait runs to the end of the window.
func NewSystemGather(nc *nats.Conn, window time.Duration) IssuerPinGather {
	if window <= 0 {
		window = DefaultIssuerPinGatherWindow
	}
	return func(ctx context.Context) ([]string, error) {
		// Bounded by the window as well as the caller's context: a flush on a wedged connection
		// must not outlast the connect this runs inside.
		fctx, cancel := context.WithTimeout(ctx, window+time.Second)
		defer cancel()
		inbox := nc.NewRespInbox()
		sub, err := nc.SubscribeSync(inbox)
		if err != nil {
			return nil, fmt.Errorf("subscribing for server replies: %w", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
		// Flushed before publishing: an unflushed subscription may register after the servers
		// have answered, and every reply would be dropped, reading as an empty cluster.
		if err := nc.FlushWithContext(fctx); err != nil {
			return nil, fmt.Errorf("flushing the reply subscription: %w", err)
		}
		if err := nc.PublishRequest(issuerPinSysPing, inbox, []byte("{}")); err != nil {
			return nil, fmt.Errorf("publishing the server ping: %w", err)
		}
		deadline := time.Now().Add(window)
		seen := make(map[string]struct{})
		ids := make([]string, 0, 3)
		claim, agreed := 0, true
		for ctx.Err() == nil {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			msg, err := sub.NextMsg(remaining)
			if err != nil {
				break
			}
			var r statszServer
			if json.Unmarshal(msg.Data, &r) != nil || !nkeys.IsValidPublicServerKey(r.Server.ID) {
				continue
			}
			if _, dup := seen[r.Server.ID]; !dup {
				seen[r.Server.ID] = struct{}{}
				ids = append(ids, r.Server.ID)
			}
			if claim == 0 {
				claim = r.Statsz.ActiveServers
			} else if r.Statsz.ActiveServers != claim {
				agreed = false
			}
			if agreed && claim > 1 && len(ids) >= claim {
				// Every reply agrees on the cluster size and that many have answered. A claim of one
				// is never enough to stop: it is what a server that has just booted says, and the
				// first reply of a lone server is indistinguishable from it. A genuinely single
				// server costs the window.
				break
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("no server answered %s within %s", issuerPinSysPing, window)
		}
		return ids, nil
	}
}
