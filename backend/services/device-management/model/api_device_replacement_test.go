// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// replacementTestApi builds the sqlite-backed Api these tests run against.
//
// 🔴 IT REGISTERS THE TOKEN GRAMMAR, WHICH THE REST OF THIS PACKAGE'S HARNESSES DO
// NOT. rdb.RegisterTokenGrammar installs the create/update callbacks that reject a
// malformed or empty token, and production registers them unconditionally
// (core/rdb/postgres.go). Every other service's model tests register them; this
// package's do not, so a token this fixture accepts could be refused by the running
// service — a fixture more permissive than production lets exactly that class of
// defect through green. Registering it here is not belt-and-braces: ReplaceDevice
// MINTS a credential token, and a fixture with no grammar could not tell a valid
// minted token from an invalid one.
//
// It also creates the ADR-014 credential-resolve unique index by hand. AutoMigrate
// cannot express a partial unique index, so without this the sqlite fixture would
// admit two live credentials sharing (tenant, type, id) — which production refuses,
// and which one of these tests depends on it refusing.
//
// 🔴 EVERY CONSTRAINT THIS FIXTURE TURNS ON WAS A MEASUREMENT, NOT A FORMALITY, AND
// THE COUNT IS THE ARGUMENT. Enabling foreign keys below immediately failed TWO
// seeds in this very file that had been writing dangling references (a bare
// Create(&Asset{}) leaving asset_type_id = 0), and broke FIVE pre-existing tests in
// api_delete_emit_test.go and api_group_scoping_test.go whose fixtures lacked a
// table the cascade touches. Every one of those was a real defect the permissive
// fixture had been hiding — including the missing device_replacements cascade,
// which made any replaced device permanently undeletable on Postgres.
//
// The rest of this package still runs with foreign keys OFF and without the token
// grammar. That is a standing gap, not a settled decision: each fixture that opts in
// is likely to surface more of the same, and the honest expectation is that it will
// cost a few test fixes each time rather than none.
func replacementTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, rdb.RegisterTokenGrammar(db), "register token grammar")
	// Everything DeleteDevice's cascade touches, not only what a replacement writes.
	// deleteEdgeEntity removes relationship edges, entity attributes, alarms and group
	// memberships before the cascade closure runs, so a fixture missing any of them
	// fails the delete for the wrong reason and hides the one under test.
	require.NoError(t, db.AutoMigrate(
		&DeviceType{}, &Device{}, &DeviceCredential{}, &DeviceReplacement{},
		&AssetType{}, &Asset{},
		&EntityRelationshipType{}, &EntityRelationship{},
		&EntityAttribute{}, &Alarm{}, &EntityGroup{}, &EntityGroupMembership{},
	), "migrate")
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX idx_device_credential_lookup ON device_credentials `+
			`(tenant_id, credential_type, credential_id) WHERE deleted_at IS NULL`).Error,
		"create credential-resolve unique index")

	// 🔴 FOREIGN KEYS ON, AND THIS ONE LINE IS THE DIFFERENCE BETWEEN THE HARNESS AND
	// PRODUCTION. sqlite enforces no foreign key unless asked, and every fixture in
	// this package leaves it off — so a cascade that forgets a child table passes here
	// and fails on Postgres with a raw constraint error. That is exactly how the
	// device_replacements FK escaped DeleteDevice's cascade: the delete "worked" in
	// every test and would have made any replaced device permanently undeletable.
	//
	// It has to be set AFTER AutoMigrate: gorm creates the tables in dependency order,
	// but turning enforcement on first would make any ordering slip a migrate failure
	// rather than the thing under test.
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error, "enable foreign keys")
	var fk int
	require.NoError(t, db.Raw(`PRAGMA foreign_keys`).Scan(&fk).Error, "read foreign_keys pragma")
	require.Equal(t, 1, fk,
		"foreign keys are still off; this fixture would not see a missing cascade")

	api := NewApi(&rdb.RdbManager{Database: db})
	return api, core.WithTenant(context.Background(), "acme")
}

// seedReplacementDevice creates one device of one type and returns it.
func seedReplacementDevice(t *testing.T, api *Api, ctx context.Context, token string) *Device {
	t.Helper()

	deviceType := &DeviceType{}
	deviceType.Token = token + "-type"
	require.NoError(t, api.RDB.DB(ctx).Create(deviceType).Error, "seed device type")

	device, err := api.CreateDevice(ctx, &DeviceCreateRequest{
		Token:           token,
		DeviceTypeToken: deviceType.Token,
	})
	require.NoError(t, err, "seed device")
	return device
}

// seedCredential writes one credential for a device.
func seedCredential(t *testing.T, api *Api, ctx context.Context,
	deviceToken, token, credentialId string, enabled bool, expiresAt *time.Time) *DeviceCredential {
	t.Helper()

	request := &DeviceCredentialCreateRequest{
		Token:          token,
		DeviceToken:    deviceToken,
		CredentialType: string(CredentialAccessToken),
		CredentialId:   credentialId,
		Enabled:        enabled,
	}
	if expiresAt != nil {
		formatted := expiresAt.Format(time.RFC3339)
		request.ExpiresAt = &formatted
	}
	created, err := api.CreateDeviceCredential(ctx, request)
	require.NoError(t, err, "seed credential %q", token)
	return created
}

// The retirement is the security-critical half, and this asserts it at the door the
// property actually lives behind: DeviceCredentialByCredentialId is what every
// transport resolves a presented credential through, so "the old unit can no longer
// authenticate" means that call stops finding it. Asserting Enabled == false would
// be asserting the mechanism; this asserts the consequence.
//
// The input class is three live credentials that differ in the ways the code branches
// on — one never-expiring, one with runway, one already EXPIRED but still enabled.
// The expired one matters: ReplaceDevice retires on `enabled`, not on expiry, and a
// filter that skipped it (as mintOrReuseCredential's reuse loop deliberately does)
// would leave an enabled row behind.
func TestReplaceDeviceRetiresEveryLiveCredential(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")

	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1000 * time.Hour)
	seedCredential(t, api, ctx, device.Token, "cred-never", "bearer-never", true, nil)
	seedCredential(t, api, ctx, device.Token, "cred-future", "bearer-future", true, &future)
	seedCredential(t, api, ctx, device.Token, "cred-expired", "bearer-expired", true, &past)

	result, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace device")

	for _, bearer := range []string{"bearer-never", "bearer-future", "bearer-expired"} {
		_, err := api.DeviceCredentialByCredentialId(ctx, string(CredentialAccessToken), bearer)
		require.ErrorIs(t, err, gorm.ErrRecordNotFound,
			"retired bearer %q still resolves: the outgoing unit can still authenticate", bearer)
	}

	// The counterweight — a replacement that disabled everything and minted nothing
	// would pass every assertion above while leaving the device unable to speak.
	resolved, err := api.DeviceCredentialByCredentialId(ctx,
		string(CredentialAccessToken), result.NewCredential.CredentialId)
	require.NoError(t, err, "the incoming unit's credential does not resolve")
	require.Equal(t, device.ID, resolved.DeviceId, "new credential bound to the wrong device")
}

// Identity, the business key and the profile binding survive the swap — this is the
// property ADR-074 exists to deliver, and the reason history carries forward at all:
// events, alarms, relationships and group memberships key on the device row, so
// nothing has to be moved as long as nothing here moves.
func TestReplaceDevicePreservesIdentity(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")

	externalId := "VIN-1HGCM82633A004352"
	name := "North pit excavator"
	metadata := `{"site":"north-pit"}`
	updated, err := api.UpdateDevice(ctx, device.Token, &DeviceUpdateRequest{
		ExternalId: dcgraphql.OptionalStringOf(externalId),
		Name:       dcgraphql.OptionalStringOf(name),
		Metadata:   dcgraphql.OptionalStringOf(metadata),
	})
	require.NoError(t, err, "seed device identity")

	// A tracked relationship edge: the anchor machinery that gives this device's
	// telemetry its permanent attribution. It must survive the swap untouched.
	assignment, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err, "ensure assignment type")
	// Through the real API, with a real asset type: a bare Create leaves
	// asset_type_id = 0, which is a dangling foreign key that only sqlite-with-FKs-off
	// tolerates.
	assetType := &AssetType{}
	assetType.Token = "crusher-type"
	require.NoError(t, api.RDB.DB(ctx).Create(assetType).Error, "seed asset type")
	asset, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token:          "crusher-07",
		AssetTypeToken: assetType.Token,
	})
	require.NoError(t, err, "seed asset")
	edge, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{
		Token:            "dozer-01-assigned",
		SourceType:       "device",
		Source:           device.Token,
		TargetType:       "asset",
		Target:           asset.Token,
		RelationshipType: assignment.Token,
	})
	require.NoError(t, err, "seed assignment edge")

	seedCredential(t, api, ctx, device.Token, "cred-old", "bearer-old", true, nil)

	_, err = api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace device")

	after, err := api.DevicesByToken(ctx, []string{device.Token})
	require.NoError(t, err, "reload device")
	require.Len(t, after, 1, "the device token no longer resolves")
	require.Equal(t, updated.ID, after[0].ID, "the device row id moved")
	require.Equal(t, externalId, after[0].ExternalId.String, "externalId moved")
	require.Equal(t, name, after[0].Name.String, "name moved")
	require.Equal(t, updated.DeviceTypeId, after[0].DeviceTypeId, "device type binding moved")
	require.JSONEq(t, metadata, string(*after[0].Metadata), "metadata moved")

	edges, err := api.EntityRelationshipsByToken(ctx, []string{edge.Token})
	require.NoError(t, err, "reload assignment edge")
	require.Len(t, edges, 1, "the assignment edge did not survive the replacement")
	require.Equal(t, updated.ID, edges[0].SourceId, "the assignment edge no longer points at the device")
}

// The journal record says who, when, why, what was retired and what replaced it —
// and the "when" and "who" are the values the API was GIVEN, not values a caller
// could have put in the request.
func TestReplaceDeviceWritesTheJournalRecord(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")
	old := seedCredential(t, api, ctx, device.Token, "cred-old", "bearer-old", true, nil)

	reason := "water ingress"
	unit := "SN-88213"
	at := time.Date(2026, 3, 4, 15, 4, 5, 0, time.UTC)

	result, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{
		DeviceToken:    device.Token,
		Reason:         &reason,
		UnitIdentifier: &unit,
	}, "tech@acme.example", at)
	require.NoError(t, err, "replace device")

	record := result.Replacement
	require.NotZero(t, record.ID, "no replacement row was written")
	require.Equal(t, device.ID, record.DeviceId, "record names the wrong device")
	require.True(t, at.Equal(record.OccurredTime), "occurredTime = %v, want the supplied instant %v",
		record.OccurredTime, at)
	require.Equal(t, "tech@acme.example", record.Actor, "actor not recorded")
	require.Equal(t, reason, record.Reason.String, "reason not recorded")
	require.Equal(t, unit, record.UnitIdentifier.String, "unit identifier not recorded")
	require.Equal(t, []string{old.Token}, record.RetiredCredentialTokenList(),
		"the record's retired list disagrees with what was retired")
	require.Equal(t, result.NewCredential.Token, record.NewCredentialToken, "new credential token not recorded")
	require.Equal(t, string(CredentialAccessToken), record.NewCredentialType, "new credential type not recorded")

	// The record names credentials by ENTITY TOKEN and never by credential id, which
	// for an ACCESS_TOKEN is the bearer. A record that leaked one would turn a
	// device:read journal into a credential read.
	require.NotContains(t, string(record.RetiredCredentialTokens), old.CredentialId,
		"the retired list carries a bearer, not an entity token")
	require.NotEqual(t, result.NewCredential.CredentialId, record.NewCredentialToken,
		"the record stores the new bearer as its credential token")

	// It is readable back through the query door, which is what "auditable" means.
	found, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
		Device:     &device.Token,
	})
	require.NoError(t, err, "query replacements")
	require.Len(t, found.Results, 1, "the replacement is not queryable")
	require.Equal(t, record.ID, found.Results[0].ID, "the wrong replacement came back")
	require.NotNil(t, found.Results[0].Device, "the device was not preloaded")
	require.Equal(t, device.Token, found.Results[0].Device.Token, "preloaded the wrong device")
}

// Replacing a device that holds no live credential SUCCEEDS and retires nothing. A
// unit that died before it ever provisioned is precisely a case the operation exists
// for, so refusing it would be backwards — and the empty list must be an empty JSON
// array rather than a null the reader has to special-case.
func TestReplaceDeviceWithNoLiveCredentials(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")

	result, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace a device with no credentials")
	require.Empty(t, result.RetiredCredentials, "retired something that was not there")
	require.Equal(t, []string{}, result.Replacement.RetiredCredentialTokenList(),
		"empty retired list did not round-trip as an empty array")
	require.NotNil(t, result.NewCredential, "no credential minted")
}

// A device that has been replaced can still be DELETED, and its journal goes with it.
//
// 🔴 THE REGRESSION THIS PINS WAS INVISIBLE TO EVERY OTHER TEST HERE. device_replacements
// carries a foreign key to devices, so once a device has one row of history the parent
// delete is refused by the database — the device becomes permanently undeletable, and the
// caller sees a raw constraint error rather than ErrEntityInUse. It only shows up with
// foreign keys enforced, which is why replacementTestApi turns them on.
//
// The control matters as much as the case: a NEVER-replaced device must delete too, or a
// cascade that simply refused everything would satisfy the first half.
func TestDeleteDeviceRemovesItsReplacementJournal(t *testing.T) {
	api, ctx := replacementTestApi(t)
	replaced := seedReplacementDevice(t, api, ctx, "dozer-01")
	control := seedReplacementDevice(t, api, ctx, "dozer-02")
	seedCredential(t, api, ctx, replaced.Token, "cred-old", "bearer-old", true, nil)

	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: replaced.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace device")

	removed, err := api.DeleteDevice(ctx, replaced.Token)
	require.NoError(t, err, "a replaced device could not be deleted")
	require.True(t, removed, "delete reported no change")

	// The journal went with it rather than being orphaned onto a device id that no
	// longer resolves.
	found, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err, "query replacements")
	require.Empty(t, found.Results, "the deleted device left its replacement journal behind")

	// The control: an unreplaced device still deletes.
	removed, err = api.DeleteDevice(ctx, control.Token)
	require.NoError(t, err, "an unreplaced device could not be deleted")
	require.True(t, removed, "control delete reported no change")
}

// RetiredCredentialTokenList answers an EMPTY SLICE for a row whose column holds
// nothing, and for one holding bytes it cannot decode.
//
// 🔴 THIS COVERS AN INPUT CLASS NOTHING ELSE IN THIS FILE CAN REACH, and it was found
// by a surviving mutant rather than by reading the code. Every other test here works
// through ReplaceDevice, which always writes at least `[]` — two bytes — so the
// `len(…) == 0` guard is UNREACHABLE from all of them. Replacing its `return tokens`
// with `return nil` left the whole estate green.
//
// The distinction is not cosmetic. The SDL field is `retiredCredentialTokens:
// [String!]!` — non-null — so a nil here renders null for a non-null field and errors
// the entire query, not just that field. The rows that can carry a zero-length column
// are the ones this package did not write: a row restored from elsewhere, or one read
// back before the column existed.
func TestRetiredCredentialTokenListIsAlwaysASlice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored datatypes.JSON
	}{
		{"zero value", nil},
		{"empty bytes", datatypes.JSON{}},
		{"sql null", datatypes.JSON("null")},
		{"not an array", datatypes.JSON(`{"nope":1}`)},
		{"malformed", datatypes.JSON("[")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := DeviceReplacement{RetiredCredentialTokens: tc.stored}
			got := record.RetiredCredentialTokenList()
			require.NotNil(t, got,
				"a %s column decoded to nil; the non-null SDL field would render null and "+
					"error the whole query", tc.name)
			require.Empty(t, got, "a %s column decoded to something", tc.name)
		})
	}

	// The counterweight: a real array still decodes, in order. A reader that answered
	// the empty slice unconditionally would pass every assertion above.
	record := DeviceReplacement{RetiredCredentialTokens: datatypes.JSON(`["a","b"]`)}
	require.Equal(t, []string{"a", "b"}, record.RetiredCredentialTokenList(),
		"a populated retired list did not decode")
}

