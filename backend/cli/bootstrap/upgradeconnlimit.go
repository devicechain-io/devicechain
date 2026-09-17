// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/fatih/color"
	pgx "github.com/jackc/pgx/v5"
)

// An upgrade re-sizes the instance's database login.
//
// 🔴 THE LIMIT IS SET BY BOOTSTRAP AND NEEDED BY THE RELEASE, AND ONLY AN UPGRADE MOVES
// BETWEEN RELEASES. instanceConnectionLimit is a function of the release — which areas a
// profile expands to, which of them open a relational pool, the pool size — and bootstrap
// writes it once, into CREATE ROLE. A bootstrap re-run over a live instance is refused, so
// nothing else ever revisits it: a release that needed more connections would roll its
// services onto a login capped for the previous one, and the store would refuse the new
// pods' pools mid-rollout while reporting itself healthy. Too LOW starves only the upgraded
// instance; too HIGH leaves budget granted that nothing uses, and admission of every other
// instance counts it.
//
// 🔴 GROW BEFORE THE ROLLOUT, SHRINK AFTER IT. The rollout is when the need peaks — a new
// pod holds a full pool before the old one lets go — so a larger limit has to be in place
// before it starts, and a smaller one only once the old release's pods are gone. A failed
// rollout shrinks nothing: the old pods may still be running.
//
// 🔴 A GROW IS ADMITTED, AND REFUSED BEFORE ANYTHING IS WRITTEN. The store's budget is
// shared, so a larger limit is admitted like a new instance — and an upgrade that could
// not get its connections must say so before the declaration, the operator or anything
// else has moved, not after half the instance is on the new release. The check at the
// start is the early answer; the grow re-runs admission under the lock, because the room
// may have been taken in between.

// loginResize is which way a login's CONNECTION LIMIT has to move.
type loginResize int

const (
	loginResizeNone loginResize = iota
	loginResizeGrow
	loginResizeShrink
)

// loginResizeFor decides which way a login holding have has to move to hold want.
//
// 🔴 EQUAL IS NOTHING, AND THAT INCLUDES ADMISSION. An operator may have lowered the
// store's --max-connections below what its instances already hold; that refuses new
// grants, and it must not refuse the upgrade of an instance that is asking for nothing
// new.
//
// A login with no limit at all (have < 0) is a grow: it was built before logins were
// admitted, so it is counted in no budget, and giving it a limit is entering the count.
func loginResizeFor(have, want int) loginResize {
	switch {
	case have < 0 || want > have:
		return loginResizeGrow
	case want < have:
		return loginResizeShrink
	default:
		return loginResizeNone
	}
}

// readInstanceLoginLimit reads the CONNECTION LIMIT of the instance's login, refusing a
// login that is missing or that dcctl's provisioner did not make.
func readInstanceLoginLimit(ctx context.Context, q instanceDBQuerier, instance string) (int, error) {
	var super, createdb, createrole, admin bool
	var limit int32
	err := q.QueryRow(ctx, `
		select r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolconnlimit,
		       coalesce((select bool_or(m.admin_option) from pg_auth_members m
		                 where m.roleid = r.oid and m.member = (select oid from pg_roles where rolname = current_user)), false)
		from pg_roles r where r.rolname = $1`, instance).Scan(&super, &createdb, &createrole, &limit, &admin)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 🔴 AN INCONSISTENCY, NOT A LOGIN TO CREATE. The upgrade read this login's password
		// out of its Secret a moment ago, so the instance was given one; the store not
		// having it means something removed it outside dcctl, or the store was replaced
		// under a running instance. Creating one here would paper over whichever it was.
		return 0, fmt.Errorf("%w: instance %q has a database login Secret, but the relational store has no "+
			"login named %q — the two disagree, so the login was removed outside dcctl or the store was "+
			"replaced under the instance. Nothing has been changed; find out which before upgrading",
			errInstanceDatabaseNotOurs, instance, instance)
	case err != nil:
		return 0, fmt.Errorf("looking up the login for instance %q: %w", instance, err)
	}
	if err := refuseALoginDcctlDidNotMake(instance, admin, super, createdb, createrole); err != nil {
		return 0, err
	}
	return int(limit), nil
}

