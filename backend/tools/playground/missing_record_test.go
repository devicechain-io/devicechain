// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// staleLost sets up a sim whose tenant is alive but whose dcctl record is gone, with the
// driver's ledger still naming it. It returns the sim, which is in a non-current slot.
func staleLost(t *testing.T, p *fakePlatform, d *Driver) string {
	t.Helper()
	p.now = at(1, 12)
	plan := PlanRotation(p.now, d.Cfg)
	name := plan.Destroy[0]
	p.tenants[name] = map[string]bool{"seed": true, "visitor-edit": true}
	p.lostRecord = map[string]bool{name: true}
	if err := d.Days.Set(name, 0); err != nil {
		t.Fatal(err)
	}
	return name
}

// A missing record must not clear the ledger while the tenant lives. dcctl made the tenant
// and died before saving its record: destroy says "no sim named", and clearing the entry on
// that alone would let the slot adopt the live tenant when it comes round.
func TestAMissingRecordDoesNotClearTheLedgerWhileTheTenantLives(t *testing.T) {
	p, d := ledgerPlatform(t)
	name := staleLost(t, p, d)
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatalf("a destroy that failed must only be logged: %v", err)
	}
	if _, ok, _ := d.Days.Get(name); !ok {
		t.Fatal("the ledger entry was cleared while the tenant is still alive")
	}
	if len(p.probes) == 0 || !strings.Contains(p.probes[0], "tenant-deletion status sim-"+name) {
		t.Fatalf("the platform was never asked about the tenant: %v", p.probes)
	}
	if !strings.Contains(p.probes[0], "--password pw") {
		t.Fatalf("the probe did not carry the admin login: %v", p.probes)
	}
}

// The slot that comes round holding a live tenant behind a lost record is refused, not
// adopted: no create runs over it and its ledger entry stays.
func TestAStaleSlotWithALostRecordAndALiveTenantIsRefusedNotAdopted(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.now = at(1, 12)
	cur := SimName("sitepulse", Slot(p.now, d.Cfg))
	p.tenants[cur] = map[string]bool{"seed": true, "visitor-edit": true}
	p.lostRecord = map[string]bool{cur: true}
	if err := d.Days.Set(cur, 0); err != nil { // an earlier day's entry
		t.Fatal(err)
	}
	_, err := d.Rotate(context.Background(), p.now)
	if err == nil || !strings.Contains(err.Error(), "still live") {
		t.Fatalf("a live tenant behind a lost record was not refused: %v", err)
	}
	if _, ok, _ := d.Days.Get(cur); !ok {
		t.Fatal("the ledger entry was cleared")
	}
	for _, l := range p.log {
		if strings.HasPrefix(l, "sim create "+cur) {
			t.Fatalf("the driver created over a live tenant: %s", l)
		}
	}
}

// Negative control: once the platform says the tenant is gone, the same missing record is
// cleared, so the guard above is the probe and not a blanket refusal.
func TestAMissingRecordIsClearedOnceThePlatformConfirmsTheTenantGone(t *testing.T) {
	p, d := ledgerPlatform(t)
	name := staleLost(t, p, d)
	delete(p.tenants, name)
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Days.Get(name); ok {
		t.Fatal("the entry for a tenant the platform says is gone was kept")
	}
	// A tenant that is mid-purge (a deletion record exists) is gone as well.
	p2, d2 := ledgerPlatform(t)
	n2 := staleLost(t, p2, d2)
	delete(p2.tenants, n2)
	p2.heldTil[n2] = p2.now.Add(time.Hour)
	if _, err := d2.Rotate(context.Background(), p2.now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d2.Days.Get(n2); ok {
		t.Fatal("the entry for a purged tenant was kept")
	}
}

// An answer the driver cannot verify is not "gone".
func TestAMissingRecordIsNotClearedWhenThePlatformCannotBeAsked(t *testing.T) {
	for name, tweak := range map[string]func(*fakePlatform, *Driver){
		"probe fails":    func(p *fakePlatform, d *Driver) { p.failOn["tenant-deletion"] = true },
		"no probe args":  func(p *fakePlatform, d *Driver) { d.ProbeArgs = nil },
		"no admin login": func(p *fakePlatform, d *Driver) { d.AdminPassword = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p, d := ledgerPlatform(t)
			sim := staleLost(t, p, d)
			delete(p.tenants, sim) // the tenant IS gone, but the driver cannot know that
			tweak(p, d)
			if _, err := d.Rotate(context.Background(), p.now); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := d.Days.Get(sim); !ok {
				t.Fatal("the entry was cleared on an answer the driver could not verify")
			}
		})
	}
}

