// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type restoreLifecycleSim struct{ *harborpulse }

func (s restoreLifecycleSim) Manifest() SimManifest                     { return SimManifest{} }
func (s restoreLifecycleSim) Bootstrap(context.Context, *Runtime) error { return nil }
func (s restoreLifecycleSim) UpdatePopulation(*Runtime) (*ScenarioUpdateFleet, error) {
	return nil, nil
}
func (s restoreLifecycleSim) Tick(ctx context.Context, rt *Runtime) error {
	s.restore.beginTick()
	s.restore.enqueue(ctx, s.harborpulse, rt, nil, 0)
	s.restore.completeTick(s.harborpulse, 0, 0)
	return nil
}

func TestHarborRestoreLifecycleStopResetStart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, rt, _, _ := restoreFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(202)
	})
	rt.Load.EmitInterval = 20 * time.Millisecond
	lc := NewLifecycle(restoreLifecycleSim{s}, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.restore.enqueue(context.Background(), s, rt, restorePending(2), 0)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no restore request")
	}
	if err := lc.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.restore.done:
	default:
		t.Fatal("Stop did not join restore worker")
	}
	close(release)
	if pendingCount(s) != 2 {
		t.Fatal("Stop lost pending entries")
	}
	frozen := drainSnapshot(s)
	time.Sleep(40 * time.Millisecond)
	if got := drainSnapshot(s); got.Accepted != frozen.Accepted || got.Ambiguous != frozen.Ambiguous {
		t.Fatal("accounting changed after Stop")
	}
	if err := lc.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.restore.mu.Lock()
	s.restore.resume = time.Time{}
	s.restore.mu.Unlock()
	if err := lc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lc.Stop() })
	waitRestore(t, func() bool { return drainSnapshot(s).Accepted == 2 })
	if pendingCount(s) != 0 || drainSnapshot(s).Offered != 2 {
		t.Fatal("restart did not drain the original entries exactly once")
	}
}

func TestOlderHarborHandshakeDerivesChartUpdateEndpoint(t *testing.T) {
	for _, origin := range []string{"http://localhost:8080", "https://instance.example"} {
		e := Endpoints{DeviceMgmtGraphQL: origin + "/api/device-management/graphql"}
		if err := e.ResolveUpdateManagement(); err != nil {
			t.Fatal(err)
		}
		if e.UpdateMgmtGraphQL != origin+"/api/update-management/graphql" {
			t.Fatal(e.UpdateMgmtGraphQL)
		}
	}
	for _, source := range []string{"", "https://instance.example/graphql", "https://instance.example/api/device-management/graphql?token=secret", "ftp://instance.example/api/device-management/graphql"} {
		e := Endpoints{DeviceMgmtGraphQL: source}
		if err := e.ResolveUpdateManagement(); err == nil || !strings.Contains(err.Error(), "updateMgmtGraphQL") {
			t.Fatalf("custom/missing endpoint did not fail loudly: %q, %v", source, err)
		}
	}
	e := Endpoints{DeviceMgmtGraphQL: "custom", UpdateMgmtGraphQL: "https://custom.example/update"}
	if err := e.ResolveUpdateManagement(); err != nil || e.UpdateMgmtGraphQL != "https://custom.example/update" {
		t.Fatal("explicit endpoint changed", err)
	}
}

func TestHarborOutageLogCadenceAcrossDaysAndUplinks(t *testing.T) {
	for _, day := range []time.Duration{HarborpulseMinDayLength, harborpulseDayLength, HarborpulseMaxDayLength} {
		for _, interval := range []time.Duration{10 * time.Millisecond, 5 * time.Second, 37 * time.Second} {
			s := NewHarborpulse(1, Load{EmitInterval: interval}).(*harborpulse)
			if err := s.ConfigureScenario(ScenarioOptions{DayLength: day}); err != nil {
				t.Fatal(err)
			}
			m := s.ensureModel()
			end := time.Duration((harborOutageTo - harborpulseStartHour) / 24 * float64(day))
			// Even an uplink that missed the entire outage collects the complete local log.
			m.reading("hp-charger-east-1", end, time.Unix(0, 0).Add(end))
			ledger := m.backfillLedger()
			if len(ledger) != 1 || ledger[0].Buffered != 90 || len(ledger[0].Values) != 90 || harborBufferNeed(day, interval) != 90 {
				t.Fatalf("day=%s interval=%s ledger=%+v", day, interval, ledger)
			}
			for _, values := range ledger[0].Values {
				if len(values) != 4 {
					t.Fatal("incomplete values")
				}
			}
			if got := m.takeBackfill(end); len(got) != 1 || len(got[0].readings) != 90 {
				t.Fatal("log did not reach restore queue")
			}
			ledger[0].Values[0][HarborpulseChargerPowerKey] = -1
			if m.backfillLedger()[0].Values[0][HarborpulseChargerPowerKey] == -1 {
				t.Fatal("ledger snapshot aliases model")
			}
		}
	}
}

func TestHarborOutageLogStartsInsideNanosecondClockBoundary(t *testing.T) {
	m := newHarborModel(1)
	origin := time.Unix(0, 123)
	from := time.Duration((harborOutageFrom - harborpulseStartHour) / 24 * float64(harborpulseDayLength))
	end := time.Duration((harborOutageTo - harborpulseStartHour) / 24 * float64(harborpulseDayLength))
	m.reading("hp-charger-east-1", end, origin.Add(end))
	ledger := m.backfillLedger()
	first, err := time.Parse(time.RFC3339Nano, ledger[0].First)
	if err != nil || first.Before(origin.Add(from)) || first.Sub(origin.Add(from)) >= time.Microsecond {
		t.Fatalf("first sample %s fell outside its platform query window: %v", first, err)
	}
}