// An ALREADY DISABLED credential is not claimed as retired by this replacement. The
// record has to be honest about what THIS swap did: over-claiming attributes an
// earlier rotation to the wrong operator and the wrong date.
func TestReplaceDeviceDoesNotClaimAlreadyDisabledCredentials(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")
	seedCredential(t, api, ctx, device.Token, "cred-dead", "bearer-dead", false, nil)
	live := seedCredential(t, api, ctx, device.Token, "cred-live", "bearer-live", true, nil)

	result, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace device")
	require.Equal(t, []string{live.Token}, result.Replacement.RetiredCredentialTokenList(),
		"the record claims a credential this replacement did not retire")
}

// A sibling device's credentials are untouched. The retirement is scoped by
// device_id; a predicate that lost that clause would silently disable a whole
// tenant's fleet while every assertion about the target device still passed.
func TestReplaceDeviceLeavesOtherDevicesAlone(t *testing.T) {
	api, ctx := replacementTestApi(t)
	target := seedReplacementDevice(t, api, ctx, "dozer-01")
	bystander := seedReplacementDevice(t, api, ctx, "dozer-02")
	seedCredential(t, api, ctx, target.Token, "cred-target", "bearer-target", true, nil)
	seedCredential(t, api, ctx, bystander.Token, "cred-bystander", "bearer-bystander", true, nil)

	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: target.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace device")

	resolved, err := api.DeviceCredentialByCredentialId(ctx,
		string(CredentialAccessToken), "bearer-bystander")
	require.NoError(t, err, "a bystander device's credential was retired")
	require.Equal(t, bystander.ID, resolved.DeviceId, "resolved the wrong device")
}

