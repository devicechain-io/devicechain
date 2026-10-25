// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graph-gophers/graphql-go/ast"
)

// The rows that cover the tenant's own administration: the role it defines, the member it
// invites and the service account it creates. They are what puts rows in iam_roles
// (tenant-owned), iam_invitations and iam_service_accounts, which the table sweep otherwise
// finds empty for every seeded tenant. The invitation and the account both hand out the
// tenant's role, so both are skipped with it.

// tenantAdminRows are the rows these tests are about, by name.
var tenantAdminRows = map[string]bool{"tenant-role": true, "member-invitation": true, "service-account": true}

// A release that predates tenant-defined roles and invitations cannot express either row,
// and must skip both — and only them — rather than send createTenantRole or inviteMember
// to a server that refuses them. Neither feeds the shared state, so the skip strands
// nothing; the customer the invitation is scoped to is still seeded.
func TestAReleaseWithoutTenantAdministrationSkipsExactlyItsTwoRows(t *testing.T) {
	b := currentTreeWithout(t, "user-management", "createTenantRole", "inviteMember")
	assertSkipsExactlyTheTenantAdminRows(t, b)
}

// 🔴 THE DEPENDENCY, NOT JUST THE NAMES. A release that could invite but not define a role
// would send the invitation a role token that does not exist, which the server refuses as
// NOT_FOUND and the seed reports as the platform declining. The shared Requires is what
// skips the invitation with the role; drop it and this test fails on member-invitation.
func TestAReleaseThatCannotDefineARoleSkipsTheInvitationToo(t *testing.T) {
	b := currentTreeWithout(t, "user-management", "createTenantRole")
	assertSkipsExactlyTheTenantAdminRows(t, b)
}

// A release that has tenant administration but predates service accounts skips the
// account row and nothing else.
func TestAReleaseWithoutServiceAccountsSkipsOnlyThatRow(t *testing.T) {
	b := currentTreeWithout(t, "user-management", "createServiceAccount")
	for _, e := range allEntities() {
		write, reason, err := plan(e, b)
		if err != nil {
			t.Errorf("%q: %v", e.Name, err)
			continue
		}
		if e.Name == "service-account" {
			if write || reason == "" {
				t.Errorf("service-account: write=%v reason=%q; want skipped with a reason", write, reason)
			}
			continue
		}
		if !write {
			t.Errorf("%q is not written by a release that only lacks service accounts", e.Name)
		}
	}
}

func assertSkipsExactlyTheTenantAdminRows(t *testing.T, b *baseline) {
	t.Helper()
	seen := 0
	for _, e := range allEntities() {
		write, reason, err := plan(e, b)
		if tenantAdminRows[e.Name] {
			seen++
			if err != nil {
				t.Errorf("%q could not be skipped: %v", e.Name, err)
			}
			if write || reason == "" {
				t.Errorf("%q: write=%v reason=%q; want skipped with a reason", e.Name, write, reason)
			}
			if e.Record != nil {
				t.Errorf("%q feeds the shared state, so a baseline cannot skip it", e.Name)
			}
			continue
		}
		if err != nil || !write {
			t.Errorf("%q is not written by a release that only lacks tenant administration: write=%v err=%v",
				e.Name, write, err)
		}
	}
	if seen != len(tenantAdminRows) {
		t.Fatalf("saw %d of the %d tenant administration rows in the table", seen, len(tenantAdminRows))
	}
}

// The current tree writes both: the counterweight, without which "skipped" is all the two
// tests above could ever observe.
func TestTheCurrentTreeWritesTheTenantAdminRows(t *testing.T) {
	b, err := loadBaseline(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatalf("load the current tree: %v", err)
	}
	for _, e := range allEntities() {
		if !tenantAdminRows[e.Name] {
			continue
		}
		if write, reason, err := plan(e, b); err != nil || !write {
			t.Errorf("%q is not written by the current tree: write=%v reason=%q err=%v", e.Name, write, reason, err)
		}
	}
}

// currentTreeWithout loads the current services tree and deletes the named Mutation fields
// from one area, standing in for a release that predates them.
func currentTreeWithout(t *testing.T, area string, mutations ...string) *baseline {
	t.Helper()
	b, err := loadBaseline(filepath.Join("..", "..", "services"))
	if err != nil {
		t.Fatalf("load the current tree: %v", err)
	}
	s := b.schemas[area]
	if s == nil {
		t.Fatalf("the current tree serves no %s schema", area)
	}
	drop := map[string]bool{}
	for _, m := range mutations {
		drop[m] = true
	}
	root, ok := s.RootOperationTypes["mutation"].(*ast.ObjectTypeDefinition)
	if !ok {
		t.Fatalf("%s declares no mutation root", area)
	}
	kept := root.Fields[:0]
	dropped := 0
	for _, f := range root.Fields {
		if drop[f.Name] {
			dropped++
			continue
		}
		kept = append(kept, f)
	}
	root.Fields = kept
	if dropped != len(mutations) {
		t.Fatalf("the stand-in removed %d of %v; it no longer stands in for that release", dropped, mutations)
	}
	return b
}

