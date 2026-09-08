// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
	"gorm.io/gorm"
)

// THE WIRE HALF of this service's partial-update guarantee.
//
// The guarantee splits across three layers, and no one of them is sufficient:
//
//   - THE SHAPE OF THE INPUT is only observable here, against the real schema: that
//     `token` is not a member of either update input, and that `request` is required.
//     Both are rejections the schema performs before any resolver runs, so a model test
//     cannot see them.
//   - THE THREE STATES REACHING STORAGE live in the model harness
//     (model/partial_update_suite_test.go), which drives the real Api against a real
//     database.
//   - THAT THE THREE STATES SURVIVE THE WIRE for the SCALARS is proved once, generically,
//     in core's graphql package, which executes a real schema and asserts absent/null/value
//     arrive distinguishably for OptionalString, OptionalBool, OptionalInt32 and the rest.
//
// 🔴 THE RULE LIST IS THE EXCEPTION, AND IT IS WHY THIS FILE EXISTS RATHER THAN BEING A
// COPY OF DEVICE-MANAGEMENT'S. OptionalNotificationRuleList is this service's own type, so
// nothing in core proves anything about it; and unlike every scalar on these inputs it
// bypasses graphql-go's StructPacker entirely — a field whose Go type implements
// decode.Unmarshaler is handed the RAW value, so the library's input-object handling, and
// with it the unknown-field rejection this repo forked the library to get, never runs on a
// rule. A unit test on UnmarshalGraphQL alone would prove nothing about the state that
// matters most, ABSENT, because absent means the method is never called at all.
//
// So the rule list is driven through a real schema, with the value arriving as a VARIABLE.
// That is not belt-and-braces either: the literal and the variable paths are decoded by
// different code in this library — the parser for one, encoding/json for the other — and
// the divergence between them is exactly what made the fork necessary. Every real client
// (console, SDKs, dcctl, codegen) sends variables.

// newWireCtx builds the request context a resolver reads its dependencies out of: a
// throwaway database with the production callbacks, a secret store over the same database
// (the channel resolvers reach it for hasSecret), a tenant, and the write authority these
// mutations require.
//
// 🔴 THE TEST NAME IS SANITIZED BEFORE IT GOES IN A URI. A raw t.Name() carries "/" for
// every subtest and "#01", "#02" … whenever one name repeats within a run, and a URI reads
// "#" as the start of a FRAGMENT — so the database name silently truncates and sqlite falls
// back to an ON-DISK FILE in the working directory, shared with every other test that
// truncates the same way and outliving the run.
//
// 🔴 AND THE POOL IS CLOSED. A shared-cache in-memory database lives while a connection to
// it is open, gorm closes none, and the next test with the same name inherits its rows.
func newWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+wireDSNName(t)+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, cerr := db.DB()
		if cerr != nil {
			t.Errorf("reach the underlying pool to close it: %v", cerr)
			return
		}
		if cerr := sqlDB.Close(); cerr != nil {
			t.Errorf("close the fixture's pool: %v — the shared-cache database outlives this "+
				"test and the next one to reuse its name inherits these rows", cerr)
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&model.NotificationChannel{}, &model.NotificationPolicy{},
		&model.NotificationRule{}, &model.NotificationState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("kek: %v", err)
	}
	rdbManager := &rdb.RdbManager{Database: db}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Authorities: []string{string(auth.NotificationRead), string(auth.NotificationWrite)},
	})
	ctx = context.WithValue(ctx, gqlcore.ContextRdbKey, rdbManager)
	return context.WithValue(ctx, gqlcore.ContextApiKey,
		model.NewApi(rdbManager, secrets.NewStore(db, kek)))
}

// wireDSNName turns a test's name into something safe to sit in a SQLite URI. See the "#"
// note on newWireCtx for why this is not cosmetic.
func wireDSNName(t *testing.T) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
}

func apiOf(t *testing.T, ctx context.Context) *model.Api {
	t.Helper()
	api, ok := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if !ok {
		t.Fatal("the wire context carries no Api")
	}
	return api
}

