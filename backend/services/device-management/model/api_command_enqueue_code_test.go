// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// TestEnqueueVerdictCarriesAStableCode pins the machine-readable half of the verdict.
//
// The Reason is prose: it names the device token and the offending parameter so a
// person can fix the command, which is exactly what makes it useless as a branch
// condition. Downstream, command-delivery relays this code and REACT's dispatcher
// decides from it whether a retry could ever succeed — so a rejection that lost its
// code, or gained the wrong one, changes whether a real actuation is retried or
// dropped, with nothing at the call site to show for it.
//
// The expected values are written out as LITERALS rather than referenced through the
// constants they pin. A test comparing a constant to itself asserts nothing about the
// value, and this value travels across a service boundary in JSON: renaming the
// constant must be visible here as a failing test, not absorbed silently on both
// sides at once.
func TestEnqueueVerdictCarriesAStableCode(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	t.Run("a nonexistent device", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		v, err := api.ValidateCommandEnqueue(ctx, "ghost", "reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed || string(v.Code) != "DEVICE_NOT_FOUND" {
			t.Fatalf("allowed=%v code=%q, want a rejection coded DEVICE_NOT_FOUND", v.Allowed, v.Code)
		}
	})

	t.Run("a command outside the published vocabulary", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-strict", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-strict", "self-destruct", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed || string(v.Code) != "COMMAND_NOT_IN_VOCABULARY" {
			t.Fatalf("allowed=%v code=%q, want a rejection coded COMMAND_NOT_IN_VOCABULARY", v.Allowed, v.Code)
		}
	})

	t.Run("a payload that violates the parameter schema", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-typed", []*CommandDefinition{
			defWithSchema(t, "drive", []CommandParameter{
				{Name: "speed", DataType: MetricInt, Required: true, MinValue: f64(0), MaxValue: f64(100)},
			}),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-typed", "drive", []byte(`{"speed":999}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Allowed || string(v.Code) != "PAYLOAD_SCHEMA_VIOLATION" {
			t.Fatalf("allowed=%v code=%q, want a rejection coded PAYLOAD_SCHEMA_VIOLATION", v.Allowed, v.Code)
		}
	})

	// The counterweight: an ACCEPT carries no code at all. A verdict that always
	// carried one would let a caller branch on a code while allowed=true — reading an
	// accepted command as a refusal, which is the reverse of the bug this fixes and
	// just as invisible.
	t.Run("an allowed command carries no code", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-ok", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})
		v, err := api.ValidateCommandEnqueue(ctx, "dev-ok", "reboot", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.Allowed {
			t.Fatalf("expected the command to be allowed, got %q", v.Reason)
		}
		if v.Code != "" {
			t.Fatalf("an allowed verdict must carry no code, got %q", v.Code)
		}
	})
}
