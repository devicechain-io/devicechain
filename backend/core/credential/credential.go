// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package credential is the one place a presented secret is compared against a stored
// bcrypt hash, and the one place that comparison is rate-limited.
//
// # Why one primitive
//
// A password guess costs the server a bcrypt compare. Before this package, the login
// mutation and the OAuth token endpoint each ran their own compare with nothing counting
// attempts, so an attacker could guess as fast as the server could hash — and, through
// GraphQL aliases, ~1,500 guesses in one request. Putting the compare and the limiter in
// ONE function means no call path can reach the first without the second: there is
// nothing to remember at a new call site, because there is no other compare to call.
// hack/check-credential-compare.sh fails the build if production code outside this
// package names bcrypt.CompareHashAndPassword.
//
// # The policy: backoff per principal, no hard lockout
//
// Every failed check on a principal (an email, an OAuth client_id) increases the delay
// before that principal's next attempt is EVALUATED, doubling up to a cap; a success
// resets it. There is no lockout: at the cap the owner can still sign in, a few times an
// hour. The state lives in NATS KV so every replica of the service sees the same count.
//
// # Existence does not leak
//
// The key is derived from the PRESENTED identifier, and an unknown principal runs the
// identical sequence — read, charge, lookup, compare (against a dummy hash) — so it is
// throttled on exactly the same schedule as a real one. A fast "throttled" reply reveals
// only throttle state the caller built up themselves, and it looks the same whether the
// account exists or not.
package credential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

// Store is the subset of nats.KeyValue the Checker uses. nats.KeyValue satisfies it;
// credentialtest.Store is an in-memory fake with the same compare-and-set semantics.
type Store interface {
	Get(key string) (nats.KeyValueEntry, error)
	Create(key string, value []byte) (uint64, error)
	Update(key string, value []byte, last uint64) (uint64, error)
	Delete(key string, opts ...nats.DeleteOpt) error
}

// Kind is what sort of principal a secret authenticates. Each kind has its own policy.
type Kind string

const (
	// KindIdentity is a human signing in with an email and password. ID is the
	// normalized email.
	KindIdentity Kind = "identity"
	// KindOAuthClient is an OAuth client authenticating with its client secret. ID is
	// the client_id.
	KindOAuthClient Kind = "oauth-client"
)

// Principal names who is being authenticated. ID is the PRESENTED identifier, whether
// or not it names anything that exists — that is what keeps existence from leaking.
type Principal struct {
	Kind Kind
	ID   string
}

// Policy is one kind's backoff schedule.
//
// Charges are counted per principal since its last success. The first Free charges
// carry no delay, so Free attempts are evaluated back to back. The Free-th charge sets
// a delay of Base before the next attempt may be evaluated, and each charge after it
// doubles that delay, up to Cap:
//
//	attempt:  1 … Free-1   Free   Free+1   Free+2   …
//	delay:    0            Base   2·Base   4·Base   … Cap
//
// The delay is measured from the moment the attempt was charged, which is BEFORE its
// compare ran (see Check).
type Policy struct {
	Free int
	Base time.Duration
	Cap  time.Duration
}

// AttemptTTL is how long a principal's record outlives its last write — the attempt
// bucket's TTL. A principal that stays quiet this long starts again from attempt 1.
//
// 🔴 IT MUST BE WELL ABOVE EVERY POLICY'S Cap, and validate enforces twice. A record is
// rewritten only when an attempt is charged, so the longest silence an escalating
// principal's record must survive is one full delay. A TTL at or under the cap would
// let the record expire DURING the delay it is enforcing, and an attacker who simply
// waited the cap out would find the count back at zero — the limiter would never climb
// past its first capped step.
//
// It is also kept short, because the attempt is charged before the account is looked
// up, so every presented identifier — real or not — costs one entry for this long. How
// many entries fit is in the bucket's declaration (kv.BucketCredentialAttempts).
const AttemptTTL = 10 * time.Minute

