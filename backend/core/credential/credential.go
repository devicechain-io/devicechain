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
// ONE function means no call path can reach the first without its kind's policy: there
// is nothing to remember at a new call site, because there is no other compare to call.
// hack/check-credential-compare.sh fails the build if production code outside this
// package names bcrypt.CompareHashAndPassword, subtle.ConstantTimeCompare or hmac.Equal
// without an exemption for that function and that member, stating why the compare is
// not a guessable credential check; every run prints the exemptions it honoured.
//
// # The policy: backoff per principal
//
// Each Kind has its own Policy. Under a throttled one, every failed check on a principal
// (for example an email) increases the delay before that principal's next attempt is
// EVALUATED, doubling up to a cap; a success resets it. The state lives in NATS KV so
// every replica of the service sees the same count. An Unthrottled kind is compared
// with no delay and no record at all.
//
// # The cost: a targeted lockout
//
// 🔴 THE THROTTLE IS KEYED ON WHAT THE CALLER PRESENTS, AND A THROTTLED ATTEMPT IS FREE.
// A refused attempt costs one KV read — no charge, no compare, no audit row — so anyone
// who knows a principal's identifier can keep polling it and take the first evaluation
// slot each time a delay expires, sending a wrong secret. While they keep that up the
// real owner is refused as throttled, even with the correct secret: for the attacker's
// target it IS a lockout, lasting as long as the attack. There is no hard lockout that
// outlives the attack, and nothing locks an account nobody is attacking.
//
// That is the price of a per-principal key, which is what slows a guesser spread over
// many sources. A kind whose secret cannot be guessed has nothing to buy with that
// price, so it should be Unthrottled rather than hand anyone a way to lock it out —
// user-management's OAuth client secrets are (identity.CredentialPolicies).
//
// # A budget per request
//
// Separately from the per-principal backoff, every check spends one unit of the
// request's credential-check budget (WithRequestBudget) before it does anything else,
// and is refused with a *RequestBudgetError when none is left. That bounds the bcrypt
// compares ONE REQUEST can buy — however many aliased sign-ins its document carries,
// and whatever the GraphQL layer's reading of that document — and it is refused before
// the principal is looked at, so it says nothing about any account. core/graphql
// installs the budget on every execution; budget.go has the rule for a context
// without one.
//
// # Existence does not leak
//
// The key is derived from the PRESENTED identifier, and an unknown principal runs the
// identical sequence — read, charge, lookup, compare (against a dummy hash of the same
// cost; an Unthrottled kind skips only the read and the charge) — so it is throttled on
// exactly the same schedule as a real one. A fast "throttled" reply reveals
// only throttle state the caller built up themselves, and it looks the same whether the
// account exists or not.
//
// # A full store fails OPEN; an unreachable one fails closed
//
// The attempt bucket is size-bounded, and every presented identifier of a throttled
// kind costs an entry — real or not — so anyone who sprays enough distinct identifiers
// can fill it. If a full bucket refused sign-in, that spray would take sign-in down for
// EVERY account on the instance, and cheaply: on the smallest preset, a few dozen
// requests a second. So a charge that JetStream refuses because the bucket is at its
// byte ceiling is not a refusal of the attempt. The attempt is evaluated WITHOUT its
// backoff — looked up and compared exactly as an admitted one would be, charged nothing
// — and counted as OutcomeStoreFull, which the chart alerts on. Losing the backoff is
// the smaller failure: guessing stays bounded by the per-request work limit and the
// bcrypt cost of every compare.
//
// 🔴 WHO LOSES THE BACKOFF IS EVERY PRINCIPAL NOT INSIDE A RUNNING DELAY, not only new
// ones. A replicated bucket checks its byte ceiling before proposing a write, counting
// the bucket's total with no allowance for a message that replaces one under the same
// key, so when full it refuses every Update and Delete as well as every Create. A
// single-server bucket lets a same-size replacement through, but refuses one that grows
// the record, which leaves that record frozen and uncharged. What does still hold is a
// delay already running: reading a record needs no space, so its attempts are refused
// until that delay ends — at most the policy's Cap. After that the principal is
// unthrottled like any other for as long as the store stays full. An account under
// attack while the store is full is therefore NOT protected; the alert is what reports it.
//
// Only that one condition fails open (see storeFull). Every other store failure — the
// broker unreachable, a timeout, a record this code cannot parse — still fails closed
// as ErrUnavailable: nobody can cause those by sending sign-in requests, and failing
// open on them would hand the limiter's off switch to anyone who can degrade NATS.
package credential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
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
// Unthrottled, set ALONE, means the kind is not rate-limited: its secret is compared
// (and an unknown principal's against the dummy, at the same cost) but no attempt is
// read, charged or recorded. It is a field of its own rather than a zero schedule so a
// Policy{} left unset is refused at construction instead of meaning "off".
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

	Unthrottled bool
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
// up, so every presented identifier of a throttled kind — real or not — costs one entry
// for this long. How
// many entries fit is in the bucket's declaration (kv.BucketCredentialAttempts).
const AttemptTTL = 10 * time.Minute

