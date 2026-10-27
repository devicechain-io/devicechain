// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Device aliases: the names a sender other than the platform knows a device by. They are
// normalized on write (a DevEUI is stored upper case), unique within the tenant, replaced
// atomically with the device update, and the key an inbound integration finds a device by.

const (
	eui1 = "70B3D57ED0000001"
	eui2 = "70B3D57ED0000002"
)

func (f *intFixture) device(token, typeToken string, aliases ...DeviceAliasInput) (*Device, error) {
	f.t.Helper()
	return f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: token, DeviceTypeToken: typeToken, Aliases: &aliases})
}

func (f *intFixture) mustDevice(token, typeToken string, aliases ...DeviceAliasInput) *Device {
	f.t.Helper()
	d, err := f.device(token, typeToken, aliases...)
	require.NoError(f.t, err)
	return d
}

func deveui(v string) DeviceAliasInput { return DeviceAliasInput{Scheme: "lorawan.deveui", Value: v} }

func (f *intFixture) aliasesOf(token string) []string {
	f.t.Helper()
	found, err := f.api.DevicesByToken(f.ctx, []string{token})
	require.NoError(f.t, err)
	require.Len(f.t, found, 1)
	rows, err := f.api.DeviceAliases(f.ctx, found[0].ID)
	require.NoError(f.t, err)
	return renderDeviceAliases(rows)
}

func TestAnAliasIsStoredInItsCanonicalSpelling(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("d-1", "dt-a", deveui(strings.ToLower(eui1)),
		DeviceAliasInput{Scheme: "tts.device-id", Value: "Sensor-01"}, DeviceAliasInput{Scheme: "custom.acme", Value: " Serial-9 "})

	// Lower case in, upper case stored: the stored spelling is the one a lookup compares against.
	assert.Equal(t, []string{"custom.acme=Serial-9", "lorawan.deveui=" + eui1, "tts.device-id=sensor-01"}, f.aliasesOf("d-1"))
}

// The same DevEUI in a different case is the SAME alias: the second device is refused, and the
// refusal is the CONFLICT every other unique value gets, not a second row a sender might reach.
func TestADifferentCaseOfAnExistingAliasIsAConflict(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("d-1", "dt-a", deveui(eui1))

	_, err := f.device("d-2", "dt-a", deveui(strings.ToLower(eui1)))
	require.Error(t, err, "a different-case spelling of a held DevEUI must be refused")
	assert.True(t, conflict.Is(err), "answered as CONFLICT, got %v", err)

	// The refused create left nothing behind: not the device, not an alias.
	found, err := f.api.DevicesByToken(f.ctx, []string{"d-2"})
	require.NoError(t, err)
	assert.Empty(t, found)
	assert.Equal(t, []string{"lorawan.deveui=" + eui1}, f.aliasesOf("d-1"))

	// The paired positive: a different DevEUI is accepted.
	f.mustDevice("d-3", "dt-a", deveui(eui2))
}

func TestAMalformedAliasIsRefusedAndWritesNothing(t *testing.T) {
	f := newIntFixture(t)
	cases := map[string][]DeviceAliasInput{
		"short deveui":     {deveui("70B3D57ED00000")},
		"non-hex deveui":   {deveui("70B3D57ED000000G")},
		"unknown scheme":   {{Scheme: "other.thing", Value: "x"}},
		"empty custom":     {{Scheme: "custom.acme", Value: "  "}},
		"two of a scheme":  {deveui(eui1), deveui(eui2)},
		"bad custom scope": {{Scheme: "custom.Acme", Value: "x"}},
	}
	for name, aliases := range cases {
		_, err := f.device("bad-"+strings.ReplaceAll(name, " ", "-"), "dt-a", aliases...)
		require.Error(t, err, name)
		class, ok := integrity.Refused(err)
		require.True(t, ok, "%s: %v", name, err)
		assert.Equal(t, integrity.ClassInvalid, class, name)
	}
	var many []DeviceAliasInput
	for i := 0; i < 9; i++ {
		many = append(many, DeviceAliasInput{Scheme: fmt.Sprintf("custom.s%d", i), Value: "v"})
	}
	_, err := f.device("too-many", "dt-a", many...)
	_, isLimit := limit.As(err)
	assert.True(t, isLimit, "more than 8 aliases is a limit refusal, got %v", err)

	all, err := f.api.Devices(f.ctx, DeviceSearchCriteria{})
	require.NoError(t, err)
	assert.Empty(t, all.Results, "a refused create wrote no device")
}