// The retirement and the mint are ONE transaction, proved by making the mint fail
// after the retirement has been written.
//
// The failure is a real one, not an injected fault: the ADR-014 unique index over
// (tenant_id, credential_type, credential_id) does not consider `enabled`, so
// re-using the outgoing unit's credential id is refused by the database. Without the
// transaction the device would be left with every credential disabled and no
// replacement — unreachable, and reported as an error the operator would reasonably
// retry into the same state.
func TestReplaceDeviceRollsBackTheRetirementWhenTheMintFails(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")
	seedCredential(t, api, ctx, device.Token, "cred-old", "bearer-old", true, nil)

	colliding := "bearer-old"
	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{
		DeviceToken:  device.Token,
		CredentialId: &colliding,
	}, "tech@acme.example", time.Now())
	require.Error(t, err, "reusing the outgoing unit's credential id was accepted")

	resolved, err := api.DeviceCredentialByCredentialId(ctx, string(CredentialAccessToken), "bearer-old")
	require.NoError(t, err,
		"the failed replacement left the device with no live credential: the retirement did not roll back")
	require.Equal(t, device.ID, resolved.DeviceId, "resolved the wrong device")

	replacements, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
		Device:     &device.Token,
	})
	require.NoError(t, err, "query replacements")
	require.Empty(t, replacements.Results, "a failed replacement still wrote a journal record")
}

