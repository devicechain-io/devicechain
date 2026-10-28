// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

const importSecret = "factory-secret-0123456789"

func newImportApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &DeviceCredential{}, &rdb.AuditEvent{})...)
	// The unique indexes production gets from migrations, so a collision is refused by the
	// database too and not only by the reads that precede the write.
	require.NoError(t, rdb.CreateTenantTokenIndex(api.RDB.Database, &Device{}))
	require.NoError(t, rdb.CreateTenantExternalIdIndex(api.RDB.Database, &Device{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(api.RDB.Database, &DeviceCredential{}))
	ctx := partialUpdateCtx()
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
	require.NoError(t, err)
	return api, ctx
}

func importRows(n int) []*DeviceImportRow {
	rows := make([]*DeviceImportRow, n)
	for i := range rows {
		rows[i] = &DeviceImportRow{
			Token:           fmt.Sprintf("dev-%04d", i+1),
			DeviceTypeToken: "sensor",
			Name:            strp(fmt.Sprintf("Device %d", i+1)),
		}
	}
	return rows
}

func countOf(t *testing.T, api *Api, ctx context.Context, m any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(m).Count(&n).Error)
	return n
}

func outcomes(r *DeviceImportResult) []string {
	out := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		out[i] = row.Outcome
	}
	return out
}

func mustImport(t *testing.T, api *Api, ctx context.Context, rows []*DeviceImportRow, mode string, dry bool) *DeviceImportResult {
	t.Helper()
	res, err := api.ImportDevices(ctx, rows, mode, dry)
	require.NoError(t, err)
	require.Len(t, res.Rows, len(rows))
	return res
}

func TestImportIsIdempotentOnTheToken(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := importRows(5)

	first := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, int32(5), first.Created)
	require.EqualValues(t, 5, countOf(t, api, ctx, &Device{}))

	second := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, int32(5), second.Exists, "a re-run of the same file is all EXISTS: %v", outcomes(second))
	require.Zero(t, second.Created)
	require.EqualValues(t, 5, countOf(t, api, ctx, &Device{}))
}

func TestImportReRunCompletesAHalfAppliedFile(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := importRows(4)
	rows[2].Token = "bad token!" // fails the grammar

	first := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, []string{"CREATED", "CREATED", "INVALID", "CREATED"}, outcomes(first), "one bad row does not affect the others")

	rows[2].Token = "dev-0003" // the operator fixes the row and re-runs the SAME file
	second := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, []string{"EXISTS", "EXISTS", "CREATED", "EXISTS"}, outcomes(second))
}

func TestImportNeverUpdatesAndReportsConflict(t *testing.T) {
	api, ctx := newImportApi(t)
	mustImport(t, api, ctx, importRows(1), DeviceImportModeNone, false)

	for name, mutate := range map[string]func(*DeviceImportRow){
		"name":        func(r *DeviceImportRow) { r.Name = strp("Renamed") },
		"description": func(r *DeviceImportRow) { r.Description = strp("now described") },
		"externalId":  func(r *DeviceImportRow) { r.ExternalId = strp("ext-1") },
		"metadata":    func(r *DeviceImportRow) { r.Metadata = strp(`{"a":1}`) },
	} {
		row := importRows(1)[0]
		mutate(row)
		res := mustImport(t, api, ctx, []*DeviceImportRow{row}, DeviceImportModeNone, false)
		require.Equal(t, "CONFLICT", res.Rows[0].Outcome, name)
		require.Contains(t, *res.Rows[0].Message, name)
	}

	got, err := api.DevicesByToken(ctx, []string{"dev-0001"})
	require.NoError(t, err)
	require.Equal(t, "Device 1", got[0].Name.String, "the stored device is untouched")
	require.False(t, got[0].ExternalId.Valid)
	require.Nil(t, got[0].Metadata)
}