func (p Policy) validate() error {
	if p.Free < 1 {
		return fmt.Errorf("Free must be at least 1, got %d", p.Free)
	}
	if p.Base <= 0 || p.Cap < p.Base {
		return fmt.Errorf("need 0 < Base <= Cap, got Base=%s Cap=%s", p.Base, p.Cap)
	}
	if 2*p.Cap > AttemptTTL {
		return fmt.Errorf("Cap %s is more than half the record TTL %s, so a record could expire "+
			"during the delay it enforces", p.Cap, AttemptTTL)
	}
	return nil
}

// delay is the wait imposed by the n-th charge (1-based).
func (p Policy) delay(n int) time.Duration {
	if n < p.Free {
		return 0
	}
	shift := n - p.Free
	// Past ~62 doublings the shift overflows; anything that far out is the cap anyway.
	if shift >= 62 || p.Base > time.Duration(math.MaxInt64>>shift) {
		return p.Cap
	}
	d := p.Base << shift
	if d > p.Cap {
		return p.Cap
	}
	return d
}

// ErrMismatch is returned when the secret does not match — or when the principal does
// not exist, which is deliberately indistinguishable.
var ErrMismatch = errors.New("credential: secret does not match")

// ErrUnavailable wraps a failure of the attempt store. The check FAILS CLOSED: an
// attempt that cannot be counted is not evaluated. Failing open would let anyone who
// can degrade NATS switch the limiter off, and the services that use this already
// depend on NATS for sign-in (their refresh-token store is a KV bucket), so failing
// closed costs no availability they had.
var ErrUnavailable = errors.New("credential: the attempt store is unavailable")

// ThrottledError is returned when a principal's next attempt is not yet allowed. The
// attempt was NOT evaluated: no lookup, no compare, no charge.
//
// It is reported DISTINCTLY rather than as a mismatch, and that is safe because it
// reveals nothing about the account or the secret — the attempt was never evaluated,
// and an unknown principal is throttled on the same schedule as a real one. Reporting
// it as "invalid credentials" would do harm: during a delay the real owner, typing the
// CORRECT password, would be told it is wrong and pushed toward a needless reset.
type ThrottledError struct {
	RetryAfter time.Duration
}

// RetryAfterSeconds is the delay rounded UP to whole seconds, and never below 1, so a
// client that waits exactly that long is not throttled again by rounding.
func (e *ThrottledError) RetryAfterSeconds() int {
	s := int(math.Ceil(e.RetryAfter.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}

func (e *ThrottledError) Error() string {
	return "too many failed sign-in attempts; try again in " + e.RetryAfterText()
}

// RetryAfterText is the delay as prose, "1 second" or "N seconds", for a message a
// person reads.
func (e *ThrottledError) RetryAfterText() string {
	if s := e.RetryAfterSeconds(); s != 1 {
		return fmt.Sprintf("%d seconds", s)
	}
	return "1 second"
}

// Extensions makes a GraphQL error carrying this one machine-readable: graphql-go copies
// it onto the error's `extensions`, so a client branches on the code rather than on the
// prose.
func (e *ThrottledError) Extensions() map[string]any {
	return map[string]any{"code": CodeThrottled, "retryAfterSeconds": e.RetryAfterSeconds()}
}

// The GraphQL extension codes a credential failure can carry.
const (
	CodeThrottled   = "THROTTLED"
	CodeUnavailable = "UNAVAILABLE"
)

// UnavailableError is how Check reports ErrUnavailable (errors.Is matches it). It
// carries an extension code so a GraphQL client can tell "the server could not check"
// from "the password is wrong" — otherwise every NATS outage would read as a wrong
// password, and push users toward resets for the same reason a throttled attempt would.
type UnavailableError struct{ Err error }

// unavailable wraps a store failure as ErrUnavailable.
func unavailable(format string, args ...any) error {
	return &UnavailableError{Err: fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, args...))}
}

