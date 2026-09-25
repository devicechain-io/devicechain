// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The MQTT password connect goes through a REAL credential.Checker, with the
// production policy, over an in-memory attempt store and a stepped clock. Only the
// database is faked.

// stepClock is a clock that moves only when a test says so.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const storedSecret = "s3cret"

// throttleRig is a responder whose fake credential resolves to sensor-001 in the tenant
// the lookup is scoped to, storing storedSecret, and records what it was asked.
type throttleRig struct {
	r        *CalloutResponder
	store    *credentialtest.Store
	clock    *stepClock
	counter  *prometheus.CounterVec
	resolves atomic.Int32
	// tenants is the context tenant of every lookup, in order.
	tenantsMu sync.Mutex
	tenants   []string
	// fail, when set, is what every lookup returns.
	fail error
}

func newThrottleRig(t *testing.T, opts ...credential.Option) *throttleRig {
	t.Helper()
	rig := &throttleRig{
		store:   credentialtest.NewStore(),
		clock:   &stepClock{now: time.Unix(1_780_000_000, 0)},
		counter: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"}),
	}
	api := fakeAuthApi{secret: storedSecret, resolves: &rig.resolves,
		authFn: func(ctx context.Context, p *model.PresentedCredential) (*model.Device, error) {
			tenant, _ := core.TenantFromContext(ctx)
			rig.tenantsMu.Lock()
			rig.tenants = append(rig.tenants, tenant)
			rig.tenantsMu.Unlock()
			if rig.fail != nil {
				return nil, rig.fail
			}
			d := &model.Device{}
			d.Token = "sensor-001"
			return d, nil
		}}
	checker := testChecker(t, rig.store, append([]credential.Option{
		credential.WithClock(rig.clock.Now), credential.WithCounter(rig.counter)}, opts...)...)
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	rig.r = mustResponder(t, nil, api, checker, creds.IssuerSeed, nil)
	rig.r.now = rig.clock.Now
	return rig
}

// connect runs one authorization with the given username and password, from the
// device's own client id, and reports whether it was granted. A denial must always be
// the generic one.
func (rig *throttleRig) connect(t *testing.T, user, pass string) bool {
	t.Helper()
	tenant, _, _ := strings.Cut(user, ":")
	jwtOut, errMsg := rig.r.authorize(testRequestFromClient(t, user, pass, "inst-1:"+tenant+":sensor-001"))
	if jwtOut == "" && errMsg != genericAuthFailure {
		t.Fatalf("a refusal must be the generic one, got %q", errMsg)
	}
	return jwtOut != ""
}

func (rig *throttleRig) outcome(o string) float64 {
	return testutil.ToFloat64(rig.counter.WithLabelValues(string(credential.KindDeviceCredential), o))
}

// After ten wrong passwords in a row the CORRECT one is refused at once, without the
// credential being looked up, until the delay has passed; then it is granted.
func TestCalloutThrottlesMqttBasicAfterFreeFailures(t *testing.T) {
	rig := newThrottleRig(t)
	for i := 0; i < DeviceCredentialPolicy.Free; i++ {
		if rig.connect(t, "acme-corp:dev1", "wrong") {
			t.Fatalf("wrong password %d was granted", i+1)
		}
	}
	if got := rig.resolves.Load(); got != int32(DeviceCredentialPolicy.Free) {
		t.Fatalf("control: the %d free attempts must each be looked up, got %d", DeviceCredentialPolicy.Free, got)
	}

	if rig.connect(t, "acme-corp:dev1", storedSecret) {
		t.Fatal("the correct password was granted inside the backoff delay")
	}
	if got := rig.resolves.Load(); got != int32(DeviceCredentialPolicy.Free) {
		t.Errorf("a throttled connect looked the credential up (%d lookups); it must not be evaluated", got)
	}
	if got := rig.outcome(credential.OutcomeThrottled); got != 1 {
		t.Errorf("throttled outcome = %v, want 1", got)
	}

	rig.clock.Advance(DeviceCredentialPolicy.Base)
	if !rig.connect(t, "acme-corp:dev1", storedSecret) {
		t.Fatal("the correct password was refused after the delay ended")
	}
	if got := rig.outcome(credential.OutcomeSuccess); got != 1 {
		t.Errorf("success outcome = %v, want 1", got)
	}
}

// An ACCESS_TOKEN connect (no password) compares no secret and is not charged: it
// writes nothing to the attempt store, and does not go through the Checker's lookup.
func TestCalloutAccessTokenWritesNothing(t *testing.T) {
	rig := newThrottleRig(t)
	if !rig.connect(t, "acme-corp:tok-1", "") {
		t.Fatal("control: the access-token connect must be granted")
	}
	if ops := rig.store.Ops(); len(ops) != 0 {
		t.Errorf("an access-token connect touched the attempt store: %v", ops)
	}
	if got := rig.resolves.Load(); got != 0 {
		t.Errorf("an access-token connect went through ResolveDeviceCredential %d time(s)", got)
	}
	// Control: a password connect on the same rig does use the store.
	rig.connect(t, "acme-corp:dev1", storedSecret)
	if len(rig.store.Ops()) == 0 {
		t.Error("control: a password connect must use the attempt store")
	}
}

