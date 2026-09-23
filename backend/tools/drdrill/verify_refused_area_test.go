// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A flag that lets a caller past a premise check is the shape that was deliberately
// removed from this tool once already: --skip-api existed, and because the rig reads
// only the exit code, a caller who passed it still got a decrypt-failure code and was
// still told the control held. --secret-area-refused must not become that flag again.
//
// What keeps it from becoming that is not the comment on it — it is that the mode
// asserts a PAIR, and one half of the pair FAILS on exactly the instance a misuse
// would be pointed at. These tests are that claim, made executable.

// areaStub serves the two areas the mode asks about. A false flag serves 503, which is
// what an ingress in front of an area that refused to start actually returns; a true
// one answers the way a live service does.
type areaStub struct {
	userManagementServing bool
	notificationServing   bool
}

func (a areaStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	serve := func(up bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !up {
				http.Error(w, "503 Service Temporarily Unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"__typename":"Query"}}`))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", serve(a.userManagementServing))
	mux.HandleFunc("/api/notification-management/graphql", serve(a.notificationServing))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// membersSay is a membershipReader that answers from fixed values, so the decision
// can be tested apart from the database read (which readMembership's own tests cover).
func membersSay(found, member bool, err error) membershipReader {
	return func(context.Context, string, string) (bool, bool, error) { return found, member, err }
}

func refusedOpts(t *testing.T, srv *httptest.Server) verifyOptions {
	t.Helper()
	return verifyOptions{
		server:            strings.TrimPrefix(srv.URL, "http://"),
		scheme:            "http",
		secretAreaRefused: true,
	}
}

func drillReceipt() Receipt {
	return Receipt{
		Instance: "drdrill", Tenant: "drdrill", Schema: "notification_management",
		SecretName: "channel/1/secret", ChannelToken: "drdrill-channel", ChannelID: 1,
		Identity: "drill@example.test", Password: "pw", Secret: "expected",
	}
}

// TestRefusedAreaModeHoldsWhenTheAreasAreDown is the shape the negative control
// actually runs in: the relational store restored, and both areas that seal under the
// root key refused to start.
func TestRefusedAreaModeHoldsWhenTheAreasAreDown(t *testing.T) {
	srv := areaStub{}.start(t)
	if err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
		membersSay(true, true, nil)); err != nil {
		t.Fatalf("the premise must hold when both areas are down and the relational store restored: %v", err)
	}
}

// 🔴 TestRefusedAreaModeRefusesAHealthyInstance IS THE ANTI-SKIP PROPERTY.
//
// This is the test that stops the flag from being what --skip-api was. Point the mode
// at an instance where the areas are serving — i.e. the root key DOES open the stored
// ciphertext — and it must refuse to produce a verdict at all. Without this, a caller
// could pass the flag anywhere and read the decrypt result as a control.
func TestRefusedAreaModeRefusesAHealthyInstance(t *testing.T) {
	srv := areaStub{userManagementServing: true, notificationServing: true}.start(t)
	err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
		membersSay(true, true, nil))
	if err == nil {
		t.Fatal("serving areas must be refused: the instance did not refuse its root key, " +
			"so nothing this run produces is a negative control. Passing here would restore the " +
			"exact hole --skip-api was removed for")
	}
	if !strings.Contains(err.Error(), "IS serving") {
		t.Fatalf("the refusal must name the serving area as the cause, got %q", err)
	}
}

// Each area is asserted on its own. An instance where only ONE of them is serving did
// not refuse on that area's key check, and must not pass as a control.
func TestRefusedAreaModeRefusesEitherAreaServing(t *testing.T) {
	for _, tc := range []struct {
		name string
		stub areaStub
		want string
	}{
		{"user-management serving", areaStub{userManagementServing: true}, "user-management IS serving"},
		{"notification-management serving", areaStub{notificationServing: true}, "notification-management IS serving"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.stub.start(t)
			err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
				membersSay(true, true, nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

// TestRefusedAreaModeStillRequiresTheRelationalRestore is the other half of the pair. An
// instance where nothing came back cannot satisfy the mode either, so "the areas are
// down" alone is never enough.
func TestRefusedAreaModeStillRequiresTheRelationalRestore(t *testing.T) {
	srv := areaStub{}.start(t)

	err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
		membersSay(false, false, nil))
	if err == nil || !strings.Contains(err.Error(), "is not in the restored") {
		t.Fatalf("with no identity restored, nothing shows the restore landed; want a refusal, got %v", err)
	}

	err = checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
		membersSay(false, false, errors.New("relation does not exist")))
	if err == nil || !strings.Contains(err.Error(), "reading identity") {
		t.Fatalf("a failed read is not a restore; want a refusal naming the read, got %v", err)
	}
}

// TestRefusedAreaModeRejectsARestoreOfADifferentInstance pins the membership check. An
// identity found in SOME restored database that is not a member of this run's tenant —
// the wrong dump, a survivor — must not vouch for this receipt's restore.
func TestRefusedAreaModeRejectsARestoreOfADifferentInstance(t *testing.T) {
	srv := areaStub{}.start(t)
	err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), drillReceipt(),
		membersSay(true, false, nil))
	if err == nil {
		t.Fatal("an identity with no membership of this run's tenant does not show THIS restore landed")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("the refusal must name the missing membership, got %q", err)
	}
}

// TestRefusedAreaModeRefusesAnIncompleteReceipt keeps the mode from being reachable with
// no identity at all, which would make the positive half unrunnable and silently absent.
func TestRefusedAreaModeRefusesAnIncompleteReceipt(t *testing.T) {
	srv := areaStub{}.start(t)
	r := drillReceipt()
	r.Identity = ""
	if err := checkRestoredWithoutSecretAreaUsing(t.Context(), refusedOpts(t, srv), r,
		membersSay(true, true, nil)); err == nil {
		t.Fatal("a receipt with no identity leaves the positive half unrunnable; the mode must refuse")
	}
}

// membershipDB builds user-management's two tables, the columns readMembership reads,
// in an attached database named for the schema — so the schema-qualified names the
// real read uses resolve exactly as they do against Postgres.
func membershipDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// One connection: an attachment belongs to the connection that made it.
	sqlDB.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`ATTACH DATABASE ':memory:' AS "user-management"`,
		`CREATE TABLE "user-management".iam_identities (id INTEGER PRIMARY KEY, email TEXT, deleted_at DATETIME)`,
		`CREATE TABLE "user-management".iam_memberships (id INTEGER PRIMARY KEY, identity_id INTEGER, tenant_id TEXT, deleted_at DATETIME)`,
		`INSERT INTO "user-management".iam_identities (id, email) VALUES (1, 'drill@example.test')`,
		`INSERT INTO "user-management".iam_identities (id, email, deleted_at) VALUES (2, 'gone@example.test', CURRENT_TIMESTAMP)`,
		`INSERT INTO "user-management".iam_memberships (identity_id, tenant_id) VALUES (1, 'drdrill')`,
		`INSERT INTO "user-management".iam_memberships (identity_id, tenant_id, deleted_at) VALUES (1, 'left', CURRENT_TIMESTAMP)`,
		`INSERT INTO "user-management".iam_memberships (identity_id, tenant_id) VALUES (2, 'drdrill')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return db
}

// readMembership against real tables: the column names it reads are the thing only a
// live drill would otherwise check, an hour into a rebuild.
func TestReadMembershipReadsTheRestoredTables(t *testing.T) {
	db := membershipDB(t)
	for _, tc := range []struct {
		identity, tenant string
		found, member    bool
	}{
		{"drill@example.test", "drdrill", true, true},
		{"drill@example.test", "someone-else", true, false},
		{"drill@example.test", "left", true, false},      // a soft-deleted membership does not count
		{"gone@example.test", "drdrill", false, false},   // nor does a soft-deleted identity
		{"nobody@example.test", "drdrill", false, false}, // absent
	} {
		found, member, err := readMembership(t.Context(), db, areaUserManagement, tc.identity, tc.tenant)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.identity, tc.tenant, err)
		}
		if found != tc.found || member != tc.member {
			t.Errorf("%s in %s: got found=%v member=%v, want found=%v member=%v",
				tc.identity, tc.tenant, found, member, tc.found, tc.member)
		}
	}
}
