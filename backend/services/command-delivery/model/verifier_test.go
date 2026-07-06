// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// stubVerifier is a canned DeviceVerifier that records the token it was asked about.
type stubVerifier struct {
	exists bool
	err    error
	called string
}

func (s *stubVerifier) DeviceExists(_ context.Context, deviceToken string) (bool, error) {
	s.called = deviceToken
	return s.exists, s.err
}

// CreateCommand gates on the verifier (W1.1b): a missing device or a verifier error
// blocks the enqueue and persists nothing; an existing device is enqueued; and a nil
// verifier skips the check entirely (prior behavior).
func TestCreateCommand_VerifierGating(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	req := func(tok string) *CommandCreateRequest {
		return &CommandCreateRequest{Token: tok, DeviceToken: "dev-x", Name: "reboot"}
	}

	t.Run("missing device blocks enqueue and persists nothing", func(t *testing.T) {
		api := newTestApi(t)
		sv := &stubVerifier{exists: false}
		api.DeviceVerifier = sv
		if _, err := api.CreateCommand(ctx, req("cmd-a")); err == nil {
			t.Fatal("expected an error enqueuing for a nonexistent device")
		}
		if sv.called != "dev-x" {
			t.Fatalf("verifier not consulted with the target token: %q", sv.called)
		}
		if got, _ := api.CommandsByToken(ctx, []string{"cmd-a"}); len(got) != 0 {
			t.Fatalf("a command was persisted despite a missing device: %+v", got)
		}
	})

	t.Run("existing device allows enqueue", func(t *testing.T) {
		api := newTestApi(t)
		api.DeviceVerifier = &stubVerifier{exists: true}
		created, err := api.CreateCommand(ctx, req("cmd-b"))
		if err != nil {
			t.Fatalf("CreateCommand failed for an existing device: %v", err)
		}
		if created.Status != CommandQueued.String() {
			t.Fatalf("expected QUEUED, got %s", created.Status)
		}
	})

	t.Run("verifier error propagates and blocks", func(t *testing.T) {
		api := newTestApi(t)
		api.DeviceVerifier = &stubVerifier{err: errors.New("device-management down")}
		if _, err := api.CreateCommand(ctx, req("cmd-c")); err == nil {
			t.Fatal("expected the verifier error to propagate")
		}
		if got, _ := api.CommandsByToken(ctx, []string{"cmd-c"}); len(got) != 0 {
			t.Fatal("a command was persisted despite a verifier error")
		}
	})

	t.Run("nil verifier skips the check", func(t *testing.T) {
		api := newTestApi(t) // DeviceVerifier left nil
		if _, err := api.CreateCommand(ctx, req("cmd-d")); err != nil {
			t.Fatalf("CreateCommand failed with no verifier wired: %v", err)
		}
	})
}
