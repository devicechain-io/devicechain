// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// What a PARTIAL update means once it reaches storage. These drive the real Api
// against a real database, so the assertions are about rows, not about structs.
//
// The wire-shape half (token is not a member of the input; request is required)
// lives in graphql/device_type_partial_update_wire_test.go, and the proof that the
// three states survive the packer at all is in core's optional_test.go.

// seedFullType creates a device type with EVERY field populated and a profile
// attached. Populating everything is what makes an erasure visible: against a
// fixture with blank fields, "preserved" and "was never set" are the same
// observation, and a full replace would pass every assertion.
func seedFullType(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-a"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{
		Token:           "sensor",
		Name:            strp("Original name"),
		Description:     strp("Original description"),
		ImageUrl:        strp("https://example.invalid/original.png"),
		Icon:            strp("gauge"),
		BackgroundColor: strp("#111111"),
		ForegroundColor: strp("#222222"),
		BorderColor:     strp("#333333"),
		ProfileToken:    strp("profile-a"),
		Manufacturer:    strp("Acme"),
		Model:           strp("A-1000"),
		Metadata:        strp(`{"fleet":"north"}`),
	}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
}

// reload reads the type back from the database rather than trusting the value the
// update returned — a resolver that mutated its in-memory copy and never persisted
// would satisfy every assertion made against its return value.
func reload(t *testing.T, api *Api, ctx context.Context) *DeviceType {
	t.Helper()
	matches, err := api.DeviceTypesByToken(ctx, []string{"sensor"})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one device type, got %d", len(matches))
	}
	return matches[0]
}

func nullStr(v sql.NullString) string {
	if !v.Valid {
		return "<null>"
	}
	return v.String
}

// THE HEADLINE CASE. Renaming a device type must change the name and NOTHING else.
//
// Under the full-replace shape this replaces, this exact request wiped imageUrl,
// icon, three colours, manufacturer, model, metadata AND the adopted profile —
// successfully, returning 200 with the emptied entity. Detaching the profile is the
// worst of them: it silently un-declares every capability the type's devices
// resolve through it.
func TestUpdateDeviceType_PartialRenameLeavesEverythingElseAlone(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedFullType(t, api, ctx)

	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got := reload(t, api, ctx)
	if n := nullStr(got.Name); n != "Renamed" {
		t.Fatalf("name = %q, want %q", n, "Renamed")
	}

	for _, p := range []struct {
		field string
		got   string
		want  string
	}{
		{"description", nullStr(got.Description), "Original description"},
		{"imageUrl", nullStr(got.ImageUrl), "https://example.invalid/original.png"},
		{"icon", nullStr(got.Icon), "gauge"},
		{"backgroundColor", nullStr(got.BackgroundColor), "#111111"},
		{"foregroundColor", nullStr(got.ForegroundColor), "#222222"},
		{"borderColor", nullStr(got.BorderColor), "#333333"},
		{"manufacturer", nullStr(got.Manufacturer), "Acme"},
		{"model", nullStr(got.ModelName), "A-1000"},
	} {
		if p.got != p.want {
			t.Errorf("a rename erased %s: got %q, want %q — the update is still a full replace",
				p.field, p.got, p.want)
		}
	}

	if got.Metadata == nil || string(*got.Metadata) != `{"fleet":"north"}` {
		t.Errorf("a rename erased metadata: %v", got.Metadata)
	}
	if got.ProfileId == nil {
		t.Error("a rename detached the adopted profile, which un-declares every capability " +
			"its devices resolve through the type")
	}
}

// The second state. An explicit null CLEARS, and clears only what it names. This is
// the half a naive "ignore empty fields" implementation gets wrong — that shape
// preserves everything, which looks correct until someone needs to remove a value
// and finds the API cannot express it.
func TestUpdateDeviceType_ExplicitNullClearsOnlyThatField(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedFullType(t, api, ctx)

	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
		Icon: dcgraphql.ClearedString(),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got := reload(t, api, ctx)
	if n := nullStr(got.Icon); n != "<null>" {
		t.Fatalf("icon = %q after an explicit null; a field that cannot be cleared is a "+
			"field that can never be corrected", n)
	}
	if n := nullStr(got.Name); n != "Original name" {
		t.Errorf("clearing icon also changed name to %q", n)
	}
	if n := nullStr(got.BackgroundColor); n != "#111111" {
		t.Errorf("clearing icon also changed backgroundColor to %q", n)
	}
	if got.ProfileId == nil {
		t.Error("clearing icon also detached the profile")
	}
}