// 🔴 THE INVITATION CODE IS A BEARER SECRET, AND NEITHER DOCUMENT MAY ASK FOR IT. Whatever a
// document selects is recorded on the receipt and compared in the open; a code there would be
// a usable credential written to disk by a drill.
func TestTheInvitationCodeIsNeverSelected(t *testing.T) {
	for _, e := range allEntities() {
		if e.Name != "member-invitation" {
			continue
		}
		for _, doc := range []string{e.createDoc(), e.readDoc()} {
			if strings.Contains(doc, "invitationCode") {
				t.Errorf("member-invitation selects the invitation code: %s", doc)
			}
		}
		return
	}
	t.Fatal("no member-invitation row in the table")
}

// ---- the argument-less read ----------------------------------------------

// tenantRoles lists the instance's roles and the tenant's own together. The row is the one
// whose token was written, never results[0] — on a healthy instance that is tenant-admin.
func TestAnArgumentlessReadPicksItsOwnRowNotTheFirst(t *testing.T) {
	e := entity{Name: "tenant-role", Read: "tenantRoles", ReadAll: true, MatchBy: "token"}
	written := json.RawMessage(`{"token":"apiprobe-tenant-role","owner":"TENANT"}`)
	got, err := readObject(e, map[string]json.RawMessage{"tenantRoles": raw(
		`[{"token":"tenant-admin","owner":"INSTANCE"},{"token":"apiprobe-tenant-role","owner":"TENANT"}]`)},
		"apiprobe-tenant-role", written)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), `"apiprobe-tenant-role"`) {
		t.Fatalf("got %s, want the row whose token was written", got)
	}
}

func TestAnArgumentlessReadWithoutItsRowReadsAsMissing(t *testing.T) {
	e := entity{Name: "tenant-role", Read: "tenantRoles", ReadAll: true, MatchBy: "token"}
	written := json.RawMessage(`{"token":"apiprobe-tenant-role"}`)
	for _, list := range []string{`[{"token":"tenant-admin"}]`, `[]`} {
		_, err := readObject(e, map[string]json.RawMessage{"tenantRoles": raw(list)}, "apiprobe-tenant-role", written)
		assertCode(t, err, exitMissing)
	}
}

// A ReadAll row with no MatchBy could only take some row out of the list; that is the tool
// unable to measure, not a finding about the platform.
func TestAnArgumentlessReadWithoutMatchByIsASetupError(t *testing.T) {
	e := entity{Name: "tenant-role", Read: "tenantRoles", ReadAll: true}
	_, err := readObject(e, map[string]json.RawMessage{"tenantRoles": raw(`[{"token":"x"}]`)}, "x",
		json.RawMessage(`{"token":"x"}`))
	assertCode(t, err, exitSetup)
}

// Every ReadAll row in the table names the field that picks it out.
func TestEveryArgumentlessReadNamesItsMatch(t *testing.T) {
	for _, e := range allEntities() {
		if e.ReadAll && e.MatchBy == "" {
			t.Errorf("%q reads every row back and names no MatchBy", e.Name)
		}
		if e.ReadAll && (e.Single || e.ReadInput != "" || e.Publish) {
			t.Errorf("%q combines ReadAll with another read shape", e.Name)
		}
	}
}

// readAllStub answers one argument-less read and keeps the variables it was sent.
type readAllStub struct{ vars map[string]any }

func (s *readAllStub) Query(_ context.Context, _, _ string, vars map[string]any, out any) error {
	s.vars = vars
	return json.Unmarshal([]byte(`{"tenantRoles":[{"token":"apiprobe-tenant-role"}]}`), out)
}

// The document declares no variable, so none is sent: a $token the server was handed and
// the document never declared is a request the next stricter validator refuses.
func TestAnArgumentlessReadSendsNoVariables(t *testing.T) {
	e := entity{Name: "tenant-role", Read: "tenantRoles", ReadAll: true, MatchBy: "token", Fields: "token"}
	stub := &readAllStub{}
	if _, err := readRow(context.Background(), stub, "u", e, "apiprobe-tenant-role",
		json.RawMessage(`{"token":"apiprobe-tenant-role"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stub.vars) != 0 {
		t.Errorf("sent variables %v to a document that declares none", stub.vars)
	}
}