// seedPolicyWithOneRule creates the channel a rule names and a policy holding exactly one
// rule through it, returning nothing — every assertion reads the policy back through the
// Api so it is measuring rows rather than the value a mutation returned.
func seedPolicyWithOneRule(t *testing.T, ctx context.Context) {
	t.Helper()
	api := apiOf(t, ctx)
	if _, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "smtp-ops", ChannelType: model.ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{
		Token: "ops-policy", Name: strptr("Original"), Enabled: true,
		Rules: []*model.NotificationRuleCreateRequest{
			{Severity: "CRITICAL", ChannelToken: "smtp-ops", Recipients: strptr(`["oncall@example.invalid"]`)},
		},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
}

func strptr(s string) *string { return &s }

// storedRules reads the policy's rule set back from the database.
func storedRules(t *testing.T, ctx context.Context) []model.NotificationRule {
	t.Helper()
	found, err := apiOf(t, ctx).NotificationPoliciesByToken(ctx, []string{"ops-policy"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload the policy: err=%v rows=%d", err, len(found))
	}
	return found[0].Rules
}

// updatePolicyDoc is the document every rule-list case below executes. All four states are
// reachable through the same document with only the variables changing — which is the
// point: a document per case would let one of them differ in something other than the
// state under test.
//
// 🔴 THE VARIABLE IS THE WHOLE REQUEST OBJECT, `$request`, AND THAT IS THE SHAPE THE
// ABSENT STATE IS A PROPERTY OF. Every real client sends it this way, because it is what
// codegen and every SDK produce. The other spelling — a variable per field, `request: {
// rules: $r }` — does NOT give you the absent state: an unsupplied `$r` arrives as a
// present null, so the field reads Set=true, Value=nil and the policy is EMPTIED. That is
// graphql-go's behaviour and it is identical on OptionalString and every other optional on
// the platform; it is filed as a fork candidate rather than something this service can fix.
// It is worth naming here because the consequence differs by field: on a string it clears
// one column, and on this field it destroys a policy's entire rule set and returns success.
// If you are hand-writing a document against this mutation, put the whole request in one
// variable and leave the fields you are not changing out of the map.
const updatePolicyDoc = `mutation($token: String!, $request: NotificationPolicyUpdateRequest!) {
  updateNotificationPolicy(token: $token, request: $request) {
    token
    name
    rules { severity recipients channel { token } }
  }
}`

func execUpdatePolicy(t *testing.T, ctx context.Context, request map[string]any) *gql.Response {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	return schema.Exec(ctx, updatePolicyDoc, "", map[string]any{
		"token": "ops-policy", "request": request,
	})
}

// 🔴 THE FOUR WIRE STATES OF `rules`, EACH ARRIVING AS A VARIABLE.
//
// ABSENT is the state this whole conversion added and the one no unit test on
// UnmarshalGraphQL could ever reach, because absent means the method is not called.
// The other three are the states a list can be sent in, and null and [] must agree.
func TestUpdatePolicyRuleListCarriesAllFourWireStates(t *testing.T) {
	t.Run("absent leaves the rule set alone", func(t *testing.T) {
		ctx := newWireCtx(t)
		seedPolicyWithOneRule(t, ctx)
		before := storedRules(t, ctx)

		res := execUpdatePolicy(t, ctx, map[string]any{"name": "Renamed"})
		requireNoErrors(t, res)

		after := storedRules(t, ctx)
		if len(after) != 1 {
			t.Fatalf("an update that said nothing about rules left %d rules, want 1", len(after))
		}
		// 🔴 THE ID IS THE ASSERTION, NOT THE COUNT. The old shape deleted every rule and
		// reinserted the request's, so a request carrying the same rule would leave the
		// same COUNT and the same CONTENT behind a brand-new row — a different id, a
		// different updated_at, and nothing in a count-or-content check able to see it.
		if after[0].ID != before[0].ID {
			t.Fatalf("the rule was deleted and reinserted (id %d -> %d): an absent rules field "+
				"must leave the stored rows untouched, not rebuild them", before[0].ID, after[0].ID)
		}
		if after[0].Severity != "CRITICAL" {
			t.Fatalf("the surviving rule changed: %+v", after[0])
		}
		// 🔴 AND THE RESPONSE STILL CARRIES THEM. The mutation returns the policy, and a
		// client that re-renders from that response would show a policy with NO RULES if
		// the resolver handed back the header alone — which looks exactly like the
		// deletion this change removes, from the one surface the operator is looking at.
		// The rows being intact is not enough; what they were told has to match.
		if !strings.Contains(string(res.Data), `"severity":"CRITICAL"`) {
			t.Fatalf("the response dropped the rule set the update preserved: %s", res.Data)
		}
		// The counterweight: the rest of the update still applied, so "untouched" was not
		// bought by the mutation doing nothing at all.
		found, err := apiOf(t, ctx).NotificationPoliciesByToken(ctx, []string{"ops-policy"})
		if err != nil || len(found) != 1 || found[0].Name.String != "Renamed" {
			t.Fatalf("the rest of the update did not apply: err=%v rows=%d", err, len(found))
		}
	})

	t.Run("an explicit null empties the rule set", func(t *testing.T) {
		ctx := newWireCtx(t)
		seedPolicyWithOneRule(t, ctx)

		res := execUpdatePolicy(t, ctx, map[string]any{"rules": nil})
		requireNoErrors(t, res)

		if after := storedRules(t, ctx); len(after) != 0 {
			t.Fatalf("an explicit null left %d rules, want 0", len(after))
		}
	})

	t.Run("an empty list empties the rule set", func(t *testing.T) {
		ctx := newWireCtx(t)
		seedPolicyWithOneRule(t, ctx)

		// [] is the spelling a form with nothing selected actually sends, and null is the
		// one almost nothing sends — so if the two ever stopped agreeing, the spelling
		// real clients use would be the untested one.
		res := execUpdatePolicy(t, ctx, map[string]any{"rules": []any{}})
		requireNoErrors(t, res)

		if after := storedRules(t, ctx); len(after) != 0 {
			t.Fatalf("an empty list left %d rules, want 0", len(after))
		}
	})

	t.Run("a list replaces the rule set", func(t *testing.T) {
		ctx := newWireCtx(t)
		seedPolicyWithOneRule(t, ctx)

		res := execUpdatePolicy(t, ctx, map[string]any{"rules": []any{
			map[string]any{"severity": "MAJOR", "channelToken": "smtp-ops"},
			map[string]any{"severity": "*", "channelToken": "smtp-ops",
				"recipients": `["dayshift@example.invalid"]`},
		}})
		requireNoErrors(t, res)

		after := storedRules(t, ctx)
		if len(after) != 2 {
			t.Fatalf("a two-rule list produced %d rules", len(after))
		}
		got := map[string]bool{}
		for _, r := range after {
			got[r.Severity] = true
			if r.Channel == nil || r.Channel.Token != "smtp-ops" {
				t.Fatalf("rule %d did not resolve its channel: %+v", r.ID, r)
			}
		}
		if !got["MAJOR"] || !got["*"] {
			t.Fatalf("the replacement rule set is not what was sent: %+v", after)
		}
	})
}

// A rule carrying a field the input type does not define is REFUSED, not dropped.
//
// This is the fork's own guarantee, checked where it is not automatic: the rule list
// bypasses StructPacker, so the refusal comes from the library's variable validation AND
// from OptionalNotificationRuleList's own decoder. Either one alone would be enough today;
// having both is what keeps a field added to the SDL and forgotten in the decoder from
// being silently written as absent.
func TestUpdatePolicyRefusesAnUndefinedFieldInsideARule(t *testing.T) {
	ctx := newWireCtx(t)
	seedPolicyWithOneRule(t, ctx)

	res := execUpdatePolicy(t, ctx, map[string]any{"rules": []any{
		map[string]any{"severity": "MAJOR", "channelToken": "smtp-ops", "recipient": "typo@example.invalid"},
	}})
	if len(res.Errors) == 0 {
		t.Fatal("a rule carrying an undefined field was accepted, so the recipients the caller " +
			"meant to set were silently dropped and the mutation returned success")
	}
	// And nothing was written: the seeded rule is still there, unchanged.
	after := storedRules(t, ctx)
	if len(after) != 1 || after[0].Severity != "CRITICAL" {
		t.Fatalf("the refused update still changed the rule set: %+v", after)
	}
}

// A rule missing a required field is refused for the same reason, and it matters more:
// a rule with an empty severity writes, reads back byte-for-byte, and matches no alarm
// for the rest of its life.
func TestUpdatePolicyRefusesARuleMissingARequiredField(t *testing.T) {
	for name, rule := range map[string]map[string]any{
		"no severity":     {"channelToken": "smtp-ops"},
		"no channelToken": {"severity": "MAJOR"},
		"null severity":   {"severity": nil, "channelToken": "smtp-ops"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := newWireCtx(t)
			seedPolicyWithOneRule(t, ctx)

			res := execUpdatePolicy(t, ctx, map[string]any{"rules": []any{rule}})
			if len(res.Errors) == 0 {
				t.Fatal("a rule missing a required field was accepted")
			}
			if after := storedRules(t, ctx); len(after) != 1 || after[0].Severity != "CRITICAL" {
				t.Fatalf("the refused update still changed the rule set: %+v", after)
			}
		})
	}
}

// ─── the shape of the update inputs ────────────────────────────────────────

var partialUpdateMutations = []struct {
	mutation, input, selection string
	probe                      map[string]any
}{
	{
		mutation: "updateNotificationChannel", input: "NotificationChannelUpdateRequest",
		selection: "token name", probe: map[string]any{"name": "Renamed"},
	},
	{
		mutation: "updateNotificationPolicy", input: "NotificationPolicyUpdateRequest",
		selection: "token name", probe: map[string]any{"name": "Renamed"},
	},
}

func updateDoc(mutation, input, selection string) string {
	return `mutation($token: String!, $request: ` + input + `!) {
	  ` + mutation + `(token: $token, request: $request) { ` + selection + ` }
	}`
}

// updateInputOf resolves an update input type out of the PARSED schema, so an assertion
// about its fields is made against the schema the service actually serves rather than
// against a substring of the SDL text.
func updateInputOf(t *testing.T, name string) *ast.InputObject {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	declared, ok := schema.AST().Types[name]
	if !ok {
		t.Fatalf("the schema declares no type named %s", name)
	}
	input, ok := declared.(*ast.InputObject)
	if !ok {
		t.Fatalf("%s is a %s, not an input object", name, declared.Kind())
	}
	return input
}

// The token is the mutation's own argument and is deliberately not a member of either
// update input, so moving a record's token is UNREPRESENTABLE rather than merely refused.
// For the channel that capability did not disappear — it moved to
// renameNotificationChannel; for the policy there was never anything keyed on its token to
// rename it for.
//
// 🔴 THE ABSENCE IS ASSERTED OVER THE PARSED SCHEMA, NOT BY WATCHING A REQUEST GET
// REJECTED, AND THE DIFFERENCE IS THE SAME ONE THE RULE DECODER'S TESTS RAN INTO. An
// earlier version of this test sent `request: { token: "moved" }` and looked for a request
// error. That passes — but what it observes is graphql-go's unknown-input-field rejection
// firing, which is the LIBRARY's guarantee (and the fork's), not this schema's. The
// property here is "the field is not declared", and the honest way to check it is to ask
// the type. It also removes a second hazard the old shape carried: these mutations are
// addressed to a token that names no row, so the resolver errors too, and an assertion on
// len(res.Errors) alone would have been satisfied by the not-found whether or not anything
// was refused.
//
// That the library still rejects an undeclared field on these inputs — the composite —
// remains covered, by TestUpdatePolicyRefusesAnUndefinedFieldInsideARule one level down.
func TestPartialUpdateInputsCannotCarryAToken(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			if f := updateInputOf(t, m.input).Values.Get("token"); f != nil {
				t.Fatalf("%s declares a token field (%s): the mutation's own argument names the "+
					"record, and a second token in the payload is the disagreement this whole "+
					"conversion removes", m.input, f.Type)
			}
		})
	}
}