// An update that names nothing at all changes nothing at all. This is the control
// for the whole semantic: if it fails, some field is being written from a zero
// value rather than from the stored one, and every partial update is silently
// erasing that field.
func TestUpdateDeviceType_EmptyRequestChangesNothing(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedFullType(t, api, ctx)
	before := reload(t, api, ctx)

	if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{}); err != nil {
		t.Fatalf("update: %v", err)
	}

	after := reload(t, api, ctx)
	for _, p := range []struct {
		field       string
		got, wanted string
	}{
		{"name", nullStr(after.Name), nullStr(before.Name)},
		{"description", nullStr(after.Description), nullStr(before.Description)},
		{"imageUrl", nullStr(after.ImageUrl), nullStr(before.ImageUrl)},
		{"icon", nullStr(after.Icon), nullStr(before.Icon)},
		{"backgroundColor", nullStr(after.BackgroundColor), nullStr(before.BackgroundColor)},
		{"foregroundColor", nullStr(after.ForegroundColor), nullStr(before.ForegroundColor)},
		{"borderColor", nullStr(after.BorderColor), nullStr(before.BorderColor)},
		{"manufacturer", nullStr(after.Manufacturer), nullStr(before.Manufacturer)},
		{"model", nullStr(after.ModelName), nullStr(before.ModelName)},
	} {
		if p.got != p.wanted {
			t.Errorf("an empty update changed %s from %q to %q", p.field, p.wanted, p.got)
		}
	}
	if after.Metadata == nil || string(*after.Metadata) != `{"fleet":"north"}` {
		t.Errorf("an empty update changed metadata: %v", after.Metadata)
	}
	if after.ProfileId == nil {
		t.Error("an empty update detached the profile")
	}
	if after.Token != before.Token {
		t.Errorf("an empty update moved the token from %q to %q", before.Token, after.Token)
	}
}

// profileToken carries the same three states as every other field, and its absent
// state is the one that mattered most — it is the reference a rename used to drop.
func TestUpdateDeviceType_ProfileTokenThreeStates(t *testing.T) {
	t.Run("null detaches", func(t *testing.T) {
		api := newRosterEmitTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		seedFullType(t, api, ctx)
		if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			ProfileToken: dcgraphql.ClearedString(),
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if reload(t, api, ctx).ProfileId != nil {
			t.Fatal("an explicit null left the profile attached")
		}
	})

	t.Run("an empty token detaches", func(t *testing.T) {
		// Carried over from the pre-partial behaviour, where empty and omitted were
		// the same thing. They are no longer the same thing, so this is pinned rather
		// than assumed: empty still detaches, omitted no longer does.
		api := newRosterEmitTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		seedFullType(t, api, ctx)
		if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			ProfileToken: dcgraphql.OptionalStringOf("   "),
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if reload(t, api, ctx).ProfileId != nil {
			t.Fatal("an empty profileToken left the profile attached")
		}
	})

	t.Run("an unknown token refuses the WHOLE update", func(t *testing.T) {
		api := newRosterEmitTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		seedFullType(t, api, ctx)

		_, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			Name:         dcgraphql.OptionalStringOf("Renamed"),
			ProfileToken: dcgraphql.OptionalStringOf("no-such-profile"),
		})
		if err == nil {
			t.Fatal("an unknown profile token was accepted, leaving a dangling reference")
		}
		// The refusal must be total. Applying the name and then failing on the
		// profile would be the worst of both designs — a caller who retries has
		// already half-applied the first attempt.
		if n := nullStr(reload(t, api, ctx).Name); n != "Original name" {
			t.Fatalf("the refused update still applied name = %q", n)
		}
	})

	t.Run("re-pointing to another profile works", func(t *testing.T) {
		api := newRosterEmitTestApi(t)
		ctx := core.WithTenant(context.Background(), "acme")
		seedFullType(t, api, ctx)
		if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "profile-b"}); err != nil {
			t.Fatalf("seed profile-b: %v", err)
		}
		before := reload(t, api, ctx).ProfileId
		if _, err := api.UpdateDeviceType(ctx, "sensor", &DeviceTypeUpdateRequest{
			ProfileToken: dcgraphql.OptionalStringOf("profile-b"),
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		after := reload(t, api, ctx).ProfileId
		if after == nil || (before != nil && *after == *before) {
			t.Fatalf("profile did not move: before=%v after=%v", before, after)
		}
	})
}

// An update to a token that does not exist is an error, not a silent create. The
// lookup moved from the request payload to the mutation argument in this
// conversion, so the not-found path is worth re-pinning at its new source.
func TestUpdateDeviceType_UnknownTokenIsNotFound(t *testing.T) {
	api := newRosterEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedFullType(t, api, ctx)

	if _, err := api.UpdateDeviceType(ctx, "no-such-type", &DeviceTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Renamed"),
	}); err == nil {
		t.Fatal("updating an unknown device type succeeded")
	}
}
