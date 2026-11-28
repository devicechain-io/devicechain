// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// sessionRig is a responder whose access-token credentials resolve to a device named by the
// credential id itself ("{tenant}:{device}" usernames), so one rig can connect many devices in
// many tenants, with the session bounds over an in-memory bucket and a clock the test moves.
type sessionRig struct {
	r     *CalloutResponder
	store *credentialtest.Store
	now   time.Time
	vec   *prometheus.CounterVec
}

func newSessionRig(t *testing.T, maxSessions, burst, perMinute int) *sessionRig {
	t.Helper()
	rig := &sessionRig{store: credentialtest.NewStore(), now: time.Unix(1_780_000_000, 0)}
	r, _ := newTestResponder(t, func(_ context.Context, p *model.PresentedCredential) (*model.Device, error) {
		d := &model.Device{}
		d.Token = p.CredentialId
		return d, nil
	})
	r.now = func() time.Time { return rig.now }
	rig.vec = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "callout_session_limited_total"}, []string{"reason"})
	r.metrics = &CalloutMetrics{SessionLimited: rig.vec}
	r.WithSessionBounds(rig.store, maxSessions, burst, perMinute)
	rig.r = r
	return rig
}

// connect presents device's own access token from client id
// "inst-1:{tenant}:{device}[:{discriminator}]" and reports whether it was granted.
func (s *sessionRig) connect(t *testing.T, tenant, device, discriminator string) bool {
	t.Helper()
	id := "inst-1:" + tenant + ":" + device
	if discriminator != "" {
		id += ":" + discriminator
	}
	jwt, errMsg := s.r.authorize(testRequestFromClient(t, tenant+":"+device, "", id))
	if errMsg != "" && errMsg != genericAuthFailure {
		t.Fatalf("a refusal must be the generic one, got %q", errMsg)
	}
	return jwt != "" && errMsg == ""
}

func (s *sessionRig) limited(reason string) float64 {
	return testutil.ToFloat64(s.vec.WithLabelValues(reason))
}

// A generous connect rate for the cap tests, so only the cap can refuse.
const roomyBurst, roomyRate = 10000, 60000

func TestSessionCapRefusesTheNPlusFirstDistinctDiscriminator(t *testing.T) {
	rig := newSessionRig(t, 3, roomyBurst, roomyRate)
	for _, d := range []string{"a", "b", "c"} {
		if !rig.connect(t, "acme-corp", "sensor-001", d) {
			t.Fatalf("discriminator %q is within the cap of 3 and was refused", d)
		}
	}
	if rig.connect(t, "acme-corp", "sensor-001", "d") {
		t.Fatal("a fourth distinct discriminator was granted past a cap of 3")
	}
	if got := rig.limited(sessionLimitedPerDevice); got != 1 {
		t.Fatalf("sessions_per_device = %v, want 1", got)
	}
	// The plain id is not a discriminator and is never counted, at the cap or not.
	if !rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("the plain client id was refused by the discriminator cap")
	}
}

func TestSessionCapAlwaysAdmitsAKnownDiscriminator(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	for _, d := range []string{"pub", "sub"} {
		if !rig.connect(t, "acme-corp", "sensor-001", d) {
			t.Fatalf("discriminator %q refused under the cap", d)
		}
	}
	if rig.connect(t, "acme-corp", "sensor-001", "third") {
		t.Fatal("a third discriminator was granted past a cap of 2")
	}
	// At the cap, the two it already holds reconnect, again and again, and across days (the
	// last-seen rewrite path).
	for day := 0; day < 3; day++ {
		for _, d := range []string{"pub", "sub", "pub"} {
			if !rig.connect(t, "acme-corp", "sensor-001", d) {
				t.Fatalf("known discriminator %q refused on day %d", d, day)
			}
		}
		rig.now = rig.now.Add(25 * time.Hour)
	}
	// Lowering the cap below what is recorded refuses new ids only.
	rig.r.WithSessionBounds(rig.store, 1, roomyBurst, roomyRate)
	if !rig.connect(t, "acme-corp", "sensor-001", "sub") || !rig.connect(t, "acme-corp", "sensor-001", "pub") {
		t.Fatal("a recorded discriminator was refused after the cap was lowered below the recorded count")
	}
	// And a store that cannot take the last-seen rewrite does not refuse a known one.
	rig.now = rig.now.Add(48 * time.Hour)
	rig.store.BeforeWrite = func(string) { rig.store.Fail = errors.New("bucket full") }
	if !rig.connect(t, "acme-corp", "sensor-001", "pub") {
		t.Fatal("a known discriminator was refused because its last-seen rewrite failed")
	}
}