// THE COUNTERWEIGHT to the two absence assertions in this file. "The field is not there"
// is trivially satisfiable by an input with no fields at all, or by a type name that no
// longer resolves — so the fields that MUST be there are named, and each is checked to be
// nullable, since a non-null input field with no default is required by validation and
// would make the absent state unrepresentable for it.
func TestPartialUpdateInputsDeclareTheirFieldsAsOptional(t *testing.T) {
	for input, fields := range map[string][]string{
		"NotificationChannelUpdateRequest": {
			"name", "description", "channelType", "config", "secret", "enabled", "metadata",
		},
		"NotificationPolicyUpdateRequest": {
			"name", "description", "throttleSeconds", "escalateAfterSeconds", "maxEscalations",
			"enabled", "rules", "metadata",
		},
	} {
		t.Run(input, func(t *testing.T) {
			declared := updateInputOf(t, input)
			for _, name := range fields {
				f := declared.Values.Get(name)
				if f == nil {
					t.Errorf("%s no longer declares %s", input, name)
					continue
				}
				if _, nonNull := f.Type.(*ast.NonNull); nonNull {
					t.Errorf("%s.%s is %s: a non-null input field with no default is REQUIRED by "+
						"validation, so the absent state — the whole point of this input — stops "+
						"being representable for it", input, name, f.Type)
				}
				if f.Default != nil {
					t.Errorf("%s.%s carries an SDL default, which the packer treats as non-null "+
						"and writes into the struct for an ABSENT field: absent then becomes "+
						"indistinguishable from \"sent the default\"", input, name)
				}
			}
			// And nothing else. A field added to the input without a fold behind it is
			// written from a zero value on every update, which is what the whole conversion
			// exists to stop.
			if got := len(declared.Values); got != len(fields) {
				names := make([]string, 0, got)
				for _, f := range declared.Values {
					names = append(names, f.Name.Name)
				}
				t.Errorf("%s declares %d fields (%s) but this test names %d — the schema and the "+
					"list have diverged", input, got, strings.Join(names, ", "), len(fields))
			}
		})
	}
}

