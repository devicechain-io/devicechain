// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the helper process for TestACancelledCommandDoesNotWaitOnItsChild:
// run as "parent" it starts itself as a "child" that inherits its output pipe, then both
// sleep, which is the shape of `dcctl sim run` and the simulator it starts.
func TestMain(m *testing.M) {
	switch os.Getenv("PLAYGROUND_TEST_HELPER") {
	case "parent":
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), "PLAYGROUND_TEST_HELPER=child")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "child":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// 🔴 A cancelled supervisor must return even though the simulator its dcctl started still
// holds the output pipe. Without WaitDelay the call waits for the child (here 10 seconds,
// in the pod for as long as the simulator runs) and the day-boundary exit never happens.
//
// Windows does not hand the child the parent's pipe, so there the test passes with or
// without the fix and proves nothing; it is skipped there rather than left to look like a
// check. The driver runs on Linux, where deleting the WaitDelay makes this fail after 10s.
func TestACancelledCommandDoesNotWaitOnItsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child does not inherit the output pipe on Windows, so this cannot fail there")
	}
	t.Setenv("PLAYGROUND_TEST_HELPER", "parent")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := execRunner{WaitDelay: 300 * time.Millisecond}.Run(ctx, os.Args[0])
	if err == nil {
		t.Fatal("a cancelled command reported success")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Run waited %s on a child holding the pipe", took)
	}
}

func ledgerPlatform(t *testing.T) (*fakePlatform, *Driver) {
	t.Helper()
	p := newPlatform()
	d := p.driver(cfg3())
	d.Days = NewDayLedger(filepath.Join(t.TempDir(), "days.json"))
	return p, d
}

// 🔴 Codex's finding, reproduced. A destroy that fails every night leaves its slot's tenant
// alive; three days later the slot is current again, `sim create` is idempotent against
// the surviving tenant, and the "reset" would keep what a visitor left. The ledger notices.
func TestASlotThatFailedToDestroyIsRefusedWhenItComesRound(t *testing.T) {
	p, d := ledgerPlatform(t)
	ctx := context.Background()
	stale := SimName("sitepulse", Slot(at(0, 3).Add(5*time.Minute), cfg3()))
	p.failOn["sim destroy "+stale] = true
	for day := 0; day < 3; day++ {
		p.now = at(day, 3).Add(5 * time.Minute)
		if _, err := d.Rotate(ctx, p.now); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		if day == 0 {
			p.tenants[stale]["visitor-edit"] = true
		}
	}
	p.now = at(3, 3).Add(5 * time.Minute)
	_, err := d.Rotate(ctx, p.now)
	if err == nil || !strings.Contains(err.Error(), "still holds the tenant of playground day") {
		t.Fatalf("slot a came round holding day 0's tenant and was reused: %v", err)
	}
	if !p.tenants[stale]["visitor-edit"] {
		t.Fatal("the fake lost the visitor's edit, so this test shows nothing")
	}
}

// Codex's second round: refusing and telling the maintainer to destroy it by hand left the
// ledger entry behind, so the slot stayed refused after the destroy. The driver now destroys
// the stale tenant itself, forgets it, and the slot is usable, and fresh, once the platform
// releases the token.
func TestAStaleSlotIsDestroyedAndComesBackFreshAfterTheHold(t *testing.T) {
	p, d := ledgerPlatform(t)
	ctx := context.Background()
	stale := SimName("sitepulse", Slot(at(0, 3).Add(5*time.Minute), cfg3()))
	p.failOn["sim destroy "+stale] = true
	for day := 0; day < 3; day++ {
		p.now = at(day, 3).Add(5 * time.Minute)
		if _, err := d.Rotate(ctx, p.now); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		if day == 0 {
			p.tenants[stale]["visitor-edit"] = true
		}
	}
	delete(p.failOn, "sim destroy "+stale) // whatever blocked the delete is gone

	p.now = at(3, 3).Add(5 * time.Minute)
	if _, err := d.Rotate(ctx, p.now); err == nil || !strings.Contains(err.Error(), "has been destroyed now") {
		t.Fatalf("the stale slot was not destroyed and reported: %v", err)
	}
	if _, still := p.tenants[stale]; still {
		t.Fatal("the stale tenant survived the driver's own destroy")
	}
	if _, ok, _ := d.Days.Get(stale); ok {
		t.Fatal("the ledger still names the destroyed sim, so the slot would stay refused")
	}

	p.now = at(3, 4) // inside the token hold: the create is refused, loudly
	if _, err := d.Rotate(ctx, p.now); err == nil {
		t.Fatal("a create inside the token hold was accepted")
	}
	p.now = at(3, 16) // hold over, same playground day
	if _, err := d.Rotate(ctx, p.now); err != nil {
		t.Fatalf("the slot did not heal after the hold: %v", err)
	}
	if got := p.tenants[stale]; len(got) != 1 || !got["seed"] {
		t.Fatalf("the healed slot is not the seed: %v", got)
	}
}