func (p Policy) validate() error {
	if p.Unthrottled {
		if p.Free != 0 || p.Base != 0 || p.Cap != 0 {
			return fmt.Errorf("an Unthrottled policy carries no schedule, got Free=%d Base=%s Cap=%s",
				p.Free, p.Base, p.Cap)
		}
		return nil
	}
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
//
// 🔴 A FULL STORE IS THE ONE EXCEPTION, and it fails OPEN instead (see the package
// doc and storeFull): unlike an outage, fullness is something a sign-in request can
// cause, so failing closed on it would let anyone take sign-in down for everyone.
var ErrUnavailable = errors.New("credential: the attempt store is unavailable")

// errStoreFull is admit's report that the charge was refused because the attempt
// bucket is at its byte ceiling. It never leaves this package: check turns it into
// an attempt evaluated without its backoff.
var errStoreFull = errors.New("credential: the attempt store is full")

// The JetStream error a write to a bucket at its MaxBytes returns. KV buckets are
// DiscardNew streams, so at the ceiling JetStream REFUSES the new message rather than
// evicting an old one, and answers with a *nats.APIError carrying the generic
// "store failed" code whose description is the store's own error text — the server's
// ErrMaxBytes. Observed against a real embedded server (jetstream_test.go):
//
//	&nats.APIError{Code: 503, ErrorCode: 10077, Description: "maximum bytes exceeded"}
//
// The clustered path's pre-proposal limit check (a replicated bucket) builds the same
// error from the same ErrMaxBytes. 10077 is shared by every store refusal — maximum
// messages, a message too large, an I/O failure — so the description is what names
// fullness, and both have to match.
const (
	jsErrCodeStreamStoreFailed nats.ErrorCode = 10077
	jsMaxBytesExceeded                        = "maximum bytes exceeded"
)

// storeFull reports whether err is JetStream refusing a write because the bucket is
// at its byte ceiling — and nothing else.
func storeFull(err error) bool {
	var apiErr *nats.APIError
	return errors.As(err, &apiErr) &&
		apiErr.ErrorCode == jsErrCodeStreamStoreFailed &&
		apiErr.Description == jsMaxBytesExceeded
}

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
	// OutcomeStoreFull is an attempt evaluated WITHOUT its backoff because the attempt
	// store was full: looked up and compared, charged nothing. It replaces the attempt's
	// own success/mismatch/error label, so it counts every attempt the limiter did not
	// govern. Any non-zero rate means the per-account backoff is OFF for every principal
	// not already inside a running delay (the package doc says why that is not only new
	// ones), and the chart alerts on it.
	OutcomeStoreFull = "store_full"
	// OutcomeRequestBudget is a check refused because its request had already spent
	// its credential-check budget (see WithRequestBudget). Nothing was evaluated. A
	// non-zero rate means some request carried more sign-ins than any client sends.
	OutcomeRequestBudget = "request_budget"
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
	// compare is bcrypt.CompareHashAndPassword. It is a field only so a test can
	// observe WHICH hash each check paid for (export_test.go): the dummy compare is
	// the timing equalizer, and an outcome-level test cannot see it being skipped.
	compare func(hash, secret []byte) error

	// fullLog rate-limits the warning a full store logs: during the spray that fills
	// the bucket, every sign-in attempt fails open, and a line per attempt would turn
	// the attack into a log flood as well.
	fullLog struct {
		sync.Mutex
		last       time.Time
		suppressed int
	}
}