// A policy's device-type scope cannot be set through an update, and for a different reason
// than the token: the dispatcher SKIPS a device-type-scoped policy, so accepting one would
// return success on a policy that delivers nothing. Asserted the same way, over the type.
//
// The COUNTERWEIGHT is on the create input, because "the field is absent" would also be
// satisfied by the field having been deleted from the API altogether — which is not what
// happened, and would take the refusal's explanation with it.
func TestUpdatePolicyInputCannotCarryADeviceTypeToken(t *testing.T) {
	if f := updateInputOf(t, "NotificationPolicyUpdateRequest").Values.Get("deviceTypeToken"); f != nil {
		t.Fatalf("NotificationPolicyUpdateRequest declares deviceTypeToken again (%s). It is not "+
			"enough to re-add it: the dispatcher still skips a device-type-scoped policy, so the "+
			"update path needs validateDeviceTypeScoping wired back in and driven, or a scoped "+
			"policy writes successfully and delivers nothing", f.Type)
	}
	if updateInputOf(t, "NotificationPolicyCreateRequest").Values.Get("deviceTypeToken") == nil {
		t.Fatal("NotificationPolicyCreateRequest no longer declares deviceTypeToken either — the " +
			"field was removed from the API rather than from the update path, which takes the " +
			"refusal that explains why scoping is not honoured yet along with it")
	}
}

