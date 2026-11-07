// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package credcheck is the synchronous credential check HTTP ingest runs before it answers 202:
// it asks device-management whether the credential an event carries would authenticate, so a
// credential that never will is answered 401 at the door instead of being accepted and
// dead-lettered later where only an operator looks.
//
// The package is written around three constraints, each one a way the obvious version is wrong.
//
//   - NO PER-REQUEST ROUND TRIP. A verdict is remembered, keyed by a keyed digest of everything
//     the verdict depends on, so a device posting every few seconds costs one call per TTL, not
//     one per event. The cache is not an authority: device-management re-authenticates every event
//     at the resolver, so a stale ACCEPTED costs exactly what the door cost before this check
//     existed, and a stale REJECTED costs a 401 for at most the rejected TTL.
//   - A VERDICT THAT REACHES THE SENDER IS AN ORACLE. Before this check a wrong credential and a
//     right one were both answered 202, so the door could not be used to guess a secret. A 401 can.
//     So a credential that keeps failing is no longer asked about: after FreeFailures rejections
//     the next attempts on that credential are answered UNKNOWN (the door falls back to 202 and
//     the resolver decides, as it always did) for an exponentially growing hold, and the table of
//     credentials that failed is bounded, with a full table also answering UNKNOWN. The result is
//     that a guesser sees 401 a handful of times and then nothing it can tell right from wrong by.
//   - FAIL OPEN. Every inability to get an answer (device-management unreachable, slow, busy, a
//     bound reached) is UNKNOWN, which the door treats as "no opinion": the event is accepted as it
//     was before and the resolver decides. A check that refused on its own failure would make
//     device-management a hard dependency of every device's write path.
package credcheck

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Verdict is the answer to "would this credential authenticate?".
type Verdict uint8

const (
	// Unknown: no opinion. The caller accepts the event as it did before this check existed.
	Unknown Verdict = iota
	// Accepted: the credential authenticates (or the policy asks for none).
	Accepted
	// Rejected: the credential does not authenticate. The caller answers 401.
	Rejected
)

// Presented is what an event's body carries. Secret and Id are the device's own bytes.
type Presented struct {
	Device string
	Type   string
	Id     string
	Secret string
}

// Backend asks device-management. An error says nothing about the credential.
type Backend interface {
	Check(ctx context.Context, tenant string, p Presented) (accepted bool, err error)
}

// Config tunes the verifier. A zero field takes its default; none of them can disable a bound.
type Config struct {
	// AcceptedTTL is how long an ACCEPTED verdict is reused. Default 30s.
	AcceptedTTL time.Duration
	// RejectedTTL is how long a REJECTED verdict is reused. Kept short: a device whose operator
	// has just fixed its credential should not wait long. Default 10s.
	RejectedTTL time.Duration
	// Entries bounds the verdict cache. Default 100,000.
	Entries int
	// FreeFailures is how many rejections one credential may collect before it is held off.
	// Default 5.
	FreeFailures int
	// MaxHold caps the hold-off. Default 60s.
	MaxHold time.Duration
	// FailureEntries bounds the table of credentials that have failed. Default 50,000.
	FailureEntries int
	// MaxInFlight bounds concurrent calls to the backend; a request over it is UNKNOWN.
	// Default 32.
	MaxInFlight int
	// Timeout bounds one call. Default 400ms: the door's tail latency is not worth a slow
	// dependency.
	Timeout time.Duration
	// Now supplies the time, for tests.
	Now func() time.Time
	// OnCall is told of each backend call and its outcome ("accepted", "rejected", "error").
	// May be nil.
	OnCall func(outcome string)
	// OnSkip is told of each check that was not made, with a reason: "busy" (the in-flight
	// bound), "held" (the credential is in hold-off), "table_full" (the failure table is at its
	// bound). May be nil.
	OnSkip func(reason string)
}