func TestSessionCapIsPerDeviceAndPerTenant(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	if !rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("first discriminator refused")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "y") {
		t.Fatal("sensor-001 was granted past its cap of 1")
	}
	// Another device of the same tenant has its own allowance.
	if !rig.connect(t, "acme-corp", "sensor-002", "y") {
		t.Fatal("another device in the tenant was refused by sensor-001's cap")
	}
	// The same device token in another tenant is another device.
	if !rig.connect(t, "globex", "sensor-001", "y") {
		t.Fatal("the same device token in another tenant was refused by acme-corp's cap")
	}
	// And it is keyed where the tenant purge looks for it.
	if _, err := rig.store.Get(messaging.MqttSessionDiscriminatorKey("globex", "sensor-001")); err != nil {
		t.Fatalf("globex's entry is not under its tenant-scoped key: %v", err)
	}
}

func TestSessionCapPlainIdTouchesNoStore(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	rig.store.Fail = errors.New("broker down")
	for i := 0; i < 5; i++ {
		if !rig.connect(t, "acme-corp", "sensor-001", "") {
			t.Fatal("a plain client id was refused while the session bucket is down")
		}
	}
	if ops := rig.store.Ops(); len(ops) != 0 {
		t.Fatalf("a plain client id read or wrote the session bucket: %v", ops)
	}
	// A new discriminator, which needs the bucket, fails closed.
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("a new discriminator was granted while the bucket could not be read")
	}
	if got := rig.limited(sessionLimitedStoreUnavailable); got != 1 {
		t.Fatalf("session_store_unavailable = %v, want 1", got)
	}
}

func TestSessionCapRefusesWhenTheNewIdCannotBeRecorded(t *testing.T) {
	rig := newSessionRig(t, 4, roomyBurst, roomyRate)
	rig.store.BeforeWrite = func(string) { rig.store.Fail = errors.New("insufficient resources") }
	if rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("a discriminator that could not be recorded was granted")
	}
	if got := rig.limited(sessionLimitedStoreUnavailable); got != 1 {
		t.Fatalf("session_store_unavailable = %v, want 1", got)
	}
}

func TestSessionCapForgetsAnIdUnusedForTheRetention(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	if !rig.connect(t, "acme-corp", "sensor-001", "old") {
		t.Fatal("first discriminator refused")
	}
	rig.now = rig.now.Add(29 * 24 * time.Hour)
	if rig.connect(t, "acme-corp", "sensor-001", "new") {
		t.Fatal("a second discriminator was granted while the first is inside the retention")
	}
	rig.now = rig.now.Add(2 * 24 * time.Hour)
	if !rig.connect(t, "acme-corp", "sensor-001", "new") {
		t.Fatal("the slot of an id unused for more than the retention was not given back")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "old") {
		t.Fatal("the forgotten id came back without counting against the cap")
	}
}

// Two replicas recording a new discriminator for the same device at once: the loser of the
// compare-and-set re-reads and counts the winner's, so the cap holds across replicas.
func TestSessionCapHoldsAcrossARacingWrite(t *testing.T) {
	rig := newSessionRig(t, 2, roomyBurst, roomyRate)
	other := &sessionBound{store: rig.store, max: 2, retention: 30 * 24 * time.Hour}
	raced := false
	rig.store.BeforeWrite = func(string) {
		if raced {
			return
		}
		raced = true
		if why := other.admit("acme-corp", "sensor-001", ":replica-b", rig.now); why != "" { // the form clientIDDiscriminator hands it
			t.Errorf("the competing replica was refused: %s", why)
		}
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "replica-a") {
		t.Fatal("the connect that lost the race was refused instead of retried")
	}
	rig.store.BeforeWrite = nil
	if rig.connect(t, "acme-corp", "sensor-001", "third") {
		t.Fatal("the cap was overrun: the racing write was not counted")
	}
	for _, d := range []string{"replica-a", "replica-b"} {
		if !rig.connect(t, "acme-corp", "sensor-001", d) {
			t.Fatalf("%s was not recorded", d)
		}
	}
}

