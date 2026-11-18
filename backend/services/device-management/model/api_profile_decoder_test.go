// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Payload decoders, authoring half: the document is validated at save, frozen at publish, and
// gated by its vectors at publish and rollback.

// decoderDoc is a small, valid FRAME decoder over the fixture's metrics. The vector passes.
const decoderDoc = `{
  "kind": "CEL", "envVersion": 1, "input": "FRAME",
  "sections": [
    {"name": "telemetry", "when": "fPort == 2", "outputs": [
      {"key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0"},
      {"key": "door", "expr": "bit(frame, 2, 0)"}
    ]}
  ],
  "vectors": [
    {"name": "room sensor", "input": {"frameHex": "00D701", "fPort": 2}, "expect": {"temperature": 21.5, "door": 1}}
  ]
}`

type decoderFixture struct {
	t   *testing.T
	api *Api
	ctx context.Context
}

// newDecoderFixture is a profile "meter" with measured metrics temperature (DOUBLE) and door (INT)
// and a calculated metric fahrenheit, in tenant acme.
func newDecoderFixture(t *testing.T) *decoderFixture {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...),
		&ProfileDecoderSample{}, &ProfileDecoderHealth{}, &Integration{}, &IntegrationDeviceType{})...)
	api.CalculatedMetricsCeiling = &stubCeiling{n: 10}
	f := &decoderFixture{t: t, api: api, ctx: partialUpdateCtx()}
	seedDeviceProfile(t, api, f.ctx, "meter")
	f.metric("temperature", "DOUBLE", "")
	f.metric("door", "INT", "")
	f.metric("fahrenheit", "DOUBLE", "temperature * 1.8 + 32.0")
	return f
}

func (f *decoderFixture) metric(key, dataType, expression string) {
	f.t.Helper()
	req := &MetricDefinitionCreateRequest{Token: "md-" + strings.ReplaceAll(key, ".", "-"), DeviceProfileToken: "meter", MetricKey: key, DataType: dataType}
	if expression != "" {
		req.Expression = &expression
	}
	_, err := f.api.CreateMetricDefinition(f.ctx, req)
	require.NoError(f.t, err)
}

func (f *decoderFixture) set(doc string) error {
	_, err := f.api.SetDeviceProfileDecoder(f.ctx, "meter", []byte(doc))
	return err
}

func (f *decoderFixture) publish() (*DeviceProfileVersion, error) {
	return f.api.PublishDeviceProfile(f.ctx, "meter", nil, nil, "tester")
}

func (f *decoderFixture) profile() *DeviceProfile {
	f.t.Helper()
	p, err := f.api.deviceProfileByToken(f.ctx, "meter")
	require.NoError(f.t, err)
	return p
}

// refusal asserts err is the typed decoder refusal with the code, and that its message contains
// every fragment (so the failing expression or vector is NAMED, not just refused).
func refusal(t *testing.T, err error, code string, fragments ...string) {
	t.Helper()
	require.Error(t, err)
	var r *DecoderRefusalError
	require.True(t, errors.As(err, &r), "not a decoder refusal: %v", err)
	assert.Equal(t, code, r.Code)
	for _, frag := range fragments {
		assert.Contains(t, err.Error(), frag)
	}
}

// withDoc returns decoderDoc with the first occurrence of old replaced.
func withDoc(t *testing.T, old, new string) string {
	t.Helper()
	require.Contains(t, decoderDoc, old)
	return strings.Replace(decoderDoc, old, new, 1)
}

func TestSetDecoderStoresTheDraftAndReadsBack(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))

	p := f.profile()
	require.NotNil(t, p.DecoderDeclaration)
	// Stored compacted, and equal in content to what was sent.
	assert.JSONEq(t, decoderDoc, string(*p.DecoderDeclaration))
	assert.NotContains(t, string(*p.DecoderDeclaration), "\n")

	_, err := f.api.ClearDeviceProfileDecoder(f.ctx, "meter")
	require.NoError(t, err)
	assert.Nil(t, f.profile().DecoderDeclaration)
}

