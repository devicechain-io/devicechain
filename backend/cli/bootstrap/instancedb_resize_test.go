// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The upgrade's re-size of an instance's login, against a REAL PostgreSQL — for the reason
// in instancedb_test.go. Named InstanceDatabase… so hack/migration-diff.sh's -run filter
// selects them; a name outside it runs nowhere.

func loginLimit(t *testing.T, q instanceDBQuerier, role string) int {
	t.Helper()
	var limit int
	if err := q.QueryRow(context.Background(), `select rolconnlimit from pg_roles where rolname = $1`, role).Scan(&limit); err != nil {
		t.Fatalf("reading %s's connection limit: %v", role, err)
	}
	return limit
}

// 🔴 A GROW THE BUDGET HOLDS IS WRITTEN; ONE IT DOES NOT IS REFUSED BY BOTH THE CHECK AND
// THE GROW, AND WRITES NOTHING.
func TestInstanceDatabaseResizeGrowsOnlyWhatTheBudgetHolds(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-a", "rs-b")
	q := pgxSession{p}
	// 100 less the 20 reserved leaves 80.
	for _, i := range []string{"rs-a", "rs-b"} {
		if err := ensureInstanceDatabase(ctx, q, i, "pw", connectionAdmission{Limit: 30, Budget: 100}); err != nil {
			t.Fatalf("admitting %s: %v", i, err)
		}
	}

	fits := connectionAdmission{Limit: 50, Budget: 100}
	if have, err := checkInstanceLoginResize(ctx, q, "rs-a", fits); err != nil || have != 30 {
		t.Fatalf("checking a grow that fits returned (%d, %v), want (30, nil)", have, err)
	}
	if got := loginLimit(t, q, "rs-a"); got != 30 {
		t.Fatalf("the check changed the login's limit to %d", got)
	}
	if have, err := growInstanceLogin(ctx, q, "rs-a", fits); err != nil || have != 30 {
		t.Fatalf("growing within the budget returned (%d, %v), want (30, nil)", have, err)
	}
	if got := loginLimit(t, q, "rs-a"); got != 50 {
		t.Fatalf("the grown login holds %d, want 50", got)
	}

	over := connectionAdmission{Limit: 51, Budget: 100}
	if _, err := checkInstanceLoginResize(ctx, q, "rs-b", over); !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("checking a grow over the budget was not refused: %v", err)
	}
	if _, err := growInstanceLogin(ctx, q, "rs-b", over); !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("a grow over the budget was not refused: %v", err)
	}
	if got := loginLimit(t, q, "rs-b"); got != 30 {
		t.Fatalf("a refused grow left the login at %d, want 30", got)
	}
}

// 🔴 A SHRINK WRITES THE SMALLER LIMIT, AND GIVES THE DIFFERENCE BACK TO THE BUDGET.
func TestInstanceDatabaseResizeShrinkGivesConnectionsBack(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-c", "rs-d")
	q := pgxSession{p}
	if err := ensureInstanceDatabase(ctx, q, "rs-c", "pw", connectionAdmission{Limit: 60, Budget: 100}); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstanceDatabase(ctx, q, "rs-d", "pw", connectionAdmission{Limit: 40, Budget: 100}); !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("the budget was not full before the shrink, so the test proves nothing: %v", err)
	}
	if have, err := shrinkInstanceLogin(ctx, q, "rs-c", connectionAdmission{Limit: 40, Budget: 100}); err != nil || have != 60 {
		t.Fatalf("shrinking returned (%d, %v), want (60, nil)", have, err)
	}
	if got := loginLimit(t, q, "rs-c"); got != 40 {
		t.Fatalf("the shrunk login holds %d, want 40", got)
	}
	if err := ensureInstanceDatabase(ctx, q, "rs-d", "pw", connectionAdmission{Limit: 40, Budget: 100}); err != nil {
		t.Fatalf("the shrink did not give its connections back: %v", err)
	}
}

// 🔴 AN UNCHANGED NEED IS NOT REFUSED BY A BUDGET LOWERED UNDER IT. The logins below hold
// 80 on a store whose budget now leaves 40; admitting anything more is refused, and an
// upgrade asking for exactly what it holds is not.
func TestInstanceDatabaseResizeOfAnUnchangedNeedIgnoresALoweredBudget(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-e", "rs-f")
	q := pgxSession{p}
	for _, i := range []string{"rs-e", "rs-f"} {
		if err := ensureInstanceDatabase(ctx, q, i, "pw", connectionAdmission{Limit: 40, Budget: 100}); err != nil {
			t.Fatal(err)
		}
	}
	lowered := connectionAdmission{Limit: 40, Budget: 60}
	if _, err := checkInstanceLoginResize(ctx, q, "rs-e", connectionAdmission{Limit: 41, Budget: 60}); !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("the lowered budget does not refuse a grow, so the test proves nothing: %v", err)
	}
	if _, err := checkInstanceLoginResize(ctx, q, "rs-e", lowered); err != nil {
		t.Fatalf("checking an unchanged need on a lowered budget was refused: %v", err)
	}
	if _, err := growInstanceLogin(ctx, q, "rs-e", lowered); err != nil {
		t.Fatalf("growing to an unchanged need on a lowered budget was refused: %v", err)
	}
	if _, err := shrinkInstanceLogin(ctx, q, "rs-e", lowered); err != nil {
		t.Fatal(err)
	}
	if got := loginLimit(t, q, "rs-e"); got != 40 {
		t.Fatalf("an unchanged need moved the login to %d", got)
	}
}

