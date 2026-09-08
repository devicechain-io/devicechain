// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// THE WIRE HALF OF THE RENAME.
//
// renameDeviceProfile is the capability updateDeviceProfile's payload token used to
// carry. The model tests (model/api_profiles_rename_test.go) pin what it does to the
// row; what only the schema can say is that the mutation exists with the arguments the
// contract names, that `newToken` is REQUIRED rather than an optional nicety a client
// could omit into a no-op, and that it is gated on the same authority the update is —
// a rename is an edit of the profile, not a new kind of act, and a read-only caller
// must not be able to perform one.

const renameProfileMutation = `
mutation ($token: String!, $newToken: String!) {
  renameDeviceProfile(token: $token, newToken: $newToken) { token }
}`

func TestGraphQLRenamesADeviceProfile(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "tracker"},
	})

	execProfileGql(t, ctx, renameProfileMutation, map[string]any{
		"token": "tracker", "newToken": "tracker-2",
	})

	// Both directions: the profile answers to its new token AND no longer answers to
	// the old one. Checking only the first would pass for a rename that COPIED the row.
	if got := profileTokens(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"tracker", "tracker-2"}})); len(got) != 1 || got[0] != "tracker-2" {
		t.Fatalf("after the rename the profiles found by {tracker, tracker-2} were %v, want exactly [tracker-2]", got)
	}
}

// profileTokens reads the tokens out of a deviceProfilesByToken response.
func profileTokens(t *testing.T, data json.RawMessage) []string {
	t.Helper()
	var parsed struct {
		Profiles []struct {
			Token string `json:"token"`
		} `json:"deviceProfilesByToken"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	out := make([]string, 0, len(parsed.Profiles))
	for _, p := range parsed.Profiles {
		out = append(out, p.Token)
	}
	return out
}

// 🔴 newToken IS NON-NULL, AND THAT IS THE HALF A MODEL TEST CANNOT SEE. Declared
// nullable, a client omitting it would reach the resolver with "" — which the blank
// check then refuses, but as a runtime error rather than as a request the schema never
// accepted. The rejection has to be a REQUEST error (nil path); a resolver error would
// be satisfied by the profile simply not existing.
func TestGraphQLRenameRequiresANewToken(t *testing.T) {
	ctx := profileLocationCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `mutation ($token: String!) {
	  renameDeviceProfile(token: $token) { token }
	}`, "", map[string]any{"token": "tracker"})

	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("a rename with no newToken reached the resolver instead of being refused by the "+
		"schema (errors: %v)", res.Errors)
}

// A rename is a profile EDIT, so it takes updateDeviceProfile's authority — the same
// one createDeviceProfile and every other profile mutation takes. Inventing a new
// authority for it would leave an operator who can rewrite every field of a profile
// unable to change its name, or the reverse.
func TestGraphQLRenameRequiresDeviceWrite(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "tracker"},
	})
	readOnly := withAuthorities(ctx, viewerBaseline...)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(readOnly, renameProfileMutation, "", map[string]any{
		"token": "tracker", "newToken": "tracker-2",
	})
	if len(resp.Errors) == 0 {
		t.Fatal("a read-only caller was allowed to rename a device profile")
	}

	// …and the counterweight, so the refusal is not bought by the mutation being
	// broken for everyone: the same request under device:write succeeds.
	writer := withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	if resp := schema.Exec(writer, renameProfileMutation, "", map[string]any{
		"token": "tracker", "newToken": "tracker-2",
	}); len(resp.Errors) > 0 {
		t.Fatalf("a device:write caller was refused a rename: %v", resp.Errors)
	}
}
