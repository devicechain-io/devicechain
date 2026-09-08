// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// THE WIRE HALF OF THE PARTIAL-UPDATE GUARANTEE, for every converted mutation on both of
// this service's schemas.
//
// The guarantees split across three layers, and no one of them is sufficient:
//
//   - THE SHAPE OF THE INPUT is only observable here, against the real schema: that the
//     record's identity is not a member of the update input, and that `request` is
//     required. Both are rejections the schema performs before any resolver runs, so a
//     service test cannot see them.
//   - THE THREE STATES REACHING STORAGE live in the two service-side suites
//     (admin/partial_update_*_test.go and identity/partial_update_test.go), which drive
//     the real services against a real database.
//   - THAT THE THREE STATES SURVIVE THE WIRE AT ALL is proved once, generically, in core:
//     graphql.TestOptionalStringCarriesThreeStates, TestOptionalScalarsCarryThreeStates
//     and the list's own tests execute a real schema and assert absent/null/value arrive
//     distinguishably. Every field on every one of these inputs is an Optional*, so that
//     proof carries here by construction rather than by repetition.
//
// 🔴 TWO SCHEMAS ARE COVERED, AND SPLITTING THEM WOULD BE THE SAME LIST TWICE. The four
// admin catalogs live on admin_schema.gql behind identity-token authorization; the
// profile edit lives on the tenant data plane in schema.gql. Both go through the table
// below, with the schema and its resolver root named per row, because the property being
// asserted is identical and a second copy of these three tests is how the second schema
// comes to be checked less thoroughly than the first.

type partialUpdateMutation struct {
	// mutation is the field name on the Mutation type.
	mutation string
	// input is the type its `request` argument takes.
	input string
	// admin selects which schema this row belongs to.
	admin bool
	// args are the mutation's IDENTITY arguments besides `request`, in declaration
	// order, each with the value to send. They are what names the record, and every one
	// of them is deliberately NOT a member of the input.
	args []wireArg
	// selection is what the mutation asks for back.
	selection string
	// probe is a well-formed field of the input, used by the counterweight test.
	probe map[string]any
	// forbidden lists fields that must NOT be members of the input, beyond the identity
	// arguments (which are checked for every row).
	forbidden []string
}

type wireArg struct {
	name  string
	gqlTy string
	value any
}

// partialUpdateMutations is every mutation carrying the partial-update semantic, with the
// input type its `request` argument takes. Converting a family adds a row — and
// TestEveryUpdateMutationIsCoveredByTheWireTests checks this list against BOTH schemas in
// both directions, so a family converted without a row here fails rather than going
// uncovered.
var partialUpdateMutations = []partialUpdateMutation{
	{
		mutation: "updateRole", input: "AdminRoleUpdateRequest", admin: true,
		// 🔴 scope AND token, because a role's identity is the PAIR. A row listing only
		// `token` would let the forbidden-field check pass while `scope` sat in the input,
		// which is the one shape that could silently move an update between two roles with
		// different authority vocabularies.
		args: []wireArg{
			{"scope", "String!", "system"},
			{"token", "String!", "whatever"},
		},
		selection: "token name",
	},
	{
		mutation: "updateTenantTier", input: "AdminTenantTierUpdateRequest", admin: true,
		args:      []wireArg{{"token", "String!", "whatever"}},
		selection: "token name",
	},
	{
		mutation: "updateTenant", input: "AdminTenantUpdateRequest", admin: true,
		args:      []wireArg{{"token", "String!", "whatever"}},
		selection: "token name",
	},
	{
		mutation: "updateOauthClient", input: "AdminOAuthClientUpdateRequest", admin: true,
		args:      []wireArg{{"clientId", "String!", "whatever"}},
		selection: "clientId name",
		// A client's identity is its clientId; `token` is not a field it has at all, so
		// the generic identity-argument check would be vacuous without this.
		forbidden: []string{"token"},
	},
	{
		mutation: "updateProfile", input: "ProfileUpdateRequest",
		// Self-scoped: the identity is named by the caller's own token subject, so this
		// mutation has NO identity argument. `email` is what an input would have to carry
		// to address someone else, and it is checked as forbidden for exactly that reason.
		args:      nil,
		selection: "email firstName",
		probe:     map[string]any{"firstName": "Augusta"},
		forbidden: []string{"email"},
	},
}

func (m partialUpdateMutation) probeOrDefault() map[string]any {
	if m.probe != nil {
		return m.probe
	}
	return map[string]any{"name": "Renamed"}
}

// schemaFor builds the schema this row belongs to, against its own resolver root.
func (m partialUpdateMutation) schemaFor() *gql.Schema {
	if m.admin {
		return gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	}
	return gql.MustParseSchema(SchemaContent, &SchemaResolver{})
}

