// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// stubValidator is a canned CommandEnqueueValidator that records what it was asked.
type stubValidator struct {
	err error

	calledDevice  string
	calledCommand string
	calledPayload string
	callCount     int
}

func (s *stubValidator) ValidateEnqueue(_ context.Context, deviceToken string, commandKey string, payload []byte) error {
	s.callCount++
	s.calledDevice = deviceToken
	s.calledCommand = commandKey
	s.calledPayload = string(payload)
	return s.err
}

// CreateCommand gates on the enqueue validator (ADR-043 decision 3): a rejection
// blocks the enqueue and persists nothing, an availability failure blocks it too but
// is reported differently, an allowed command is enqueued, and a nil validator skips
// the gate entirely (prior behavior).
func TestCreateCommand_EnqueueGating(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	payload := `{"mode":"eco"}`
	req := func(tok string) *CommandCreateRequest {
		return &CommandCreateRequest{Token: tok, DeviceToken: "dev-x", Name: "reboot", Payload: &payload}
	}

	t.Run("rejection blocks enqueue and persists nothing", func(t *testing.T) {
		api := newTestApi(t)
		sv := &stubValidator{err: &EnqueueRejected{Reason: `device "dev-x" accepts no command "reboot"`}}
		api.EnqueueValidator = sv
		_, err := api.CreateCommand(ctx, req("cmd-a"))
		if err == nil {
			t.Fatal("expected an error enqueuing a rejected command")
		}
		// The rejection reason must reach the client verbatim — it is the client's
		// fault and the only way they can fix the command.
		if !strings.Contains(err.Error(), `accepts no command "reboot"`) {
			t.Fatalf("rejection reason was not relayed to the caller: %v", err)
		}
		if got, _ := api.CommandsByToken(ctx, []string{"cmd-a"}); len(got) != 0 {
			t.Fatalf("a command was persisted despite rejection: %+v", got)
		}
	})

	t.Run("the validator is consulted with device, command key and payload", func(t *testing.T) {
		api := newTestApi(t)
		sv := &stubValidator{}
		api.EnqueueValidator = sv
		if _, err := api.CreateCommand(ctx, req("cmd-b")); err != nil {
			t.Fatalf("CreateCommand failed for an allowed command: %v", err)
		}
		if sv.calledDevice != "dev-x" {
			t.Fatalf("validator got the wrong device token: %q", sv.calledDevice)
		}
		// The command NAME is what must be matched against the profile's CommandKey.
		if sv.calledCommand != "reboot" {
			t.Fatalf("validator got the wrong command key: %q", sv.calledCommand)
		}
		if sv.calledPayload != payload {
			t.Fatalf("validator got the wrong payload: %q", sv.calledPayload)
		}
		if sv.callCount != 1 {
			t.Fatalf("expected exactly one validation call, got %d", sv.callCount)
		}
	})

	t.Run("allowed command is enqueued QUEUED", func(t *testing.T) {
		api := newTestApi(t)
		api.EnqueueValidator = &stubValidator{}
		created, err := api.CreateCommand(ctx, req("cmd-c"))
		if err != nil {
			t.Fatalf("CreateCommand failed for an allowed command: %v", err)
		}
		if created.Status != CommandQueued.String() {
			t.Fatalf("expected QUEUED, got %s", created.Status)
		}
	})

	t.Run("availability failure blocks and stays sanitized", func(t *testing.T) {
		api := newTestApi(t)
		api.EnqueueValidator = &stubValidator{err: errors.New("dial tcp 10.4.2.9:8080: connection refused")}
		_, err := api.CreateCommand(ctx, req("cmd-d"))
		if err == nil {
			t.Fatal("expected the availability failure to block the enqueue")
		}
		// An outage must NOT read as "your command is invalid", and must not leak the
		// in-cluster topology to the tenant API client.
		if strings.Contains(err.Error(), "10.4.2.9") || strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("transport detail leaked to the caller: %v", err)
		}
		if !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("an outage should be reported as unavailable, got: %v", err)
		}
		if got, _ := api.CommandsByToken(ctx, []string{"cmd-d"}); len(got) != 0 {
			t.Fatal("a command was persisted despite a validation failure")
		}
	})

	t.Run("nil validator skips the gate", func(t *testing.T) {
		api := newTestApi(t) // EnqueueValidator left nil
		if _, err := api.CreateCommand(ctx, req("cmd-e")); err != nil {
			t.Fatalf("CreateCommand failed with no validator wired: %v", err)
		}
	})

	t.Run("absent payload reaches the validator as nil, not empty", func(t *testing.T) {
		api := newTestApi(t)
		sv := &stubValidator{}
		api.EnqueueValidator = sv
		r := &CommandCreateRequest{Token: "cmd-f", DeviceToken: "dev-x", Name: "ping"} // no Payload
		if _, err := api.CreateCommand(ctx, r); err != nil {
			t.Fatalf("CreateCommand failed: %v", err)
		}
		if sv.calledPayload != "" {
			t.Fatalf("expected an absent payload, got %q", sv.calledPayload)
		}
	})
}