// A credential the lookup cannot use — unknown, expired, or stored with no secret — is
// refused generically, CHARGED, and compared against the dummy exactly once, so its
// answer and its cost are those of a wrong password. Misconfigured is also logged at
// Warn (an operator must see it); the device's own wrong answers stay at Debug.
func TestCalloutRefusalsAreChargedAndDummyCompared(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		level string
	}{
		{"unknown credential", model.ErrCredentialNotResolved, "debug"},
		{"expired credential", model.ErrCredentialExpired, "debug"},
		{"stored with no secret", model.ErrCredentialMisconfigured, "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var compared [][]byte
			rig := newThrottleRig(t, credential.WithCompareObserver(func(stored []byte) {
				compared = append(compared, append([]byte(nil), stored...))
			}))
			rig.fail = tc.err
			logs := captureWarnings(t)

			if rig.connect(t, "acme-corp:dev1", storedSecret) {
				t.Fatal("a refused credential was granted")
			}
			if len(compared) != 1 {
				t.Fatalf("want exactly one (dummy) compare, got %d", len(compared))
			}
			if len(compared[0]) == 0 || string(compared[0]) == storedSecret {
				t.Errorf("the compare was against %q, not the kind's dummy", compared[0])
			}
			if got := rig.outcome(credential.OutcomeMismatch); got != 1 {
				t.Errorf("mismatch outcome = %v, want 1: the refusal must read as a wrong password "+
					"(error=%v)", got, rig.outcome(credential.OutcomeError))
			}
			if ops := rig.store.Ops(); len(ops) < 2 || ops[1] != "create:ok" {
				t.Errorf("the attempt was not charged; store ops %v", ops)
			}
			levels := calloutAuthLevels(t, logs.String())
			if len(levels) != 1 || levels[0] != tc.level {
				t.Errorf("want exactly one %s line, got %v\n%s", tc.level, levels, logs.String())
			}
		})
	}
}

// The backoff is per tenant AND credential: failures on acme-corp:dev1 do not
// throttle other-corp:dev1. A spelling of the tenant that differs only in case is a
// principal of its own, and the lookup is scoped to exactly that spelling, so it names
// a different tenant rather than reaching acme-corp's credential under a fresh backoff.
func TestCalloutPrincipalIsPerTenant(t *testing.T) {
	rig := newThrottleRig(t)
	for i := 0; i < DeviceCredentialPolicy.Free; i++ {
		rig.connect(t, "acme-corp:dev1", "wrong")
	}
	if rig.connect(t, "acme-corp:dev1", storedSecret) {
		t.Fatal("control: acme-corp:dev1 must be inside its delay")
	}
	if !rig.connect(t, "other-corp:dev1", storedSecret) {
		t.Error("failures on acme-corp:dev1 throttled other-corp:dev1")
	}

	rig.tenantsMu.Lock()
	rig.tenants = nil
	rig.tenantsMu.Unlock()
	rig.connect(t, "ACME-corp:dev1", storedSecret)
	rig.tenantsMu.Lock()
	defer rig.tenantsMu.Unlock()
	if len(rig.tenants) != 1 || rig.tenants[0] != "ACME-corp" {
		t.Errorf("the case-variant connect looked up in tenants %v; want exactly the spelling "+
			"presented, ACME-corp, so it cannot reach acme-corp's credential", rig.tenants)
	}
}

// A success forgives: nine failures, a success, and nine more failures are all
// evaluated, and the correct password after them is granted at once.
func TestCalloutSuccessForgives(t *testing.T) {
	rig := newThrottleRig(t)
	for round := 0; round < 2; round++ {
		for i := 0; i < DeviceCredentialPolicy.Free-1; i++ {
			rig.connect(t, "acme-corp:dev1", "wrong")
		}
		if !rig.connect(t, "acme-corp:dev1", storedSecret) {
			t.Fatalf("round %d: the correct password was refused after %d failures", round, DeviceCredentialPolicy.Free-1)
		}
	}
	if got := rig.outcome(credential.OutcomeThrottled); got != 0 {
		t.Errorf("throttled outcome = %v, want 0: a success must reset the count", got)
	}
}

