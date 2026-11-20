// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"
)

func restoreFixture(t *testing.T, handler http.HandlerFunc, defects ...string) (*harborpulse, *Runtime, context.Context, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := harborWith(t, defects...)
	s.s3b.done = map[string]bool{"0/threshold-reset": true}
	rt := &Runtime{Endpoints: Endpoints{Ingress: srv.URL}, HTTPClient: srv.Client(), Devices: []DeviceInstance{{Token: "hp-charger-east-1", CredentialId: "secret"}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if s.restore.done != nil {
			select {
			case <-s.restore.done:
			case <-time.After(2 * time.Second):
				t.Error("restore worker did not stop")
			}
		}
	})
	return s, rt, ctx, cancel
}

func restorePending(n int) []harborBackfill {
	p := harborBackfill{token: "hp-charger-east-1"}
	for i := 0; i < n; i++ {
		p.readings = append(p.readings, harborBuffered{at: time.Date(2026, 11, 18, 1, 0, 0, 123456000, time.UTC).Add(time.Duration(i) * time.Second), values: map[string]float64{HarborpulseChargerPowerKey: 3.7 + float64(i), HarborpulseMaxKwKey: 22}})
	}
	return []harborBackfill{p}
}

func waitRestore(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("restore condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func drainSnapshot(s *harborpulse) ScenarioRestoreDrain {
	l := s.s5Ledger()
	if l == nil || len(l.RestoreDrains) == 0 {
		return ScenarioRestoreDrain{}
	}
	return l.RestoreDrains[0]
}

func pendingCount(s *harborpulse) int {
	s.restore.mu.Lock()
	defer s.restore.mu.Unlock()
	return len(s.restore.entries)
}

func TestHarborRestore429KeepsTimeValuesAndWaitsForBothGates(t *testing.T) {
	bodies := make(chan []byte, 5)
	var mu sync.Mutex
	calls := 0
	s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- b
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		} else {
			w.WriteHeader(202)
		}
	})
	p := restorePending(1)
	s.restore.enqueue(ctx, s, rt, p, 0)
	s.restore.completeTick(s, 0, 0)
	first := <-bodies
	waitRestore(t, func() bool { return drainSnapshot(s).Refused429 == 1 })
	if pendingCount(s) != 1 {
		t.Fatal("429 discarded the entry")
	}
	// A completed tick alone cannot bypass Retry-After.
	s.restore.completeTick(s, 0, 0)
	select {
	case <-bodies:
		t.Fatal("retried before Retry-After")
	case <-time.After(150 * time.Millisecond):
	}
	// The timer alone cannot bypass the required NEXT tick: repeat with a second refusal.
	waitRestore(t, func() bool {
		s.restore.mu.Lock()
		defer s.restore.mu.Unlock()
		return time.Now().After(s.restore.resume)
	})
	s.restore.completeTick(s, 0, 0)
	second := <-bodies
	if !bytes.Equal(first, second) {
		t.Fatalf("retry changed body: %s / %s", first, second)
	}
	waitRestore(t, func() bool { return drainSnapshot(s).Accepted == 1 })
	if pendingCount(s) != 0 || !bytes.Contains(first, []byte("123456Z")) || !bytes.Contains(first, []byte("3.7")) {
		t.Fatalf("lost taken identity/value: %s", first)
	}
}

func TestHarborRestorePauseRequiresNextCompletedTick(t *testing.T) {
	requests := make(chan struct{}, 4)
	s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
	})
	s.restore.enqueue(ctx, s, rt, restorePending(1), 0)
	s.restore.completeTick(s, 0, 0)
	<-requests
	waitRestore(t, func() bool { return drainSnapshot(s).Refused429 == 1 })
	select {
	case <-requests:
		t.Fatal("retried without next tick")
	case <-time.After(1100 * time.Millisecond):
	}
	s.restore.beginTick()
	select {
	case <-requests:
		t.Fatal("retried before ordinary emits completed")
	case <-time.After(30 * time.Millisecond):
	}
	s.restore.completeTick(s, 0, 0)
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("next tick did not resume drain")
	}
}

func TestHarborRestoreBackpressureAndAmbiguousAreSeparate(t *testing.T) {
	for _, status := range []int{503, 0} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			bodies := make(chan []byte, 3)
			s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				bodies <- b
				if status == 503 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(503)
				} else {
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
				}
			})
			s.restore.enqueue(ctx, s, rt, restorePending(1), 0)
			s.restore.completeTick(s, 0, 0)
			first := <-bodies
			waitRestore(t, func() bool { l := drainSnapshot(s); return l.Backpressured+l.Ambiguous == 1 })
			l := drainSnapshot(s)
			if l.Refused429 != 0 || (status == 503 && (l.Backpressured != 1 || l.Ambiguous != 0)) || (status == 0 && (l.Ambiguous != 1 || l.Backpressured != 0)) {
				t.Fatalf("wrong classification: %+v", l)
			}
			time.Sleep(1050 * time.Millisecond)
			s.restore.completeTick(s, 0, 0)
			if second := <-bodies; !bytes.Equal(first, second) {
				t.Fatal("retry changed ambiguous/backpressured body")
			}
		})
	}
}