// 🔴 A LOGIN OF ITS OWN WITH NO LIMIT IS LIMITED, THROUGH ADMISSION. It is counted in no
// budget, so giving it one is entering the count.
func TestInstanceDatabaseResizeLimitsAnUnlimitedOwnLogin(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-u")
	q := pgxSession{p}
	if err := ensureInstanceDatabase(ctx, q, "rs-u", "pw", testAdmission); err != nil {
		t.Fatal(err)
	}
	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, `ALTER ROLE "rs-u" CONNECTION LIMIT -1`); err != nil {
		t.Fatal(err)
	}
	if have, err := checkInstanceLoginResize(ctx, q, "rs-u", testAdmission); err != nil || have != -1 {
		t.Fatalf("checking an unlimited login returned (%d, %v), want (-1, nil)", have, err)
	}
	if have, err := growInstanceLogin(ctx, q, "rs-u", testAdmission); err != nil || have != -1 {
		t.Fatalf("growing an unlimited login returned (%d, %v), want (-1, nil)", have, err)
	}
	if got := loginLimit(t, q, "rs-u"); got != testAdmission.Limit {
		t.Fatalf("the unlimited login holds %d after the grow, want %d", got, testAdmission.Limit)
	}
}

// 🔴 NO LOGIN, OR A LOGIN dcctl DID NOT MAKE, IS REFUSED — NOT CREATED, NOT RE-SIZED.
func TestInstanceDatabaseResizeRefusesALoginThatIsMissingOrNotOurs(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-none", "rs-foreign")
	q := pgxSession{p}
	admit := connectionAdmission{Limit: 40, Budget: 600}

	_, err := checkInstanceLoginResize(ctx, q, "rs-none", admit)
	if !errors.Is(err, errInstanceDatabaseNotOurs) || !strings.Contains(err.Error(), "no login named") {
		t.Fatalf("a missing login was not refused as an inconsistency: %v", err)
	}
	if _, err := growInstanceLogin(ctx, q, "rs-none", admit); !errors.Is(err, errInstanceDatabaseNotOurs) {
		t.Fatalf("growing a missing login was not refused: %v", err)
	}
	var exists bool
	if err := p.QueryRow(ctx, `select exists(select 1 from pg_roles where rolname = 'rs-none')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("re-sizing a missing login created one")
	}

	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, `CREATE ROLE "rs-foreign" LOGIN CONNECTION LIMIT 10`); err != nil {
		t.Fatal(err)
	}
	if _, err := growInstanceLogin(ctx, q, "rs-foreign", admit); !errors.Is(err, errInstanceDatabaseNotOurs) {
		t.Fatalf("a login the provisioner did not make was re-sized: %v", err)
	}
	if got := loginLimit(t, pgxSession{su}, "rs-foreign"); got != 10 {
		t.Fatalf("a refused re-size moved a foreign login to %d", got)
	}
}

// 🔴 THROUGH THE UPGRADE'S OWN STEPS: the check passes, and the resize grows and shrinks
// the login it is pointed at.
func TestInstanceDatabaseResizeThroughTheUpgradeSteps(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "rs-g", "rs-h")
	q := pgxSession{p}
	restore := upgradeLoginSession
	t.Cleanup(func() { upgradeLoginSession = restore })
	upgradeLoginSession = func(_ context.Context, _ *State, fn func(instanceDBQuerier) error) error { return fn(q) }

	// Two relational areas: 2 × 20 × 2 = 80.
	st := func() *State {
		return &State{
			Instance:     "rs-g",
			EnabledAreas: []string{"user-management", "device-management"},
			Install:      &InstallRecord{Outputs: InstallOutputs{Rdb: ClusterRdb{MaxConnections: 200}}},
		}
	}
	for _, i := range []string{"rs-g", "rs-h"} {
		if err := ensureInstanceDatabase(ctx, q, i, "pw", connectionAdmission{Limit: 40, Budget: 120}); err != nil {
			t.Fatal(err)
		}
	}

	var err error
	captureStdout(t, func() { err = precheckUpgradeLogin(ctx, st()) })
	if err != nil {
		t.Fatalf("an upgrade the store has room for was refused: %v", err)
	}
	out := captureStdout(t, func() { err = resizeUpgradeLogin(ctx, st(), loginResizeGrow) })
	if err != nil || loginLimit(t, q, "rs-g") != 80 || !strings.Contains(out, "40 → 80 connections") {
		t.Fatalf("growing through the upgrade step: err %v, limit %d, said %q", err, loginLimit(t, q, "rs-g"), out)
	}

	// A later release needing fewer: one relational area is 40.
	smaller := st()
	smaller.EnabledAreas = []string{"user-management"}
	out = captureStdout(t, func() { err = resizeUpgradeLogin(ctx, smaller, loginResizeShrink) })
	if err != nil || loginLimit(t, q, "rs-g") != 40 || !strings.Contains(out, "80 → 40 connections") {
		t.Fatalf("shrinking through the upgrade step: err %v, limit %d, said %q", err, loginLimit(t, q, "rs-g"), out)
	}
}
