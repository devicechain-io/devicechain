// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	pgx "github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The per-instance login and database, against a REAL PostgreSQL.
//
// 🔴 NOTHING HERE CAN BE FAKED USEFULLY. Every property that matters — that a CREATEROLE
// role cannot create a database owned by the role it just made until it grants itself
// SET, that a REVOKE by a non-owner is a warning rather than an error, that the password
// verifier dcctl computes is one the server accepts at login — is PostgreSQL's
// behaviour, and a fake would encode my model of it. So these run against a server, and
// skip loudly without one.
//
// DCCTL_TEST_POSTGRES_URL is a SUPERUSER connection string, used only to stand up the
// provisioner the way dcctl declares it and to tear everything down; every
// statement under test runs as the provisioner. Locally:
//
//	docker run -d --rm --name dcctl-pg -e POSTGRES_PASSWORD=pw -p 55432:5432 postgres:17
//	DCCTL_TEST_POSTGRES_URL=postgres://postgres:pw@127.0.0.1:55432/postgres?sslmode=disable \
//	  go test ./bootstrap -run InstanceDatabase -count=1
const testPostgresURLEnv = "DCCTL_TEST_POSTGRES_URL"

const testProvisioner = rdbProvisionerUsername

func superuserConn(t *testing.T) (*pgx.Conn, *url.URL) {
	t.Helper()
	raw := os.Getenv(testPostgresURLEnv)
	if raw == "" {
		t.Skipf("%s is not set; the per-instance database checks need a real PostgreSQL", testPostgresURLEnv)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %s: %v", testPostgresURLEnv, err)
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatalf("connecting as the superuser: %v", err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return c, u
}

// connectAs opens a session as role/password on database, reusing the superuser URL's
// host and port.
func connectAs(t *testing.T, base *url.URL, role, password, database string) (*pgx.Conn, error) {
	t.Helper()
	u := *base
	u.User = url.UserPassword(role, password)
	u.Path = "/" + database
	return pgx.Connect(context.Background(), u.String())
}

// serverChecksPasswords reports whether the server rejects a wrong password.
func serverChecksPasswords(t *testing.T, base *url.URL) bool {
	t.Helper()
	c, err := connectAs(t, base, base.User.Username(), "certainly-not-the-password", "postgres")
	if err == nil {
		c.Close(context.Background())
		return false
	}
	return true
}

// withProvisioner declares the provisioner the way dcctl does and cleans up every
// instance database and role afterwards.
func withProvisioner(t *testing.T, instances ...string) (*pgx.Conn, *url.URL) {
	t.Helper()
	ctx := context.Background()
	su, base := superuserConn(t)
	cleanup := func() {
		for _, i := range instances {
			id := pgx.Identifier{i}.Sanitize()
			_, _ = su.Exec(ctx, "DROP DATABASE IF EXISTS "+id+" WITH (FORCE)")
			_, _ = su.Exec(ctx, "DROP ROLE IF EXISTS "+id)
		}
		_, _ = su.Exec(ctx, "DROP ROLE IF EXISTS "+testProvisioner)
	}
	cleanup()
	t.Cleanup(cleanup)
	// Declared with the very statement dcctl runs as the superuser — so this also proves
	// the verifier it computes is one the server accepts at login.
	sql, err := provisionerSQL("provisioner-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, sql); err != nil {
		t.Fatalf("declaring the provisioner: %v", err)
	}
	p, err := connectAs(t, base, testProvisioner, "provisioner-pw", "postgres")
	if err != nil {
		t.Fatalf("connecting as the provisioner: %v", err)
	}
	t.Cleanup(func() { p.Close(ctx) })
	return p, base
}

// 🔴🔴 THE PROPERTY THE MODEL EXISTS FOR: two instances cannot reach each other's
// databases. The positive half (each login reaches its own) is what makes the negative
// half mean anything — a login that could reach nothing would pass it too.
func TestInstanceDatabaseLoginsCannotReachEachOthersDatabases(t *testing.T) {
	ctx := context.Background()
	p, base := withProvisioner(t, "alpha", "beta")
	q := pgxSession{p}

	for _, i := range []string{"alpha", "beta"} {
		if err := ensureInstanceDatabase(ctx, q, i, i+"-pw", testAdmission); err != nil {
			t.Fatalf("provisioning %s: %v", i, err)
		}
	}

	for _, i := range []string{"alpha", "beta"} {
		c, err := connectAs(t, base, i, i+"-pw", i)
		if err != nil {
			t.Fatalf("%s could not connect to its OWN database with the password it was given: %v", i, err)
		}
		// Services create a schema per functional area and tables in it, as the owner.
		if _, err := c.Exec(ctx, `CREATE SCHEMA "device-management"; CREATE TABLE "device-management".t (id int)`); err != nil {
			t.Errorf("%s cannot create its schema in its own database: %v", i, err)
		}
		c.Close(ctx)
	}

	for _, pair := range [][2]string{{"alpha", "beta"}, {"beta", "alpha"}} {
		c, err := connectAs(t, base, pair[0], pair[0]+"-pw", pair[1])
		if err == nil {
			c.Close(ctx)
			t.Errorf("%s connected to %s's database; instances must not reach each other's data", pair[0], pair[1])
			continue
		}
		if pgErrorCode(err) != "42501" {
			t.Errorf("%s → %s failed, but not with permission denied (42501): %v", pair[0], pair[1], err)
		}
	}

	// The provisioner itself holds no instance's privileges while it is not acting as
	// that instance: INHERIT FALSE. Asked of the membership rather than of a privilege: a
	// database still open to PUBLIC would answer "can connect" for other reasons.
	var inherits bool
	if err := p.QueryRow(ctx, `select pg_has_role(current_user, 'alpha', 'USAGE')`).Scan(&inherits); err != nil {
		t.Fatal(err)
	}
	if inherits {
		t.Error("the provisioner inherits the instance login's privileges; its membership must be SET without INHERIT")
	}
}

// A re-run changes the password to the one given and nothing else, and the old one stops
// working — which is what lets a lost Secret be repaired by writing a new value.
func TestInstanceDatabaseRerunTakesTheNewPassword(t *testing.T) {
	ctx := context.Background()
	p, base := withProvisioner(t, "gamma")
	q := pgxSession{p}
	if err := ensureInstanceDatabase(ctx, q, "gamma", "first-pw", testAdmission); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstanceDatabase(ctx, q, "gamma", "second-pw", testAdmission); err != nil {
		t.Fatalf("a re-run over its own login and database was refused: %v", err)
	}
	if c, err := connectAs(t, base, "gamma", "second-pw", "gamma"); err != nil {
		t.Errorf("the new password does not log in: %v", err)
	} else {
		c.Close(ctx)
	}
	// Only meaningful against a server that checks passwords: a throwaway server started
	// with `trust` host authentication admits any password, which says nothing about
	// what was set.
	if !serverChecksPasswords(t, base) {
		t.Log("this server admits any password (trust authentication); the old password's rejection is not checkable here")
		return
	}
	if c, err := connectAs(t, base, "gamma", "first-pw", "gamma"); err == nil {
		c.Close(ctx)
		t.Error("the old password still logs in after a re-run set a new one")
	}
}

// 🔴 THE PRE-ISOLATION SHAPE IS REFUSED, NOT ADOPTED. A database the store's shared owner
// created must not be handed to a new login (its tables would be unreadable) nor
// silently re-owned (that carries the shared access forward).
func TestInstanceDatabaseRefusesADatabaseItDidNotCreate(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "delta")
	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, `CREATE DATABASE delta`); err != nil {
		t.Fatal(err)
	}
	err := ensureInstanceDatabase(ctx, pgxSession{p}, "delta", "pw", testAdmission)
	if !errors.Is(err, errInstanceDatabaseNotOurs) {
		t.Fatalf("a database owned by someone else must be refused as not ours; got %v", err)
	}
	if !strings.Contains(err.Error(), "dcctl destroy") {
		t.Errorf("the refusal must say how to get out of it: %v", err)
	}
}