func TestUpdateReplacesTheAliasSetAtomically(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("d-1", "dt-a", deveui(eui1), DeviceAliasInput{Scheme: "custom.acme", Value: "a"})
	f.mustDevice("d-2", "dt-a", deveui(eui2))

	// Omitted leaves them alone.
	_, err := f.api.UpdateDevice(f.ctx, "d-1", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	assert.Equal(t, []string{"custom.acme=a", "lorawan.deveui=" + eui1}, f.aliasesOf("d-1"))

	// A list replaces the set: the DevEUI changes in place, the custom alias goes.
	_, err = f.api.UpdateDevice(f.ctx, "d-1", &DeviceUpdateRequest{
		Aliases: OptionalDeviceAliasListOf([]DeviceAliasInput{deveui("70b3d57ed0000009")})})
	require.NoError(t, err)
	assert.Equal(t, []string{"lorawan.deveui=70B3D57ED0000009"}, f.aliasesOf("d-1"))

	// An alias another device holds refuses the WHOLE update: the name does not change.
	_, err = f.api.UpdateDevice(f.ctx, "d-1", &DeviceUpdateRequest{
		Name:    dcgraphql.OptionalStringOf("must-not-apply"),
		Aliases: OptionalDeviceAliasListOf([]DeviceAliasInput{deveui(strings.ToLower(eui2))})})
	require.Error(t, err)
	assert.True(t, conflict.Is(err), "got %v", err)
	found, err := f.api.DevicesByToken(f.ctx, []string{"d-1"})
	require.NoError(t, err)
	assert.Equal(t, "renamed", found[0].Name.String, "the refused update applied none of its fields")
	assert.Equal(t, []string{"lorawan.deveui=70B3D57ED0000009"}, f.aliasesOf("d-1"))

	// An empty list removes them all.
	_, err = f.api.UpdateDevice(f.ctx, "d-1", &DeviceUpdateRequest{Aliases: OptionalDeviceAliasListOf(nil)})
	require.NoError(t, err)
	assert.Empty(t, f.aliasesOf("d-1"))
}

func TestDeletingADeviceFreesItsAliases(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("d-1", "dt-a", deveui(eui1))
	_, err := f.device("d-2", "dt-a", deveui(eui1))
	require.Error(t, err, "held while d-1 exists")

	// DeleteDevice cascades through tables the integration fixture does not migrate; the alias
	// delete is the statement under test, so it is driven the way DeleteDevice drives it.
	found, err := f.api.DevicesByToken(f.ctx, []string{"d-1"})
	require.NoError(t, err)
	require.NoError(t, deleteAliasesOnTx(f.api.RDB.DB(f.ctx), found[0].ID))
	require.NoError(t, f.api.RDB.DB(f.ctx).Unscoped().Delete(found[0]).Error)

	f.mustDevice("d-2", "dt-a", deveui(eui1))
}

func TestDevicesAreSearchableByAliasInAnySpelling(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("d-1", "dt-a", deveui(eui1))
	f.mustDevice("d-2", "dt-a", deveui(eui2))

	res, err := f.api.Devices(f.ctx, DeviceSearchCriteria{Alias: &DeviceAliasInput{Scheme: "lorawan.deveui", Value: strings.ToLower(eui1)}})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "d-1", res.Results[0].Token)

	_, err = f.api.Devices(f.ctx, DeviceSearchCriteria{Alias: &DeviceAliasInput{Scheme: "lorawan.deveui", Value: "nope"}})
	require.Error(t, err, "a malformed alias is refused, not matched against nothing")
}

