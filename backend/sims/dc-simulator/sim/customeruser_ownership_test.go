// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeLedger is the Runtime's record of minted accounts. before runs at Record time, so a test can
// assert what had (not) happened to the platform by then.
type fakeLedger struct {
	have   []string
	before func()
	err    error
}

func (l *fakeLedger) Recorded(e string) bool { return slices.Contains(l.have, e) }
func (l *fakeLedger) Record(e string) error {
	if l.before != nil {
		l.before()
	}
	if l.err != nil {
		return l.err
	}
	l.have = append(l.have, e)
	return nil
}

// withRemoveMember adds removeMember to the members fake.
func withRemoveMember(inner func(string, map[string]any) map[string]any) func(string, map[string]any) map[string]any {
	return func(q string, v map[string]any) map[string]any {
		if strings.Contains(q, "removeMember(") {
			return map[string]any{"removeMember": true}
		}
		return inner(q, v)
	}
}

const taggedOwner = "owner+acme@halden-labs.example"

// A new account is filed in the ledger BEFORE it is created, so a crash between the two never
// leaves an account teardown cannot tell is the sim's.
func TestAMintedCustomerUserIsRecordedBeforeItIsInvited(t *testing.T) {
	g, rt := newRecordingGraphQL(t, membersFake(nil, "code-1"))
	led := &fakeLedger{}
	led.before = func() {
		if n := len(g.called("inviteMember(")); n != 0 {
			t.Errorf("the account was invited (%d) before it was recorded", n)
		}
	}
	rt.Minted = led
	if err := ensureCustomerUsers(context.Background(), rt, []CustomerUserSpec{annexOwner()}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(led.have, []string{taggedOwner}) {
		t.Errorf("ledger = %v, want exactly the tagged owner", led.have)
	}
}

// If the account cannot be recorded it is not created: an unrecorded account is one teardown could
// never remove, which is worse than a failed bootstrap.
func TestACustomerUserThatCannotBeRecordedIsNotCreated(t *testing.T) {
	g, rt := newRecordingGraphQL(t, membersFake(nil, "code-1"))
	rt.Minted = &fakeLedger{err: errors.New("disk full")}
	err := ensureCustomerUser(context.Background(), rt, CustomerUserSpec{Email: taggedOwner, CustomerToken: TowerpulseAnnexOwnerToken})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if n := len(g.called("inviteMember(")); n != 0 {
		t.Errorf("invited %d times despite the failed record", n)
	}
}

func existingTagged() []map[string]any {
	return []map[string]any{{
		"email": taggedOwner, "enabled": true, "roles": viewerRoles,
		"scope": []map[string]any{{"kind": "CUSTOMER", "token": TowerpulseAnnexOwnerToken}},
	}}
}

// An existing member that signs in with the sim's password is the sim's own (a sim converged before
// the ledger existed) and is filed; one that merely carries the tag and cannot sign in is a stranger
// and is not.
func TestAnExistingMemberIsRecordedOnlyIfItSignsInWithTheSimPassword(t *testing.T) {
	spec := CustomerUserSpec{Email: taggedOwner, CustomerToken: TowerpulseAnnexOwnerToken}

	_, rt := newRecordingGraphQL(t, membersFake(existingTagged(), ""))
	rt.Minted = &fakeLedger{}
	if err := ensureCustomerUser(context.Background(), rt, spec); err != nil {
		t.Fatal(err)
	}
	if got := rt.Minted.(*fakeLedger).have; !slices.Equal(got, []string{taggedOwner}) {
		t.Errorf("a member that signs in with the sim password was not adopted: %v", got)
	}

	g, rt2 := newRecordingGraphQL(t, membersFake(existingTagged(), ""))
	g.refuseLogin = taggedOwner
	rt2.Minted = &fakeLedger{}
	if err := ensureCustomerUser(context.Background(), rt2, spec); err != nil {
		t.Fatal(err)
	}
	if got := rt2.Minted.(*fakeLedger).have; len(got) != 0 {
		t.Errorf("a same-tag member that cannot sign in with the sim password was recorded as the sim's: %v", got)
	}
}

// Converging a legacy sim removes the shared untagged owner from THIS tenant (and only calls
// removeMember, which keeps the account), after the sim's own account is in place.
func TestConvergeRemovesTheLegacyOwnerMembershipFromThisTenant(t *testing.T) {
	existing := append(existingTagged(), map[string]any{
		"email": TowerpulseAnnexOwnerEmail, "enabled": true, "roles": viewerRoles,
		"scope": []map[string]any{{"kind": "CUSTOMER", "token": TowerpulseAnnexOwnerToken}},
	})
	g, rt := newRecordingGraphQL(t, withRemoveMember(membersFake(existing, "")))
	if err := ensureCustomerUsers(context.Background(), rt, []CustomerUserSpec{annexOwner()}); err != nil {
		t.Fatal(err)
	}
	rm := g.called("removeMember(")
	if len(rm) != 1 || rm[0].vars["email"] != TowerpulseAnnexOwnerEmail {
		t.Fatalf("removeMember calls = %v, want exactly one, for the legacy owner", rm)
	}
	for _, c := range g.calls {
		if strings.Contains(c.query, "elete") {
			t.Errorf("a delete was issued: %s", c.query)
		}
	}
	if len(g.called("inviteMember(")) != 0 {
		t.Error("the already-present tagged owner was invited again")
	}

	// A sim that never had the legacy owner is left alone: no removal, no failure.
	g2, rt2 := newRecordingGraphQL(t, withRemoveMember(membersFake(existingTagged(), "")))
	if err := ensureCustomerUsers(context.Background(), rt2, []CustomerUserSpec{annexOwner()}); err != nil {
		t.Fatal(err)
	}
	if n := len(g2.called("removeMember(")); n != 0 {
		t.Errorf("removeMember called %d times with no legacy member present", n)
	}
}

// The ledger is written back into the handshake file in place: dcctl's own fields survive, the file
// stays private, repeats are idempotent, and a handshake with no file records nothing.
func TestTheHandshakeLedgerIsWrittenBackWithoutDroppingDcctlsFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim.json")
	raw := `{"name":"x","controlAddr":"localhost:8090","adminURL":"http://a/admin","tenant":"acme","simEmail":"x@sim.local",` +
		`"simPassword":"pw","endpoints":{"userGraphQL":"u","deviceMgmtGraphQL":"d","ingress":"i","eventMgmtWS":"w"},"instanceId":"dc"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	hs, err := LoadHandshake(path)
	if err != nil {
		t.Fatal(err)
	}
	led := NewFileLedger(hs, path)
	if err := led.Record("Owner+Acme@Halden-Labs.example"); err != nil {
		t.Fatal(err)
	}
	if err := led.Record(taggedOwner); err != nil {
		t.Fatal(err)
	}
	back, err := LoadHandshake(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.ScenarioIdentities, []string{taggedOwner}) {
		t.Errorf("ledger on file = %v", back.ScenarioIdentities)
	}
	var doc map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["name"] != "x" || doc["controlAddr"] != "localhost:8090" || doc["adminURL"] != "http://a/admin" {
		t.Errorf("dcctl-only fields were dropped: %v", doc)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
	if !led.Recorded(taggedOwner) {
		t.Error("the ledger does not report what it recorded")
	}
	if err := NewFileLedger(&Handshake{}, "").Record(taggedOwner); err == nil {
		t.Error("a ledger with no file claimed to record")
	}
}

// dcctl never imports this module, so its copy of the legacy-owner list is held to this one by
// reading its source (through the parser, so prose cannot satisfy it).
func TestLegacyOwnersMatchDcctl(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../../../cli/sim/admin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var theirs []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "LegacyScenarioOwnerEmails" || len(vs.Values) != 1 {
			return true
		}
		if cl, ok := vs.Values[0].(*ast.CompositeLit); ok {
			for _, e := range cl.Elts {
				if bl, ok := e.(*ast.BasicLit); ok {
					u, _ := strconv.Unquote(bl.Value)
					theirs = append(theirs, u)
				}
			}
		}
		return false
	})
	if len(theirs) == 0 || !slices.Equal(theirs, LegacyScenarioOwnerEmails) {
		t.Errorf("dcctl's LegacyScenarioOwnerEmails = %v, want %v", theirs, LegacyScenarioOwnerEmails)
	}
}
