// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/sim"
)

// instanceTeardown is a small model of user-management's identity table: memberships per identity,
// and a tenant delete that is refused while any membership references the tenant (as the real
// DeleteTenant is). It replaces the network only; destroySim is the real code.
type instanceTeardown struct {
	memberships map[string]map[string]bool // email -> tenants
	failOnce    string                     // an email whose first DeleteIdentity fails
	tenantsGone map[string]bool
	calls       []string
}

func newInstance(m map[string][]string) *instanceTeardown {
	f := &instanceTeardown{memberships: map[string]map[string]bool{}, tenantsGone: map[string]bool{}}
	for e, ts := range m {
		f.memberships[e] = map[string]bool{}
		for _, t := range ts {
			f.memberships[e][t] = true
		}
	}
	return f
}

func (f *instanceTeardown) MembershipsOf(_ context.Context, emails []string) ([]sim.OwnedMember, error) {
	var out []sim.OwnedMember
	for _, e := range emails {
		ts, ok := f.memberships[e]
		if !ok {
			continue
		}
		m := sim.OwnedMember{Email: e}
		for t := range ts {
			m.Tenants = append(m.Tenants, t)
		}
		sort.Strings(m.Tenants)
		out = append(out, m)
	}
	return out, nil
}

func (f *instanceTeardown) RemoveMembership(_ context.Context, email, tenant string) error {
	f.calls = append(f.calls, "remove:"+email+"@"+tenant)
	delete(f.memberships[email], tenant)
	return nil
}

func (f *instanceTeardown) DeleteIdentity(_ context.Context, email string) (bool, error) {
	f.calls = append(f.calls, "delete:"+email)
	if f.failOnce == email {
		f.failOnce = ""
		return false, fmt.Errorf("delete identity: 503")
	}
	_, had := f.memberships[email]
	delete(f.memberships, email)
	return had, nil
}

func (f *instanceTeardown) DeleteTenant(_ context.Context, tenant string) (bool, error) {
	for e, ts := range f.memberships {
		if ts[tenant] {
			return false, fmt.Errorf("tenant %q still has a membership (%s)", tenant, e)
		}
	}
	f.tenantsGone[tenant] = true
	return true, nil
}

// towerRecord is a Towerpulse sim record. minted are the identities the simulator recorded.
func towerRecord(minted ...string) *sim.Record {
	return &sim.Record{Name: "tp-fu", Tenant: "sim-tp-fu", SimEmail: sim.DeriveEmail("tp-fu"), ScenarioIdentities: minted}
}

const legacyOwner = "owner@halden-labs.example"

const (
	ownerA = "owner+sim-tp-fu@halden-labs.example"
	ownerB = "owner+sim-tp-other@halden-labs.example"
)

// A Towerpulse destroy removes the annex owner's membership before the tenant, so the tenant
// delete the server refuses while any membership remains goes through.
func TestDestroySimRemovesTheScenarioOwnedMembersBeforeTheTenant(t *testing.T) {
	rec := towerRecord(ownerA)
	f := newInstance(map[string][]string{
		rec.SimEmail: {rec.Tenant},
		ownerA:       {rec.Tenant},
		ownerB:       {"sim-tp-other"},
	})
	recordDeleted := false
	if err := destroySim(context.Background(), f, rec, func() error { recordDeleted = true; return nil }, io.Discard); err != nil {
		t.Fatalf("destroySim: %v", err)
	}
	if !f.tenantsGone[rec.Tenant] || !recordDeleted {
		t.Fatalf("tenant gone=%t record deleted=%t", f.tenantsGone[rec.Tenant], recordDeleted)
	}
	if _, left := f.memberships[ownerA]; left {
		t.Error("the annex owner survived the teardown")
	}
	if _, kept := f.memberships[ownerB]; !kept {
		t.Error("ANOTHER sim's owner was deleted: ownership must be matched on the whole tenant tag")
	}
}

// An account the sim minted that ALSO belongs to another tenant loses only the membership here.
func TestDestroySimNeverDeletesAnIdentityWithMembershipsElsewhere(t *testing.T) {
	rec := towerRecord(ownerA)
	f := newInstance(map[string][]string{
		rec.SimEmail: {rec.Tenant},
		ownerA:       {rec.Tenant, "unrelated-tenant"},
	})
	if err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard); err != nil {
		t.Fatalf("destroySim: %v", err)
	}
	if !f.memberships[ownerA]["unrelated-tenant"] || f.memberships[ownerA][rec.Tenant] {
		t.Errorf("memberships of the shared account = %v, want only the unrelated tenant", f.memberships[ownerA])
	}
	if got := strings.Join(f.calls, ","); strings.Contains(got, "delete:"+ownerA) {
		t.Errorf("the shared identity was deleted: %s", got)
	}
}