// checkInstanceLoginResize reads the login's limit and, when the release needs more,
// asks whether the store would admit it — without changing anything. It returns the limit
// the login holds now.
func checkInstanceLoginResize(ctx context.Context, q instanceDBQuerier, instance string, admit connectionAdmission) (int, error) {
	if err := validLoginResize(instance, admit.Limit); err != nil {
		return 0, err
	}
	have, err := readInstanceLoginLimit(ctx, q, instance)
	if err != nil {
		return 0, err
	}
	if loginResizeFor(have, admit.Limit) != loginResizeGrow {
		return have, nil
	}
	return have, admitInstance(ctx, q, instance, admit)
}

// growInstanceLogin raises the login's limit to admit.Limit when it holds less, admitting
// the larger limit under the same lock bootstrap admits under. It returns the limit the
// login held before.
func growInstanceLogin(ctx context.Context, q instanceDBQuerier, instance string, admit connectionAdmission) (int, error) {
	if err := validLoginResize(instance, admit.Limit); err != nil {
		return 0, err
	}
	if _, err := q.Exec(ctx, "select pg_advisory_lock($1)", int64(instanceAdmissionLock)); err != nil {
		return 0, fmt.Errorf("serialising admission to the relational store: %w", err)
	}
	defer func() {
		_, _ = q.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", int64(instanceAdmissionLock))
	}()
	// Read under the lock, not carried over from the check: this is the value admission
	// is decided on.
	have, err := readInstanceLoginLimit(ctx, q, instance)
	if err != nil {
		return 0, err
	}
	if loginResizeFor(have, admit.Limit) != loginResizeGrow {
		return have, nil
	}
	if err := admitInstance(ctx, q, instance, admit); err != nil {
		return have, err
	}
	return have, alterInstanceLoginLimit(ctx, q, instance, admit.Limit)
}

// shrinkInstanceLogin lowers the login's limit to limit when it holds more. It returns
// the limit the login held before.
//
// 🔴 IT COMPARES AGAINST WHAT THE LOGIN HOLDS, NOT AGAINST WHETHER THIS RUN GREW IT. A
// re-run after a rollout that failed on an earlier attempt finds the login still at the
// old release's larger limit, grew nothing, and must still bring it down.
//
// No admission and no lock: a smaller limit can only make an admission running beside it
// count more than is granted, never less.
func shrinkInstanceLogin(ctx context.Context, q instanceDBQuerier, instance string, limit int) (int, error) {
	if err := validLoginResize(instance, limit); err != nil {
		return 0, err
	}
	have, err := readInstanceLoginLimit(ctx, q, instance)
	if err != nil {
		return 0, err
	}
	if loginResizeFor(have, limit) != loginResizeShrink {
		return have, nil
	}
	return have, alterInstanceLoginLimit(ctx, q, instance, limit)
}

// alterInstanceLoginLimit sets the login's limit and nothing else — no PASSWORD, which
// an upgrade reads and never writes.
func alterInstanceLoginLimit(ctx context.Context, q instanceDBQuerier, instance string, limit int) error {
	if _, err := q.Exec(ctx, fmt.Sprintf("ALTER ROLE %s CONNECTION LIMIT %d",
		pgx.Identifier{instance}.Sanitize(), limit)); err != nil {
		return fmt.Errorf("setting the connection limit of the login for instance %q: %w", instance, err)
	}
	return nil
}

func validLoginResize(instance string, limit int) error {
	if err := ValidateInstanceName(instance); err != nil {
		return err
	}
	if limit <= 0 {
		return fmt.Errorf("no connection limit was settled for instance %q (limit %d); an unlimited "+
			"login could starve every other instance on the store", instance, limit)
	}
	return nil
}

// upgradeLoginSession opens a session on the shared store as the provisioner. Indirected
// so the steps below can be driven against a real PostgreSQL without a cluster, and so a
// dry run can prove it opens none.
var upgradeLoginSession = func(ctx context.Context, st *State, fn func(instanceDBQuerier) error) error {
	return withProvisionerSession(ctx, st.KubeContext, st.Install.Outputs.Rdb, fn)
}

// upgradeLoginAdmission is this release's need, admitted against the store's budget.
func upgradeLoginAdmission(st *State) (connectionAdmission, error) {
	limit, err := instanceConnectionLimit(st)
	if err != nil {
		return connectionAdmission{}, fmt.Errorf("sizing this instance's connection limit: %w", err)
	}
	if st.Install == nil {
		return connectionAdmission{}, errors.New("no install record was read for this cluster, so the " +
			"relational store holding this instance's login cannot be found")
	}
	return connectionAdmission{Limit: limit, Budget: st.Install.Outputs.Rdb.MaxConnections}, nil
}

