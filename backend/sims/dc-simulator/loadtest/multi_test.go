// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
)

var multiT0 = time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)

// mqttRep is a passing MQTT report for tenant i, started at multiT0+off and held for hold.
func mqttRep(i int, off, hold time.Duration) *MQTTReport {
	return &MQTTReport{
		Manifest: "devicepulse", Seed: int64(i), Tenant: fmt.Sprintf("t%02d", i),
		StartedAt: multiT0.Add(off), FinishedAt: multiT0.Add(off + hold + time.Minute),
		Transport: MQTTTransport{Broker: "ssl://b:8883", TLSRequested: true, QoS: 1},
		Drive: MQTTDrive{Devices: 1000, Connections: 1000, TargetRatePS: 2000, AckedRatePS: 1990, HoldSeconds: hold.Seconds(),
			Snapshot: mqttload.Snapshot{Acked: 1990 * int64(hold.Seconds())}},
		Storage:    MQTTStorage{Promised: 1990 * int64(hold.Seconds()), Persisted: 1990 * int64(hold.Seconds()), Reached: true, IdentityMode: IdentityCount},
		Invariants: []Invariant{{Name: InvPubackDurability, Passed: true, Detail: "ok"}},
	}
}

func httpRep(i int, off, hold time.Duration) *Report {
	return &Report{
		Manifest: "devicepulse", Seed: int64(i), Tenant: fmt.Sprintf("h%02d", i),
		StartedAt: multiT0.Add(off), FinishedAt: multiT0.Add(off + hold),
		Drive:         DriveStats{Devices: 500, TargetRatePS: 500, EmitLedger: EmitLedger{AchievedRatePS: 498, Accepted: 498 * int64(hold.Seconds())}, HoldSeconds: hold.Seconds()},
		PersistedSeen: 498 * int64(hold.Seconds()), Reached: true,
		Identity:   &IdentityReport{Reconciled: true},
		Invariants: []Invariant{{Name: InvIdentity, Passed: true, Detail: "ok"}},
	}
}

func writeReports(t *testing.T, reps ...any) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i, r := range reps {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, fmt.Sprintf("r%d.json", i))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func invOf(t *testing.T, m *MergedReport, name string) Invariant {
	t.Helper()
	for _, i := range m.Invariants {
		if i.Name == name {
			return i
		}
	}
	t.Fatalf("no invariant %s in %+v", name, m.Invariants)
	return Invariant{}
}

// The gate: six harness reports, from disk, merge into one passing run whose rates and counts
// are the sums and whose windows are the intersection.
func TestMergeSixHarnessReports(t *testing.T) {
	var reps []any
	for i := 0; i < 6; i++ {
		// starts 0..2.5s apart, 10 minutes each
		reps = append(reps, mqttRep(i, time.Duration(i)*500*time.Millisecond, 10*time.Minute))
	}
	m, err := MergeFiles(writeReports(t, reps...), MergeOptions{Expect: 6})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Passed() || m.ExitCode() != ExitPass {
		t.Fatalf("six aligned passing harnesses must merge to a pass:\n%s", m.Human())
	}
	if len(m.Harnesses) != 6 || m.Tenants != 6 || m.Devices != 6000 || m.TargetRatePS != 12000 || m.AchievedRatePS != 11940 {
		t.Fatalf("sums wrong: %+v", m)
	}
	if m.Accepted != 6*1990*600 || m.Persisted != m.Accepted || m.Lost != 0 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if m.StartSpreadSeconds != 2.5 || m.CommonWindowSeconds != 600-2.5 {
		t.Fatalf("window wrong: spread %v common %v", m.StartSpreadSeconds, m.CommonWindowSeconds)
	}
	if !m.CommonFrom.Equal(multiT0.Add(2500*time.Millisecond)) || !m.CommonTo.Equal(multiT0.Add(10*time.Minute)) {
		t.Fatalf("common window %v..%v", m.CommonFrom, m.CommonTo)
	}
	if !strings.Contains(m.Human(), "PASS") {
		t.Fatalf("human: %s", m.Human())
	}
	if _, err := m.JSON(); err != nil {
		t.Fatal(err)
	}
}

