// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/gorm"
)

// 🔴 ANOTHER TENANT'S RECORD IS ANSWERED EXACTLY AS A MISSING ONE. NOT_FOUND is a new,
// machine-readable signal, so it must carry no more than the prose did: a profile that exists
// in tenant acme, named from tenant globex, gets the same code and the same message, byte for
// byte, as a token that exists nowhere.
func TestAnotherTenantsRecordIsIndistinguishableFromAMissingOne(t *testing.T) {
	acme := conflictWireCtx(t)
	requireNoErrors(t, execServed(t, acme, wireCreateProfile,
		map[string]any{"request": map[string]any{"token": "rover"}}), "the profile create")
	globex := core.WithTenant(acme, "globex")

	answer := func(profile string) (any, string) {
		qe := onlyError(t, execServed(t, globex, wireCreateCommand, map[string]any{"request": map[string]any{
			"token": "cmd-1", "deviceProfileToken": profile, "commandKey": "drive"}}))
		return qe.Extensions["code"], qe.Message
	}
	otherCode, otherMsg := answer("rover")
	missingCode, missingMsg := answer("nope")
	if otherCode != "NOT_FOUND" || missingCode != "NOT_FOUND" {
		t.Fatalf("codes: another tenant's profile %v, a missing one %v; want NOT_FOUND for both", otherCode, missingCode)
	}
	if otherMsg != missingMsg {
		t.Fatalf("another tenant's profile is told apart from a missing one: %q vs %q", otherMsg, missingMsg)
	}
	// The premise: the profile is really there for its own tenant.
	requireNoErrors(t, execServed(t, acme, wireCreateCommand, map[string]any{"request": map[string]any{
		"token": "cmd-1", "deviceProfileToken": "rover", "commandKey": "drive"}}), "acme's own command")
}

// An alarm stream filtered by an originator that does not exist is refused at subscribe time,
// and the refusal keeps the store's miss in its chain: that sentinel is what the shared GraphQL
// boundary answers with NOT_FOUND, so a resolver that re-words the miss into a fresh error is
// the one place every other service's miss and this one would part ways on the wire.
func TestAnAlarmStreamForAnUnknownOriginatorKeepsTheMiss(t *testing.T) {
	ctx := conflictWireCtx(t)
	ctx = withAuthorities(ctx, auth.AlarmRead)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if err := api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		AutoMigrate(&model.Device{}); err != nil {
		t.Fatalf("migrate devices: %v", err)
	}
	kind, token := "device", "nope"

	_, err := (&SchemaResolver{}).AlarmStream(ctx, struct {
		OriginatorType *string
		Originator     *string
		State          *string
		Severity       *string
		AlarmKey       *string
		Anchor         *model.AlarmAnchor
	}{OriginatorType: &kind, OriginatorToken: &token})

	if err == nil {
		t.Fatal("an alarm stream for an originator that does not exist was accepted")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("the refusal %q lost the store's miss, so it is served with no NOT_FOUND code", err)
	}
}

// Redeeming a claim on a device that has no claim is a miss, and a miss carries NOT_FOUND:
// ErrClaimNotFound replaced the store's sentinel with a plain one, which dropped the code.
func TestRedeemingAClaimThatDoesNotExistCarriesNotFound(t *testing.T) {
	ctx := conflictWireCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if err := api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		AutoMigrate(&model.Device{}, &model.DeviceClaim{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	qe := onlyError(t, execServed(t, ctx,
		`mutation($request: DeviceClaimRequest!) { claimDevice(request: $request) { id } }`,
		map[string]any{"request": map[string]any{
			"deviceToken": "ghost", "claimSecret": "s", "customerToken": "c", "relationshipType": "owns"}}))
	if qe.Extensions["code"] != "NOT_FOUND" {
		t.Fatalf("code = %v (%q), want NOT_FOUND", qe.Extensions["code"], qe.Message)
	}
}