// ---- importDevices ----------------------------------------------------------------------

func aliasImportRow(token string, aliases ...DeviceAliasInput) *DeviceImportRow {
	return &DeviceImportRow{Token: token, DeviceTypeToken: "dt-a", Aliases: &aliases}
}

func TestImportCarriesAliasesAndASecondRunIsAllExists(t *testing.T) {
	f := newIntFixture(t)
	rows := []*DeviceImportRow{aliasImportRow("i-1", deveui(strings.ToLower(eui1))), aliasImportRow("i-2", deveui(eui2))}

	dry, err := f.api.ImportDevices(f.ctx, rows, DeviceImportModeNone, true)
	require.NoError(t, err)
	assert.EqualValues(t, 2, dry.Created)
	assert.Empty(t, f.aliasesOfIfExists("i-1"), "a dry run writes nothing")

	first, err := f.api.ImportDevices(f.ctx, rows, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, first.Created)
	assert.Equal(t, []string{"lorawan.deveui=" + eui1}, f.aliasesOf("i-1"), "normalized on import")

	again, err := f.api.ImportDevices(f.ctx, rows, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, again.Exists, "an identical re-run is all EXISTS")
}

func (f *intFixture) aliasesOfIfExists(token string) []string {
	found, err := f.api.DevicesByToken(f.ctx, []string{token})
	require.NoError(f.t, err)
	if len(found) == 0 {
		return nil
	}
	return f.aliasesOf(token)
}

func TestImportConflictSemanticsForAliases(t *testing.T) {
	f := newIntFixture(t)
	f.mustDevice("held", "dt-a", deveui(eui1))
	f.mustDevice("exists", "dt-a", deveui(eui2))

	res, err := f.api.ImportDevices(f.ctx, []*DeviceImportRow{
		aliasImportRow("new-clash", deveui(strings.ToLower(eui1))),                      // held by another device
		aliasImportRow("exists", deveui("70B3D57ED00000AA")),                            // exists with a different alias
		aliasImportRow("bad", deveui("zz")),                                             // malformed
		aliasImportRow("dup-a", DeviceAliasInput{Scheme: "custom.x", Value: "same"}),    // in-file duplicate pair
		aliasImportRow("dup-b", DeviceAliasInput{Scheme: "custom.x", Value: "same"}),    //
		aliasImportRow("fine", DeviceAliasInput{Scheme: "custom.x", Value: "distinct"}), //
	}, DeviceImportModeNone, false)
	require.NoError(t, err)

	byToken := map[string]*DeviceImportRowResult{}
	for _, r := range res.Rows {
		byToken[r.Token] = r
	}
	assert.Equal(t, DeviceImportConflict, byToken["new-clash"].Outcome)
	assert.Equal(t, conflict.Code, *byToken["new-clash"].Code)
	assert.NotContains(t, *byToken["new-clash"].Message, eui1, "a message names the scheme, never the value")
	assert.Equal(t, DeviceImportConflict, byToken["exists"].Outcome)
	assert.Contains(t, *byToken["exists"].Message, "aliases")
	assert.Equal(t, DeviceImportInvalid, byToken["bad"].Outcome)
	assert.Equal(t, DeviceImportCreated, byToken["dup-a"].Outcome)
	assert.Equal(t, DeviceImportInvalid, byToken["dup-b"].Outcome)
	assert.Equal(t, importCodeDuplicateInFile, *byToken["dup-b"].Code)
	assert.Equal(t, DeviceImportCreated, byToken["fine"].Outcome)
	assert.Empty(t, f.aliasesOfIfExists("new-clash"), "a conflicting row creates nothing")
}

// ---- resolveIntegrationDevice -----------------------------------------------------------

func (f *intFixture) resolve(integration, secret, scheme, value string) *IntegrationDeviceResolution {
	f.t.Helper()
	got, err := f.api.ResolveIntegrationDevice(f.ctx, integration, secret, scheme, value)
	require.NoError(f.t, err)
	return got
}

