// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package platformtest is a stand-in for the platform side of an update, for tests only.
//
// It runs the REAL update reducer from the core contract package (Apply, Tick, Cancel,
// Reconcile) against whatever a simulated device sends, over an in-memory Link. It is not a
// service and has no storage: one Platform holds one attempt. Nothing outside tests should import
// it; it lives under internal/ in a non-test file so that tests in more than one package of this
// module can share it and no other module can import it.
package platformtest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
)

// Clock is a manually advanced clock, so deadlines are tested without sleeping.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{t: t} }

// Now returns the current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Delivery is the record of one frame the platform received.
type Delivery struct {
	Kind    string
	Seq     uint64
	Stage   string
	Verdict ota.Verdict // empty when the frame did not decode
	Err     error
}

// Platform holds one update attempt and applies what the device reports to it.
type Platform struct {
	mu       sync.Mutex
	clock    *Clock
	policy   ota.Policy
	attempt  ota.Attempt
	history  []Delivery
	counts   map[ota.Verdict]int
	rejected int
}

// New returns a platform tracking attempt.
func New(clock *Clock, policy ota.Policy, attempt ota.Attempt) *Platform {
	return &Platform{clock: clock, policy: policy, attempt: attempt, counts: map[ota.Verdict]int{}}
}

// Attempt returns the platform's current view of the attempt.
func (p *Platform) Attempt() ota.Attempt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempt
}

// Receive decodes one frame the way the platform would and applies it. A frame that does not
// decode is rejected (counted, never applied), as is a frame of a kind the reducer does not
// take. The verdict is empty only when the frame did not decode.
func (p *Platform) Receive(payload []byte) (ota.Verdict, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rep, err := ota.DecodeReport(payload)
	if err != nil {
		p.rejected++
		p.history = append(p.history, Delivery{Err: err})
		return "", err
	}
	var v ota.Verdict
	switch rep.Kind {
	case ota.KindProgress:
		p.attempt, v = ota.Apply(p.attempt, rep, p.clock.Now())
	case ota.KindInventory:
		p.attempt, v = ota.Reconcile(p.attempt, rep, p.clock.Now())
	default:
		v = ota.VerdictWrongKind
	}
	p.counts[v]++
	p.history = append(p.history, Delivery{Kind: rep.Kind, Seq: rep.Seq, Stage: string(rep.Stage), Verdict: v})
	return v, nil
}

// Tick applies the deadlines at the clock's current time and reports whether anything moved.
func (p *Platform) Tick() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next, changed, err := ota.Tick(p.attempt, p.policy, p.clock.Now())
	if err != nil {
		return false, err
	}
	p.attempt = next
	return changed, nil
}

// RequestCancel asks the reducer to cancel the attempt.
func (p *Platform) RequestCancel() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next, err := ota.Cancel(p.attempt, p.clock.Now())
	if err != nil {
		return err
	}
	p.attempt = next
	return nil
}

// Count returns how many frames drew the verdict.
func (p *Platform) Count(v ota.Verdict) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[v]
}

// Rejected returns how many frames failed to decode.
func (p *Platform) Rejected() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rejected
}

// History returns every frame received, in order.
func (p *Platform) History() []Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Delivery(nil), p.history...)
}

// Link errors.
var (
	// ErrLinkDown is returned by Send while the link is down: the frame was not delivered.
	ErrLinkDown = errors.New("platformtest: link is down")
	// ErrAckLost is returned by Send when the frame WAS delivered but the sender is told it
	// was not, so the sender will send it again.
	ErrAckLost = errors.New("platformtest: acknowledgement lost")
)

// Link is the in-memory transport between a device and a Platform. It satisfies the updater's
// Transport interface structurally.
type Link struct {
	mu       sync.Mutex
	p        *Platform
	down     bool
	loseAcks int
	frames   [][]byte
}

// NewLink connects a device to p.
func NewLink(p *Platform) *Link { return &Link{p: p} }

// SetDown takes the link down (frames are refused) or brings it back up.
func (l *Link) SetDown(down bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.down = down
}

// LoseNextAcks delivers the next n frames but reports each as a failed send.
func (l *Link) LoseNextAcks(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loseAcks = n
}

// Send delivers a frame to the platform. The sender learns nothing of the verdict.
func (l *Link) Send(_ context.Context, payload []byte) error {
	l.mu.Lock()
	if l.down {
		l.mu.Unlock()
		return ErrLinkDown
	}
	l.frames = append(l.frames, append([]byte(nil), payload...))
	lose := l.loseAcks > 0
	if lose {
		l.loseAcks--
	}
	l.mu.Unlock()

	_, _ = l.p.Receive(payload)
	if lose {
		return ErrAckLost
	}
	return nil
}

// Frames returns every frame delivered so far, in order.
func (l *Link) Frames() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.frames...)
}

// Decoded returns the delivered frames as generic maps, for tests that assert on fields.
func (l *Link) Decoded() []map[string]any {
	var out []map[string]any
	for _, f := range l.Frames() {
		m := map[string]any{}
		if err := json.Unmarshal(f, &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}