func (e *UnavailableError) Error() string {
	return "sign-in is temporarily unavailable; try again shortly"
}
func (e *UnavailableError) Unwrap() error { return e.Err }
func (e *UnavailableError) Extensions() map[string]any {
	return map[string]any{"code": CodeUnavailable}
}

// Outcome labels for the checks counter.
const (
	OutcomeSuccess     = "success"
	OutcomeMismatch    = "mismatch"
	OutcomeThrottled   = "throttled"
	OutcomeUnavailable = "unavailable"
	// OutcomeError is an admitted attempt whose lookup failed — a database error, not a
	// decision about the secret.
	OutcomeError = "error"
)

// casAttempts bounds the compare-and-set retries on one principal's record.
const casAttempts = 3

// contendedRetry is what a caller is told when its charge lost the compare-and-set
// race casAttempts times: concurrent attempts on one principal are exactly what the
// limiter exists to slow down.
const contendedRetry = time.Second

// Checker compares secrets against bcrypt hashes behind a per-principal backoff.
type Checker struct {
	store    Store
	policies map[Kind]Policy
	dummy    []byte
	now      func() time.Time
	checks   *prometheus.CounterVec
}

// Option configures a Checker.
type Option func(*Checker)

// WithClock replaces the wall clock, so a test can step time instead of sleeping.
func WithClock(now func() time.Time) Option { return func(c *Checker) { c.now = now } }

// WithCounter records every check's outcome on a counter labelled {kind, outcome}.
//
// It is the only view of an attack held at the cap: a throttled attempt writes no audit
// row (so an attacker cannot drive database writes with it), which leaves this counter
// and the unavailable outcome as the signals.
func WithCounter(c *prometheus.CounterVec) Option { return func(ch *Checker) { ch.checks = c } }

// dummySecret is hashed at construction so an unknown principal pays a real compare.
const dummySecret = "dc-credential-timing-equalizer"