// A teardown that fails partway is retryable: the second run finds whoever is left, and the local
// record is only removed once the tenant is.
func TestDestroySimRetriesAfterAPartialTeardown(t *testing.T) {
	rec := towerRecord(ownerA)
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, ownerA: {rec.Tenant}})
	f.failOnce = rec.SimEmail
	recordDeleted := false
	del := func() error { recordDeleted = true; return nil }

	if err := destroySim(context.Background(), f, rec, del, io.Discard); err == nil {
		t.Fatal("the first attempt was expected to fail")
	}
	if recordDeleted || f.tenantsGone[rec.Tenant] {
		t.Fatal("the record or tenant went despite the failure")
	}
	if err := destroySim(context.Background(), f, rec, del, io.Discard); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !recordDeleted || !f.tenantsGone[rec.Tenant] {
		t.Error("the retry did not finish the teardown")
	}
}

// A member added by hand is not the sim's: it is left, and the refusal says whose it is.
func TestDestroySimAdviceNamesMembersAddedOutsideTheSimFlow(t *testing.T) {
	rec := towerRecord()
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, "human@example.test": {rec.Tenant}})
	err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "added outside the sim flow") {
		t.Fatalf("err = %v", err)
	}
	if _, kept := f.memberships["human@example.test"]; !kept {
		t.Error("a hand-added member was deleted")
	}
}

// An identity that merely CARRIES the tag is not the sim's: with no membership anywhere it is an
// orphan someone else owns, and teardown leaves it.
func TestDestroySimLeavesAnUnrelatedOrphanWithTheSameTag(t *testing.T) {
	rec := towerRecord()
	const orphan = "finance+sim-tp-fu@company.example"
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, orphan: {}})
	if err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard); err != nil {
		t.Fatalf("destroySim: %v", err)
	}
	if _, kept := f.memberships[orphan]; !kept {
		t.Error("an orphan identity that only carries the tag was deleted globally")
	}
}

// A person added to the tenant by hand with the same tag is not the sim's either: it is not
// deleted, and the tenant delete says whose it is.
func TestDestroySimLeavesAHandAddedHumanWithTheSameTag(t *testing.T) {
	rec := towerRecord()
	const human = "ops+sim-tp-fu@company.example"
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, human: {rec.Tenant}})
	err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "added outside the sim flow") {
		t.Fatalf("err = %v, want the tenant delete refused over the hand-added member", err)
	}
	if !f.memberships[human][rec.Tenant] {
		t.Error("a hand-added human with the tag lost its identity or membership")
	}
}

// A sim converged by an earlier simulator is attached to the shared legacy owner. Destroy takes that
// membership out of THIS tenant -- the tenant delete is refused while it stands -- and never deletes
// the identity, which other sims and its other tenants still use. A retry after a partial teardown
// progresses.
func TestDestroySimMigratesTheLegacyOwnerAndNeverDeletesIt(t *testing.T) {
	rec := towerRecord(ownerA)
	f := newInstance(map[string][]string{
		rec.SimEmail: {rec.Tenant},
		ownerA:       {rec.Tenant},
		legacyOwner:  {rec.Tenant, "sim-tp-other"},
	})
	f.failOnce = rec.SimEmail
	recordDeleted := false
	del := func() error { recordDeleted = true; return nil }
	if err := destroySim(context.Background(), f, rec, del, io.Discard); err == nil {
		t.Fatal("the first attempt was expected to fail")
	}
	if err := destroySim(context.Background(), f, rec, del, io.Discard); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !recordDeleted || !f.tenantsGone[rec.Tenant] {
		t.Fatalf("teardown did not finish: record deleted=%t tenant gone=%t", recordDeleted, f.tenantsGone[rec.Tenant])
	}
	if !f.memberships[legacyOwner]["sim-tp-other"] || f.memberships[legacyOwner][rec.Tenant] {
		t.Errorf("legacy owner memberships = %v, want only the other tenant", f.memberships[legacyOwner])
	}
	if got := strings.Join(f.calls, ","); strings.Contains(got, "delete:"+legacyOwner) {
		t.Errorf("the legacy owner's global identity was deleted: %s", got)
	}
}

// Even when the legacy owner belongs to nothing else, it is not ours to delete globally.
func TestDestroySimNeverDeletesTheLegacyOwnerEvenWhenItsOnlyMembershipIsHere(t *testing.T) {
	rec := towerRecord()
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, legacyOwner: {rec.Tenant}})
	if err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard); err != nil {
		t.Fatalf("destroySim: %v", err)
	}
	if _, kept := f.memberships[legacyOwner]; !kept {
		t.Error("the shared legacy identity was deleted globally")
	}
}

// A recorded identity that also belongs to another tenant is exactly one that must not be deleted,
// and one recorded for ANOTHER sim is not this sim's to touch.
func TestDestroySimIgnoresIdentitiesNotInItsOwnRecord(t *testing.T) {
	rec := towerRecord(ownerA)
	f := newInstance(map[string][]string{rec.SimEmail: {rec.Tenant}, ownerA: {rec.Tenant}, ownerB: {"sim-tp-other"}})
	if err := destroySim(context.Background(), f, rec, func() error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, kept := f.memberships[ownerB]; !kept {
		t.Error("an identity outside the record was deleted")
	}
}