func (f *intFixture) sightings(token string) []IntegrationUnregisteredDevice {
	f.t.Helper()
	res, err := f.api.UnregisteredIntegrationDevices(f.ctx, UnregisteredDeviceSearchCriteria{Integration: token})
	require.NoError(f.t, err)
	return res.Results
}

func TestResolveIntegrationDevice(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1") // scoped to dt-a
	f.mustDevice("in-scope", "dt-a", deveui(eui1))
	f.mustDevice("out-of-scope", "dt-b", deveui(eui2))

	got := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", strings.ToLower(eui1))
	assert.Equal(t, IntegrationDeviceResolved, got.Outcome, "the paired positive: a device in scope resolves, in any spelling")
	assert.Equal(t, "in-scope", got.DeviceToken)
	assert.Equal(t, "dt-a", got.DeviceTypeToken)
	assert.Empty(t, f.sightings("chirp-1"), "a resolved device is not a sighting")

	// An alias held by a device of an out-of-scope type is UNREGISTERED and never resolved, and
	// reads exactly like an alias nobody holds.
	scoped := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui2)
	nobody := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", "70B3D57ED0000099")
	assert.Equal(t, IntegrationDeviceUnregistered, scoped.Outcome)
	assert.Equal(t, *nobody, *scoped, "an out-of-scope device is indistinguishable from an unknown one")
	assert.Empty(t, scoped.DeviceToken)
	// Only the value nobody holds is a sighting: a value a device outside the scope holds is not an
	// unregistered device, and a device created with it would be refused.
	require.Len(t, f.sightings("chirp-1"), 1)
	assert.Equal(t, "70B3D57ED0000099", f.sightings("chirp-1")[0].Value)

	// Widening the scope makes the same alias resolve.
	_, err := f.api.UpdateIntegration(f.ctx, "chirp-1", &IntegrationUpdateRequest{
		DeviceTypeTokens: dcgraphql.OptionalStringListOf([]string{"dt-a", "dt-b"})})
	require.NoError(t, err)
	assert.Equal(t, IntegrationDeviceResolved, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui2).Outcome)
}

func TestAnUnregisteredSightingIsUpsertedWithItsCount(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")

	for i := 0; i < 3; i++ {
		got := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui1)
		require.Equal(t, IntegrationDeviceUnregistered, got.Outcome)
		require.False(t, got.Overflow)
	}
	rows := f.sightings("chirp-1")
	require.Len(t, rows, 1, "one row per (integration, scheme, value), however many sightings")
	assert.EqualValues(t, 3, rows[0].Count)
	assert.Equal(t, "chirp-1", rows[0].IntegrationToken)
	assert.Equal(t, eui1, rows[0].Value)
	assert.False(t, rows[0].LastSeen.Before(rows[0].FirstSeen))

	// Creating the device turns the next sighting into a resolution; the old row remains as
	// history (the console lists it with the device that now covers it).
	f.mustDevice("now-registered", "dt-a", deveui(eui1))
	assert.Equal(t, IntegrationDeviceResolved, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui1).Outcome)
}

// The cap is per integration and exact: past it nothing more is stored, the caller is told, and
// a value already recorded still refreshes.
func TestTheUnregisteredCapIsPerIntegrationAndStopsInserting(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")
	other := f.create("chirp-2")

	integrations, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	rows := make([]IntegrationUnregisteredDevice, 0, MaxUnregisteredPerIntegration)
	for i := 0; i < MaxUnregisteredPerIntegration; i++ {
		rows = append(rows, IntegrationUnregisteredDevice{
			IntegrationId: integrations[0].ID, Scheme: "lorawan.deveui", Value: fmt.Sprintf("%016X", i+1),
			FirstSeen: integrations[0].CreatedAt, LastSeen: integrations[0].CreatedAt, Count: 1})
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).CreateInBatches(&rows, 200).Error)

	over := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", "FFFFFFFFFFFFFFFF")
	assert.Equal(t, IntegrationDeviceUnregistered, over.Outcome)
	assert.True(t, over.Overflow, "past the cap the sighting is not stored, and the caller is told")
	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&IntegrationUnregisteredDevice{}).Where("integration_id = ?", integrations[0].ID).Count(&n).Error)
	assert.EqualValues(t, MaxUnregisteredPerIntegration, n)

	known := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", fmt.Sprintf("%016X", 1))
	assert.False(t, known.Overflow, "a value already recorded refreshes at the cap")

	// The cap belongs to the integration: another one still records.
	fresh := f.resolve("chirp-2", other.IssuedToken, "lorawan.deveui", "FFFFFFFFFFFFFFFF")
	assert.False(t, fresh.Overflow)
	assert.Len(t, f.sightings("chirp-2"), 1)
}