func TestImportDifferentTypeIsAConflict(t *testing.T) {
	api, ctx := newImportApi(t)
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "gateway"})
	require.NoError(t, err)
	mustImport(t, api, ctx, importRows(1), DeviceImportModeNone, false)
	row := importRows(1)[0]
	row.DeviceTypeToken = "gateway"
	res := mustImport(t, api, ctx, []*DeviceImportRow{row}, DeviceImportModeNone, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
	require.Contains(t, *res.Rows[0].Message, "deviceTypeToken")
}

func TestImportMetadataIsComparedByValue(t *testing.T) {
	api, ctx := newImportApi(t)
	row := importRows(1)[0]
	row.Metadata = strp(`{"b": 2, "a": 1}`)
	mustImport(t, api, ctx, []*DeviceImportRow{row}, DeviceImportModeNone, false)
	row.Metadata = strp(`{"a":1,"b":2}`)
	res := mustImport(t, api, ctx, []*DeviceImportRow{row}, DeviceImportModeNone, false)
	require.Equal(t, "EXISTS", res.Rows[0].Outcome, "key order and whitespace do not make a row differ")
}

func TestImportExternalIdConflictsWithAnotherDevice(t *testing.T) {
	api, ctx := newImportApi(t)
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "other", DeviceTypeToken: "sensor", ExternalId: strp("SN-1")})
	require.NoError(t, err)

	row := importRows(1)[0]
	row.ExternalId = strp("SN-1")
	res := mustImport(t, api, ctx, []*DeviceImportRow{row}, DeviceImportModeNone, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
	require.Equal(t, "CONFLICT", *res.Rows[0].Code)
	require.EqualValues(t, 1, countOf(t, api, ctx, &Device{}), "the colliding row wrote nothing")
}

func TestImportDryRunWritesNothingAndPredictsTheRealRun(t *testing.T) {
	api, ctx := newImportApi(t)
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "taken", DeviceTypeToken: "sensor", ExternalId: strp("E1")})
	require.NoError(t, err)

	rows := importRows(4)
	_, err = api.CreateDevice(ctx, &DeviceCreateRequest{Token: "taken2", DeviceTypeToken: "sensor", ExternalId: strp("E2")})
	require.NoError(t, err)
	rows[1].ExternalId = strp("E2")                                                                          // conflict
	rows[2].DeviceTypeToken = "no-such"                                                                      // invalid
	rows = append(rows, &DeviceImportRow{Token: "taken", DeviceTypeToken: "sensor", ExternalId: strp("E1")}) // exists

	devices, creds := countOf(t, api, ctx, &Device{}), countOf(t, api, ctx, &DeviceCredential{})
	dry := mustImport(t, api, ctx, rows, DeviceImportModeNone, true)
	require.True(t, dry.DryRun)
	require.Equal(t, devices, countOf(t, api, ctx, &Device{}), "a dry run creates no device")
	require.Equal(t, creds, countOf(t, api, ctx, &DeviceCredential{}))

	real := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, outcomes(dry), outcomes(real), "the dry run decides each row by the code the real run uses")
	require.Equal(t, []string{"CREATED", "CONFLICT", "INVALID", "CREATED", "EXISTS"}, outcomes(real))
}

func TestImportRefusesMoreThanTheRowCapAndEmpty(t *testing.T) {
	api, ctx := newImportApi(t)

	_, err := api.ImportDevices(ctx, importRows(MaxDeviceImportRows+1), DeviceImportModeNone, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "1000")
	require.Zero(t, countOf(t, api, ctx, &Device{}), "an oversize call writes nothing")

	_, err = api.ImportDevices(ctx, nil, DeviceImportModeNone, false)
	require.Error(t, err)

	for _, mode := range []string{"GENERATE", "CHECK_PRE_PROVISIONED", ""} {
		_, err = api.ImportDevices(ctx, importRows(1), mode, false)
		require.Error(t, err, "mode %q is not one this build supports", mode)
	}

	res := mustImport(t, api, ctx, importRows(MaxDeviceImportRows), DeviceImportModeNone, false)
	require.Equal(t, int32(MaxDeviceImportRows), res.Created, "exactly the cap is accepted")
}