// A role the provisioner did not create is not re-passworded and handed to services.
func TestInstanceDatabaseRefusesARoleItDidNotCreate(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "epsilon")
	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, `CREATE ROLE epsilon LOGIN PASSWORD 'theirs'`); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstanceDatabase(ctx, pgxSession{p}, "epsilon", "pw", testAdmission); !errors.Is(err, errInstanceDatabaseNotOurs) {
		t.Fatalf("a role another identity created must be refused; got %v", err)
	}
}

// Drop removes both and verifies it; dropping what is already gone is success; and a
// database dcctl did not create is left alone.
func TestInstanceDatabaseDropRemovesBothAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	p, base := withProvisioner(t, "zeta", "eta")
	q := pgxSession{p}
	if err := ensureInstanceDatabase(ctx, q, "zeta", "pw", testAdmission); err != nil {
		t.Fatal(err)
	}
	// A live session on the database, as a running service would hold.
	held, err := connectAs(t, base, "zeta", "pw", "zeta")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close(ctx)

	if err := dropInstanceDatabase(ctx, q, "zeta"); err != nil {
		t.Fatalf("dropping an instance's database and login: %v", err)
	}
	var dbLeft, roleLeft bool
	if err := p.QueryRow(ctx, `select exists(select 1 from pg_database where datname='zeta'),
	                                  exists(select 1 from pg_roles where rolname='zeta')`).Scan(&dbLeft, &roleLeft); err != nil {
		t.Fatal(err)
	}
	if dbLeft || roleLeft {
		t.Errorf("after the drop: database=%t login=%t", dbLeft, roleLeft)
	}
	if err := dropInstanceDatabase(ctx, q, "zeta"); err != nil {
		t.Errorf("dropping what is already gone must succeed: %v", err)
	}

	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, `CREATE DATABASE eta`); err != nil {
		t.Fatal(err)
	}
	if err := dropInstanceDatabase(ctx, q, "eta"); !errors.Is(err, errInstanceDatabaseNotOurs) {
		t.Errorf("a database dcctl did not create must not be dropped; got %v", err)
	}
}