// NewChecker builds a Checker. A nil store and a kind without a valid policy are
// refused: a checker that could not count attempts would be the unthrottled compare
// this package exists to remove.
func NewChecker(store Store, policies map[Kind]Policy, opts ...Option) (*Checker, error) {
	if store == nil {
		return nil, errors.New("credential: a Checker needs an attempt store")
	}
	for _, k := range []Kind{KindIdentity, KindOAuthClient} {
		p, ok := policies[k]
		if !ok {
			return nil, fmt.Errorf("credential: no policy for kind %q", k)
		}
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("credential: policy for kind %q: %w", k, err)
		}
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(dummySecret), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	c := &Checker{store: store, policies: policies, dummy: dummy, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// record is one principal's stored state.
type record struct {
	// Failures counts charges since the last success, INCLUDING an attempt whose
	// compare is still running.
	Failures int `json:"f"`
	// NotBefore is when the next attempt may be evaluated (Unix milliseconds).
	NotBefore int64 `json:"nb"`
}

// Key is the attempt-store key for a principal. The identifier is hashed because a KV
// key has a restricted grammar an email does not fit, and so that no plaintext email or
// client_id is written to NATS. The kind is inside the hash as well as in front of it,
// so two kinds with the same identifier can never share a record.
func Key(p Principal) string {
	sum := sha256.Sum256([]byte(string(p.Kind) + "\x00" + p.ID))
	return string(p.Kind) + "." + hex.EncodeToString(sum[:])
}

// Check authenticates secret for principal p.
//
// lookup returns the stored bcrypt hash, or "" when the principal does not exist or is
// not allowed to authenticate — in which case a dummy hash is compared instead, so the
// two cost the same. lookup is called only for an attempt that is admitted.
//
// It returns nil on a match, ErrMismatch on a mismatch, a *ThrottledError when the
// attempt is not yet allowed, a lookup error unchanged, and an *UnavailableError
// (matching ErrUnavailable) when the attempt store cannot be read or written.
//
// 🔴 THE ATTEMPT IS CHARGED BEFORE ITS COMPARE RUNS. Counting only on failure leaves a
// hole the size of the compare: N requests arriving together all read "no failures
// yet", all pass admission, and all run. Charging first means each admitted attempt
// has already moved the schedule for the next one, so concurrent requests cannot all
// get in — and an attempt that crashes mid-compare still counts. A success undoes the
// charge by deleting the record.
func (c *Checker) Check(ctx context.Context, p Principal, secret string, lookup func(context.Context) (string, error)) error {
	err := c.check(ctx, p, secret, lookup)
	c.observe(p.Kind, err)
	return err
}

func (c *Checker) check(ctx context.Context, p Principal, secret string, lookup func(context.Context) (string, error)) error {
	policy, ok := c.policies[p.Kind]
	if !ok {
		return fmt.Errorf("credential: no policy for kind %q", p.Kind)
	}
	key := Key(p)

	if err := c.admit(key, policy); err != nil {
		return err
	}

	hash, err := lookup(ctx)
	if err != nil {
		// The charge stands: a lookup that fails is not a free attempt.
		return err
	}
	stored := []byte(hash)
	if hash == "" {
		stored = c.dummy
	}
	if bcrypt.CompareHashAndPassword(stored, []byte(secret)) != nil || hash == "" {
		return ErrMismatch
	}

	if err := c.store.Delete(key); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		// The secret matched, so the caller authenticates. The record that failed to
		// clear only errs toward a delay on this principal's next failure, and it
		// expires with the bucket's TTL.
		log.Warn().Err(err).Str("kind", string(p.Kind)).
			Msg("Could not reset a credential attempt record after a successful check.")
	}
	return nil
}

// admit reads the principal's record, refuses the attempt while its delay is running,
// and otherwise charges it by compare-and-set.
func (c *Checker) admit(key string, policy Policy) error {
	for i := 0; i < casAttempts; i++ {
		now := c.now()
		var rec record
		var rev uint64
		entry, err := c.store.Get(key)
		switch {
		case err == nil:
			if jerr := json.Unmarshal(entry.Value(), &rec); jerr != nil {
				// A record this code did not write is not trusted to mean "no failures".
				return unavailable("unreadable attempt record: %v", jerr)
			}
			rev = entry.Revision()
		case errors.Is(err, nats.ErrKeyNotFound):
			// First attempt, or the record expired, or a success cleared it.
		default:
			return unavailable("%v", err)
		}

		if wait := time.UnixMilli(rec.NotBefore).Sub(now); wait > 0 {
			return &ThrottledError{RetryAfter: wait}
		}

		next := record{Failures: rec.Failures + 1}
		next.NotBefore = now.Add(policy.delay(next.Failures)).UnixMilli()
		body, _ := json.Marshal(next)
		if rev == 0 {
			_, err = c.store.Create(key, body)
		} else {
			_, err = c.store.Update(key, body, rev)
		}
		switch {
		case err == nil:
			return nil
		case errors.Is(err, nats.ErrKeyExists), errors.Is(err, nats.ErrKeyRevisionMismatch):
			// Another attempt on this principal charged first; read again.
			continue
		default:
			return unavailable("%v", err)
		}
	}
	return &ThrottledError{RetryAfter: contendedRetry}
}

func (c *Checker) observe(kind Kind, err error) {
	if c.checks == nil {
		return
	}
	outcome := OutcomeSuccess
	var throttled *ThrottledError
	switch {
	case err == nil:
	case errors.As(err, &throttled):
		outcome = OutcomeThrottled
	case errors.Is(err, ErrUnavailable):
		outcome = OutcomeUnavailable
	case errors.Is(err, ErrMismatch):
		outcome = OutcomeMismatch
	default:
		outcome = OutcomeError
	}
	c.checks.WithLabelValues(string(kind), outcome).Inc()
}
