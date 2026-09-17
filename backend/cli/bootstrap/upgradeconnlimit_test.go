// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"strings"
	"testing"

	pgx "github.com/jackc/pgx/v5"
)

// The upgrade's re-size of the instance's database login, without a store. What only
// PostgreSQL can answer is in instancedb_resize_test.go; what is here is the DECISION and
// the WIRING, which a real-store suite that runs only under hack/migration-diff.sh would
// leave unguarded in every ordinary `go test`.

func TestTheLoginMovesOnlyTowardsWhatTheReleaseNeeds(t *testing.T) {
	for _, c := range []struct {
		have, want int
		dir        loginResize
	}{
		{have: 280, want: 280, dir: loginResizeNone},
		{have: 280, want: 320, dir: loginResizeGrow},
		{have: 320, want: 280, dir: loginResizeShrink},
		// A login with no limit is counted in no budget, so limiting it is an admission.
		{have: -1, want: 280, dir: loginResizeGrow},
	} {
		if got := loginResizeFor(c.have, c.want); got != c.dir {
			t.Errorf("loginResizeFor(%d, %d) = %d, want %d", c.have, c.want, got, c.dir)
		}
	}
}

// fakeLoginStore answers the two reads the re-size makes — the login's own row and the
// admission count — and records what was run. Only what these tests need: the SQL itself
// is PostgreSQL's to judge, against a real server.
type fakeLoginStore struct {
	limit     int32
	others    int32 // the limits already granted to other logins, as one
	admitted  int   // how many times admission was asked
	statement []string
}

type fakeRow func(dest ...any) error

func (r fakeRow) Scan(dest ...any) error { return r(dest...) }

func (f *fakeLoginStore) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "array_agg"):
		f.admitted++
		return fakeRow(func(dest ...any) error {
			*dest[0].(*[]string) = []string{"other"}
			*dest[1].(*[]int32) = []int32{f.others}
			return nil
		})
	case strings.Contains(sql, "r.rolconnlimit"):
		return fakeRow(func(dest ...any) error {
			*dest[0].(*bool), *dest[1].(*bool), *dest[2].(*bool) = false, false, false
			*dest[3].(*int32) = f.limit
			*dest[4].(*bool) = true
			return nil
		})
	}
	return fakeRow(func(...any) error { return fmt.Errorf("unexpected query: %s", sql) })
}

func (f *fakeLoginStore) Exec(_ context.Context, sql string, _ ...any) (pgconnCommandTag, error) {
	f.statement = append(f.statement, sql)
	var name string
	var limit int32
	if n, _ := fmt.Sscanf(sql, "ALTER ROLE %s CONNECTION LIMIT %d", &name, &limit); n == 2 {
		f.limit = limit
	}
	return nil, nil
}

func (f *fakeLoginStore) altered() bool {
	for _, s := range f.statement {
		if strings.HasPrefix(s, "ALTER ROLE") {
			return true
		}
	}
	return false
}

// 🔴 AN UNCHANGED NEED ASKS THE BUDGET NOTHING. The budget here is already over-granted —
// an operator lowered --max-connections after the instances were admitted — and admission
// would refuse. An upgrade asking for the limit the login already holds must not be
// refused for it, so admission must not be ASKED; that it would refuse is the negative
// control that makes "not asked" mean something.
func TestAnUnchangedNeedIsNotAdmittedAgain(t *testing.T) {
	ctx := context.Background()
	overGranted := connectionAdmission{Limit: 40, Budget: 60} // 40 usable, 40 held elsewhere

	store := &fakeLoginStore{limit: 40, others: 40}
	if _, err := checkInstanceLoginResize(ctx, store, "prod", overGranted); err != nil {
		t.Fatalf("the check refused an upgrade that asks for nothing new: %v", err)
	}
	if _, err := growInstanceLogin(ctx, store, "prod", overGranted); err != nil {
		t.Fatalf("the grow refused an upgrade that asks for nothing new: %v", err)
	}
	if store.admitted != 0 || store.altered() {
		t.Fatalf("an unchanged limit was admitted %d time(s) (altered: %t); want neither", store.admitted, store.altered())
	}

	// The control: one more connection on the same store is refused.
	store = &fakeLoginStore{limit: 40, others: 40}
	more := overGranted
	more.Limit = 41
	if _, err := checkInstanceLoginResize(ctx, store, "prod", more); !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("a grow on an over-granted store was not refused, so the test above proves nothing: %v", err)
	}
}