func TestMergeMixedKinds(t *testing.T) {
	m, err := MergeFiles(writeReports(t, mqttRep(1, 0, time.Minute), httpRep(2, time.Second, time.Minute)), MergeOptions{})
	if err != nil || !m.Passed() {
		t.Fatalf("an http and an mqtt report merge: %v\n%s", err, m.Human())
	}
	kinds := m.Harnesses[0].Kind + "," + m.Harnesses[1].Kind
	if kinds != "http,mqtt" {
		t.Fatalf("kinds not detected: %s", kinds)
	}
}

// Negative controls: each way a merge must NOT pass.
func TestMergeRefusals(t *testing.T) {
	good := func() []any {
		var r []any
		for i := 0; i < 6; i++ {
			r = append(r, mqttRep(i, 0, 10*time.Minute))
		}
		return r
	}
	t.Run("one failed harness fails the run", func(t *testing.T) {
		r := good()
		bad := mqttRep(3, 0, 10*time.Minute)
		bad.Invariants = []Invariant{{Name: InvPubackDurability, Passed: false, Detail: "x"}}
		r[3] = bad
		m, _ := MergeFiles(writeReports(t, r...), MergeOptions{Expect: 6})
		if m.Passed() || m.ExitCode() != ExitFail || invOf(t, m, InvHarnessesPassed).Passed || !strings.Contains(invOf(t, m, InvHarnessesPassed).Detail, "t03") {
			t.Fatalf("%s", m.Human())
		}
	})
	t.Run("a loss anywhere is a loss", func(t *testing.T) {
		r := good()
		bad := mqttRep(2, 0, 10*time.Minute)
		bad.Loss, bad.Storage.Lost = true, 12
		bad.Invariants = []Invariant{{Name: InvPubackDurability, Passed: false, Detail: "x"}}
		r[2] = bad
		m, _ := MergeFiles(writeReports(t, r...), MergeOptions{})
		if m.ExitCode() != ExitLoss || m.Lost != 12 {
			t.Fatalf("%s", m.Human())
		}
	})
	t.Run("a late starter is a misaligned window", func(t *testing.T) {
		r := good()
		r[4] = mqttRep(4, 40*time.Second, 10*time.Minute)
		m, _ := MergeFiles(writeReports(t, r...), MergeOptions{})
		if m.Passed() || invOf(t, m, InvWindowsAligned).Passed {
			t.Fatalf("a 40s start spread passed:\n%s", m.Human())
		}
	})
	t.Run("windows that barely overlap", func(t *testing.T) {
		// starts together but one hold is far shorter, so the common window is a sliver
		r := good()
		r[1] = mqttRep(1, 0, 30*time.Second)
		m, _ := MergeFiles(writeReports(t, r...), MergeOptions{})
		if m.Passed() || invOf(t, m, InvWindowsAligned).Passed {
			t.Fatalf("%s", m.Human())
		}
	})
	t.Run("two harnesses on one tenant", func(t *testing.T) {
		r := good()
		dup := mqttRep(5, 0, 10*time.Minute)
		dup.Tenant = "t00"
		r[5] = dup
		m, _ := MergeFiles(writeReports(t, r...), MergeOptions{})
		if m.Passed() || invOf(t, m, InvTenantsDistinct).Passed {
			t.Fatalf("%s", m.Human())
		}
	})
	t.Run("a missing report is a failed harness", func(t *testing.T) {
		m, _ := MergeFiles(writeReports(t, good()[:5]...), MergeOptions{Expect: 6})
		if m.Passed() || invOf(t, m, InvHarnessCount).Passed {
			t.Fatalf("%s", m.Human())
		}
	})
	t.Run("nothing merged is not a pass", func(t *testing.T) {
		if m := MergeReports(nil, MergeOptions{}); m.Passed() || m.ExitCode() == ExitPass {
			t.Fatal("an empty merge passed")
		}
	})
	t.Run("not a report", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "x.json")
		_ = os.WriteFile(p, []byte(`{"hello":1}`), 0o644)
		if _, err := MergeFiles([]string{p}, MergeOptions{}); err == nil {
			t.Fatal("a file with no tenant must be refused")
		}
	})
}