func TestSetDecoderRefusesAnUnknownKey(t *testing.T) {
	f := newDecoderFixture(t)
	// A typo is refused, never a silently ignored field.
	refusal(t, f.set(withDoc(t, `"input": "FRAME",`, `"input": "FRAME", "inptu": "x",`)), CodeInvalidDecoder, "inptu")
	refusal(t, f.set(withDoc(t, `{"key": "door",`, `{"ky": "door", "key": "door",`)), CodeInvalidDecoder, "ky")
	assert.Nil(t, f.profile().DecoderDeclaration, "a refused save wrote something")
}

func TestSetDecoderRefusesAnUndeclaredMetric(t *testing.T) {
	f := newDecoderFixture(t)
	err := f.set(withDoc(t, `"key": "door"`, `"key": "doorr"`))
	refusal(t, err, CodeInvalidDecoder, "sections[0].outputs[1].key", "doorr", "not a declared measured metric")
}

func TestSetDecoderRefusesACalculatedMetric(t *testing.T) {
	f := newDecoderFixture(t)
	// fahrenheit is declared, but calculated: the resolver would drop a device-sent value for it.
	err := f.set(withDoc(t, `"key": "door"`, `"key": "fahrenheit"`))
	refusal(t, err, CodeInvalidDecoder, "sections[0].outputs[1].key", "fahrenheit", "calculated")
	assert.Nil(t, f.profile().DecoderDeclaration)
}

func TestSetDecoderRefusesAReservedKey(t *testing.T) {
	f := newDecoderFixture(t)
	// Declare the metric so the ONLY thing wrong with the row is the reserved prefix.
	f.metric("lorawan.rssi", "DOUBLE", "")
	err := f.set(withDoc(t, `"key": "door"`, `"key": "lorawan.rssi"`))
	refusal(t, err, CodeInvalidDecoder, "sections[0].outputs[1].key", "reserved")
}

func TestSetDecoderRefusesADuplicateScalarKey(t *testing.T) {
	f := newDecoderFixture(t)
	err := f.set(withDoc(t, `"key": "door"`, `"key": "temperature"`))
	refusal(t, err, CodeInvalidDecoder, "sections[0].outputs[1].key", "temperature", "twice")
}

