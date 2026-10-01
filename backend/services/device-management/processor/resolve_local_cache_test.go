// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// These run the resolver over production's caches — the in-process tier ON — and count
// what a warm event still sends to the key-value store. They use only the rig and the
// resolver, so they compile against a tree without the tier and fail there by value.

// 🔑 A warm event within the in-process TTL asks the key-value store NOTHING. Every one of
// its lookups was answered from memory, and it still carries what the store would have
// given it: the unit and the published version.
func TestAWarmEventWithinTheLocalTtlAsksNoKeyValueStore(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	resolved := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, resolved); got != "Cel" || resolved.ProfileVersionToken != "p@1" {
		t.Fatalf("warm event resolved unit %q at %q, want Cel at p@1", got, resolved.ProfileVersionToken)
	}
	if got := rig.totalGets(); got != 0 {
		t.Errorf("key-value reads for a warm event within the in-process TTL = %d, want 0 (gets by cache: %v)",
			got, rig.gets())
	}
	if stmts := rig.statements(); len(stmts) != 0 {
		t.Errorf("a warm event reached the database %d times: %v", len(stmts), stmts)
	}
}

// The same in a rule-scoped tenant, where the store would otherwise be asked for the
// membership of the device and of each tracked anchor as well.
func TestAWarmEventWithinTheLocalTtlAsksNoKeyValueStoreInARuleScopedTenant(t *testing.T) {
	rig := newCountingResolveRig(t, true, nil)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	resolved := rig.resolve(tempEvent("21"))
	if got := stampedUnit(t, resolved); got != "Cel" {
		t.Fatalf("stamped unit = %q, want Cel", got)
	}
	if got := rig.totalGets(); got != 0 {
		t.Errorf("key-value reads for a warm scoped event within the in-process TTL = %d, want 0 (gets by cache: %v)",
			got, rig.gets())
	}
}

// 🔑 THE DEFAULT AUTH MODE, WHICH IS WHAT PRODUCTION RUNS. A credentialed event takes its
// device from the credential it presents, and so never asks the device-by-token cache. Its
// key-value reads are the other three (profile, tracked relationships, the scoped-groups
// gate), and within the in-process TTL those are answered from memory; and within the
// credential cache's 5 s so is the credential, so a warm credentialed event asks nothing
// at all. A revocation is still seen on the next event: the last step checks it.
func TestAWarmCredentialedEventAsksNoKeyValueStoreAndNoDatabase(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	secret := "s3cret"
	if _, err := rig.api.CreateDeviceCredential(rig.ctx, &model.DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	event := func() *esmodel.UnresolvedEvent {
		e := tempEvent("21")
		ctype, cid := string(model.CredentialMqttBasic), "cred-1"
		e.CredentialType, e.CredentialId, e.CredentialSecret = &ctype, &cid, &secret
		return e
	}
	resolve := func() *model.ResolvedEvent {
		results, reason, err := rez.ResolveEvent(rig.ctx, event())
		if err != nil || reason != 0 || len(results) != 1 {
			t.Fatalf("resolve a credentialed event: %d results, reason %d, err %v", len(results), reason, err)
		}
		return results[0].Resolved
	}
	resolve()
	rig.resetCounters()

	resolved := resolve()
	if got := stampedUnit(t, resolved); got != "Cel" || resolved.SourceDeviceToken != "dev" {
		t.Fatalf("resolved unit %q for device %q, want Cel for dev", got, resolved.SourceDeviceToken)
	}
	if got := rig.totalGets(); got != 0 {
		t.Errorf("key-value reads for a warm credentialed event within the in-process TTL = %d, want 0 "+
			"(gets by cache: %v)", got, rig.gets())
	}
	var credentialReads int
	for _, s := range rig.statements() {
		if strings.Contains(s, "device_credentials") {
			credentialReads++
		}
	}
	if credentialReads != 0 {
		t.Errorf("the warm credentialed event read its credential from the database %d times, want 0 "+
			"(statements: %v)", credentialReads, rig.statements())
	}

	// Revoked through the plain Api, as the GraphQL mutation does: the next event is refused.
	if _, err := rig.api.UpdateDeviceCredential(rig.ctx, "c-1",
		&model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable the credential: %v", err)
	}
	if _, reason, err := rez.ResolveEvent(rig.ctx, event()); !errors.Is(err, model.ErrCredentialNotResolved) ||
		reason != uint(dmproto.FailureReason_Unauthenticated) {
		t.Fatalf("an event presenting the disabled credential: reason %d, err %v; want Unauthenticated, %v",
			reason, err, model.ErrCredentialNotResolved)
	}
}

// Memory is keyed on the tenant as the store is: a device held for acme is not served to
// a lookup of the same token under another tenant, which goes to the database and finds
// nothing.
func TestADeviceHeldForOneTenantIsNotServedToAnother(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	rig.resolve(tempEvent("21"))
	rig.resetCounters()

	other := core.WithTenant(context.Background(), "globex")
	devices, err := rig.capi.DevicesByToken(other, []string{"dev"})
	if err != nil {
		t.Fatalf("DevicesByToken under globex: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("globex's lookup of token dev returned %d devices; acme's device was served across tenants", len(devices))
	}
	if got := rig.stores["DeviceByToken"].Gets; got != 1 {
		t.Errorf("globex's lookup made %d key-value reads, want 1 (a memory miss asks the store)", got)
	}
	if len(rig.statements()) == 0 {
		t.Error("globex's lookup never reached the database")
	}
	if rig.stores["DeviceByToken"].Puts != 0 {
		t.Errorf("globex's lookup wrote %d entries; a miss must not be cached", rig.stores["DeviceByToken"].Puts)
	}
}