func TestAwaitStart(t *testing.T) {
	if err := awaitStart(context.Background(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := awaitStart(context.Background(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal("a start in the past must not block")
	}
	t0 := time.Now()
	if err := awaitStart(context.Background(), t0.Add(60*time.Millisecond)); err != nil || time.Since(t0) < 55*time.Millisecond {
		t.Fatalf("did not wait: %v after %v", err, time.Since(t0))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := awaitStart(ctx, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("a cancelled wait must fail")
	}
}

func TestProfileRefusesAFarStartAt(t *testing.T) {
	p := Profile{Manifest: "devicepulse", StartAt: time.Now().Add(MaxStartDelay + time.Hour)}.withDefaults()
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "start-at") {
		t.Fatalf("a start-at a day away must be refused: %v", err)
	}
}

// RunMany gives every harness the SAME start instant, and counts a harness that wrote nothing.
func TestRunManyAlignsAndCounts(t *testing.T) {
	var hs []*sim.Handshake
	for i := 0; i < 6; i++ {
		hs = append(hs, &sim.Handshake{Tenant: fmt.Sprintf("t%02d", i), Seed: int64(i)})
	}
	var mu sync.Mutex
	starts := map[string]time.Time{}
	run := func(fail string) Runner {
		return func(_ context.Context, h *sim.Handshake, at time.Time) ([]byte, error) {
			mu.Lock()
			starts[h.Tenant] = at
			mu.Unlock()
			if h.Tenant == fail {
				return nil, errors.New("cluster unreachable")
			}
			return json.Marshal(mqttRep(int(h.Seed), at.Sub(multiT0), time.Minute))
		}
	}
	m, errs := RunMany(context.Background(), hs, run(""), time.Minute, t.TempDir(), MergeOptions{})
	if len(errs) != 0 || !m.Passed() || len(m.Harnesses) != 6 || m.StartSpreadSeconds != 0 {
		t.Fatalf("errs=%v\n%s", errs, m.Human())
	}
	var first time.Time
	for _, at := range starts {
		// Shared AND in the future: a per-harness time.Now() can tie on a coarse clock, so equality
		// alone would not tell a barrier from no barrier.
		if time.Until(at) < 30*time.Second {
			t.Fatalf("the start instant %v is not the shared barrier (now + the lead)", at)
		}
		if first.IsZero() {
			first = at
		}
		if !at.Equal(first) {
			t.Fatalf("harnesses were given different start instants: %v", starts)
		}
	}
	m, errs = RunMany(context.Background(), hs, run("t03"), time.Minute, "", MergeOptions{})
	if len(errs) != 1 || m.Passed() || invOf(t, m, InvHarnessCount).Passed {
		t.Fatalf("a harness that produced no report must fail the run: errs=%v\n%s", errs, m.Human())
	}
}

func TestPerHarnessFiles(t *testing.T) {
	if got := suffixPath("/run/steady.json", "t1"); got != "/run/steady-t1.json" {
		t.Fatal(got)
	}
	if got := suffixPath("ledger", "t1"); got != "ledger-t1" {
		t.Fatal(got)
	}
	var nilMarker *SteadyMarker
	if nilMarker.For("x") != nil {
		t.Fatal("nil marker must stay nil")
	}
	m := (&SteadyMarker{Path: "/m/steady.json", Phase: "R1", Warmup: time.Minute}).For("t1")
	if m.Path != "/m/steady-t1.json" || m.Phase != "R1" || m.Warmup != time.Minute {
		t.Fatalf("%+v", m)
	}
}