// storeFullLogInterval is the most often a full attempt store is logged.
const storeFullLogInterval = time.Minute

// Option configures a Checker.
type Option func(*Checker)

// WithCompareObserver calls seen with the stored hash of every compare the checker
// runs — the dummy included — before running it. It only observes: it cannot change
// what is compared or the result. It exists so a test in another package can count the
// bcrypt compares a request actually paid for, which no outcome or error can show.
func WithCompareObserver(seen func(hash []byte)) Option {
	return func(c *Checker) {
		inner := c.compare
		c.compare = func(hash, secret []byte) error {
			seen(hash)
			return inner(hash, secret)
		}
	}
}

// WithClock replaces the wall clock, so a test can step time instead of sleeping.
func WithClock(now func() time.Time) Option { return func(c *Checker) { c.now = now } }

// WithCounter records every check's outcome on a counter labelled {kind, outcome}.
//
// It is the only view of an attack held at the cap: a throttled attempt writes no audit
// row (so an attacker cannot drive database writes with it), which leaves this counter
// and the unavailable outcome as the signals. It is also the only view of a full attempt
// store (OutcomeStoreFull), which is why NewChecker exports that series at zero for every
// throttled kind: an alert over increase() cannot see a series' FIRST sample, so a
// series created by the first fail-open would hide exactly that one.
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
	c := &Checker{store: store, policies: policies, dummy: dummy, now: time.Now,
		compare: bcrypt.CompareHashAndPassword}
	for _, o := range opts {
		o(c)
	}
	if c.checks != nil {
		for k, p := range policies {
			if !p.Unthrottled {
				c.checks.WithLabelValues(string(k), OutcomeStoreFull)
			}
		}
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
// (matching ErrUnavailable) when the attempt store cannot be read or written — except
// when the write is refused because the store is FULL, in which case the attempt is
// evaluated without its backoff and returns whatever that evaluation returns (see the
// package doc).
//
// 🔴 THE ATTEMPT IS CHARGED BEFORE ITS COMPARE RUNS. Counting only on failure leaves a
// hole the size of the compare: N requests arriving together all read "no failures
// yet", all pass admission, and all run. Charging first means each admitted attempt
// has already moved the schedule for the next one, so concurrent requests cannot all
// get in — and an attempt that crashes mid-compare still counts. A success clears the
// principal's record by deleting it, unconditionally: that also erases any charges
// other attempts made while this one's compare ran. Each of those was still admitted
// and evaluated on its turn; what a success forgives is the count they leave behind.
//
// An Unthrottled kind skips all of that — no read, no charge, no delete — and only
// compares.
func (c *Checker) Check(ctx context.Context, p Principal, secret string, lookup func(context.Context) (string, error)) error {
	// 🔴 THE REQUEST BUDGET IS SPENT FIRST, before the principal is so much as keyed:
	// a refusal here does no read, no charge, no lookup and no compare, and so it
	// looks the same for every principal (see budget.go).
	if !spend(ctx) {
		err := &RequestBudgetError{}
		c.observe(p.Kind, err, false)
		return err
	}
	failedOpen, err := c.check(ctx, p, secret, lookup)
	c.observe(p.Kind, err, failedOpen)
	return err
}

// check reports, beside its result, whether the attempt was evaluated WITHOUT its
// backoff because the store was full.
func (c *Checker) check(ctx context.Context, p Principal, secret string, lookup func(context.Context) (string, error)) (bool, error) {
	policy, ok := c.policies[p.Kind]
	if !ok {
		return false, fmt.Errorf("credential: no policy for kind %q", p.Kind)
	}
	key := Key(p)

	// hadRecord is whether a success has a record to clear. It is always true for a
	// charged attempt — the charge wrote one — and false only for a fail-open attempt
	// on a principal with no record, where a delete would write a tombstone into a
	// bucket that has no room for it.
	failedOpen, hadRecord := false, true
	if !policy.Unthrottled {
		had, err := c.admit(key, policy)
		switch {
		case errors.Is(err, errStoreFull):
			// 🔴 FAIL OPEN: the rest of this function runs exactly as it does for an
			// admitted attempt — the same lookup, the same compare (the dummy for an
			// unknown principal) — so timing and existence still do not leak. Only the
			// charge is missing.
			failedOpen, hadRecord = true, had
			c.logStoreFull(p.Kind)
		case err != nil:
			return false, err
		}
	}

	hash, err := lookup(ctx)
	if err != nil {
		// The charge stands: a lookup that fails is not a free attempt.
		return failedOpen, err
	}
	stored := []byte(hash)
	if hash == "" {
		stored = c.dummy
	}
	if c.compare(stored, []byte(secret)) != nil || hash == "" {
		return failedOpen, ErrMismatch
	}
	if policy.Unthrottled || !hadRecord {
		return failedOpen, nil
	}

	if err := c.store.Delete(key); err != nil && !errors.Is(err, nats.ErrKeyNotFound) {
		// The secret matched, so the caller authenticates. The record that failed to
		// clear only errs toward a delay on this principal's next failure, and it
		// expires with the bucket's TTL.
		if storeFull(err) {
			// A delete writes a tombstone, so a full bucket can refuse it too; that is
			// the condition already being reported, not a new one per success.
			c.logStoreFull(p.Kind)
		} else {
			log.Warn().Err(err).Str("kind", string(p.Kind)).
				Msg("Could not reset a credential attempt record after a successful check.")
		}
	}
	return failedOpen, nil
}

// logStoreFull warns that the attempt store is full, at most once per
// storeFullLogInterval, carrying how many fail-opens the interval hid. The counter,
// not this line, is what sees every one.
func (c *Checker) logStoreFull(kind Kind) {
	c.fullLog.Lock()
	now := c.now()
	if !c.fullLog.last.IsZero() && now.Sub(c.fullLog.last) < storeFullLogInterval {
		c.fullLog.suppressed++
		c.fullLog.Unlock()
		return
	}
	suppressed := c.fullLog.suppressed
	c.fullLog.last, c.fullLog.suppressed = now, 0
	c.fullLog.Unlock()

	log.Warn().Str("kind", string(kind)).Int("suppressed", suppressed).
		Msg("The credential attempt store is full, so sign-in attempts are being checked " +
			"WITHOUT their per-account backoff until entries expire. This usually means " +
			"someone is sending sign-ins for many distinct identifiers.")
}

// admit reads the principal's record, refuses the attempt while its delay is running,
// and otherwise charges it by compare-and-set.
//
// It reports whether the principal had a record, and returns errStoreFull — and
// nothing else — when the charge was refused because the store is full. A read that
// succeeded followed by a charge refused for fullness is still errStoreFull: the
// attempt was not in a delay, so it is evaluated, not denied.
func (c *Checker) admit(key string, policy Policy) (bool, error) {
	for i := 0; i < casAttempts; i++ {
		now := c.now()
		var rec record
		var rev uint64
		entry, err := c.store.Get(key)
		switch {
		case err == nil:
			if jerr := json.Unmarshal(entry.Value(), &rec); jerr != nil {
				// A record this code did not write is not trusted to mean "no failures".
				return false, unavailable("unreadable attempt record: %v", jerr)
			}
			rev = entry.Revision()
		case errors.Is(err, nats.ErrKeyNotFound):
			// First attempt, or the record expired, or a success cleared it.
		default:
			return false, unavailable("%v", err)
		}

		if wait := time.UnixMilli(rec.NotBefore).Sub(now); wait > 0 {
			return true, &ThrottledError{RetryAfter: wait}
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
			return true, nil
		case errors.Is(err, nats.ErrKeyExists), errors.Is(err, nats.ErrKeyRevisionMismatch):
			// Another attempt on this principal charged first; read again.
			continue
		case storeFull(err):
			return rev != 0, errStoreFull
		default:
			return false, unavailable("%v", err)
		}
	}
	return true, &ThrottledError{RetryAfter: contendedRetry}
}

func (c *Checker) observe(kind Kind, err error, failedOpen bool) {
	if c.checks == nil {
		return
	}
	outcome := OutcomeSuccess
	var throttled *ThrottledError
	switch {
	case failedOpen:
		outcome = OutcomeStoreFull
	case errors.Is(err, ErrRequestBudgetExhausted):
		outcome = OutcomeRequestBudget
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
