// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// deviceLocationDeclaration(deviceToken) answers "does THIS device report its
// position?" through the profile version the device ACTUALLY RESOLVES (ADR-078).
//
// Every test here is driven through the REAL schema with REAL variables against a
// REAL sqlite-backed Api — not by calling the resolver method — because the property
// under test is a resolution CHAIN (device → type → profile → active published
// version → declaration), and a test that hands the resolver a pre-resolved answer
// would be measuring its own fixture. The one thing most likely to be built wrong is
// reading the editable draft instead of the published version, and only driving the
// whole chain can tell those apart.

func deviceLocationCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.MetricDefinition{}, &model.CommandDefinition{},
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	// device:read is the gate, and it is in the read-only viewer baseline — this is
	// profile metadata, not position data.
	ctx = withAuthorities(ctx, auth.DeviceRead, auth.DeviceWrite)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// apiOf pulls the real Api back out of the context the resolvers read it from, so a
// test seeds through exactly the code path the query then resolves against.
func apiOf(ctx context.Context) *model.Api {
	return ctx.Value(gqlcore.ContextApiKey).(*model.Api)
}

const deviceLocationQuery = `
query ($deviceToken: String!) {
  deviceLocationDeclaration(deviceToken: $deviceToken) {
    declared
    declaration { expectedAccuracyMeters expectedUpdateIntervalSeconds }
  }
}`

// declarationAnswer is the parsed response, keeping the THREE distinct answers apart:
// a null capability (no such device), declared=false (a real device that declares
// nothing), and declared=true with a possibly-empty declaration object.
type declarationAnswer struct {
	capabilityPresent  bool
	declared           bool
	declarationPresent bool
	accuracy           *float64
	interval           *int32
}

// askDeclaration runs the query and fails the test on any GraphQL error, so an
// errored response can never be mistaken for a null capability.
func askDeclaration(t *testing.T, ctx context.Context, deviceToken string) declarationAnswer {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, deviceLocationQuery, "", map[string]any{"deviceToken": deviceToken})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors: %v", resp.Errors)
	}
	var parsed struct {
		Capability *struct {
			Declared    bool `json:"declared"`
			Declaration *struct {
				ExpectedAccuracyMeters        *float64 `json:"expectedAccuracyMeters"`
				ExpectedUpdateIntervalSeconds *int32   `json:"expectedUpdateIntervalSeconds"`
			} `json:"declaration"`
		} `json:"deviceLocationDeclaration"`
	}
	if err := json.Unmarshal(resp.Data, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if parsed.Capability == nil {
		return declarationAnswer{}
	}
	answer := declarationAnswer{capabilityPresent: true, declared: parsed.Capability.Declared}
	if decl := parsed.Capability.Declaration; decl != nil {
		answer.declarationPresent = true
		answer.accuracy = decl.ExpectedAccuracyMeters
		answer.interval = decl.ExpectedUpdateIntervalSeconds
	}
	return answer
}

// seedDevice writes the whole chain a resolution walks — profile (optionally with a
// draft declaration), a type adopting it, and a device of that type — and returns the
// profile token so the caller can publish or edit the draft afterwards.
func seedDevice(t *testing.T, ctx context.Context, deviceToken, profileToken string,
	draft *model.LocationDeclaration) {
	t.Helper()
	api := apiOf(ctx)

	var profileId *uint
	if profileToken != "" {
		profile, err := api.CreateDeviceProfile(ctx, &model.DeviceProfileCreateRequest{
			Token: profileToken, Location: draft,
		})
		if err != nil {
			t.Fatalf("create profile: %v", err)
		}
		profileId = &profile.ID
	}

	deviceType := &model.DeviceType{}
	deviceType.Token = deviceToken + "-type"
	deviceType.ProfileId = profileId
	if err := api.RDB.DB(ctx).Create(deviceType).Error; err != nil {
		t.Fatalf("create device type: %v", err)
	}

	device := &model.Device{DeviceTypeId: deviceType.ID}
	device.Token = deviceToken
	if err := api.RDB.DB(ctx).Create(device).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}
}

func publishProfile(t *testing.T, ctx context.Context, profileToken string) {
	t.Helper()
	api := apiOf(ctx)
	if _, err := api.PublishDeviceProfile(ctx, profileToken, nil, nil, "tester"); err != nil {
		t.Fatalf("publish profile %q: %v", profileToken, err)
	}
}

