// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
)

const (
	wireCreateCustomer = `mutation ($request: CustomerCreateRequest) {
  createCustomer(request: $request) { token } }`
	wireDeleteCustomer = `mutation ($token: String!) { deleteCustomer(token: $token) }`
)

// A deleted customer's token, offered to a new customer, ON THE WIRE. The refusal is
// INVALID_VALUE and keeps its own sentence; it must never be CONFLICT, which a tolerant
// client reads as "it already exists, carry on".
func TestRecreatingADeletedCustomerAnswersInvalidValue(t *testing.T) {
	f := newScopeFixture(t)
	// The fixture builds what its own tests read; a customer delete also cascades the
	// customer's group memberships.
	if err := f.db.AutoMigrate(&model.EntityGroupMembership{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := f.operator()
	requireNoErrors(t, execServed(t, ctx, wireDeleteCustomer, map[string]any{"token": "acme"}), "the delete")

	qe := onlyError(t, execServed(t, ctx, wireCreateCustomer, map[string]any{"request": map[string]any{
		"token": "acme", "customerTypeToken": "retail"}}))
	if got := qe.Extensions["code"]; got != "INVALID_VALUE" {
		t.Fatalf("extensions.code = %v, want INVALID_VALUE (message %q)", got, qe.Message)
	}
	if !strings.Contains(qe.Message, "that customer token is retired") {
		t.Fatalf("the refusal lost its own sentence: %s", qe.Message)
	}
	for _, word := range []string{"already exists", "duplicate", "unique"} {
		if strings.Contains(strings.ToLower(qe.Message), word) {
			t.Errorf("the message contains %q, which an older client reads as an already-exists: %s", word, qe.Message)
		}
	}

	// The live-duplicate control: a re-create of a customer that still exists stays CONFLICT.
	qe = onlyError(t, execServed(t, ctx, wireCreateCustomer, map[string]any{"request": map[string]any{
		"token": "globex", "customerTypeToken": "retail"}}))
	if got := qe.Extensions["code"]; got != "CONFLICT" {
		t.Fatalf("a live duplicate answered %v, want CONFLICT (message %q)", got, qe.Message)
	}
}