// A disabled integration is reported as DISABLED, which is not a credential failure, and nothing
// is resolved or recorded for it. A wrong token is UNAUTHENTICATED, indistinguishable from an
// unknown integration.
func TestResolveRefusesADisabledIntegrationAndWrongCredentialsApart(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")
	f.mustDevice("d-1", "dt-a", deveui(eui1))

	_, err := f.api.UpdateIntegration(f.ctx, "chirp-1", &IntegrationUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)

	disabled := f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui1)
	assert.Equal(t, IntegrationDeviceDisabled, disabled.Outcome, "a matching token on a disabled integration is DISABLED")
	assert.Empty(t, disabled.DeviceToken, "a disabled integration resolves nothing")
	assert.Equal(t, IntegrationDeviceDisabled, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", "70B3D57ED0000077").Outcome)
	assert.Empty(t, f.sightings("chirp-1"), "a disabled integration records nothing")

	wrong := f.resolve("chirp-1", "not-the-token", "lorawan.deveui", eui1)
	unknown := f.resolve("no-such", chirp.IssuedToken, "lorawan.deveui", eui1)
	assert.Equal(t, IntegrationDeviceUnauthenticated, wrong.Outcome)
	assert.Equal(t, *wrong, *unknown, "an unknown integration reads exactly like a wrong token")
}

func TestResolveRefusesAMalformedRequest(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")

	for name, c := range map[string][2]string{
		"scheme the integration does not resolve by": {"tts.device-id", "sensor-1"},
		"malformed deveui":                           {"lorawan.deveui", "nothex"},
	} {
		_, err := f.api.ResolveIntegrationDevice(f.ctx, "chirp-1", chirp.IssuedToken, c[0], c[1])
		require.Error(t, err, name)
		class, ok := integrity.Refused(err)
		require.True(t, ok, name)
		assert.Equal(t, integrity.ClassInvalid, class, name)
	}
	assert.Empty(t, f.sightings("chirp-1"), "a malformed request records nothing")
}

func TestDeletingAnIntegrationTakesItsSightingsAndLeavesTheAliases(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")
	f.mustDevice("d-1", "dt-a", deveui(eui1))
	f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui2)
	require.Len(t, f.sightings("chirp-1"), 1)

	deleted, err := f.api.DeleteIntegration(f.ctx, "chirp-1")
	require.NoError(t, err)
	require.True(t, deleted)
	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&IntegrationUnregisteredDevice{}).Count(&n).Error)
	assert.Zero(t, n)
	assert.Equal(t, []string{"lorawan.deveui=" + eui1}, f.aliasesOf("d-1"), "aliases belong to devices, not integrations")
}

// An alias change is a control-plane change and is journalled; a sighting is an observation the
// data plane writes and is not, so a sender spraying identifiers cannot drive journal writes.
func TestAliasWritesAreJournalledAndSightingsAreNot(t *testing.T) {
	f := newIntFixture(t)
	require.NoError(t, rdb.RegisterAuditJournal(f.api.RDB.Database))
	chirp := f.create("chirp-1")

	f.mustDevice("d-1", "dt-a", deveui(eui1))
	for i := 0; i < 5; i++ {
		f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", fmt.Sprintf("%016X", 0xA0+i))
	}

	var rows []rdb.AuditEvent
	require.NoError(t, f.api.RDB.DB(f.ctx).Find(&rows).Error)
	tables := map[string]int{}
	for _, r := range rows {
		tables[r.TableName]++
		if r.TableName == "device_aliases" {
			assert.Equal(t, "lorawan.deveui="+eui1, r.EntityLabel)
		}
	}
	assert.Equal(t, 1, tables["device_aliases"], "the alias create is journalled")
	assert.Zero(t, tables["integration_unregistered_devices"], "sightings are not journalled")
}