// Codex's second round: the day was written after the create, so a driver stopped between
// the two left a tenant the ledger could not see.
func TestTheDayIsRecordedBeforeTheCreate(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.failOn["sim create "] = true
	p.now = at(1, 12)
	if _, err := d.Rotate(context.Background(), p.now); err == nil {
		t.Fatal("expected the create to fail")
	}
	name := SimName("sitepulse", Slot(p.now, d.Cfg))
	if day, ok, _ := d.Days.Get(name); !ok || day != Day(p.now, d.Cfg.ResetHour) {
		t.Fatalf("the day was not recorded ahead of the create: %v %v", day, ok)
	}
}

// The control for the test above: with no ledger, the same nights end in a "successful"
// rotation that keeps the visitor's edit. That is the defect, and it is why the test above
// is not vacuous.
func TestWithoutTheLedgerAStaleSlotIsSilentlyReused(t *testing.T) {
	p := newPlatform()
	d := p.driver(cfg3())
	ctx := context.Background()
	stale := SimName("sitepulse", Slot(at(0, 3).Add(5*time.Minute), cfg3()))
	p.failOn["sim destroy "+stale] = true
	for day := 0; day < 4; day++ {
		p.now = at(day, 3).Add(5 * time.Minute)
		if _, err := d.Rotate(ctx, p.now); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
		if day == 0 {
			p.tenants[stale]["visitor-edit"] = true
		}
	}
	if !p.tenants[stale]["visitor-edit"] {
		t.Fatal("expected the stale tenant to survive the unprotected rotation")
	}
}

func TestARestartInsideTheDayIsNotStale(t *testing.T) {
	p, d := ledgerPlatform(t)
	ctx := context.Background()
	for _, h := range []int{4, 9, 20} {
		p.now = at(2, h)
		if _, err := d.Rotate(ctx, p.now); err != nil {
			t.Fatalf("restart at %02d:00: %v", h, err)
		}
	}
}

func TestADestroyedSlotCanBeUsedAgainAfterItsHold(t *testing.T) {
	p, d := ledgerPlatform(t)
	ctx := context.Background()
	for day := 0; day < 7; day++ {
		p.now = at(day, 3).Add(5 * time.Minute)
		if _, err := d.Rotate(ctx, p.now); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
	}
}

func TestAnUnreadableLedgerIsNotTrustedAsEmpty(t *testing.T) {
	p, d := ledgerPlatform(t)
	if err := os.WriteFile(d.Days.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.now = at(1, 12)
	if _, err := d.Rotate(context.Background(), p.now); err == nil {
		t.Fatal("a corrupt ledger was read as an empty one, which turns the staleness check off")
	}
}

// An entry for a sim whose record is gone (the volume was restored from an older copy, say)
// must not outlive the destroy that finds nothing to destroy: the slot would be called
// stale for ever.
func TestAnEntryForAVanishedSimIsDropped(t *testing.T) {
	p, d := ledgerPlatform(t)
	p.now = at(1, 12)
	plan := PlanRotation(p.now, d.Cfg)
	gone := plan.Destroy[0]
	if err := d.Days.Set(gone, 99); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Rotate(context.Background(), p.now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Days.Get(gone); ok {
		t.Fatalf("%s is still in the ledger after a destroy that found no sim", gone)
	}
}

func TestANilLedgerRecordsNothing(t *testing.T) {
	var l *DayLedger
	if err := l.Set("x", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := l.Get("x"); ok || err != nil {
		t.Fatalf("nil ledger found something: %v %v", ok, err)
	}
	if err := l.Forget("x"); err != nil {
		t.Fatal(err)
	}
}
