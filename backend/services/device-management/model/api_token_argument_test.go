// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
)

// THE TOKEN ARGUMENT NAMES THE ROW. Nothing else does.
//
// Every update mutation in this service declares `token: String!`, and until this
// change that argument did three different things depending on which one you called:
//
//   - NINE of the seventeen ignored it outright and located the row by the token
//     inside the request payload. Sending a `token` argument naming one entity and a
//     `request.token` naming another silently updated the SECOND and returned it with
//     a 200 — a mandatory argument that was pure decoration, which is worse than an
//     absent one, because a client that gets the argument right and the payload wrong
//     has no way to find out.
//   - SEVEN located by the argument and then ASSIGNED the payload token over it, so
//     the payload still moved the row — and an empty payload token, which the schema
//     permits (String! admits ""), blanked the entity's token entirely.
//   - ONE, updateGeoFence, reconciled the two and refused a mismatch.
//
// The rule is now geoFence's, everywhere. Seven families went further and dropped the
// payload token from their input altogether (see partial_update_families_test.go),
// which makes the disagreement unrepresentable; the ones still sharing a create input
// reconcile it. updateDeviceProfile is the documented exception — a profile rename is
// a real capability — and takes the RENAME rule instead, which refuses only a blank.
//
// The two rules themselves are exercised exhaustively in core
// (graphql.TestErrPayloadTokenDisagrees, TestErrRenameTokenUnusable). What is checked
// HERE is that each mutation is WIRED to one — which is a different claim, and the one
// that was missing: six of these families had the call and no test, so deleting it was
// invisible.

// ─── the reconciling families ──────────────────────────────────────────────

// tokenRuleFamily is one update still taking a *CreateRequest, described well enough
// to drive the rule against it. Adding a family is a row, exactly as with the partial
// harness — and a family CONVERTED to a dedicated *UpdateRequest leaves this list,
// because its input no longer has a token to disagree with.
type tokenRuleFamily struct {
	// name is the entity as the schema names it.
	name string
	// migrate lists the tables the fixture needs.
	migrate []any
	// seed creates two rows, "row-a" and "row-b", plus whatever they depend on. TWO
	// is load-bearing: with one row, "the other row was not written" is vacuous, and
	// a lookup falling back to "the only row there is" would pass.
	seed func(t *testing.T, api *Api, ctx context.Context)
	// update calls the real Api update with the given token argument and payload token.
	update func(api *Api, ctx context.Context, token, payloadToken string) error
	// tokenOf reports the token the named row currently stores, and whether it was
	// found at all — a row blanked by an empty payload token is found by NEITHER
	// token, which is the failure this exists to catch.
	tokenOf func(t *testing.T, api *Api, ctx context.Context, token string) (string, bool)
}

const (
	rowA = "row-a"
	rowB = "row-b"
)

func tokenRuleFamilies() []tokenRuleFamily {
	return []tokenRuleFamily{
		entityGroupTokenRule(),
		metricDefinitionTokenRule(),
		commandDefinitionTokenRule(),
		detectionRuleTokenRule(),
		deviceCredentialTokenRule(),
		provisioningProfileTokenRule(),
		entityRelationshipTypeTokenRule(),
	}
}

// A payload token naming a DIFFERENT row is refused, and neither row moves.
//
// This is the defect in its original form: the request named row-b, the argument
// named row-a, and the old code wrote row-b and returned it as though it were row-a.
func TestTokenRule_ADisagreeingPayloadTokenIsRefused(t *testing.T) {
	for _, fam := range tokenRuleFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)

			if err := fam.update(api, ctx, rowA, rowB); err == nil {
				t.Fatal("an update whose payload named a different row was accepted — the " +
					"token argument is decoration again")
			}
			for _, tok := range []string{rowA, rowB} {
				got, found := fam.tokenOf(t, api, ctx, tok)
				if !found {
					t.Errorf("%s no longer exists after a refused update", tok)
					continue
				}
				if got != tok {
					t.Errorf("the refused update moved %s to %q", tok, got)
				}
			}
		})
	}
}

// An EMPTY payload token is "unspecified" and must not blank the row.
//
// This is the OTHER half, and the one that shipped: the families that honoured the
// argument still assigned the payload token over the stored one, so a caller who left
// it out — legal, since `token: String!` admits "" — got a successful update and a row
// addressable by nothing.
func TestTokenRule_AnEmptyPayloadTokenDoesNotBlankTheRow(t *testing.T) {
	for _, fam := range tokenRuleFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)

			if err := fam.update(api, ctx, rowA, ""); err != nil {
				t.Fatalf("an update with no payload token was refused: %v", err)
			}
			got, found := fam.tokenOf(t, api, ctx, rowA)
			if !found {
				t.Fatal("the row is no longer findable by its own token — an empty payload " +
					"token blanked it, leaving it addressable by nothing")
			}
			if got != rowA {
				t.Fatalf("an empty payload token moved the row's token to %q", got)
			}
		})
	}
}