// A name that is not an instance name never reaches SQL.
func TestInstanceDatabaseRefusesANameThatIsNotAnInstanceName(t *testing.T) {
	for _, bad := range []string{`a"; DROP ROLE dc_provisioner; --`, "Alpha", "postgres", ""} {
		if err := ensureInstanceDatabase(context.Background(), nil, bad, "pw", testAdmission); err == nil {
			t.Errorf("%q was accepted", bad)
		}
		if err := dropInstanceDatabase(context.Background(), nil, bad); err == nil {
			t.Errorf("%q was accepted for a drop", bad)
		}
	}
}

func TestTheVerifierHasTheShapePostgreSQLStores(t *testing.T) {
	v, err := scramSHA256Verifier("abc_DEF-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v, "SCRAM-SHA-256$4096:") || strings.Count(v, "$") != 2 || strings.Count(v, ":") != 2 {
		t.Errorf("unexpected verifier shape: %d chars", len(v))
	}
	w, _ := scramSHA256Verifier("abc_DEF-123")
	if v == w {
		t.Error("two verifiers of one password share a salt")
	}
	for _, bad := range []string{"", "has space", "naïve", "quote'"[0:5] + "\n"} {
		if _, err := scramSHA256Verifier(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A store built before per-instance logins is refused by its owner; one this dcctl
// builds, and one restored from it, is not.
func TestAStoreBuiltBeforeInstanceLoginsIsRefused(t *testing.T) {
	cluster := func(path []string, owner string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		if err := unstructured.SetNestedField(u.Object, owner, path...); err != nil {
			t.Fatal(err)
		}
		return u
	}
	initdb := []string{"spec", "bootstrap", "initdb", "owner"}
	recovery := []string{"spec", "bootstrap", "recovery", "owner"}
	if err := refuseAPreIsolationOwner(cluster(initdb, "dc_owner")); err != nil {
		t.Errorf("a store built by this dcctl was refused: %v", err)
	}
	if err := refuseAPreIsolationOwner(cluster(recovery, "dc_owner")); err != nil {
		t.Errorf("a restored store built by this dcctl was refused: %v", err)
	}
	for _, c := range []*unstructured.Unstructured{
		cluster(initdb, "devicechain"), cluster(recovery, "devicechain"),
		{Object: map[string]any{"spec": map[string]any{}}},
	} {
		err := refuseAPreIsolationOwner(c)
		if err == nil || !strings.Contains(err.Error(), "Recreate the cluster") {
			t.Errorf("a store that cannot isolate instances was not refused with the way out: %v", err)
		}
	}
}

// Running the provisioner statement again resets the role to exactly its attributes and
// the given password — which is what makes a lost Secret repairable and a tampered role
// correctable.
func TestTheProvisionerStatementConvergesTheRole(t *testing.T) {
	ctx := context.Background()
	_, base := withProvisioner(t)
	su, _ := superuserConn(t)
	if _, err := su.Exec(ctx, "ALTER ROLE "+testProvisioner+" SUPERUSER NOCREATEROLE CONNECTION LIMIT -1"); err != nil {
		t.Fatal(err)
	}
	sql, err := provisionerSQL("second-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := su.Exec(ctx, sql); err != nil {
		t.Fatalf("re-running the provisioner statement: %v", err)
	}
	var super, createrole, createdb bool
	var limit int
	if err := su.QueryRow(ctx, `select rolsuper, rolcreaterole, rolcreatedb, rolconnlimit from pg_roles where rolname = $1`,
		testProvisioner).Scan(&super, &createrole, &createdb, &limit); err != nil {
		t.Fatal(err)
	}
	if super || !createrole || !createdb || limit != provisionerConnectionLimit {
		t.Errorf("provisioner after re-run: super=%t createrole=%t createdb=%t limit=%d", super, createrole, createdb, limit)
	}
	if c, err := connectAs(t, base, testProvisioner, "second-pw", "postgres"); err != nil {
		t.Errorf("the new password does not log in: %v", err)
	} else {
		c.Close(ctx)
	}
}

// 🔴 A DROP BLOCKED BY A SUPERUSER'S SESSION IS A WAIT, NOT A REFUSAL. The database
// operator's metrics exporter connects to every database as a superuser; a non-superuser
// FORCE cannot end that session. It must come back retryable — and the database must
// still be there to drop once the session goes.
func TestADropBlockedByASuperuserSessionIsRetryable(t *testing.T) {
	ctx := context.Background()
	p, base := withProvisioner(t, "theta")
	q := pgxSession{p}
	if err := ensureInstanceDatabase(ctx, q, "theta", "pw", testAdmission); err != nil {
		t.Fatal(err)
	}
	su := *base
	su.Path = "/theta"
	held, err := pgx.Connect(ctx, su.String())
	if err != nil {
		t.Fatal(err)
	}
	err = dropInstanceDatabase(ctx, q, "theta")
	if !errors.Is(err, errStoreNotReady) {
		held.Close(ctx)
		t.Fatalf("a drop blocked by a superuser session must be retryable; got %v", err)
	}
	held.Close(ctx)
	// The session is gone; the retry succeeds.
	var dropErr error
	for i := 0; i < 20; i++ {
		if dropErr = dropInstanceDatabase(ctx, q, "theta"); dropErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dropErr != nil {
		t.Errorf("the drop did not succeed once the session ended: %v", dropErr)
	}
}

// testAdmission is a limit and budget every other test here fits in comfortably.
var testAdmission = connectionAdmission{Limit: 40, Budget: 600}

// 🔴 THE BUDGET IS ENFORCED BY THE STORE'S OWN COUNT. Admitted while the granted limits
// fit, refused when the next would not, and a dropped login gives its share back — the
// last is what makes the count a count rather than a ratchet.
func TestInstanceDatabaseAdmitsOnlyWhatTheBudgetHolds(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "adm-a", "adm-b", "adm-c")
	q := pgxSession{p}
	// 100 less the 20 reserved leaves 80: two logins of 40, and not a third.
	tight := connectionAdmission{Limit: 40, Budget: 100}

	for _, i := range []string{"adm-a", "adm-b"} {
		if err := ensureInstanceDatabase(ctx, q, i, "pw", tight); err != nil {
			t.Fatalf("admitting %s: %v", i, err)
		}
	}
	var limit int
	if err := p.QueryRow(ctx, `select rolconnlimit from pg_roles where rolname = 'adm-a'`).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	if limit != tight.Limit {
		t.Fatalf("adm-a's login has CONNECTION LIMIT %d, want %d", limit, tight.Limit)
	}

	err := ensureInstanceDatabase(ctx, q, "adm-c", "pw", tight)
	if !errors.Is(err, errNoConnectionBudget) {
		t.Fatalf("a third login over a full budget was not refused for the budget: %v", err)
	}
	var exists bool
	if err := p.QueryRow(ctx, `select exists(select 1 from pg_roles where rolname = 'adm-c')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("the refused instance's login was created anyway")
	}

	// A re-run of an admitted instance does not count against itself.
	if err := ensureInstanceDatabase(ctx, q, "adm-a", "pw2", tight); err != nil {
		t.Fatalf("re-running an admitted instance on a full budget was refused: %v", err)
	}

	if err := dropInstanceDatabase(ctx, q, "adm-b"); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstanceDatabase(ctx, q, "adm-c", "pw", tight); err != nil {
		t.Fatalf("a dropped login did not give its share back: %v", err)
	}
}

// An administered login with no limit is an instance from before the budget, and the
// count cannot say what it may take — so admission refuses rather than counting it as 0.
func TestInstanceDatabaseRefusesAdmissionBesideAnUnlimitedLogin(t *testing.T) {
	ctx := context.Background()
	p, _ := withProvisioner(t, "unlimited", "adm-d")
	if _, err := p.Exec(ctx, `CREATE ROLE unlimited LOGIN`); err != nil {
		t.Fatal(err)
	}
	err := ensureInstanceDatabase(ctx, pgxSession{p}, "adm-d", "pw", testAdmission)
	if !errors.Is(err, errNoConnectionBudget) || !strings.Contains(err.Error(), "unlimited") {
		t.Fatalf("admission beside an unlimited login was not refused naming it: %v", err)
	}
}

func TestInstanceDatabaseRefusesAnUnsizedAdmission(t *testing.T) {
	for _, a := range []connectionAdmission{{Limit: 0, Budget: 600}, {Limit: 40, Budget: 0}} {
		if err := ensureInstanceDatabase(context.Background(), nil, "sized", "pw", a); err == nil {
			t.Fatalf("admission %+v was accepted", a)
		}
	}
}