// The first night, and a wiped volume with an empty ledger, have nothing to protect and
// must not need the platform.
func TestAnUnrecordedSimWithNoRecordNeedsNoProbe(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.now = at(1, 12)
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if len(p.probes) != 0 {
		t.Fatalf("the platform was probed for sims the ledger never held: %v", p.probes)
	}
}

func TestSimsOfARemovedScenarioAreDestroyed(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.now = at(1, 12)
	// An earlier configuration also ran widgetlab; its slot-b sim is still alive and recorded.
	old := SimName("widgetlab", 1)
	p.tenants[old] = map[string]bool{"seed": true}
	if err := d.Days.Set(old, 0); err != nil {
		t.Fatal(err)
	}
	// Ledger names the driver did not make are left alone.
	if err := d.Days.Set("someone-else", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if _, still := p.tenants[old]; still {
		t.Fatal("a sim of a scenario no longer configured survived the reset")
	}
	if _, ok, _ := d.Days.Get(old); ok {
		t.Fatal("the removed scenario is still in the ledger")
	}
	if _, ok, _ := d.Days.Get("someone-else"); !ok {
		t.Fatal("an entry outside the pg- namespace was touched")
	}
}

func TestSimsOfAReducedSlotCountAreDestroyed(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.now = at(1, 12)
	old := SimName("sitepulse", 5) // slot f, beyond the three configured
	p.tenants[old] = map[string]bool{"seed": true}
	if err := d.Days.Set(old, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if _, still := p.tenants[old]; still {
		t.Fatal("a sim beyond the configured slots survived the reset")
	}
}

func TestTailBufferKeepsOnlyTheEndWithinItsBound(t *testing.T) {
	tb := &tailBuffer{max: 10}
	for _, w := range []string{"abcdef", "ghijkl", strings.Repeat("z", 25), "12", "34"} {
		if n, err := tb.Write([]byte(w)); err != nil || n != len(w) {
			t.Fatalf("write %q: %d %v", w, n, err)
		}
		if len(tb.String()) > 10 {
			t.Fatalf("buffer grew to %d", len(tb.String()))
		}
	}
	if got := tb.String(); got != strings.Repeat("z", 6)+"1234" {
		t.Fatalf("tail = %q", got)
	}
}

func TestARunawaySimulatorIsBoundedInMemoryAndStreamedLive(t *testing.T) {
	t.Setenv("PLAYGROUND_TEST_HELPER", "spew")
	var live bytes.Buffer
	out, err := execRunner{WaitDelay: time.Second, Stream: &live}.Run(context.Background(), os.Args[0], "sim", "run", "x")
	if err == nil {
		t.Fatal("the helper exits 1")
	}
	if len(out) > maxOutput {
		t.Fatalf("the kept output is %d bytes, over the %d bound", len(out), maxOutput)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "THE-END") {
		t.Fatalf("the kept output lost its tail: %q", tail(out, 40))
	}
	if live.Len() < 20000*100 {
		t.Fatalf("only %d bytes were streamed to the pod log", live.Len())
	}
	// Other commands are not streamed: `sim create` prints what it minted.
	live.Reset()
	if _, err := (execRunner{WaitDelay: time.Second, Stream: &live}).Run(context.Background(), os.Args[0], "sim", "create", "x"); err == nil {
		t.Fatal("the helper exits 1")
	}
	if live.Len() != 0 {
		t.Fatal("a command other than sim run was streamed")
	}
}