func TestImportValidatesLikeASingleCreate(t *testing.T) {
	api, ctx := newImportApi(t)
	long := strings.Repeat("x", 129)
	rows := []*DeviceImportRow{
		{Token: "", DeviceTypeToken: "sensor"},
		{Token: "has space", DeviceTypeToken: "sensor"},
		{Token: strings.Repeat("a", 129), DeviceTypeToken: "sensor"},
		{Token: "t1", DeviceTypeToken: ""},
		{Token: "t2", DeviceTypeToken: "missing"},
		{Token: "t3", DeviceTypeToken: "sensor", Name: &long},
		{Token: "t4", DeviceTypeToken: "sensor", Metadata: strp("{nope")},
		{Token: "t5", DeviceTypeToken: "sensor", ExternalId: strp(strings.Repeat("e", 257))},
		{Token: "t6", DeviceTypeToken: "sensor", Description: strp(strings.Repeat("d", 1025))},
		{Token: "t7", DeviceTypeToken: "sensor", Name: strp("a" + string(rune(0)) + "b")},
		nil,
	}
	res := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	for i, r := range res.Rows {
		require.Equal(t, "INVALID", r.Outcome, "row %d", i+1)
	}
	require.Equal(t, "NOT_FOUND", *res.Rows[4].Code)
	require.Zero(t, countOf(t, api, ctx, &Device{}))
}

func TestImportDuplicatesWithinTheFile(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := importRows(4)
	rows[1].Token = rows[0].Token
	rows[2].ExternalId, rows[3].ExternalId = strp("same"), strp("same")

	res := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, []string{"CREATED", "INVALID", "CREATED", "INVALID"}, outcomes(res))
	require.Equal(t, "DUPLICATE_IN_FILE", *res.Rows[1].Code)
	require.Contains(t, *res.Rows[1].Message, "row 1")
	require.Equal(t, "DUPLICATE_IN_FILE", *res.Rows[3].Code)
}

func TestImportCredentialColumnsAreRefusedOutsideCredentialsMode(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := importRows(1)
	rows[0].CredentialType, rows[0].CredentialId = strp("ACCESS_TOKEN"), strp("tok")
	res := mustImport(t, api, ctx, rows, DeviceImportModeNone, true)
	require.Equal(t, "INVALID", res.Rows[0].Outcome)
}

func credRow(i int, ctype, id, secret string) *DeviceImportRow {
	r := importRows(i)[i-1]
	r.CredentialType, r.CredentialId = &ctype, &id
	if secret != "" {
		r.CredentialValue = &secret
	}
	return r
}

func TestImportCreatesTheDeviceAndItsCredentialTogether(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := []*DeviceImportRow{
		credRow(1, "MQTT_BASIC", "user-1", importSecret),
		credRow(2, "ACCESS_TOKEN", "bearer-2", ""),
	}
	res := mustImport(t, api, ctx, rows, DeviceImportModeCredentials, false)
	require.Equal(t, []string{"CREATED", "CREATED"}, outcomes(res))
	require.EqualValues(t, 2, countOf(t, api, ctx, &DeviceCredential{}))

	// The device can authenticate with what the factory supplied.
	d, err := api.AuthenticateDevice(ctx, basic("user-1", importSecret), time.Now())
	require.NoError(t, err)
	require.Equal(t, "dev-0001", d.Token)
	requireNoColumnHolds(t, api, "dev-0001-credential", importSecret)

	// A re-run is EXISTS without comparing the secret: a different value changes nothing.
	again := []*DeviceImportRow{
		credRow(1, "MQTT_BASIC", "user-1", "a-completely-different-secret"),
		credRow(2, "ACCESS_TOKEN", "bearer-2", ""),
	}
	res = mustImport(t, api, ctx, again, DeviceImportModeCredentials, false)
	require.Equal(t, []string{"EXISTS", "EXISTS"}, outcomes(res))
	_, err = api.AuthenticateDevice(ctx, basic("user-1", importSecret), time.Now())
	require.NoError(t, err, "the stored secret was not replaced")
}