// DeleteDevice removes the device's aliases itself, not only through the foreign key, so the
// delete does not depend on the database being the one that enforces it. SQLite enforces no
// foreign keys, which is what makes this observable here.
func TestDeleteDeviceRemovesItsAliases(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	dev := &Device{}
	dev.Token = "dev-1"
	require.NoError(t, api.RDB.DB(ctx).Create(dev).Error)
	require.NoError(t, insertAliasesOnTx(api.RDB.DB(ctx), dev.ID, []DeviceAliasInput{deveui(eui1)}))

	deleted, err := api.DeleteDevice(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, deleted)

	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceAlias{}).Count(&n).Error)
	assert.Zero(t, n, "the device's aliases went with it")
}

// ---- the sightings list empties ----------------------------------------------------------

func (f *intFixture) sight(integration, token string, values ...string) {
	f.t.Helper()
	for _, v := range values {
		require.Equal(f.t, IntegrationDeviceUnregistered, f.resolve(integration, token, "lorawan.deveui", v).Outcome)
	}
}

// Creating a device with the alias a sender was seen using removes the sighting, in every
// integration, whichever way the alias arrives: a create, an update, an import.
// rawSightings are the stored rows, including ones the list hides because a device now holds their
// value: the clearing under test is what removes them.
func (f *intFixture) rawSightings(token string) []IntegrationUnregisteredDevice {
	f.t.Helper()
	integrations, err := f.api.IntegrationsByToken(f.ctx, []string{token})
	require.NoError(f.t, err)
	var rows []IntegrationUnregisteredDevice
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("integration_id = ?", integrations[0].ID).Find(&rows).Error)
	return rows
}

func TestAnAliasWriteClearsItsSightings(t *testing.T) {
	f := newIntFixture(t)
	a, b := f.create("chirp-1"), f.create("chirp-2")
	vals := []string{"70B3D57ED0000011", "70B3D57ED0000012", "70B3D57ED0000013", "70B3D57ED0000014"}
	f.sight("chirp-1", a.IssuedToken, vals...)
	f.sight("chirp-2", b.IssuedToken, vals[0])
	require.Len(t, f.rawSightings("chirp-1"), 4)

	f.mustDevice("d-create", "dt-a", deveui(vals[0]))
	assert.Len(t, f.rawSightings("chirp-1"), 3, "a create clears the value")
	assert.Empty(t, f.rawSightings("chirp-2"), "in every integration of the tenant")

	f.mustDevice("d-update", "dt-a")
	_, err := f.api.UpdateDevice(f.ctx, "d-update", &DeviceUpdateRequest{
		Aliases: OptionalDeviceAliasListOf([]DeviceAliasInput{deveui(vals[1])})})
	require.NoError(t, err)
	assert.Len(t, f.rawSightings("chirp-1"), 2, "an update clears the value")

	res, err := f.api.ImportDevices(f.ctx, []*DeviceImportRow{aliasImportRow("d-import", deveui(vals[2]))}, DeviceImportModeNone, false)
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Created)
	left := f.rawSightings("chirp-1")
	require.Len(t, left, 1, "an import clears the value")
	assert.Equal(t, vals[3], left[0].Value)
}