// editDraftDeclaration replaces the profile's draft declaration. A nil draft is an
// EXPLICIT CLEAR rather than an omission: under the three-state update input, omitting
// the field preserves the stored declaration, so the pointer's nil has to be spelled as
// the clear it means here.
func editDraftDeclaration(t *testing.T, ctx context.Context, profileToken string,
	draft *model.LocationDeclaration) {
	t.Helper()
	api := apiOf(ctx)
	location := model.ClearedLocationDeclaration()
	if draft != nil {
		location = model.OptionalLocationDeclarationOf(*draft)
	}
	if _, err := api.UpdateDeviceProfile(ctx, profileToken, &model.DeviceProfileUpdateRequest{
		Location: location,
	}); err != nil {
		t.Fatalf("update profile %q: %v", profileToken, err)
	}
}

func floatOf(v float64) *float64 { return &v }
func int32Of(v int32) *int32     { return &v }

// shown formats an optional expectation for a failure message. Without it `%v` on a
// pointer prints an ADDRESS, which turns "the draft was read" into an unreadable hex
// number at exactly the moment the message has to name what went wrong.
func shown[T any](v *T) string {
	if v == nil {
		return "absent"
	}
	return fmt.Sprintf("%v", *v)
}

// 🔴 THE ONE THAT MATTERS. A device resolves the ACTIVE PUBLISHED version, so an edit
// to the draft must not move the answer until it is published. The published and draft
// values are deliberately distinct and non-round: a resolver that read the draft would
// return 9.5 here and the assertion names the number, so the failure says which side of
// the publish boundary was read rather than merely "wrong".
func TestDeviceLocationDeclarationResolvesPublishedNotDraft(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "excavator-7", "tracker",
		&model.LocationDeclaration{
			ExpectedAccuracyMeters:        floatOf(2.5),
			ExpectedUpdateIntervalSeconds: int32Of(30),
		})
	publishProfile(t, ctx, "tracker")

	// Now edit the DRAFT to something different, and do not publish it.
	editDraftDeclaration(t, ctx, "tracker",
		&model.LocationDeclaration{
			ExpectedAccuracyMeters:        floatOf(9.5),
			ExpectedUpdateIntervalSeconds: int32Of(600),
		})

	answer := askDeclaration(t, ctx, "excavator-7")
	if !answer.capabilityPresent {
		t.Fatal("a live device answered null — the token did not resolve at all")
	}
	if !answer.declared || !answer.declarationPresent {
		t.Fatal("a published declaration did not resolve: the device reads as undeclared")
	}
	if answer.accuracy == nil || *answer.accuracy != 2.5 {
		t.Errorf("expectedAccuracyMeters = %s, want 2.5 (9.5 means the DRAFT was read)", shown(answer.accuracy))
	}
	if answer.interval == nil || *answer.interval != 30 {
		t.Errorf("expectedUpdateIntervalSeconds = %s, want 30 (600 means the DRAFT was read)", shown(answer.interval))
	}
}

// The other half of the publish boundary, and the case where reading the draft looks
// most convincingly correct: a declaration authored but NEVER published. The draft
// says "reports position"; the device resolves nothing, so the answer is undeclared.
func TestDeviceLocationDeclarationIgnoresUnpublishedDraft(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "excavator-7", "tracker",
		&model.LocationDeclaration{ExpectedAccuracyMeters: floatOf(2.5)})

	answer := askDeclaration(t, ctx, "excavator-7")
	if !answer.capabilityPresent {
		t.Fatal("a live device answered null")
	}
	if answer.declared || answer.declarationPresent {
		t.Error("an unpublished draft declaration resolved — a device resolves the ACTIVE published version")
	}
}

// 🔴 Declared-but-empty is a DIFFERENT claim from absent, and both survive the whole
// chain. "Reports position, nothing stated about it" must not collapse into "does not
// report position": a console showing a panel for the first and hiding it for the
// second would show the wrong thing for every device whose profile states no
// expectations, which is the common case.
func TestDeviceLocationDeclarationDistinguishesDeclaredEmptyFromAbsent(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "declared-empty-device", "declared-empty", &model.LocationDeclaration{})
	publishProfile(t, ctx, "declared-empty")
	seedDevice(t, ctx, "silent-device", "silent", nil)
	publishProfile(t, ctx, "silent")

	declaredEmpty := askDeclaration(t, ctx, "declared-empty-device")
	if !declaredEmpty.declared {
		t.Error("a declared-but-unconfigured location answered declared=false — the declaration itself was lost")
	}
	if !declaredEmpty.declarationPresent {
		t.Fatal("a declared-but-unconfigured location answered a null declaration object")
	}
	if declaredEmpty.accuracy != nil || declaredEmpty.interval != nil {
		t.Errorf("declared-empty gained values from nowhere: accuracy=%s interval=%s",
			shown(declaredEmpty.accuracy), shown(declaredEmpty.interval))
	}

	absent := askDeclaration(t, ctx, "silent-device")
	if absent.declared || absent.declarationPresent {
		t.Error("a profile declaring no location answered declared=true")
	}

	// Stated as the comparison a consumer actually makes.
	if declaredEmpty.declared == absent.declared {
		t.Error("declared-but-empty and undeclared answered the same question the same way")
	}
}

