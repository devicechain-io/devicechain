// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// THE WIRE HALF of the provider's partial-update guarantee.
//
// The SHAPE of the input is only observable here, against the real admin schema: that
// `token` is not a member of AiProviderUpdateRequest, and that `request` is required.
// Both are rejections the schema performs before any resolver runs, so the model
// harness cannot see them. What reaches storage is the model harness's half
// (model/partial_update_suite_test.go).
//
// 🔴 THE RESOLVER IS REACHED WITH AN ai:admin IDENTITY AND NO Api IN CONTEXT, which is
// deliberate: apiFrom would panic on the type assertion, and graphql-go turns a
// resolver panic into a RESOLVER error carrying the field's path. That is exactly the
// distinction these assertions rest on — a REQUEST error (nil path) means the schema
// refused the document, a resolver error means it accepted it. Building a database here
// would only make the resolver fail on a missing row instead, which is the same signal
// through more machinery.

// adminWireCtx is an ai:admin identity.
//
// 🔴 THE TOKEN TYPE IS LOAD-BEARING, NOT DECORATION. ai:admin is a SYSTEM-TIER
// authority, and core/auth refuses one to a tenant ACCESS token however privileged its
// authority list — the belt to /admin/graphql's own refusal of an access token. Claims
// carrying the authority and nothing else are rejected, which is what
// TestAdminWireCtxSatisfiesAiAdmin caught the first time this file was written.
func adminWireCtx() context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeIdentity,
		Authorities: []string{string(auth.AIAdmin)},
	})
}

const updateProviderDoc = `
mutation ($token: String!, $request: AiProviderUpdateRequest!) {
  updateAiProvider(token: $token, request: $request) { token }
}`

// The token is the mutation's own argument and is deliberately not a member of the
// update input, so moving a provider's token through an update is UNREPRESENTABLE
// rather than merely refused. The rejection arrives from the unknown-input-field guard,
// which makes this a check on that guard too: a silently dropped field would tell the
// caller their rename succeeded.
func TestAiProviderUpdateInputCannotCarryAToken(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(adminWireCtx(), updateProviderDoc, "", map[string]any{
		"token": "whatever", "request": map[string]any{"token": "moved"},
	})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("`token` on AiProviderUpdateRequest reached the resolver instead of being refused "+
		"by the schema (errors: %v) — either the field was re-added to the input, or an "+
		"undeclared field is being silently dropped again", res.Errors)
}

// `request` is non-null, so a caller who sends nothing gets a request error rather than
// a silently successful no-op that returns the provider unchanged.
func TestAiProviderUpdateRequiresARequest(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(adminWireCtx(), `mutation ($token: String!) {
	  updateAiProvider(token: $token, request: null) { token }
	}`, "", map[string]any{"token": "whatever"})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("a null request reached the resolver instead of being refused by the schema "+
		"(errors: %v)", res.Errors)
}

// THE COUNTERWEIGHT, and the reason the two rejections above mean anything: they are
// only safe while a well-formed partial update still parses and reaches the resolver.
// Without this, renaming the input or mistyping a field would make both tests above
// pass for exactly the wrong reason — every request rejected, the guarantee held
// vacuously.
//
// It also covers the three states' SPELLING: `enabled: null` and `secret: null` are
// well-formed documents the schema must accept and hand on for the resolver to refuse
// or honour. A schema that declared them non-null would reject them here.
func TestAiProviderUpdateAcceptsAWellFormedPartialRequest(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	for name, request := range map[string]map[string]any{
		"a value":                {"model": "claude-haiku-4-5-20251001"},
		"an explicit null":       {"params": nil},
		"a null on the secret":   {"secret": nil},
		"a null on the boolean":  {"enabled": nil},
		"nothing but the object": {},
	} {
		t.Run(name, func(t *testing.T) {
			res := schema.Exec(adminWireCtx(), updateProviderDoc, "", map[string]any{
				"token": "whatever", "request": request,
			})
			for _, e := range res.Errors {
				if e.Path == nil {
					t.Fatalf("a well-formed partial update was rejected before the resolver: %v", e)
				}
			}
		})
	}
}

// renameAiProvider's `newToken` is NON-NULL, which is the half a model test cannot see.
// Declared nullable, a client omitting it would reach the resolver with "" — refused by
// the blank check, but as a runtime error rather than as a request the schema never
// accepted.
func TestAiProviderRenameRequiresANewToken(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(adminWireCtx(), `mutation ($token: String!) {
	  renameAiProvider(token: $token) { token }
	}`, "", map[string]any{"token": "whatever"})
	for _, e := range res.Errors {
		if e.Path == nil {
			return
		}
	}
	t.Fatalf("a rename with no newToken reached the resolver instead of being refused by the "+
		"schema (errors: %v)", res.Errors)
}

// A rename is gated on ai:admin like every other mutation on this plane. The
// counterweight is the test above, which reaches the resolver with the authority — so
// this refusal is about the authority rather than about the mutation being broken.
func TestAiProviderRenameRequiresAiAdmin(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(context.Background(), `mutation ($token: String!, $newToken: String!) {
	  renameAiProvider(token: $token, newToken: $newToken) { token }
	}`, "", map[string]any{"token": "whatever", "newToken": "renamed"})
	if len(res.Errors) == 0 {
		t.Fatal("an unauthenticated caller was allowed to rename a provider")
	}
}

// A guard on the guard: the context above really does satisfy ai:admin, so every
// "reached the resolver" reading in this file is about the DOCUMENT rather than about
// an authorization failure that would produce a resolver error either way.
func TestAdminWireCtxSatisfiesAiAdmin(t *testing.T) {
	if err := auth.Authorize(adminWireCtx(), auth.AIAdmin); err != nil {
		t.Fatalf("the fixture context does not satisfy ai:admin: %v — every assertion in this "+
			"file would then be reading an authorization refusal as a resolver error", err)
	}
}