// ctxFor is a request context that both PASSES authorization and carries a real service
// over a throwaway database.
//
// 🔴 BOTH HALVES ARE REQUIRED, FOR DIFFERENT REASONS.
//
// The credential must pass, because every assertion below distinguishes a REQUEST error
// (the schema refused the document) from a RESOLVER error, and an authorization failure
// is a resolver error — an under-privileged context would make the counterweight test
// pass while asserting nothing about whether a well-formed update is accepted.
//
// The SERVICE must be present because the resolvers reach it with a type assertion, and
// a missing one PANICS rather than erroring. A panic inside a resolver is caught by the
// library and reported as an error with a non-nil path, so the two rejection tests would
// still pass — on a stack trace, having exercised nothing past argument packing.
func (m partialUpdateMutation) ctxFor(t *testing.T) context.Context {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.TenantTier{}, &iam.Tenant{},
		&iam.OAuthClient{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	if m.admin {
		ctx := adminCtx(string(auth.RoleWrite), string(auth.TenantWrite), string(auth.ClientWrite))
		svc := admin.NewService(iam.NewStore(rdbm), 300*time.Second, 12*time.Hour, nil)
		return context.WithValue(ctx, ContextAdminKey, svc)
	}
	// updateProfile needs only to be authenticated: it is self-scoped, so the token
	// subject IS the authorization. The Manager is built through NewManager with no
	// microservice, lock or issuer — UpdateProfile reads none of them, and building the
	// rest would make this file need a live NATS KV to check a document's shape.
	ctx := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Username: "nobody@example.invalid",
	})
	mgr := identity.NewManager(nil, rdbm, nil, 0, 0, "", identity.BootstrapConfig{})
	return context.WithValue(ctx, ContextIdentityKey, mgr)
}

// updateMutationDoc composes the document for one row: identity arguments plus a
// `$request` of the input's own type, so the schema type-checks what is sent.
func updateMutationDoc(m partialUpdateMutation) string {
	var decls, call []string
	for _, a := range m.args {
		decls = append(decls, "$"+a.name+": "+a.gqlTy)
		call = append(call, a.name+": $"+a.name)
	}
	decls = append(decls, "$request: "+m.input+"!")
	call = append(call, "request: $request")
	return "mutation(" + strings.Join(decls, ", ") + ") {\n  " +
		m.mutation + "(" + strings.Join(call, ", ") + ") { " + m.selection + " }\n}"
}

func (m partialUpdateMutation) variables(request any) map[string]any {
	vars := map[string]any{"request": request}
	for _, a := range m.args {
		vars[a.name] = a.value
	}
	return vars
}

// TestPartialUpdateInputsCannotCarryTheRecordsIdentity is the shape assertion the
// conversion exists to make: the mutation's own arguments name the record, and the input
// has no way to name a different one.
//
// The rejection arrives from the unknown-input-field guard (the pinned graphql-go fork),
// which makes this a check on that guard too: without it a misnamed field is silently
// DROPPED and the caller is told their update succeeded.
func TestPartialUpdateInputsCannotCarryTheRecordsIdentity(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			fields := m.forbidden
			for _, a := range m.args {
				fields = append(fields, a.name)
			}
			require.NotEmpty(t, fields,
				"%s declares neither an identity argument nor a forbidden field, so this "+
					"subtest would iterate nothing and report success", m.mutation)
			for _, field := range fields {
				t.Run(field, func(t *testing.T) {
					assertFieldRejectedBeforeTheResolver(t, m, field, "moved")
				})
			}
		})
	}
}

// 🔴 THE REJECTION HAS TO BE A REQUEST ERROR, AND CHECKING ONLY "there was an error" IS A
// FAIL-OPEN. Every one of these mutations is addressed to a record that does not exist
// (and reaches a resolver whose service is not even on the context), so the resolver
// errors too — an assertion of "some error happened" would be satisfied by that whether
// or not the schema had rejected anything.
//
// A request error — a validation failure, which is what an undeclared field produces —
// arrives with a nil Path, because it happened before any field was resolved. A resolver
// error carries the path of the field that failed. That distinction is the whole
// assertion.
func assertFieldRejectedBeforeTheResolver(t *testing.T, m partialUpdateMutation, field string, value any) {
	t.Helper()
	res := m.schemaFor().Exec(m.ctxFor(t), updateMutationDoc(m), "",
		m.variables(map[string]any{field: value}))
	for _, e := range res.Errors {
		if e.Path == nil {
			return // a request error: the schema refused the field, which is the claim
		}
	}
	t.Fatalf("%s on %s reached the resolver instead of being refused by the schema (errors: %v) — "+
		"either the field was added to the input, or an undeclared field is being silently "+
		"dropped again", field, m.input, res.Errors)
}

// TestPartialUpdateRequiresARequest: `request` is non-null, so a caller who sends nothing
// gets a request error rather than a silently successful no-op.
//
// 🔴 Non-null refuses a MISSING request, not an EMPTY one. `{}` is a perfectly good
// non-null input object and is accepted as a no-op, which is the correct reading of
// "change nothing" and what the service suites' EmptyRequestChangesNothing asserts.
func TestPartialUpdateRequiresARequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			res := m.schemaFor().Exec(m.ctxFor(t), updateMutationDoc(m), "", m.variables(nil))
			refused := false
			for _, e := range res.Errors {
				if e.Path == nil {
					refused = true
				}
			}
			require.Truef(t, refused,
				"a null request reached the resolver instead of being refused by the schema "+
					"(errors: %v)", res.Errors)
		})
	}
}