// An unresolvable token is null, not an error — a saved view outlives its device, and
// erroring on a non-null field would null out every sibling in a batched document.
// 🔴 And that null stays DISTINCT from a real device that declares nothing, which the
// second half asserts: if both were null the client could not tell "gone" from "does
// not report position".
func TestDeviceLocationDeclarationUnknownDeviceIsNullNotError(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "silent-device", "silent", nil)
	publishProfile(t, ctx, "silent")

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, deviceLocationQuery, "", map[string]any{"deviceToken": "no-such-device"})
	if len(resp.Errors) > 0 {
		t.Fatalf("an unresolvable token errored instead of answering null: %v", resp.Errors)
	}
	if unknown := askDeclaration(t, ctx, "no-such-device"); unknown.capabilityPresent {
		t.Error("an unresolvable token answered a capability object rather than null")
	}

	existing := askDeclaration(t, ctx, "silent-device")
	if !existing.capabilityPresent {
		t.Error("a real device that declares nothing answered null — indistinguishable from a deleted device")
	}
}

// A type with no profile, and a profile that was never published, both answer
// "undeclared" without error — the same limiting case an empty capability set has.
func TestDeviceLocationDeclarationNoProfileOrUnpublishedIsUndeclared(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "orphan-device", "", nil)
	seedDevice(t, ctx, "unpublished-device", "never-published", nil)

	orphan := askDeclaration(t, ctx, "orphan-device")
	if !orphan.capabilityPresent {
		t.Fatal("a device whose type has no profile answered null — it exists")
	}
	if orphan.declared {
		t.Error("a device whose type has no profile answered declared=true")
	}

	unpublished := askDeclaration(t, ctx, "unpublished-device")
	if !unpublished.capabilityPresent {
		t.Fatal("a device whose profile is unpublished answered null — it exists")
	}
	if unpublished.declared {
		t.Error("a device whose profile was never published answered declared=true")
	}
}

// Gated on device:read. The read-only viewer baseline carries it, which is the point:
// this is profile metadata, so a user who may not see coordinates can still be told
// the panel is irrelevant to this device.
func TestDeviceLocationDeclarationRequiresDeviceRead(t *testing.T) {
	ctx := deviceLocationCtx(t)
	seedDevice(t, ctx, "excavator-7", "tracker", &model.LocationDeclaration{})
	publishProfile(t, ctx, "tracker")

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	// No claims at all — an unauthenticated request — is refused.
	anonymous := context.WithValue(context.Background(), gqlcore.ContextApiKey, apiOf(ctx))
	anonymous = core.WithTenant(anonymous, "acme")
	resp := schema.Exec(anonymous, deviceLocationQuery, "", map[string]any{"deviceToken": "excavator-7"})
	if len(resp.Errors) == 0 {
		t.Error("a caller with no authorities read a device's location declaration")
	}

	// A caller holding authorities but NOT device:read is refused too — so the gate is
	// device:read specifically, not merely "is authenticated".
	withoutDeviceRead := withAuthorities(ctx, auth.EventRead, auth.StateRead, auth.LocationRead)
	resp = schema.Exec(withoutDeviceRead, deviceLocationQuery, "", map[string]any{"deviceToken": "excavator-7"})
	if len(resp.Errors) == 0 {
		t.Error("a caller without device:read read a device's location declaration")
	}

	// The counterweight: the read-only viewer baseline — which does NOT include
	// location:read — is allowed. Gating this on location:read would break exactly here.
	viewer := withAuthorities(ctx, viewerBaseline...)
	if answer := askDeclaration(t, viewer, "excavator-7"); !answer.declared {
		t.Error("the read-only viewer baseline could not read a device's location declaration")
	}
}