// 🔴 THE GROW ADMITS AGAIN, UNDER THE LOCK. The check at the start of the upgrade is the
// early answer; room can be taken between it and the grow, and a grow that trusted the
// check would write a limit the store no longer has.
func TestTheGrowIsAdmittedUnderTheLock(t *testing.T) {
	ctx := context.Background()
	store := &fakeLoginStore{limit: 40, others: 60}
	_, err := growInstanceLogin(ctx, store, "prod", connectionAdmission{Limit: 80, Budget: 150})
	if !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("a grow the store has no room for was not refused: %v", err)
	}
	if store.altered() {
		t.Fatal("a refused grow altered the login anyway")
	}
	if store.admitted != 1 || !strings.Contains(strings.Join(store.statement, ";"), "pg_advisory_lock") {
		t.Fatalf("the grow was admitted %d time(s) with statements %v; want once, under the advisory lock",
			store.admitted, store.statement)
	}

	store = &fakeLoginStore{limit: 40, others: 20}
	if have, err := growInstanceLogin(ctx, store, "prod", connectionAdmission{Limit: 80, Budget: 150}); err != nil || have != 40 {
		t.Fatalf("an admitted grow returned (%d, %v), want (40, nil)", have, err)
	}
	if store.limit != 80 {
		t.Fatalf("an admitted grow left the login at %d, want 80", store.limit)
	}
}

// 🔴 THE SHRINK READS WHAT THE LOGIN HOLDS. A re-run after a failed rollout grew nothing,
// and still has to bring a login left at the larger limit down.
func TestTheShrinkComparesAgainstWhatTheLoginHolds(t *testing.T) {
	ctx := context.Background()
	store := &fakeLoginStore{limit: 320}
	if have, err := shrinkInstanceLogin(ctx, store, "prod", 280); err != nil || have != 320 {
		t.Fatalf("shrink returned (%d, %v), want (320, nil)", have, err)
	}
	if store.limit != 280 {
		t.Fatalf("the login was left at %d, want 280", store.limit)
	}

	// Never upwards: raising is the grow's, and only with admission.
	store = &fakeLoginStore{limit: 200}
	if _, err := shrinkInstanceLogin(ctx, store, "prod", 280); err != nil || store.altered() {
		t.Fatalf("a shrink below the need raised the login (err %v, statements %v)", err, store.statement)
	}
}

// 🔴 A REHEARSAL OPENS NO SESSION. The only session dcctl opens converges the provisioner
// role as the superuser first, which is a write.
func TestARehearsedUpgradeOpensNoSessionOnTheStore(t *testing.T) {
	restore := upgradeLoginSession
	t.Cleanup(func() { upgradeLoginSession = restore })
	upgradeLoginSession = func(context.Context, *State, func(instanceDBQuerier) error) error {
		t.Error("a dry run opened a session on the relational store")
		return nil
	}
	st := &State{Instance: "prod", DryRun: true, EnabledAreas: []string{"user-management", "device-management"}}
	out := captureStdout(t, func() {
		if err := precheckUpgradeLogin(context.Background(), st); err != nil {
			t.Errorf("the rehearsed check failed: %v", err)
		}
	})
	if !strings.Contains(out, "[dry-run]") || !strings.Contains(out, "80 connections") {
		t.Fatalf("the rehearsal did not say what the real run checks:\n%s", out)
	}
}

// 🔴 A REFUSAL IS THE TYPED BUDGET REFUSAL, NAMING THE REMEDY. Driven through the session
// seam, so the wrapping the step adds is what is checked.
func TestAnUpgradeOverBudgetIsRefusedNamingTheRemedy(t *testing.T) {
	restore := upgradeLoginSession
	t.Cleanup(func() { upgradeLoginSession = restore })
	store := &fakeLoginStore{limit: 40, others: 60}
	upgradeLoginSession = func(_ context.Context, _ *State, fn func(instanceDBQuerier) error) error {
		return fn(store)
	}
	st := &State{
		Instance:     "prod",
		EnabledAreas: []string{"user-management", "device-management"},
		Install:      &InstallRecord{Outputs: InstallOutputs{Rdb: ClusterRdb{MaxConnections: 150}}},
	}
	var err error
	captureStdout(t, func() { err = precheckUpgradeLogin(context.Background(), st) })
	var typed *ErrConnectionBudget
	if !errors.As(err, &typed) {
		t.Fatalf("an upgrade over budget was not refused as a budget refusal: %v", err)
	}
	for _, want := range []string{"--max-connections", "80 connections, up from 40", "nothing has been changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if store.altered() {
		t.Fatal("the check altered the login")
	}
}