func TestSetDecoderRefusesWASMAndGarbage(t *testing.T) {
	f := newDecoderFixture(t)
	refusal(t, f.set(withDoc(t, `"kind": "CEL"`, `"kind": "WASM"`)), CodeUnsupportedDecoderKind)
	refusal(t, f.set(`not json`), CodeInvalidDecoder)
	refusal(t, f.set(decoderDoc+` {}`), CodeInvalidDecoder, "trailing")
	refusal(t, f.set(`{"kind":"CEL","envVersion":2,"input":"FRAME","sections":[]}`), CodeInvalidDecoder)
	huge := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[],"table":"` + strings.Repeat("x", 33<<10) + `"}`
	refusal(t, f.set(huge), CodeInvalidDecoder, "above the maximum")
}

func TestSetDecoderNeedsAnExistingProfile(t *testing.T) {
	f := newDecoderFixture(t)
	_, err := f.api.SetDeviceProfileDecoder(f.ctx, "nope", []byte(decoderDoc))
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = f.api.ClearDeviceProfileDecoder(f.ctx, "nope")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// Vectors gate publish, not save: an author can save work in progress.
func TestSetDecoderDoesNotRequireVectorsToPass(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(withDoc(t, `"door": 1}`, `"door": 0}`)))
}

// An edit of the profile's ordinary fields must not write a stale decoder back.
func TestUpdatingTheProfileDoesNotTouchTheDecoder(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	_, err := f.api.UpdateDeviceProfile(f.ctx, "meter", &DeviceProfileUpdateRequest{})
	require.NoError(t, err)
	require.NotNil(t, f.profile().DecoderDeclaration, "UpdateDeviceProfile cleared the decoder")
}

func TestPublishFreezesTheDecoderAndLaterEditsDoNotTouchIt(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	v, err := f.publish()
	require.NoError(t, err)

	snap, err := parseProfileSnapshot(v.Snapshot)
	require.NoError(t, err)
	assert.JSONEq(t, decoderDoc, string(snap.Decoder))

	// Clearing the draft afterwards changes only the next publish.
	_, err = f.api.ClearDeviceProfileDecoder(f.ctx, "meter")
	require.NoError(t, err)
	active, err := f.api.activeProfileSnapshot(f.ctx, f.profile().ID)
	require.NoError(t, err)
	assert.JSONEq(t, decoderDoc, string(active.Decoder))

	v2, err := f.publish()
	require.NoError(t, err)
	snap2, err := parseProfileSnapshot(v2.Snapshot)
	require.NoError(t, err)
	assert.Empty(t, snap2.Decoder, "a profile with no decoder publishes none")
}

// A.11: a failing vector refuses publish, and names itself.
func TestAFailingVectorRefusesPublishAndRollbackIsUntouched(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	_, err := f.publish()
	require.NoError(t, err)

	require.NoError(t, f.set(withDoc(t, `"door": 1}`, `"door": 0}`)))
	_, err = f.publish()
	refusal(t, err, CodeInvalidDecoder, "cannot publish device profile", `"room sensor"`, "door")

	var n int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceProfileVersion{}).Count(&n).Error)
	assert.EqualValues(t, 1, n, "a refused publish minted a version")
	assert.EqualValues(t, 1, f.profile().ActiveVersion.Int32)
}

func TestAVectorThatCannotDecodeRefusesPublish(t *testing.T) {
	f := newDecoderFixture(t)
	// The frame is one byte short for i16be at offset 0 plus bit at offset 2.
	require.NoError(t, f.set(withDoc(t, `"frameHex": "00D701"`, `"frameHex": "00D7"`)))
	_, err := f.publish()
	refusal(t, err, CodeInvalidDecoder, `"room sensor"`)
}

// A10: the strict-key check at publish sees the metrics being published.
func TestPublishRefusesAnOutputThatIsNoLongerADeclaredMetric(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	// The metric the decoder writes is deleted after the decoder was saved.
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("metric_key = ?", "door").Delete(&MetricDefinition{}).Error)
	_, err := f.publish()
	refusal(t, err, CodeInvalidDecoder, "cannot publish device profile", "sections[0].outputs[1].key", "door")
}

func TestPublishRefusesAnOutputThatBecameCalculated(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	// door becomes calculated through a writer that bypasses the decoder check (a delete and a
	// re-create with a formula), which is what a later metric edit could do.
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("metric_key = ?", "door").Delete(&MetricDefinition{}).Error)
	expr := "temperature > 20.0 ? 1 : 0"
	_, err := f.api.CreateMetricDefinition(f.ctx, &MetricDefinitionCreateRequest{
		Token: "md-door2", DeviceProfileToken: "meter", MetricKey: "door", DataType: "INT", Expression: &expr})
	require.NoError(t, err)
	_, err = f.publish()
	refusal(t, err, CodeInvalidDecoder, "cannot publish device profile", "door", "calculated")
}

// The gate fails closed on a draft that reached the column by a route other than the setter.
func TestPublishRefusesADraftThatBypassedTheSetter(t *testing.T) {
	f := newDecoderFixture(t)
	bad := datatypes.JSON(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"nope","expr":"1"}]}]}`)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceProfile{}).Where("token = ?", "meter").
		Select("DecoderDeclaration").Updates(&DeviceProfile{DecoderDeclaration: &bad}).Error)
	_, err := f.publish()
	refusal(t, err, CodeInvalidDecoder, "nope")
	// Unparseable bytes too.
	junk := datatypes.JSON(`"junk"`)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceProfile{}).Where("token = ?", "meter").
		Select("DecoderDeclaration").Updates(&DeviceProfile{DecoderDeclaration: &junk}).Error)
	_, err = f.publish()
	refusal(t, err, CodeInvalidDecoder)
}