// `request` is non-null, so a caller who sends nothing gets a request error rather than a
// silently successful no-op that returns the record unchanged.
func TestPartialUpdateRequiresARequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			ctx := newWireCtx(t)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, `mutation($token: String!) {
			  `+m.mutation+`(token: $token, request: null) { token }
			}`, "", map[string]any{"token": "no-such-token"})
			for _, e := range res.Errors {
				if e.Path == nil {
					return
				}
			}
			t.Fatalf("a null request reached the resolver instead of being refused by the "+
				"schema (errors: %v)", res.Errors)
		})
	}
}

// THE COUNTERWEIGHT, and it is the reason the rejections above mean anything. They are
// only safe while a well-formed partial update still parses and reaches the resolver.
// Without this, renaming an input or mistyping a field name would make every test above
// pass for the wrong reason — every request rejected, the guarantee "held" vacuously.
func TestPartialUpdateAcceptsAWellFormedPartialRequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			ctx := newWireCtx(t)
			seedForWellFormedProbe(t, ctx)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, updateDoc(m.mutation, m.input, m.selection), "", map[string]any{
				"token": "probe", "request": m.probe,
			})
			requireNoErrors(t, res)
			if !strings.Contains(string(res.Data), `"name":"Renamed"`) {
				t.Fatalf("the update did not apply: %s", res.Data)
			}
		})
	}
}

// seedForWellFormedProbe creates one channel and one policy under the same token, so the
// counterweight above can address both mutations without a per-mutation fixture. They are
// different tables, so one token serves both.
func seedForWellFormedProbe(t *testing.T, ctx context.Context) {
	t.Helper()
	api := apiOf(t, ctx)
	if _, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "probe", Name: strptr("Original"), ChannelType: model.ChannelTypeSMTP, Enabled: true,
	}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{
		Token: "probe", Name: strptr("Original"), Enabled: true,
		Rules: []*model.NotificationRuleCreateRequest{},
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
}

func requireNoErrors(t *testing.T, res *gql.Response) {
	t.Helper()
	if len(res.Errors) > 0 {
		t.Fatalf("the request failed: %v", res.Errors)
	}
}