func (c Config) withDefaults() Config {
	if c.AcceptedTTL <= 0 {
		c.AcceptedTTL = 30 * time.Second
	}
	if c.RejectedTTL <= 0 {
		c.RejectedTTL = 10 * time.Second
	}
	if c.Entries <= 0 {
		c.Entries = 100_000
	}
	if c.FreeFailures <= 0 {
		c.FreeFailures = 5
	}
	if c.MaxHold <= 0 {
		c.MaxHold = 60 * time.Second
	}
	if c.FailureEntries <= 0 {
		c.FailureEntries = 50_000
	}
	if c.MaxInFlight <= 0 {
		c.MaxInFlight = 32
	}
	if c.Timeout <= 0 {
		c.Timeout = 400 * time.Millisecond
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type key = [sha256.Size]byte

type verdictEntry struct {
	accepted bool
	at       time.Time
}

type failureEntry struct {
	count int
	last  time.Time
}

// Verifier is the cached, throttled check. It is safe for concurrent use.
type Verifier struct {
	cfg     Config
	backend Backend
	// salt keys every digest below. It is random per process and never leaves it, so a digest
	// neither reveals the secret it covers nor can be computed by anyone else.
	salt []byte
	sem  chan struct{}
	sf   singleflight.Group

	mu       sync.Mutex
	verdicts map[key]verdictEntry
	failures map[key]failureEntry
}

// New builds a verifier over backend.
func New(backend Backend, cfg Config) (*Verifier, error) {
	cfg = cfg.withDefaults()
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return &Verifier{
		cfg:      cfg,
		backend:  backend,
		salt:     salt,
		sem:      make(chan struct{}, cfg.MaxInFlight),
		verdicts: make(map[key]verdictEntry),
		failures: make(map[key]failureEntry),
	}, nil
}

// digest is the HMAC of the length-prefixed fields, so ("ab","c") and ("a","bc") differ.
func (v *Verifier) digest(fields ...string) key {
	m := hmac.New(sha256.New, v.salt)
	var n [4]byte
	for _, f := range fields {
		n[0], n[1], n[2], n[3] = byte(len(f)>>24), byte(len(f)>>16), byte(len(f)>>8), byte(len(f))
		m.Write(n[:])
		m.Write([]byte(f))
	}
	var out key
	m.Sum(out[:0])
	return out
}

// Verify returns the verdict for an event's credential in a tenant.
func (v *Verifier) Verify(ctx context.Context, tenant string, p Presented) Verdict {
	now := v.cfg.Now()
	full := v.digest(tenant, p.Device, p.Type, p.Id, p.Secret)
	if e, ok := v.cached(full, now); ok {
		return verdictOf(e.accepted)
	}
	// The failure key leaves the secret out: it is the CREDENTIAL that is being guessed at.
	who := v.digest(tenant, p.Type, p.Id)
	if v.held(who, now) {
		v.skip("held")
		return Unknown
	}
	res, err, _ := v.sf.Do(string(full[:]), func() (any, error) {
		select {
		case v.sem <- struct{}{}:
		default:
			return nil, errBusy
		}
		defer func() { <-v.sem }()
		// Detached from the request: a shared call must not be cancelled by the caller that
		// happened to start it, and the timeout is the bound.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v.cfg.Timeout)
		defer cancel()
		return v.backend.Check(cctx, tenant, p)
	})
	if err != nil {
		if err == errBusy {
			v.skip("busy")
		} else if v.cfg.OnCall != nil {
			v.cfg.OnCall("error")
		}
		return Unknown
	}
	accepted := res.(bool)
	if v.cfg.OnCall != nil {
		if accepted {
			v.cfg.OnCall("accepted")
		} else {
			v.cfg.OnCall("rejected")
		}
	}
	end := v.cfg.Now()
	if accepted {
		v.remember(full, verdictEntry{accepted: true, at: end}, who, true)
		return Accepted
	}
	if !v.recordFailure(who, end) {
		// The failure table is full, so this credential cannot be counted, and a credential that
		// cannot be counted cannot be throttled. It is not answered.
		v.skip("table_full")
		return Unknown
	}
	v.remember(full, verdictEntry{accepted: false, at: end}, who, false)
	return Rejected
}

type errString string

func (e errString) Error() string { return string(e) }

const errBusy = errString("credcheck: at the in-flight bound")

func (v *Verifier) skip(reason string) {
	if v.cfg.OnSkip != nil {
		v.cfg.OnSkip(reason)
	}
}

func verdictOf(accepted bool) Verdict {
	if accepted {
		return Accepted
	}
	return Rejected
}

func (v *Verifier) ttl(accepted bool) time.Duration {
	if accepted {
		return v.cfg.AcceptedTTL
	}
	return v.cfg.RejectedTTL
}

func (v *Verifier) cached(k key, now time.Time) (verdictEntry, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.verdicts[k]
	if !ok {
		return verdictEntry{}, false
	}
	if now.Sub(e.at) >= v.ttl(e.accepted) {
		delete(v.verdicts, k)
		return verdictEntry{}, false
	}
	return e, true
}

// remember stores a verdict, and on a success clears the credential's failure record: a
// credential that authenticates is not being guessed at.
func (v *Verifier) remember(k key, e verdictEntry, who key, success bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.verdicts[k]; !ok && len(v.verdicts) >= v.cfg.Entries {
		// Expired entries first; if that frees nothing, drop arbitrary ones. The cache is a
		// saving, never a correctness path, so any entry may go.
		for old, oe := range v.verdicts {
			if e.at.Sub(oe.at) >= v.ttl(oe.accepted) {
				delete(v.verdicts, old)
			}
		}
		for old := range v.verdicts {
			if len(v.verdicts) < v.cfg.Entries {
				break
			}
			delete(v.verdicts, old)
		}
	}
	v.verdicts[k] = e
	if success {
		delete(v.failures, who)
	}
}

// hold is how long a credential with count failures is held off: nothing while it has fewer than
// FreeFailures, then one second, doubling with each further failure up to MaxHold.
func (v *Verifier) hold(count int) time.Duration {
	over := count - v.cfg.FreeFailures + 1
	if over <= 0 {
		return 0
	}
	if over > 16 {
		return v.cfg.MaxHold
	}
	d := time.Second << uint(over-1)
	if d > v.cfg.MaxHold {
		d = v.cfg.MaxHold
	}
	return d
}

func (v *Verifier) held(who key, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, ok := v.failures[who]
	if !ok {
		return false
	}
	if now.Sub(f.last) >= v.cfg.MaxHold*2 {
		// A record that has outlived its longest hold is forgotten, which is also what bounds
		// the table against credentials that failed once and never came back.
		delete(v.failures, who)
		return false
	}
	return now.Sub(f.last) < v.hold(f.count)
}

// recordFailure counts a rejection and reports whether it could be counted.
func (v *Verifier) recordFailure(who key, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	f, ok := v.failures[who]
	if !ok && len(v.failures) >= v.cfg.FailureEntries {
		for k, old := range v.failures {
			if now.Sub(old.last) >= v.cfg.MaxHold*2 {
				delete(v.failures, k)
			}
		}
		if len(v.failures) >= v.cfg.FailureEntries {
			return false
		}
	}
	f.count++
	f.last = now
	v.failures[who] = f
	return true
}