// A type whose credential id the server cannot invent is refused, by a sentinel, and
// nothing is retired. Minting a random id for an X.509 credential would produce a
// credential that authenticates nothing while reporting success — a plausible value
// where an error belongs.
func TestReplaceDeviceRequiresACredentialIdForUnmintableTypes(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")
	seedCredential(t, api, ctx, device.Token, "cred-old", "bearer-old", true, nil)

	for _, ctype := range []CredentialType{CredentialX509Certificate, CredentialMqttBasic} {
		name := string(ctype)
		_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{
			DeviceToken:    device.Token,
			CredentialType: &name,
		}, "tech@acme.example", time.Now())
		require.ErrorIs(t, err, ErrCredentialIdRequired, "%s without an id was accepted", ctype)
	}

	// Nothing was retired by the refusals.
	_, err := api.DeviceCredentialByCredentialId(ctx, string(CredentialAccessToken), "bearer-old")
	require.NoError(t, err, "a refused replacement still retired the device's credential")

	// The counterweight: WITH an id, the same type succeeds. A blanket refusal of
	// everything but ACCESS_TOKEN would pass the assertions above.
	thumbprint := "AA:BB:CC:DD"
	x509 := string(CredentialX509Certificate)
	result, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{
		DeviceToken:    device.Token,
		CredentialType: &x509,
		CredentialId:   &thumbprint,
	}, "tech@acme.example", time.Now())
	require.NoError(t, err, "an X509 replacement carrying a thumbprint was refused")
	require.Equal(t, thumbprint, result.NewCredential.CredentialId, "the supplied thumbprint was not used")
	require.Equal(t, x509, result.Replacement.NewCredentialType, "the record names the wrong credential type")
}

