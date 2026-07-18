// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// A profile may declare a given command key exactly once. Two definitions of one
// key make the vocabulary ambiguous: the ADR-043 enqueue gate resolves a command by
// key and can only honour one, so a payload's validity would depend on which row a
// SELECT happened to return first. The ambiguity is refused where it is introduced.
func TestCommandKeyUniquePerProfile(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	newProfile := func(t *testing.T, api *Api, token string) *DeviceProfile {
		t.Helper()
		p := &DeviceProfile{}
		p.Token = token
		if err := api.RDB.DB(ctx).Create(p).Error; err != nil {
			t.Fatalf("seed profile: %v", err)
		}
		return p
	}

	t.Run("a duplicate key on the same profile is rejected", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		p := newProfile(t, api, "prof-dup")

		first, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
			Token: "cd-1", DeviceProfileToken: p.Token, CommandKey: "drive",
		})
		if err != nil {
			t.Fatalf("first definition should succeed: %v", err)
		}
		if first == nil {
			t.Fatal("expected a created definition")
		}

		_, err = api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
			Token: "cd-2", DeviceProfileToken: p.Token, CommandKey: "drive",
		})
		if err == nil {
			t.Fatal("a second definition of the same command key must be rejected")
		}
		if !strings.Contains(err.Error(), "drive") {
			t.Fatalf("error should name the duplicated key: %v", err)
		}
	})

	// The constraint is per profile, not global: two profiles may each declare
	// "drive" with entirely different parameter schemas.
	t.Run("the same key on a different profile is allowed", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		a := newProfile(t, api, "prof-a")
		b := newProfile(t, api, "prof-b")

		if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
			Token: "cd-a", DeviceProfileToken: a.Token, CommandKey: "drive",
		}); err != nil {
			t.Fatalf("definition on profile A: %v", err)
		}
		if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
			Token: "cd-b", DeviceProfileToken: b.Token, CommandKey: "drive",
		}); err != nil {
			t.Fatalf("the same key on a different profile must be allowed: %v", err)
		}
	})

	// Re-saving a definition without changing its key must not collide with itself.
	t.Run("updating a definition in place is not a self-collision", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		p := newProfile(t, api, "prof-self")

		if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
			Token: "cd-self", DeviceProfileToken: p.Token, CommandKey: "drive",
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		desc := "now with a description"
		if _, err := api.UpdateCommandDefinition(ctx, "cd-self", &CommandDefinitionCreateRequest{
			Token: "cd-self", DeviceProfileToken: p.Token, CommandKey: "drive", Description: &desc,
		}); err != nil {
			t.Fatalf("updating a definition without changing its key must succeed: %v", err)
		}
	})
}