func TestImportCredentialConflictsAndMissingCredential(t *testing.T) {
	api, ctx := newImportApi(t)
	mustImport(t, api, ctx, []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "bearer-1", "")}, DeviceImportModeCredentials, false)

	// A new device claiming a credential id another device holds.
	res := mustImport(t, api, ctx, []*DeviceImportRow{credRow(2, "ACCESS_TOKEN", "bearer-1", "")}, DeviceImportModeCredentials, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
	require.EqualValues(t, 1, countOf(t, api, ctx, &Device{}), "the device was not created without its credential")

	// An existing device, with a different credential id than the one it holds.
	res = mustImport(t, api, ctx, []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "bearer-other", "")}, DeviceImportModeCredentials, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
	require.EqualValues(t, 1, countOf(t, api, ctx, &DeviceCredential{}), "an import never adds a credential to an existing device")

	// An existing device with no credential at all.
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev-0003", DeviceTypeToken: "sensor", Name: strp("Device 3")})
	require.NoError(t, err)
	res = mustImport(t, api, ctx, []*DeviceImportRow{credRow(3, "ACCESS_TOKEN", "bearer-3", "")}, DeviceImportModeCredentials, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
}

// An identical existing device whose named credential id belongs to ANOTHER device is a
// conflict, not an EXISTS: the row would otherwise report success for a credential that
// authenticates somebody else.
func TestImportExistingDeviceNamingAnotherDevicesCredentialIsAConflict(t *testing.T) {
	api, ctx := newImportApi(t)
	mustImport(t, api, ctx, []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "bearer-X", "")}, DeviceImportModeCredentials, false)
	_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev-0002", DeviceTypeToken: "sensor", Name: strp("Device 2")})
	require.NoError(t, err)

	res := mustImport(t, api, ctx, []*DeviceImportRow{credRow(2, "ACCESS_TOKEN", "bearer-X", "")}, DeviceImportModeCredentials, false)
	require.Equal(t, "CONFLICT", res.Rows[0].Outcome)
	require.Contains(t, *res.Rows[0].Message, "another device")
	require.EqualValues(t, 1, countOf(t, api, ctx, &DeviceCredential{}))
}

func TestImportCredentialRowsAreAtomic(t *testing.T) {
	api, ctx := newImportApi(t)
	// A credential already holds the token the import would derive for dev-0001's credential,
	// so the credential insert fails AFTER the device insert succeeded.
	other, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "holder", DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	require.NotNil(t, other)
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{Token: "dev-0001-credential", DeviceToken: "holder",
		CredentialType: "ACCESS_TOKEN", CredentialId: "unrelated", Enabled: true})
	require.NoError(t, err)

	rows := []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "bearer-1", ""), credRow(2, "ACCESS_TOKEN", "bearer-2", "")}
	res := mustImport(t, api, ctx, rows, DeviceImportModeCredentials, false)
	require.Equal(t, []string{"CONFLICT", "CREATED"}, outcomes(res), "the failed row rolled back whole; its neighbour was untouched")
	found, err := api.DevicesByToken(ctx, []string{"dev-0001"})
	require.NoError(t, err)
	require.Empty(t, found, "no device survives without its credential")
}

func TestImportCredentialValidation(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := []*DeviceImportRow{
		credRow(1, "MQTT_BASIC", "u1", "short"),
		credRow(2, "MQTT_BASIC", "u2", ""),
		credRow(3, "ACCESS_TOKEN", "t3", importSecret),
		credRow(4, "BOGUS", "u4", importSecret),
		credRow(5, "X509_CERTIFICATE", "u5", ""),
		importRows(6)[5],
		credRow(7, "MQTT_BASIC", "u7", strings.Repeat("s", 4097)),
	}
	res := mustImport(t, api, ctx, rows, DeviceImportModeCredentials, false)
	require.Equal(t, []string{"INVALID", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID", "INVALID"}, outcomes(res))
	// A certificate credential's chain is checked against a trust anchor at create, which an
	// import row has no way to name.
	require.Equal(t, "INVALID_VALUE", *res.Rows[4].Code)
	require.Contains(t, *res.Rows[4].Message, "createDeviceCredential")
	require.Zero(t, countOf(t, api, ctx, &Device{}))
}

func TestImportCredentialsModeNeedsASecretKey(t *testing.T) {
	api, ctx := newImportApi(t)
	api.DeviceSecretKey = nil
	_, err := api.ImportDevices(ctx, []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "t", "")}, DeviceImportModeCredentials, true)
	require.Error(t, err, "no key, no secret store: refuse loudly, even on a dry run")
}