// precheckUpgradeLogin answers, before the upgrade writes anything, whether the store can
// give this release the connections it needs.
//
// 🔴 A DRY RUN OPENS NO SESSION. The only session dcctl can open converges the provisioner
// role first, as the database superuser — an idempotent write, but a write, and a
// rehearsal writes nothing. A read-only session would need the provisioner to be signed
// in to without being converged, which is a second path to the store kept only for this
// line. So a rehearsal says what the real run checks, the way the bootstrap's budget
// check does under --dry-run.
func precheckUpgradeLogin(ctx context.Context, st *State) error {
	limit, err := instanceConnectionLimit(st)
	if err != nil {
		return fmt.Errorf("sizing this instance's connection limit: %w", err)
	}
	if st.DryRun {
		wouldDo(fmt.Sprintf("check the instance's database login against this release's need of %d "+
			"connections, re-sizing it (grow before the rollout, shrink after) and refusing a grow the "+
			"relational store has no budget for", limit))
		return nil
	}
	admit, err := upgradeLoginAdmission(st)
	if err != nil {
		return err
	}

	doing("checking the instance's database login against this release's connection need")
	var have int
	err = upgradeLoginSession(ctx, st, func(q instanceDBQuerier) (err error) {
		have, err = checkInstanceLoginResize(ctx, q, st.Instance, admit)
		return err
	})
	if errors.Is(err, errNoConnectionBudget) {
		err = fmt.Errorf("this release needs instance %q's database login to hold %d connections, up from %s, "+
			"and nothing has been changed: %w", st.Instance, admit.Limit, describeLoginLimit(have), err)
	}
	if err != nil {
		return fail("checking the instance's database login", asBudgetRefusal(err))
	}
	done()
	return nil
}

// resizeUpgradeLogin moves the login's limit in one direction, printing what it did.
// Indirected so the rollout's bracket can be driven without a store; the SQL is
// growInstanceLogin and shrinkInstanceLogin, tested against a real PostgreSQL.
var resizeUpgradeLogin = func(ctx context.Context, st *State, dir loginResize) error {
	admit, err := upgradeLoginAdmission(st)
	if err != nil {
		return err
	}
	what := "sizing the instance's database login for the rollout"
	if dir == loginResizeShrink {
		what = "trimming the instance's database login to this release's need"
	}
	doing(what)
	var have int
	err = upgradeLoginSession(ctx, st, func(q instanceDBQuerier) (err error) {
		if dir == loginResizeShrink {
			have, err = shrinkInstanceLogin(ctx, q, st.Instance, admit.Limit)
		} else {
			have, err = growInstanceLogin(ctx, q, st.Instance, admit)
		}
		return err
	})
	if errors.Is(err, errNoConnectionBudget) {
		// 🔴 THE CHECK AT THE START PASSED, SO SOMETHING TOOK THE ROOM SINCE — and by now the
		// declaration and the operator have moved. Said, because "re-run" is the remedy and
		// the operator needs to know it finishes rather than repeats.
		err = fmt.Errorf("this release needs instance %q's database login to hold %d connections, up from %s, "+
			"and the room it had when this upgrade started has been taken since. The operator has been "+
			"upgraded and the instance's services have NOT; make room and re-run `dcctl upgrade` to finish: %w",
			st.Instance, admit.Limit, describeLoginLimit(have), err)
	}
	if err != nil {
		return fail(what, asBudgetRefusal(err))
	}
	if loginResizeFor(have, admit.Limit) == dir {
		fmt.Println(color.GreenString("%s → %d connections.", describeLoginLimit(have), admit.Limit))
		return nil
	}
	done()
	return nil
}

func describeLoginLimit(limit int) string {
	if limit < 0 {
		return "unlimited"
	}
	return fmt.Sprint(limit)
}

// rolloutWithLoginResize runs a rollout between growing the instance's login to this
// release's need and shrinking it back down. A rollout that fails shrinks nothing.
//
// 🔴 A FUNCTION OF ITS OWN SO THE BRACKET IS SOMETHING A TEST CAN BREAK. Upgrade sits
// behind a live cluster from its first line, so a grow that moved after the rollout, or a
// shrink that ran over a failed one, would break nothing a unit test could reach.
func rolloutWithLoginResize(ctx context.Context, st *State, rollout func() error) error {
	if err := resizeUpgradeLogin(ctx, st, loginResizeGrow); err != nil {
		return err
	}
	if err := rollout(); err != nil {
		return err
	}
	return resizeUpgradeLogin(ctx, st, loginResizeShrink)
}