// An unknown credential type is reported as an unknown TYPE, not as a missing id.
// Both are refusals, and telling an operator who mistyped "ACCES_TOKEN" to supply a
// credential id sends them to fix the wrong field.
func TestReplaceDeviceRejectsAnUnknownCredentialType(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")

	bogus := "ACCES_TOKEN"
	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{
		DeviceToken:    device.Token,
		CredentialType: &bogus,
	}, "tech@acme.example", time.Now())
	require.Error(t, err, "an unknown credential type was accepted")
	require.False(t, errors.Is(err, ErrCredentialIdRequired),
		"a mistyped credential type was reported as a missing credential id")
	require.Contains(t, err.Error(), "invalid credential type", "unexpected error: %v", err)
}

// An unknown device token is refused before anything is written.
func TestReplaceDeviceRefusesAnUnknownDevice(t *testing.T) {
	api, ctx := replacementTestApi(t)

	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: "no-such-device"},
		"tech@acme.example", time.Now())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "an unknown device token was accepted")
}

// Two successive replacements each retire the previous unit's credential and mint a
// distinct one, and the journal reads newest-first.
//
// This is the input class a single-replacement test cannot reach: it is the second
// call that proves the retirement query reads the CURRENT credential set rather than
// the set that existed when the device was created.
func TestSuccessiveReplacementsChainAndReadNewestFirst(t *testing.T) {
	api, ctx := replacementTestApi(t)
	device := seedReplacementDevice(t, api, ctx, "dozer-01")
	seedCredential(t, api, ctx, device.Token, "cred-gen0", "bearer-gen0", true, nil)

	first, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech-a@acme.example", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err, "first replacement")

	second, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token},
		"tech-b@acme.example", time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err, "second replacement")

	require.NotEqual(t, first.NewCredential.CredentialId, second.NewCredential.CredentialId,
		"the second replacement handed back the first unit's bearer")
	require.Equal(t, []string{first.NewCredential.Token},
		second.Replacement.RetiredCredentialTokenList(),
		"the second replacement did not retire the credential the first one minted")

	_, err = api.DeviceCredentialByCredentialId(ctx,
		string(CredentialAccessToken), first.NewCredential.CredentialId)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound,
		"the first replacement's unit can still authenticate after the second swap")

	found, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
		Device:     &device.Token,
	})
	require.NoError(t, err, "query replacements")
	require.Len(t, found.Results, 2, "expected both replacements in the journal")
	require.Equal(t, second.Replacement.ID, found.Results[0].ID, "the journal did not lead with the newest")
	require.Equal(t, "tech-b@acme.example", found.Results[0].Actor, "wrong actor on the newest record")
}

// The device filter narrows: another device's replacements are not returned. Without
// the filter the journal would answer "what happened to this device" with the whole
// fleet's history, which reads as a correct page of rows.
func TestDeviceReplacementsFilterByDevice(t *testing.T) {
	api, ctx := replacementTestApi(t)
	target := seedReplacementDevice(t, api, ctx, "dozer-01")
	other := seedReplacementDevice(t, api, ctx, "dozer-02")

	_, err := api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: target.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace target")
	_, err = api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: other.Token},
		"tech@acme.example", time.Now())
	require.NoError(t, err, "replace other")

	found, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
		Device:     &target.Token,
	})
	require.NoError(t, err, "query replacements")
	require.Len(t, found.Results, 1, "the device filter returned another device's history")
	require.Equal(t, target.ID, found.Results[0].DeviceId, "wrong device in the filtered page")

	// The counterweight: with no filter, both are visible. A filter that returned
	// nothing would also pass the assertion above.
	all, err := api.DeviceReplacements(ctx, DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err, "query all replacements")
	require.Len(t, all.Results, 2, "the unfiltered journal lost a record")
}