// An attempt store that cannot be reached refuses the connect without looking the
// credential up, and warns — once per interval however many connects it refuses.
func TestCalloutStoreUnavailableFailsClosed(t *testing.T) {
	rig := newThrottleRig(t)
	rig.store.Fail = errors.New("nats: connection closed")
	logs := captureWarnings(t)

	for i := 0; i < 3; i++ {
		if rig.connect(t, "acme-corp:dev1", storedSecret) {
			t.Fatal("a connect was granted with the attempt store unreachable")
		}
	}
	if got := rig.resolves.Load(); got != 0 {
		t.Errorf("the credential was looked up %d time(s) with the store unreachable", got)
	}
	if got := rig.outcome(credential.OutcomeUnavailable); got != 3 {
		t.Errorf("unavailable outcome = %v, want 3", got)
	}
	if got := strings.Count(logs.String(), `"level":"warn"`); got != 1 {
		t.Errorf("want exactly one warning for three refusals inside one interval, got %d\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "attempt store in JetStream cannot be reached") {
		t.Errorf("the warning does not name the attempt store:\n%s", logs.String())
	}
	rig.clock.Advance(unavailableLogInterval)
	rig.connect(t, "acme-corp:dev1", storedSecret)
	if got := strings.Count(logs.String(), `"level":"warn"`); got != 2 {
		t.Errorf("after the interval the warning must repeat; got %d warnings", got)
	}
}

// A FULL attempt store fails OPEN: the correct password is granted, uncharged, and
// counted as store_full.
func TestCalloutStoreFullFailsOpen(t *testing.T) {
	rig := newThrottleRig(t)
	full := &nats.APIError{Code: 503, ErrorCode: 10077, Description: "maximum bytes exceeded"}
	rig.store.BeforeWrite = func(string) { rig.store.Fail = full }

	if !rig.connect(t, "acme-corp:dev1", storedSecret) {
		t.Fatal("a correct password was refused because the attempt store is full")
	}
	if got := rig.outcome(credential.OutcomeStoreFull); got != 1 {
		t.Errorf("store_full outcome = %v, want 1", got)
	}
	if got := rig.store.Len(); got != 0 {
		t.Errorf("the attempt was charged (%d records) although the store refused the write", got)
	}
}

// A deleted tenant is refused before the attempt store is touched, so its fleet's
// reconnect storm writes nothing.
func TestCalloutDeletedTenantChargesNothing(t *testing.T) {
	store := credentialtest.NewStore()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	api := fakeAuthApi{secret: storedSecret, authFn: func(context.Context, *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = "sensor-001"
		return d, nil
	}}
	r := mustResponder(t, nil, api, testChecker(t, store), creds.IssuerSeed, func(tenant string) bool { return tenant == "acme-corp" })
	if jwtOut, _ := r.authorize(testRequest(t, "acme-corp:dev1", storedSecret)); jwtOut != "" {
		t.Fatal("a deleted tenant was granted")
	}
	if ops := store.Ops(); len(ops) != 0 {
		t.Errorf("a deleted tenant's connect touched the attempt store: %v", ops)
	}
	// Control: a live tenant on the same responder does touch it.
	if jwtOut, _ := r.authorize(testRequestFromClient(t, "live-corp:dev1", storedSecret, "inst-1:live-corp:sensor-001")); jwtOut == "" {
		t.Fatal("control: a live tenant must be granted")
	}
	if len(store.Ops()) == 0 {
		t.Error("control: a live tenant's connect must use the attempt store")
	}
}

// Two connects for one username that race — a reconnect overlapping a stale session —
// are both evaluated and both granted: the charge that loses the compare-and-set reads
// again and counts on top, and a count of two is inside the free attempts.
func TestCalloutRacingConnectsForOneUsernameAreBothGranted(t *testing.T) {
	rig := newThrottleRig(t)
	var raced atomic.Bool
	rig.store.BeforeWrite = func(key string) {
		if raced.Swap(true) {
			return
		}
		// The other connect charges first, in the gap between this one's read and write.
		body, _ := json.Marshal(map[string]int64{"f": 1, "nb": rig.clock.Now().UnixMilli()})
		if _, err := rig.store.Create(key, body); err != nil {
			t.Errorf("competing charge: %v", err)
		}
	}
	if !rig.connect(t, "acme-corp:dev1", storedSecret) {
		t.Fatal("a connect whose charge lost the race to another connect for the same username was refused")
	}
	if !raced.Load() {
		t.Fatal("control: the competing charge never landed, so nothing raced")
	}
	if got := strings.Join(rig.store.Ops(), ","); !strings.Contains(got, "create:conflict") {
		t.Errorf("control: the charge must have lost a compare-and-set; ops %s", got)
	}
}

// A responder with no Checker is refused, never built to compare unthrottled.
func TestNewCalloutResponderRequiresChecker(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewCalloutResponder(nil, fakeAuthApi{}, nil, creds.IssuerSeed, "inst-1", nil)
	if !errors.Is(err, errNoCredentialChecker) || r != nil {
		t.Fatalf("got (%v, %v), want (nil, errNoCredentialChecker)", r, err)
	}
}