// THE COUNTERWEIGHT, and the reason the two rejections above mean anything. They are only
// safe while a well-formed partial update still parses and reaches the resolver — without
// this, renaming an input or mistyping a field would make both tests above pass for
// exactly the wrong reason, every request rejected and the guarantee "held" vacuously.
func TestPartialUpdateAcceptsAWellFormedPartialRequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			res := m.schemaFor().Exec(m.ctxFor(t), updateMutationDoc(m), "",
				m.variables(m.probeOrDefault()))
			// A REQUEST error carries a nil Path — the schema refused the document before
			// any field resolved. A RESOLVER error carries the failing field's path, and
			// that is what is expected here: every row is addressed to a record that does
			// not exist, so reaching the not-found IS the claim that the request was
			// accepted.
			for _, e := range res.Errors {
				require.NotNilf(t, e.Path,
					"a well-formed partial update was rejected before the resolver: %v", e)
			}
		})
	}
}

// TestAListFieldAcceptsAnExplicitNull drives the state a `[String!]!` could not express,
// which is the reason those three fields were widened to `[String!]`.
//
// It is a SCHEMA assertion, not a semantic one: what null MEANS differs by field —
// emptying a role's authorities, being refused on an OAuth client's allowlists — and both
// meanings live in the service suites. What has to be true here is that the document
// PARSES, because a non-null list is required by validation and a caller could not even
// send the request.
func TestAListFieldAcceptsAnExplicitNull(t *testing.T) {
	lists := map[string][]string{
		"updateRole":        {"authorities"},
		"updateOauthClient": {"redirectUris", "scopes"},
	}
	for _, m := range partialUpdateMutations {
		fields, ok := lists[m.mutation]
		if !ok {
			continue
		}
		for _, field := range fields {
			t.Run(m.mutation+"/"+field, func(t *testing.T) {
				res := m.schemaFor().Exec(m.ctxFor(t), updateMutationDoc(m), "",
					m.variables(map[string]any{field: nil}))
				for _, e := range res.Errors {
					require.NotNilf(t, e.Path,
						"%s: null was refused BEFORE the resolver, so the field is still "+
							"non-null in the SDL and the absent state is unrepresentable: %v",
						field, e)
				}
			})
		}
	}
	require.Len(t, lists, 2, "the list-field table names no mutations, so this test drives nothing")
}

// 🔴 THE LIST ABOVE IS CHECKED AGAINST BOTH SCHEMAS, BECAUSE A LIST IS NOT A GUARD.
//
// partialUpdateMutations is hand-written and everything else in this file iterates it, so
// a row DELETED from it takes its mutation out of every check here and leaves the package
// green. So the set of `update*` mutations is derived from the SDL — through the server's
// own introspection rather than a regex over the file, which is the same rule rather than
// an approximation of it — and every one must be listed or NAMED as an exemption.
func TestEveryUpdateMutationIsCoveredByTheWireTests(t *testing.T) {
	// The mutations deliberately outside the partial-update contract. There are none in
	// this service; the map is written out so "the residual is zero" is a statement a
	// reader can count rather than infer.
	//
	// setTenantBranding, setTenantLogo, setSystemRoles, setMembershipRoles, setPassword
	// and the setXEnabled pairs are NOT exemptions and never appear here: they are `set*`,
	// not `update*`. Each is a single-purpose write whose whole payload IS the thing it
	// sets, so there is no field for an omission to erase.
	exempt := map[string]string{}

	listed := map[string]bool{}
	for _, m := range partialUpdateMutations {
		listed[m.mutation] = true
	}

	found := 0
	for _, schema := range []*gql.Schema{
		gql.MustParseSchema(AdminSchemaContent, &AdminResolver{}),
		gql.MustParseSchema(SchemaContent, &SchemaResolver{}),
		gql.MustParseSchema(SettingsSchemaContent, &SettingsResolver{}),
	} {
		mutationType := schema.Inspect().MutationType()
		require.NotNil(t, mutationType, "a schema declares no Mutation type; this guard is reading nothing")
		fields := mutationType.Fields(&struct{ IncludeDeprecated bool }{})
		require.NotEmpty(t, fields, "a Mutation type reports no fields; this guard is reading nothing")

		for _, f := range *fields {
			if !strings.HasPrefix(f.Name(), "update") {
				continue
			}
			found++
			if listed[f.Name()] || exempt[f.Name()] != "" {
				continue
			}
			t.Errorf("%s is an update mutation this service serves, but it is in neither "+
				"partialUpdateMutations nor the exemption list — so nothing in this file sends "+
				"it and its wire shape is certified by nobody", f.Name())
		}
	}

	// The other direction, so a row naming a mutation no schema serves fails too.
	require.Equal(t, len(partialUpdateMutations), found,
		"the schemas serve %d update* mutations and this file lists %d — the two have diverged",
		found, len(partialUpdateMutations))
}