func TestHarborRestoreDefinitiveRefusalIsBounded(t *testing.T) {
	requests := make(chan struct{}, harborRestoreMaxAttempts+2)
	s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{}; w.WriteHeader(401) })
	s.restore.enqueue(ctx, s, rt, restorePending(1), 0)
	s.restore.completeTick(s, 0, 0)
	for i := 1; i <= harborRestoreMaxAttempts; i++ {
		<-requests
		waitRestore(t, func() bool {
			s.restore.mu.Lock()
			defer s.restore.mu.Unlock()
			return len(s.restore.entries) == 0 || s.restore.waitTick == s.restore.tick
		})
		if i < harborRestoreMaxAttempts {
			// Backoff timing is exercised above; advance this refusal-only fixture's deadline.
			s.restore.mu.Lock()
			s.restore.resume = time.Now().Add(-time.Second)
			s.restore.mu.Unlock()
			s.restore.completeTick(s, 0, 0)
		}
	}
	waitRestore(t, func() bool { return drainSnapshot(s).DeadLettered == 1 })
	l := drainSnapshot(s)
	if pendingCount(s) != 0 || l.Accepted != 0 || l.Ambiguous != 0 || l.Refused429 != 0 {
		t.Fatalf("wrong terminal accounting %+v", l)
	}
}

func TestHarborRestoreCancellationPreservesPendingCount(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s, rt, ctx, cancel := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release })
	s.restore.enqueue(ctx, s, rt, restorePending(2), 0)
	s.restore.completeTick(s, 0, 0)
	<-entered
	cancel()
	select {
	case <-s.restore.done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop worker")
	}
	close(release)
	l := drainSnapshot(s)
	if l.Offered != 2 || l.Accepted != 0 || l.Ambiguous != 1 || l.DeadLettered != 0 || pendingCount(s) != 2 {
		t.Fatalf("cancellation lost count: %+v", l)
	}
}

func TestHarborRestoreQueueBoundAndDropExactlyOnePerDay(t *testing.T) {
	t.Run("bound", func(t *testing.T) {
		s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
		cap := harborBufferCapFor(s.dayLength, s.load.Interval())
		s.restore.enqueue(ctx, s, rt, restorePending(cap+3), 0)
		if pendingCount(s) != cap || drainSnapshot(s).DeadLettered != 3 || drainSnapshot(s).Offered != cap+3 {
			t.Fatal("queue overflow was not bounded and counted")
		}
	})
	t.Run("drop per day", func(t *testing.T) {
		requests := make(chan struct{}, 8)
		s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
			requests <- struct{}{}
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		}, HarborpulseDefectBurstDropOn429)
		for day := 0; day < 2; day++ {
			s.restore.enqueue(ctx, s, rt, restorePending(2), day)
			// Leave day 0's retained entry queued; it remains FIFO ahead of day 1.
			if day == 1 {
				s.restore.mu.Lock()
				s.restore.entries = s.restore.entries[1:]
				s.restore.mu.Unlock()
			}
			for n := 0; n < 2; n++ {
				s.restore.mu.Lock()
				s.restore.resume = time.Now().Add(-time.Second)
				s.restore.mu.Unlock()
				s.restore.completeTick(s, day, 0)
				<-requests
				waitRestore(t, func() bool {
					s.restore.mu.Lock()
					defer s.restore.mu.Unlock()
					return s.restore.waitTick == s.restore.tick
				})
			}
			if pendingCount(s) != 1 {
				t.Fatal("drop-on-429 must discard exactly one per day")
			}
		}
		for _, l := range s.s5Ledger().RestoreDrains {
			if l.Refused429 != 2 || len(l.Refused) != 1 || len(l.Refused[0].Times) != 2 {
				t.Fatalf("lost refusal declaration %+v", l)
			}
		}
	})
}

func TestHarborTickReturnsWhileRestoreIngressIsBlocked(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, rt, ctx, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte("2026-11-18T01:00")) {
			once.Do(func() { close(entered) })
			<-release
		}
		w.WriteHeader(202)
	})
	defer close(release)
	s.restore.enqueue(ctx, s, rt, restorePending(1), 0)
	s.restore.completeTick(s, 0, 0)
	<-entered
	finished := make(chan struct{})
	go func() { _ = s.Tick(ctx, rt); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("blocked restore froze Tick")
	}
}

func TestHarborpulseGovernedTierCoversMeasuredDayAndRefusesRestore(t *testing.T) {
	b := HarborpulseDefaultIngestBaseline()
	t.Log(b)
	if b.MaxTick != 116 || b.MinTick != 90 || b.BeforeRestoreMin != 90 || b.BeforeRestoreMax != 92 || b.AfterRestoreMin != 114 || b.AfterRestoreMax != 116 || b.RestoreOffered != 720 || b.TickAtRestore != 116 {
		t.Fatalf("baseline changed: %v; remeasure and size tier", b)
	}
	source, err := os.ReadFile("../../../cli/sim/tier.go")
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile(`(?s)var HarborpulseGovernedTier = TierSpec\{(.*?)\n\}`).FindSubmatch(source)
	if len(section) != 2 {
		t.Fatal("governed tier literal missing")
	}
	get := func(key string) int {
		m := regexp.MustCompile(`"` + key + `":\s*(\d+)`).FindSubmatch(section[1])
		if len(m) != 2 {
			t.Fatalf("missing literal %s", key)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	r, burst := get("ingestReadingsPerSecond"), get("ingestBurst")
	beff := max(burst, 256)
	if float64(r)*DefaultEmitInterval.Seconds() < 1.25*float64(b.MaxTick) {
		t.Fatal("ordinary tick has less than 25% headroom")
	}
	if b.RestoreOffered < 2*beff {
		t.Fatal("restore does not force an ingress 429")
	}
	if get("calculatedMetricsPerProfile") != 1 {
		t.Fatal("pump needs exactly one calculated metric")
	}
}