func TestPublishWithoutADecoderIsUnchanged(t *testing.T) {
	f := newDecoderFixture(t)
	v, err := f.publish()
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(v.Snapshot, &top))
	_, has := top["decoder"]
	assert.False(t, has, "a decoder-free profile's snapshot gained a decoder key")
}

// Rollback re-runs the gate on the target snapshot.
func TestRollbackRefusesAVersionWhoseDecoderCannotCompile(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	_, err := f.publish() // v1: good
	require.NoError(t, err)
	_, err = f.api.ClearDeviceProfileDecoder(f.ctx, "meter")
	require.NoError(t, err)
	_, err = f.publish() // v2: no decoder
	require.NoError(t, err)

	// Rolling back to v1 passes.
	_, err = f.api.RollbackDeviceProfile(f.ctx, "meter", 1)
	require.NoError(t, err)
	_, err = f.api.RollbackDeviceProfile(f.ctx, "meter", 2)
	require.NoError(t, err)

	// A version that names a function library this build does not know (a downgrade) is refused.
	p := f.profile()
	future := strings.Replace(decoderDoc, `"envVersion": 1`, `"envVersion": 2`, 1)
	snap, err := json.Marshal(map[string]any{"metrics": []any{}, "commands": []any{}, "rules": []any{},
		"decoder": json.RawMessage(future)})
	require.NoError(t, err)
	v3 := &DeviceProfileVersion{DeviceProfileId: p.ID, Version: 3, Snapshot: datatypes.JSON(snap)}
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(v3).Error)
	_, err = f.api.RollbackDeviceProfile(f.ctx, "meter", 3)
	refusal(t, err, CodeInvalidDecoder, `cannot make version 3 of device profile "meter" active`, "version 2")
	assert.EqualValues(t, 2, f.profile().ActiveVersion.Int32, "a refused rollback moved the pointer")
}

func TestRollbackRefusesAVersionWhoseVectorFails(t *testing.T) {
	f := newDecoderFixture(t)
	p := f.profile()
	defs, err := f.api.MetricDefinitionsByDeviceProfile(f.ctx, p.ID)
	require.NoError(t, err)
	for _, d := range defs {
		d.DeviceProfile = nil
	}
	bad := withDoc(t, `"door": 1}`, `"door": 0}`)
	snap, err := json.Marshal(map[string]any{
		"metrics": defs, "commands": []any{}, "rules": []any{}, "decoder": json.RawMessage(bad)})
	require.NoError(t, err)
	v := &DeviceProfileVersion{DeviceProfileId: p.ID, Version: 1, Snapshot: datatypes.JSON(snap)}
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(v).Error)
	_, err = f.api.RollbackDeviceProfile(f.ctx, "meter", 1)
	refusal(t, err, CodeInvalidDecoder, "cannot make version 1", `"room sensor"`)

	// Contrast: the same snapshot with the vector corrected rolls back.
	good := &DeviceProfileVersion{DeviceProfileId: p.ID, Version: 2}
	snap, err = json.Marshal(map[string]any{
		"metrics": defs, "commands": []any{}, "rules": []any{}, "decoder": json.RawMessage(decoderDoc)})
	require.NoError(t, err)
	good.Snapshot = datatypes.JSON(snap)
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(good).Error)
	_, err = f.api.RollbackDeviceProfile(f.ctx, "meter", 2)
	require.NoError(t, err)
}

// The decoder is tenant data: another tenant's profile of the same token is a different profile.
func TestADecoderIsPerTenant(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	other := core.WithTenant(context.Background(), "other")
	seedDeviceProfile(t, f.api, other, "meter")
	p, err := f.api.deviceProfileByToken(other, "meter")
	require.NoError(t, err)
	assert.Nil(t, p.DecoderDeclaration, "tenant other sees tenant acme's decoder")
	// Setting it for the other tenant fails on ITS metrics, not acme's.
	_, err = f.api.SetDeviceProfileDecoder(other, "meter", []byte(decoderDoc))
	refusal(t, err, CodeInvalidDecoder, "temperature")
}