func TestSessionCapRewritesAnUnreadableEntry(t *testing.T) {
	rig := newSessionRig(t, 1, roomyBurst, roomyRate)
	if _, err := rig.store.Create(messaging.MqttSessionDiscriminatorKey("acme-corp", "sensor-001"), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if !rig.connect(t, "acme-corp", "sensor-001", "x") {
		t.Fatal("an unreadable entry locked the device out of new sessions")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "y") {
		t.Fatal("the rewritten entry does not count")
	}
}

func TestConnectRateIsPerDevice(t *testing.T) {
	rig := newSessionRig(t, 16, 3, 60) // three at once, then one a second
	for i := 0; i < 3; i++ {
		if !rig.connect(t, "acme-corp", "sensor-001", "") {
			t.Fatalf("connect %d inside the burst was refused", i+1)
		}
	}
	if rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("a fourth connect inside the same instant was granted over a burst of 3")
	}
	if got := rig.limited(sessionLimitedConnectRate); got != 1 {
		t.Fatalf("connect_rate = %v, want 1", got)
	}
	// Another device, and the same token in another tenant, are not charged for it.
	if !rig.connect(t, "acme-corp", "sensor-002", "") || !rig.connect(t, "globex", "sensor-001", "") {
		t.Fatal("another device was refused by sensor-001's connect rate")
	}
	// A second later one connect has refilled, and only one.
	rig.now = rig.now.Add(time.Second)
	if !rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("the connect that refilled after a second was refused")
	}
	if rig.connect(t, "acme-corp", "sensor-001", "") {
		t.Fatal("more than the refill was granted")
	}
	// A connect refused by the rate records no discriminator.
	if rig.connect(t, "acme-corp", "sensor-001", "rate-refused") {
		t.Fatal("over the rate, a connect with a discriminator was granted")
	}
	for _, op := range rig.store.Ops() {
		if strings.HasPrefix(op, "create") || strings.HasPrefix(op, "update") {
			t.Fatalf("a connect refused by the rate wrote the session bucket: %v", rig.store.Ops())
		}
	}
}

func TestConnectLimiterSweepsRefilledBuckets(t *testing.T) {
	l := newConnectLimiter(2, 60)
	now := time.Unix(1_780_000_000, 0)
	for _, d := range []string{"a", "b", "c"} {
		if !l.allow("t", d, now) {
			t.Fatalf("%s refused on its first connect", d)
		}
	}
	if l.size() != 3 {
		t.Fatalf("size = %d, want 3", l.size())
	}
	// After a sweep period every bucket has refilled, so the next charge drops the others.
	now = now.Add(connectLimiterSweepEvery)
	if !l.allow("t", "a", now) {
		t.Fatal("a refused after refilling")
	}
	if l.size() != 1 {
		t.Fatalf("size after the sweep = %d, want 1 (only the device that just connected)", l.size())
	}
}

func TestSessionBoundsDefaults(t *testing.T) {
	r, _ := newTestResponder(t, nil)
	if m, b, p := r.SessionBounds(); m != 0 || b != 0 || p != 0 {
		t.Fatalf("bounds before WithSessionBounds = %d/%d/%d, want off", m, b, p)
	}
	r.WithSessionBounds(credentialtest.NewStore(), 0, 0, 0)
	if m, b, p := r.SessionBounds(); m != DefaultMaxSessionsPerDevice || b != DefaultConnectBurstPerDevice || p != DefaultConnectsPerMinutePerDevice {
		t.Fatalf("defaulted bounds = %d/%d/%d", m, b, p)
	}
}

func TestClientIDDiscriminator(t *testing.T) {
	const req = "inst-1:acme:dev"
	for presented, want := range map[string]string{
		req:              "",
		req + ":a":       ":a",
		req + ":a:b":     ":a:b",
		req + "::":       "::",
		req + ":":        ":",
		req + ":pub-001": ":pub-001",
	} {
		if got := clientIDDiscriminator(presented, req); got != want {
			t.Errorf("clientIDDiscriminator(%q) = %q, want %q", presented, got, want)
		}
	}
}