// THE COUNTERWEIGHT. Both refusals above are only meaningful while an AGREEING update
// still works. Without this, a guard tightened until every update failed would leave
// both tests passing for the wrong reason.
func TestTokenRule_AnAgreeingPayloadTokenStillUpdates(t *testing.T) {
	for _, fam := range tokenRuleFamilies() {
		t.Run(fam.name, func(t *testing.T) {
			api := newPartialUpdateApi(t, fam.migrate...)
			ctx := partialUpdateCtx()
			fam.seed(t, api, ctx)

			if err := fam.update(api, ctx, rowA, rowA); err != nil {
				t.Fatalf("an agreeing update was refused: %v", err)
			}
			got, found := fam.tokenOf(t, api, ctx, rowA)
			if !found || got != rowA {
				t.Fatalf("token = %q found = %v, want %q", got, found, rowA)
			}
		})
	}
}

// ─── the families ──────────────────────────────────────────────────────────

// seedProfileFor creates the device profile the profile-scoped families hang off.
func seedProfileFor(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: token}); err != nil {
		t.Fatalf("seed profile %q: %v", token, err)
	}
}

var profileTables = []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
	&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}}

func entityGroupTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "entityGroup",
		migrate: []any{&EntityGroup{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{rowA, rowB} {
				if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
					Token: tok, MemberType: "device", Name: strp("Original " + tok),
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateEntityGroup(ctx, token, &EntityGroupCreateRequest{
				Token: payload, MemberType: "device", Name: strp("Renamed"),
			})
			return err
		},
		tokenOf: groupTokenOf,
	}
}

func metricDefinitionTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "metricDefinition",
		migrate: profileTables,
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			seedProfileFor(t, api, ctx, "prof")
			for i, tok := range []string{rowA, rowB} {
				if _, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
					Token: tok, DeviceProfileToken: "prof",
					MetricKey: []string{"k-a", "k-b"}[i], DataType: string(MetricDouble),
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateMetricDefinition(ctx, token, &MetricDefinitionCreateRequest{
				Token: payload, DeviceProfileToken: "prof", MetricKey: "k-a",
				DataType: string(MetricDouble),
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.MetricDefinitionsByToken(ctx, []string{tok})
			return firstToken(t, "metric definition", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func commandDefinitionTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "commandDefinition",
		migrate: profileTables,
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			seedProfileFor(t, api, ctx, "prof")
			for i, tok := range []string{rowA, rowB} {
				if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
					Token: tok, DeviceProfileToken: "prof", CommandKey: []string{"c-a", "c-b"}[i],
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateCommandDefinition(ctx, token, &CommandDefinitionCreateRequest{
				Token: payload, DeviceProfileToken: "prof", CommandKey: "c-a",
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.CommandDefinitionsByToken(ctx, []string{tok})
			return firstToken(t, "command definition", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func detectionRuleTokenRule() tokenRuleFamily {
	const def = `{"when":{"all":[]}}`
	return tokenRuleFamily{
		name:    "detectionRule",
		migrate: profileTables,
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			seedProfileFor(t, api, ctx, "prof")
			for _, tok := range []string{rowA, rowB} {
				if _, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
					Token: tok, DeviceProfileToken: "prof", Definition: def, Enabled: true,
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateDetectionRule(ctx, token, &DetectionRuleCreateRequest{
				Token: payload, DeviceProfileToken: "prof", Definition: def, Enabled: true,
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.DetectionRulesByToken(ctx, []string{tok})
			return firstToken(t, "detection rule", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func deviceCredentialTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "deviceCredential",
		migrate: append(append([]any{}, profileTables...), &DeviceCredential{}),
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
				t.Fatalf("seed device type: %v", err)
			}
			if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{
				Token: "dev", DeviceTypeToken: "dt",
			}); err != nil {
				t.Fatalf("seed device: %v", err)
			}
			for i, tok := range []string{rowA, rowB} {
				if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
					Token: tok, DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
					CredentialId: []string{"id-a", "id-b"}[i], Enabled: true,
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateDeviceCredential(ctx, token, &DeviceCredentialCreateRequest{
				Token: payload, DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
				CredentialId: "id-a", Enabled: true,
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.DeviceCredentialsByToken(ctx, []string{tok})
			return firstToken(t, "device credential", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func provisioningProfileTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "provisioningProfile",
		migrate: append(append([]any{}, profileTables...), &ProvisioningProfile{}),
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
				t.Fatalf("seed device type: %v", err)
			}
			for i, tok := range []string{rowA, rowB} {
				if _, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
					Token: tok, ProvisionKey: []string{"pk-a", "pk-b"}[i], ProvisionSecret: "s3cret",
					Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateProvisioningProfile(ctx, token, &ProvisioningProfileCreateRequest{
				Token: payload, ProvisionKey: "pk-a", ProvisionSecret: "s3cret",
				Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt", Enabled: true,
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.ProvisioningProfilesByToken(ctx, []string{tok})
			return firstToken(t, "provisioning profile", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func entityRelationshipTypeTokenRule() tokenRuleFamily {
	return tokenRuleFamily{
		name:    "entityRelationshipType",
		migrate: []any{&EntityRelationshipType{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{rowA, rowB} {
				if _, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{
					Token: tok, Name: strp("Original " + tok),
				}); err != nil {
					t.Fatalf("seed %q: %v", tok, err)
				}
			}
		},
		update: func(api *Api, ctx context.Context, token, payload string) error {
			_, err := api.UpdateEntityRelationshipType(ctx, token, &EntityRelationshipTypeCreateRequest{
				Token: payload, Name: strp("Renamed"),
			})
			return err
		},
		tokenOf: func(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
			rows, err := api.EntityRelationshipTypesByToken(ctx, []string{tok})
			return firstToken(t, "entity relationship type", err, len(rows), func() string { return rows[0].Token })
		},
	}
}

func groupTokenOf(t *testing.T, api *Api, ctx context.Context, tok string) (string, bool) {
	rows, err := api.EntityGroupsByToken(ctx, []string{tok})
	return firstToken(t, "entity group", err, len(rows), func() string { return rows[0].Token })
}

// firstToken normalizes the "load by token" shape every family shares. A lookup
// ERROR is fatal — it means the fixture is broken, which is a different thing from
// the row not being there, and reporting it as "not found" would let a broken fixture
// masquerade as the defect under test.
func firstToken(t *testing.T, what string, err error, n int, read func() string) (string, bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("load %s: %v", what, err)
	}
	if n == 0 {
		return "", false
	}
	if n > 1 {
		t.Fatalf("expected at most one %s, got %d", what, n)
	}
	return read(), true
}

// ─── the rename exception ──────────────────────────────────────────────────

// updateDeviceProfile is the documented exception, and gets the RENAME rule: a
// differing payload token is the new name, and only a BLANK one is refused. Pinned
// beside the rule it departs from, since an exception nobody can find is one the next
// change deletes by accident.
//
// That a rename is refused once the profile is published or adopted is a separate,
// pre-existing guard with its own test (TestUpdateDeviceProfile_RejectsRenameAfterPublish).
func TestUpdateDeviceProfile_RefusesABlankPayloadToken(t *testing.T) {
	// 🔴 Whitespace is included because the token GRAMMAR does not catch it — this is
	// the fixture-weaker-than-production hole that let it through: "   " reached the
	// row and left the profile findable by nothing.
	for _, blank := range []string{"", "   ", "\t"} {
		t.Run("blank="+blank, func(t *testing.T) {
			api := newPartialUpdateApi(t, profileTables...)
			ctx := partialUpdateCtx()
			seedProfileFor(t, api, ctx, "prof")

			if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{
				Token: blank, Name: strp("Renamed"),
			}); err == nil {
				t.Fatalf("a blank payload token %q was accepted, which blanks the profile's token", blank)
			}
			rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
			p := requireOne(t, "device profile", rows, ferr)
			if p.Token != "prof" {
				t.Fatalf("token moved to %q", p.Token)
			}
		})
	}
}

// …and the counterweight: a real rename still works, so the refusal above has not
// been bought by removing the capability.
func TestUpdateDeviceProfile_ADifferingTokenStillRenames(t *testing.T) {
	api := newPartialUpdateApi(t, profileTables...)
	ctx := partialUpdateCtx()
	seedProfileFor(t, api, ctx, "prof")

	if _, err := api.UpdateDeviceProfile(ctx, "prof", &DeviceProfileCreateRequest{
		Token: "prof2", Name: strp("Renamed"),
	}); err != nil {
		t.Fatalf("a rename was refused: %v", err)
	}
	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof2"})
	if requireOne(t, "device profile", rows, ferr).Token != "prof2" {
		t.Fatal("the rename did not take")
	}
}