// resizeCalls swaps resizeUpgradeLogin for a recorder.
func resizeCalls(t *testing.T, fail map[loginResize]error) *[]string {
	t.Helper()
	restore := resizeUpgradeLogin
	t.Cleanup(func() { resizeUpgradeLogin = restore })
	var calls []string
	resizeUpgradeLogin = func(_ context.Context, _ *State, dir loginResize) error {
		calls = append(calls, map[loginResize]string{loginResizeGrow: "grow", loginResizeShrink: "shrink"}[dir])
		return fail[dir]
	}
	return &calls
}

// 🔴 GROW, ROLL, SHRINK — AND A FAILED ROLLOUT SHRINKS NOTHING. The old release's pods
// may still be running after a failed rollout, holding the connections the shrink would
// take away; and a refused grow must not reach the rollout it was sizing for.
func TestTheRolloutIsBracketedByAGrowAndAShrink(t *testing.T) {
	ctx := context.Background()

	calls := resizeCalls(t, nil)
	if err := rolloutWithLoginResize(ctx, &State{}, func() error {
		*calls = append(*calls, "rollout")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, ","); got != "grow,rollout,shrink" {
		t.Fatalf("the rollout ran as %s; want grow,rollout,shrink", got)
	}

	calls = resizeCalls(t, nil)
	rolloutErr := errors.New("services did not roll over")
	if err := rolloutWithLoginResize(ctx, &State{}, func() error {
		*calls = append(*calls, "rollout")
		return rolloutErr
	}); !errors.Is(err, rolloutErr) {
		t.Fatalf("a failed rollout returned %v", err)
	}
	if got := strings.Join(*calls, ","); got != "grow,rollout" {
		t.Fatalf("a failed rollout ran as %s; want grow,rollout and no shrink", got)
	}

	growErr := errors.New("no room")
	calls = resizeCalls(t, map[loginResize]error{loginResizeGrow: growErr})
	if err := rolloutWithLoginResize(ctx, &State{}, func() error {
		*calls = append(*calls, "rollout")
		return nil
	}); !errors.Is(err, growErr) {
		t.Fatalf("a refused grow returned %v", err)
	}
	if got := strings.Join(*calls, ","); got != "grow" {
		t.Fatalf("a refused grow ran as %s; want the grow alone", got)
	}
}

// 🔴 AND UPGRADE HAS TO CALL THEM, IN THIS ORDER. Upgrade reaches a cluster from its first
// line, so no unit test drives it: a check moved after recordUpgradedVersion — a refusal
// that then leaves the instance declared on a release it cannot run — or a helm upgrade
// moved out of the bracket would break nothing above.
func TestUpgradeChecksTheBudgetFirstAndRollsOutInsideTheBracket(t *testing.T) {
	want := []string{"precheckUpgradeLogin", "recordUpgradedVersion", "rolloutWithLoginResize"}
	if got := callsWithin(t, "Upgrade", want...); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Upgrade calls %v; want %v, in that order", got, want)
	}

	var bracket *ast.CallExpr
	_, files := packageFiles(t)
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "Upgrade" || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "rolloutWithLoginResize" {
						bracket = call
					}
				}
				return true
			})
		}
	}
	if bracket == nil || len(bracket.Args) == 0 {
		t.Fatal("Upgrade does not call rolloutWithLoginResize")
	}
	inside := map[string]bool{}
	ast.Inspect(bracket.Args[len(bracket.Args)-1], func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				inside[id.Name] = true
			}
		}
		return true
	})
	for _, fn := range []string{"helmInstall", "waitForAreas"} {
		if !inside[fn] {
			t.Errorf("Upgrade calls %s outside the rollout the login is sized around", fn)
		}
	}
}