func TestDecoderMetricsAdapter(t *testing.T) {
	f := newDecoderFixture(t)
	defs, err := f.api.MetricDefinitionsByDeviceProfile(f.ctx, f.profile().ID)
	require.NoError(t, err)
	m := newDecoderMetrics(defs)
	_, ok := m.MetricType("temperature")
	assert.True(t, ok)
	_, ok = m.MetricType("fahrenheit")
	assert.False(t, ok, "a calculated metric is not an output")
	_, ok = m.MetricType("nope")
	assert.False(t, ok)
	assert.NoError(t, m.ValidateMeasurement("nope", "x"), "undeclared keys pass, as the resolver passes them")
	assert.Error(t, m.ValidateMeasurement("door", "1.5"), "an INT metric refuses a fraction")
}

// A snapshot published before decoders existed has no such key and reads as no decoder: its
// profile's uplinks are flattened, as they always were.
func TestASnapshotWithoutADecoderKeyReadsAsNoDecoder(t *testing.T) {
	snap, err := parseProfileSnapshot(datatypes.JSON(`{"metrics":[],"commands":[],"rules":[]}`))
	require.NoError(t, err)
	assert.Empty(t, snap.Decoder)
	require.NoError(t, checkSnapshotDecoder(datatypes.JSON(`{"metrics":[],"commands":[],"rules":[]}`), "cannot publish device profile"))
}

// UpdateDeviceProfile saves the profile row and must not name the decoder column: a save built
// from a read that predates a concurrent setDeviceProfileDecoder would otherwise write the stale
// value back. The statement is observed, because the race itself cannot be staged.
func TestUpdatingTheProfileNeverWritesTheDecoderColumn(t *testing.T) {
	f := newDecoderFixture(t)
	require.NoError(t, f.set(decoderDoc))
	var updates []string
	require.NoError(t, f.api.RDB.Database.Callback().Update().After("gorm:update").Register("b1a:capture", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "device_profiles" {
			updates = append(updates, tx.Statement.SQL.String())
		}
	}))
	_, err := f.api.UpdateDeviceProfile(f.ctx, "meter", &DeviceProfileUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	require.NotEmpty(t, updates, "the update statement was not observed")
	for _, sql := range updates {
		assert.NotContains(t, sql, "decoder_declaration")
	}
}

// 🔴 The cap is on the STORED rendering, not the bytes sent: jsonb writes 1e100000 out in full, so
// this 18 KB document would make every read of the profile return about 200 MB. (The Postgres
// round trip itself is profile_decoder_jsonb_integration_test.go.)
func TestSetDecoderRefusesANumberThatRendersHuge(t *testing.T) {
	f := newDecoderFixture(t)
	hostile := `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true}],"table":{"v":1,"x":[` +
		strings.TrimSuffix(strings.Repeat("1e100000,", 2000), ",") + `]}}`
	require.Less(t, len(hostile), 32<<10, "under the cap as sent")
	refusal(t, f.set(hostile), CodeInvalidDecoder, "once stored")
	assert.Nil(t, f.profile().DecoderDeclaration, "nothing was written")
}

func TestSetDecoderRefusesANonObjectTable(t *testing.T) {
	f := newDecoderFixture(t)
	refusal(t, f.set(withDoc(t, `"vectors"`, `"table": [1,2], "vectors"`)), CodeInvalidDecoder, "table must be an object")
}

// A draft already stored over the cap (written before the gate measured the stored form) is never
// frozen into a snapshot, and the preview reports it instead of compiling it.
func TestAStoredDecoderOverTheCapIsRefusedOnPublishAndPreview(t *testing.T) {
	f := newDecoderFixture(t)
	big := datatypes.JSON(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true}],"table":{"x":"` +
		strings.Repeat("a", 40<<10) + `"}}`)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceProfile{}).Where("token = ?", "meter").
		Update("decoder_declaration", big).Error)

	_, err := f.publish()
	refusal(t, err, CodeInvalidDecoder, "save it again")

	res, err := f.api.TestPayloadDecoder(f.ctx, &PayloadDecoderTestRequest{ProfileToken: "meter"})
	require.NoError(t, err)
	require.NotNil(t, res.Failure)
}