// 🔴 A secret appears in no outcome, no error and no log line.
func TestImportNeverEchoesOrLogsASecret(t *testing.T) {
	logs := logSink.Capture(t)
	// The positive control: a line logged now is seen, so an empty capture means nothing was
	// logged rather than that the sink is detached.
	log.Info().Msg("import-capture-control")
	require.Contains(t, logs.String(), "import-capture-control")

	api, ctx := newImportApi(t)
	rows := []*DeviceImportRow{
		credRow(1, "MQTT_BASIC", "u1", importSecret),
		credRow(2, "MQTT_BASIC", "u2", importSecret+"-two"),
		credRow(3, "MQTT_BASIC", "u3", "short-"+importSecret[:3]),
	}
	rows[1].Token = "bad token" // an invalid row that carries a secret
	var seen []string
	for _, dry := range []bool{true, false, false} {
		res := mustImport(t, api, ctx, rows, DeviceImportModeCredentials, dry)
		b, err := json.Marshal(res)
		require.NoError(t, err)
		seen = append(seen, string(b))
	}
	for _, s := range append(seen, logs.String()) {
		require.NotContains(t, s, importSecret)
		require.NotContains(t, s, "short-"+importSecret[:3])
	}
}

func TestImportRowsAreIsolatedFromEachOther(t *testing.T) {
	api, ctx := newImportApi(t)
	rows := importRows(6)
	rows[0].DeviceTypeToken = "missing"
	rows[3].Metadata = strp("[")
	res := mustImport(t, api, ctx, rows, DeviceImportModeNone, false)
	require.Equal(t, []string{"INVALID", "CREATED", "CREATED", "INVALID", "CREATED", "CREATED"}, outcomes(res))
	require.EqualValues(t, 4, countOf(t, api, ctx, &Device{}))
	require.Equal(t, int32(4), res.Created)
	require.Equal(t, int32(2), res.Invalid)
}

func TestImportHonoursTheTenant(t *testing.T) {
	api, ctx := newImportApi(t)
	mustImport(t, api, ctx, importRows(2), DeviceImportModeNone, false)
	// Another tenant sees none of it: the same file is a fresh import there (it would need
	// its own device type, which it lacks, so every row is INVALID rather than EXISTS).
	other := core.WithTenant(context.Background(), "globex")
	res := mustImport(t, api, other, importRows(2), DeviceImportModeNone, true)
	require.Equal(t, []string{"INVALID", "INVALID"}, outcomes(res))
}

// The audit journal records an imported device and its credential. The journal is a gorm
// callback, so the fixture registers it and the assertion reads the journal's own table.
func TestImportWritesAuditEntries(t *testing.T) {
	api, ctx := newImportApi(t)
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))

	mustImport(t, api, ctx, []*DeviceImportRow{credRow(1, "ACCESS_TOKEN", "bearer-1", "")}, DeviceImportModeCredentials, false)

	var rows []rdb.AuditEvent
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&rows).Error)
	var device, cred bool
	for _, r := range rows {
		if r.Operation != "create" {
			continue
		}
		switch r.TableName {
		case "devices":
			device = r.EntityLabel == "dev-0001"
		case "device_credentials":
			cred = true
		}
	}
	require.True(t, device, "no audit entry for the imported device: %v", rows)
	require.True(t, cred, "no audit entry for the imported credential: %v", rows)

	// A dry run records nothing.
	before := len(rows)
	mustImport(t, api, ctx, importRows(3)[1:], DeviceImportModeNone, true)
	var after []rdb.AuditEvent
	require.NoError(t, api.RDB.DB(ctx).Find(&after).Error)
	require.Len(t, after, before)
}
