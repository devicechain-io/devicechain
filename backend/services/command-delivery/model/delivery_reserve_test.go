// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// machineryCtx marks ctx as a platform service-token caller — the class the delivery
// machinery reserve does not apply to, so it is bounded by the tenant's ceiling itself.
//
// The ceiling and headroom tests in this package use it so they keep measuring the
// CEILING. Without it the reserve silently moves every one of their numbers, and a test
// written to pin one bound would be quietly pinning two.
func machineryCtx(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeService})
}

// accessCtx marks ctx as an ordinary access-token caller: the console, an SDK, dcctl, or
// a tenant's own integration. This is the class the reserve bounds.
func accessCtx(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess})
}

// The reserve is the difference between the two classes at the same ceiling, on the
// SINGLE-command path.
//
// 🔴 This path is not an afterthought — it is the one that makes the reserve hold at all.
// A reserve enforced only where batches are created is walked around by a client looping
// createCommand, which is exactly the fleet write it exists to bound.
func TestTheReserveBoundsAnAccessTokenButNotAServiceToken(t *testing.T) {
	api := newTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2 // ⇒ 2 reserved, restricted limit 8

	base := core.WithTenant(context.Background(), "A")
	for i := 0; i < 8; i++ {
		seedWithStatus(t, api, machineryCtx(base), "held-"+string(rune('a'+i)), CommandHeld)
	}

	// The restricted caller is already at its limit of 8, though the ceiling is 10.
	_, err := api.CreateCommand(accessCtx(base), &CommandCreateRequest{
		Token: "from-a-human", DeviceToken: "d", Name: "reboot",
	})
	rejection := asRejection(t, err)
	if rejection.Code != RejectHeldCeilingExceeded {
		t.Fatalf("expected %s, got %s", RejectHeldCeilingExceeded, rejection.Code)
	}

	// The same tenant, the same instant, the same backlog: delivery machinery still gets
	// through. This is the whole point — a fleet write must not silently disable the
	// tenant's alarm-driven automation.
	if _, err := api.CreateCommand(machineryCtx(base), &CommandCreateRequest{
		Token: "from-react", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("a service token must draw on the reserve, got: %v", err)
	}

	// And the reserve is a reserve, not a second ceiling: machinery is still bounded.
	// One more takes the tenant to the ceiling of 10 (8 seeded + the command above),
	// after which even a service token is refused.
	seedWithStatus(t, api, machineryCtx(base), "held-extra", CommandHeld)
	_, err = api.CreateCommand(machineryCtx(base), &CommandCreateRequest{
		Token: "over-the-ceiling", DeviceToken: "d", Name: "reboot",
	})
	if rejection := asRejection(t, err); rejection.Code != RejectHeldCeilingExceeded {
		t.Fatalf("a service token past the CEILING must still be refused, got %s", rejection.Code)
	}
}

// The batch path is bounded by the same effective limit — the scenario the reserve was
// designed for: one fleet write that would otherwise take the whole ceiling.
func TestAFleetWriteCannotConsumeTheReservedShare(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2 // ⇒ 2 reserved, restricted limit 8
	api.BatchValidator = &countingValidator{}

	base := core.WithTenant(context.Background(), "acme")
	request := batchRequest("fleet-wide", deviceTokens(10))
	request.AllowPartial = true

	batch, err := api.CreateCommandBatch(accessCtx(base), request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if batch.Accepted != 8 {
		t.Fatalf("accepted = %d, want 8 — a fleet write must stop at the reserved share, not the ceiling",
			batch.Accepted)
	}
	if batch.Resolved != 10 {
		t.Fatalf("resolved = %d, want 10", batch.Resolved)
	}

	// What is left is exactly the reserve, and it is still usable by delivery machinery —
	// which is the entire purpose. A tenant's alarm rules keep firing while its fleet
	// write is in flight.
	if _, err := api.CreateCommand(machineryCtx(base), &CommandCreateRequest{
		Token: "from-react", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("the reserved share must remain available to a service token, got: %v", err)
	}
}

// The counterweight: the reserve must not cap a service-token batch, or automation would
// be bounded by the very share set aside for it.
func TestAServiceTokenBatchFillsTheWholeCeiling(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2
	api.BatchValidator = &countingValidator{}

	base := core.WithTenant(context.Background(), "acme")
	request := batchRequest("machinery-wide", deviceTokens(10))
	request.AllowPartial = true

	batch, err := api.CreateCommandBatch(machineryCtx(base), request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if batch.Accepted != 10 {
		t.Fatalf("accepted = %d, want all 10 — the reserve must not bound its own beneficiary",
			batch.Accepted)
	}
}

// A whole-batch refusal has to explain the reserve too, for the same reason the single
// enqueue does: "room for 0" against a visible ceiling of 10 is unreconcilable.
func TestAWholeBatchRefusalExplainsTheReserve(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2
	api.BatchValidator = &countingValidator{}

	base := core.WithTenant(context.Background(), "acme")
	request := batchRequest("too-big", deviceTokens(9)) // 9 > the restricted limit of 8
	request.AllowPartial = false

	_, err := api.CreateCommandBatch(accessCtx(base), request)
	var rejection *BatchRejected
	if !errors.As(err, &rejection) {
		t.Fatalf("expected a batch rejection, got %v", err)
	}
	if !strings.Contains(rejection.Reason, "reserved for command delivery") {
		t.Errorf("a batch refused by the reserve must say so:\n%s", rejection.Reason)
	}
	if !strings.Contains(rejection.Reason, "this caller may hold 8 of the tenant's ceiling of 10") {
		t.Errorf("a batch refusal must name the effective limit and the ceiling:\n%s", rejection.Reason)
	}
}

// 🔴 NO CLAIMS MEANS RESTRICTED. "I could not tell who this is" must not be the way to
// reach the reserved share — that would make the reserve vanish exactly where identity is
// least established.
func TestAClaimlessCallerIsBoundedLikeAnAccessToken(t *testing.T) {
	api := newTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2

	base := core.WithTenant(context.Background(), "A")
	for i := 0; i < 8; i++ {
		seedWithStatus(t, api, machineryCtx(base), "held-"+string(rune('a'+i)), CommandHeld)
	}

	// No claims layered on at all.
	_, err := api.CreateCommand(base, &CommandCreateRequest{
		Token: "anonymous", DeviceToken: "d", Name: "reboot",
	})
	if rejection := asRejection(t, err); rejection.Code != RejectHeldCeilingExceeded {
		t.Fatalf("a claims-less caller must be bounded by the restricted limit, got %s", rejection.Code)
	}
}

// The refusal must name the limit that ACTUALLY applied, and say why it is not the
// ceiling. Quoting the ceiling while refusing below it is a message its reader cannot
// reconcile against the tenant's own settings.
func TestTheRefusalNamesTheEffectiveLimitAndTheReserve(t *testing.T) {
	api := newTestApi(t)
	api.DefaultHeldCommandCeiling = 10
	api.DeliveryMachineryReserve = 0.2

	base := core.WithTenant(context.Background(), "A")
	for i := 0; i < 8; i++ {
		seedWithStatus(t, api, machineryCtx(base), "held-"+string(rune('a'+i)), CommandHeld)
	}

	_, err := api.CreateCommand(accessCtx(base), &CommandCreateRequest{
		Token: "from-a-human", DeviceToken: "d", Name: "reboot",
	})
	reason := asRejection(t, err).Reason
	for _, want := range []string{
		"the limit for this caller is 8",
		"2 of the tenant's ceiling of 10 is reserved",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("refusal reason does not mention %q:\n%s", want, reason)
		}
	}

	// The counterweight: a machinery caller refused at the ceiling must NOT be told about
	// a reserve, because none applied to it.
	for i := 0; i < 2; i++ {
		seedWithStatus(t, api, machineryCtx(base), "held-extra-"+string(rune('a'+i)), CommandHeld)
	}
	_, err = api.CreateCommand(machineryCtx(base), &CommandCreateRequest{
		Token: "over-the-ceiling", DeviceToken: "d", Name: "reboot",
	})
	machineryReason := asRejection(t, err).Reason
	if strings.Contains(machineryReason, "reserved") {
		t.Errorf("a service-token refusal must not blame a reserve that did not apply:\n%s", machineryReason)
	}
	if !strings.Contains(machineryReason, "the limit is 10") {
		t.Errorf("a service-token refusal must quote the ceiling:\n%s", machineryReason)
	}
}