// A sighting that survives (a stale row, however it came to be) is not listed while a device holds
// its value, and goes when the value resolves.
func TestAResolvedValueDropsItsStaleSighting(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")
	f.sight("chirp-1", chirp.IssuedToken, eui1)
	integrations, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	// Make the device exist WITHOUT the clearing path, as a stale row would have it.
	d := f.mustDevice("d-1", "dt-a")
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceAlias{DeviceId: d.ID, Scheme: "lorawan.deveui", Value: eui1}).Error)
	require.Len(t, f.sightings("chirp-1"), 0, "a value a device holds is not listed even while its row exists")
	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&IntegrationUnregisteredDevice{}).Where("integration_id = ?", integrations[0].ID).Count(&n).Error)
	require.EqualValues(t, 1, n, "the stale row is still there")

	assert.Equal(t, IntegrationDeviceResolved, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", eui1).Outcome)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&IntegrationUnregisteredDevice{}).Where("integration_id = ?", integrations[0].ID).Count(&n).Error)
	assert.Zero(t, n, "resolving the value dropped its sighting")
}

// Values a device holds do not count against the cap, so an onboarding that created the devices
// leaves the room back even if stale rows remain.
func TestHeldValuesAreNotCountedAgainstTheCap(t *testing.T) {
	f := newIntFixture(t)
	chirp := f.create("chirp-1")
	integrations, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	rows := make([]IntegrationUnregisteredDevice, 0, MaxUnregisteredPerIntegration)
	for i := 0; i < MaxUnregisteredPerIntegration; i++ {
		rows = append(rows, IntegrationUnregisteredDevice{IntegrationId: integrations[0].ID, Scheme: "lorawan.deveui",
			Value: fmt.Sprintf("%016X", i+1), FirstSeen: integrations[0].CreatedAt, LastSeen: integrations[0].CreatedAt, Count: 1})
	}
	require.NoError(t, f.api.RDB.DB(f.ctx).CreateInBatches(&rows, 200).Error)
	assert.True(t, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", "FFFFFFFFFFFFFFFF").Overflow, "full")

	// 100 of the rows now name devices (stale rows left behind).
	for i := 0; i < 100; i++ {
		other := f.mustDevice(fmt.Sprintf("h-%d", i), "dt-a")
		require.NoError(t, f.api.RDB.DB(f.ctx).Create(&DeviceAlias{DeviceId: other.ID, Scheme: "lorawan.deveui", Value: fmt.Sprintf("%016X", i+1)}).Error)
	}
	assert.False(t, f.resolve("chirp-1", chirp.IssuedToken, "lorawan.deveui", "FFFFFFFFFFFFFFFF").Overflow,
		"room came back: held values are not counted")
}

func TestClearUnregisteredIntegrationDevices(t *testing.T) {
	f := newIntFixture(t)
	a, b := f.create("chirp-1"), f.create("chirp-2")
	f.sight("chirp-1", a.IssuedToken, eui1, eui2)
	f.sight("chirp-2", b.IssuedToken, eui1)

	n, err := f.api.ClearUnregisteredIntegrationDevices(f.ctx, "chirp-1")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	assert.Empty(t, f.sightings("chirp-1"))
	assert.Len(t, f.sightings("chirp-2"), 1, "another integration's sightings are untouched")

	n, err = f.api.ClearUnregisteredIntegrationDevices(f.ctx, "no-such")
	require.NoError(t, err)
	assert.Zero(t, n)

	f.sight("chirp-1", a.IssuedToken, eui1)
	assert.Len(t, f.sightings("chirp-1"), 1, "a sender still sending is recorded again")
}

// Alias writes go in (scheme, value) order.
func TestAliasWritesAreOrdered(t *testing.T) {
	got := sortedAliases([]DeviceAliasInput{
		{Scheme: "tts.device-id", Value: "b"}, {Scheme: "custom.a", Value: "z"}, {Scheme: "custom.a", Value: "y"}, {Scheme: "lorawan.deveui", Value: eui1}})
	assert.Equal(t, []DeviceAliasInput{
		{Scheme: "custom.a", Value: "y"}, {Scheme: "custom.a", Value: "z"}, {Scheme: "lorawan.deveui", Value: eui1}, {Scheme: "tts.device-id", Value: "b"}}, got)
}
