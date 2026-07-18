// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// DeviceCommandVocabulary is the listing counterpart of the enqueue gate. Its
// contract is agreement: what it offers must be what the gate accepts. A listing
// that drifts from the gate is worse than no listing, because it moves the failure
// from "I guessed a command key wrong" to "the console offered me a command and the
// platform then refused it".
func TestDeviceCommandVocabulary(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")

	t.Run("nonexistent device reports no device", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		v, err := api.DeviceCommandVocabulary(ctx, "ghost")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.DeviceExists {
			t.Fatal("a token that resolves to nothing must not report a live device")
		}
	})

	// ADR-043 decision 4. The distinction this pins is the one a caller is most
	// likely to get wrong: an empty list means the vocabulary is OPEN, not that the
	// device accepts nothing. Constrained is what says which.
	t.Run("no published vocabulary is unconstrained, not empty-and-closed", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-freeform", nil)

		v, err := api.DeviceCommandVocabulary(ctx, "dev-freeform")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.DeviceExists {
			t.Fatal("seeded device should resolve")
		}
		if v.Constrained {
			t.Fatal("a profile declaring no commands must report an OPEN vocabulary")
		}
		if len(v.Commands) != 0 {
			t.Fatalf("an unconstrained vocabulary lists nothing, got %d", len(v.Commands))
		}

		// The bit that makes the distinction load-bearing: the gate accepts a key
		// that appears nowhere in the listing.
		verdict, err := api.ValidateCommandEnqueue(ctx, "dev-freeform", "anything", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !verdict.Allowed {
			t.Fatal("an unconstrained device must still accept an unlisted command")
		}
	})

	t.Run("published vocabulary is listed with its parameter schema", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-strict", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
			defWithSchema(t, "setpoint", []CommandParameter{
				{Name: "celsius", DataType: MetricDouble, Required: true},
			}),
		})

		v, err := api.DeviceCommandVocabulary(ctx, "dev-strict")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !v.Constrained {
			t.Fatal("a published vocabulary must report as constrained")
		}
		if len(v.Commands) != 2 {
			t.Fatalf("expected 2 published commands, got %d", len(v.Commands))
		}

		byKey := map[string]*CommandDefinition{}
		for _, cd := range v.Commands {
			byKey[cd.CommandKey] = cd
		}
		if byKey["reboot"] == nil || byKey["setpoint"] == nil {
			t.Fatalf("expected reboot + setpoint, got %v", byKey)
		}
		// The schema is what drives the console's parameter form. Listing a command
		// without it would put the user back to typing raw JSON.
		if byKey["setpoint"].ParameterSchema == nil || len(*byKey["setpoint"].ParameterSchema) == 0 {
			t.Fatal("a command's parameter schema must survive into the listing")
		}
	})

	// The publish boundary is the actual defect this slice fixes. The console used to
	// read DRAFT definitions, so it offered commands the gate would then reject.
	t.Run("a draft-only command is not listed and is not accepted", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-draft", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
		})

		// Author a second command AFTER publishing, so it exists only as a draft.
		profileId := v1ProfileIdFor(t, api, ctx, "dev-draft")
		draft := defWithSchema(t, "not-yet-published", nil)
		draft.DeviceProfileId = profileId
		draft.Token = "cd-draft-only"
		if err := api.RDB.DB(ctx).Create(draft).Error; err != nil {
			t.Fatalf("seed draft command: %v", err)
		}

		v, err := api.DeviceCommandVocabulary(ctx, "dev-draft")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, cd := range v.Commands {
			if cd.CommandKey == "not-yet-published" {
				t.Fatal("an unpublished draft must not appear in a device's vocabulary")
			}
		}

		verdict, err := api.ValidateCommandEnqueue(ctx, "dev-draft", "not-yet-published", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if verdict.Allowed {
			t.Fatal("gate accepted a draft-only command; listing and gate disagree")
		}
	})

	// The agreement property itself, stated directly: every listed command is
	// accepted by the gate. This is what the shared resolution buys, and it is the
	// assertion that would fail first if someone later gave the listing its own
	// resolution path.
	t.Run("every listed command is accepted by the gate", func(t *testing.T) {
		api := newEnqueueTestApi(t)
		seedDeviceWithCommands(t, api, ctx, "dev-agree", []*CommandDefinition{
			defWithSchema(t, "reboot", nil),
			defWithSchema(t, "identify", nil),
			defWithSchema(t, "setpoint", []CommandParameter{
				{Name: "celsius", DataType: MetricDouble, Required: true},
			}),
		})

		v, err := api.DeviceCommandVocabulary(ctx, "dev-agree")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(v.Commands) == 0 {
			t.Fatal("nothing listed; the agreement assertion would be vacuous")
		}
		for _, cd := range v.Commands {
			// A payload satisfying the listed schema, built from the listing alone —
			// which is exactly what the console does.
			var payload []byte
			if cd.CommandKey == "setpoint" {
				payload = []byte(`{"celsius":21.5}`)
			}
			verdict, err := api.ValidateCommandEnqueue(ctx, "dev-agree", cd.CommandKey, payload)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", cd.CommandKey, err)
			}
			if !verdict.Allowed {
				t.Fatalf("vocabulary listed %q but the gate rejected it: %s", cd.CommandKey, verdict.Reason)
			}
		}
	})
}

// v1ProfileIdFor returns the profile id behind a device seeded by
// seedDeviceWithCommands, for tests that need to author against it post-publish.
func v1ProfileIdFor(t *testing.T, api *Api, ctx context.Context, deviceToken string) uint {
	t.Helper()
	profile := &DeviceProfile{}
	if err := api.RDB.DB(ctx).Where("token = ?", "prof-"+deviceToken).First(profile).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	return profile.ID
}
