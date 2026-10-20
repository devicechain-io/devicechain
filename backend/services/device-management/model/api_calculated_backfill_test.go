// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// backfillFixture is a profile "meter" on a database that also holds devices, with a ceiling
// that admits calculated metrics.
func backfillFixture(t *testing.T) *calcFixture {
	t.Helper()
	f := newCalcFixture(t)
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		AutoMigrate(&Device{}, &GeoFence{}, &GeoFenceSetVersion{}, &GeoFenceGeometryBlob{}))
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: 10}
	return f
}

func (f *calcFixture) setExpression(key, expr string) {
	f.t.Helper()
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&MetricDefinition{}).Where("metric_key = ?", key).
		Update("expression", expr).Error)
}

func (f *calcFixture) dropMetric(key string) {
	f.t.Helper()
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("metric_key = ?", key).Delete(&MetricDefinition{}).Error)
}

// The plan door describes exactly the plan the live resolver compiles for the same version: the
// door's answer, compiled with calcfield.NewPlan, has the same steps in the same order reading the
// same keys and constants as the plan inside the ProfileResolution the resolver builds from the
// version row. A chain (kw reads power) and a constant (gain) are in it, and kw is declared before
// power so an order taken from declarations would differ from the evaluation order.
func TestPlanDoorDescribesThePlanTheResolverCompiles(t *testing.T) {
	f := backfillFixture(t)
	f.metric("voltage", "DOUBLE", "")
	f.metric("current", "DOUBLE", "")
	f.metric("kw", "DOUBLE", "power / 1000.0 * device.gain", func(r *MetricDefinitionCreateRequest) {
		min := 0.0
		r.MinValue = &min
	})
	f.metric("power", "DOUBLE", "voltage * current")
	f.metric("flag", "BOOLEAN", "power > 10.0")
	require.NoError(t, f.publish())

	got, err := f.api.CalculatedMetricPlan(f.ctx, "meter", nil)
	require.NoError(t, err)
	assert.Equal(t, "meter@1", got.VersionToken)

	// The resolver's side: the resolution built from the same version row.
	var profile DeviceProfile
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("token = ?", "meter").First(&profile).Error)
	snap, err := f.api.profileVersionSnapshot(f.ctx, profile.ID, 1)
	require.NoError(t, err)
	live := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@1"}, snap.Metrics)
	require.NotNil(t, live.calc)
	require.NotNil(t, live.calc.plan)

	// Compile the door's output the way a worker would, handing the definitions over in the
	// reverse order: the plan's order must come from the graph, not from the input.
	inputs := make([]calcfield.Input, 0)
	for _, m := range got.Measured {
		ty, ok := calcfield.ParseType(m.DataType)
		require.True(t, ok)
		inputs = append(inputs, calcfield.Input{Key: m.Key, Type: ty})
	}
	defs := make([]calcfield.Definition, 0)
	for _, m := range got.Calculated {
		ty, ok := calcfield.ParseType(m.DataType)
		require.True(t, ok)
		defs = append(defs, calcfield.Definition{Key: m.Key, Expression: m.Expression, Type: ty, Min: m.MinValue, Max: m.MaxValue})
	}
	for i, j := 0, len(defs)-1; i < j; i, j = i+1, j-1 {
		defs[i], defs[j] = defs[j], defs[i]
	}
	fromDoor, err := calcfield.NewPlan(inputs, defs)
	require.NoError(t, err)

	require.Equal(t, live.calc.plan.Len(), fromDoor.Len())
	require.Equal(t, live.calc.plan.Len(), len(got.Calculated))
	for i := 0; i < fromDoor.Len(); i++ {
		a, b := live.calc.plan.Step(i), fromDoor.Step(i)
		assert.Equal(t, a.Key, b.Key, "step %d", i)
		assert.Equal(t, a.Key, got.Calculated[i].Key, "the door lists the live plan's order")
		require.NotNil(t, a.Formula)
		require.NotNil(t, b.Formula)
		assert.Equal(t, a.Formula.InputKeys(), b.Formula.InputKeys(), "%s reads the same keys", a.Key)
		assert.Equal(t, a.Formula.ConstantKeys(), b.Formula.ConstantKeys(), "%s reads the same constants", a.Key)
		assert.Equal(t, a.Formula.Expression(), b.Formula.Expression())
		assert.Equal(t, a.Depth, b.Depth)
	}
	assert.Equal(t, live.ConstantKeys(), got.ConstantKeys)
	assert.Equal(t, []string{"gain"}, got.ConstantKeys)

	order := map[string]int{}
	for i, m := range got.Calculated {
		order[m.Key] = i
	}
	assert.Less(t, order["power"], order["kw"])
	assert.Equal(t, []CalculatedPlanInput{{"voltage", "DOUBLE"}, {"current", "DOUBLE"}}, got.Measured)
	// The classifier and type are the stamp the live entries carry.
	for i, meta := range live.calc.metas {
		assert.Equal(t, meta.classifier, got.Calculated[i].Classifier)
		assert.Equal(t, meta.dataType, got.Calculated[i].DataType)
	}
	// The bound travels with the definition.
	for _, m := range got.Calculated {
		if m.Key == "kw" {
			require.NotNil(t, m.MinValue)
			assert.Equal(t, 0.0, *m.MinValue)
			assert.Nil(t, m.MaxValue)
		}
	}
}

// A version with no calculated metric has an empty plan, not an error and not nil lists.
func TestPlanDoorOfAVersionWithoutFormulasIsEmpty(t *testing.T) {
	f := backfillFixture(t)
	f.metric("voltage", "DOUBLE", "")
	require.NoError(t, f.publish())
	got, err := f.api.CalculatedMetricPlan(f.ctx, "meter", nil)
	require.NoError(t, err)
	assert.Empty(t, got.Calculated)
	assert.NotNil(t, got.Calculated)
	assert.NotNil(t, got.ConstantKeys)
}

// newInVersion / changedSinceEarlier across versions:
//
//	v1: a = x*2, b = x+1
//	v2: a = x*2, b = x+2 (changed), c = x (new)
//	v3: a = x*3 (changed), b = x+2, c dropped, d new
//	v4: c reinstated unchanged
func TestPlanDoorFlagsNewAndChangedAcrossVersions(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	f.metric("a", "DOUBLE", "x * 2.0")
	f.metric("b", "DOUBLE", "x + 1.0")
	require.NoError(t, f.publish())

	f.setExpression("b", "x + 2.0")
	f.metric("c", "DOUBLE", "x")
	require.NoError(t, f.publish())

	f.setExpression("a", "x * 3.0")
	f.dropMetric("c")
	f.metric("d", "DOUBLE", "x - 1.0")
	require.NoError(t, f.publish())

	type flags struct{ isNew, changed bool }
	read := func(version int32) map[string]flags {
		p, err := f.api.CalculatedMetricPlan(f.ctx, "meter", &version)
		require.NoError(t, err)
		out := map[string]flags{}
		for _, m := range p.Calculated {
			out[m.Key] = flags{m.NewInVersion, m.ChangedSinceEarlier}
		}
		return out
	}
	assert.Equal(t, map[string]flags{"a": {true, false}, "b": {true, false}}, read(1))
	assert.Equal(t, map[string]flags{"a": {false, false}, "b": {false, true}, "c": {true, false}}, read(2))
	assert.Equal(t, map[string]flags{"a": {false, true}, "b": {false, true}, "d": {true, false}}, read(3))

	f.metric("c", "DOUBLE", "x")
	require.NoError(t, f.publish())
	assert.Equal(t, flags{true, false}, read(4)["c"], "reinstated unchanged: new again, not changed")

	// The active version carries its activation instant; an earlier one does not.
	active, err := f.api.CalculatedMetricPlan(f.ctx, "meter", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(4), active.Version)
	assert.NotNil(t, active.ActiveSince)
	v2 := int32(2)
	earlier, err := f.api.CalculatedMetricPlan(f.ctx, "meter", &v2)
	require.NoError(t, err)
	assert.Nil(t, earlier.ActiveSince)
}

// A draft is not a version: an unpublished profile, a version never published and an unknown
// profile are all not found, and another tenant cannot see the profile.
func TestPlanDoorRefusesWhatIsNotAPublishedVersion(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	f.metric("a", "DOUBLE", "x * 2.0") // a draft formula, never published
	_, err := f.api.CalculatedMetricPlan(f.ctx, "meter", nil)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "never published: %v", err)
	one := int32(1)
	_, err = f.api.CalculatedMetricPlan(f.ctx, "meter", &one)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "version 1 is only a draft: %v", err)

	require.NoError(t, f.publish())
	for _, v := range []int32{0, 2, -1} {
		v := v
		_, err = f.api.CalculatedMetricPlan(f.ctx, "meter", &v)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "version %d: %v", v, err)
	}
	_, err = f.api.CalculatedMetricPlan(f.ctx, "nope", nil)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	_, err = f.api.CalculatedMetricPlan(core.WithTenant(context.Background(), "other"), "meter", &one)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "another tenant's profile is not found: %v", err)
}

func (f *calcFixture) adoptingType() *DeviceType {
	f.t.Helper()
	var profile DeviceProfile
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Where("token = ?", "meter").First(&profile).Error)
	dt := &DeviceType{ProfileId: &profile.ID}
	dt.Token = "meter-type"
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Create(dt).Error)
	return dt
}

func (f *calcFixture) device(dt *DeviceType, token string, metadata string) *Device {
	f.t.Helper()
	d := &Device{DeviceTypeId: dt.ID}
	d.Token = token
	if metadata != "" {
		md := datatypes.JSON(metadata)
		d.Metadata = &md
	}
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Create(d).Error)
	return d
}

// The roster lists EVERY device of the profile (one with no metadata, and one holding none of the
// requested names, included), with createdAt and the constants, and refuses what it must.
func TestRosterListsEveryDeviceWithItsConstants(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	require.NoError(t, f.publish())
	dt := f.adoptingType()
	f.device(dt, "d-gain", `{"gain": 2.5, "site": "n"}`)
	f.device(dt, "d-bare", "")
	f.device(dt, "d-text", `{"gain": "high"}`)
	f.device(dt, "d-other", `{"unrelated": 1}`)
	// A device of another profile is not on the roster.
	other := &DeviceProfile{}
	other.Token = "other"
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(other).Error)
	odt := &DeviceType{ProfileId: &other.ID}
	odt.Token = "other-type"
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(odt).Error)
	f.device(odt, "d-foreign", `{"gain": 9}`)

	page, err := f.api.CalculatedBackfillRoster(f.ctx, "meter", []string{"gain"}, nil, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 4)
	require.NotNil(t, page.Total)
	assert.Equal(t, int64(4), *page.Total)
	assert.Nil(t, page.NextCursor)
	byTok := map[string]BackfillRosterEntry{}
	for _, e := range page.Entries {
		byTok[e.DeviceToken] = e
		assert.False(t, e.CreatedAt.IsZero(), e.DeviceToken)
		assert.NotNil(t, e.Constants)
	}
	require.Len(t, byTok["d-gain"].Constants, 1)
	require.NotNil(t, byTok["d-gain"].Constants[0].Number)
	assert.Equal(t, 2.5, *byTok["d-gain"].Constants[0].Number)
	assert.Empty(t, byTok["d-bare"].Constants)
	assert.Empty(t, byTok["d-other"].Constants)
	require.Len(t, byTok["d-text"].Constants, 1)
	assert.Nil(t, byTok["d-text"].Constants[0].Number, "present and unusable is not absent")
	_, foreign := byTok["d-foreign"]
	assert.False(t, foreign)

	// No constants asked for: still every device.
	page, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, 0, 10)
	require.NoError(t, err)
	assert.Len(t, page.Entries, 4)

	// A token filter narrows; a foreign or unknown token is refused by name; a duplicate is fine.
	page, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, []string{"d-bare", "d-bare"}, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, int64(1), *page.Total)
	for _, bad := range []string{"d-foreign", "nobody"} {
		_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, []string{"d-bare", bad}, 0, 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), bad)
		assert.Equal(t, "INVALID_VALUE", codeOf(err))
	}

	// Bounds.
	for _, limit := range []int{0, -1, MaxBackfillRosterPageSize + 1} {
		_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, 0, limit)
		assert.Equal(t, "INVALID_VALUE", codeOf(err), "limit %d: %v", limit, err)
	}
	many := make([]string, calcfield.MaxProfileConstants+1)
	for i := range many {
		many[i] = fmt.Sprintf("k%d", i)
	}
	_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", many, nil, 0, 10)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", []string{""}, nil, 0, 10)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	tooMany := make([]string, MaxBackfillRosterDeviceTokens+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("t%d", i)
	}
	_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, tooMany, 0, 10)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))

	// Another tenant cannot name the profile, let alone list its devices.
	_, err = f.api.CalculatedBackfillRoster(core.WithTenant(context.Background(), "other"), "meter", nil, nil, 0, 10)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

// A walk in pages of 3 over 10 devices sees each device exactly once, in id order, and a device
// created between pages (a higher id) is seen later in the same walk, never twice and never
// skipping an existing one. Only the first page carries a total.
func TestRosterKeysetWalkHasNoDuplicatesOrGapsUnderInserts(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	require.NoError(t, f.publish())
	dt := f.adoptingType()
	for i := 0; i < 10; i++ {
		f.device(dt, fmt.Sprintf("d%02d", i), "")
	}

	seen := []string{}
	var after uint64
	pages, inserted := 0, 0
	for {
		page, err := f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, after, 3)
		require.NoError(t, err)
		if pages == 0 {
			require.NotNil(t, page.Total)
			assert.Equal(t, int64(10), *page.Total)
		} else {
			assert.Nil(t, page.Total, "total only on the first page")
		}
		for _, e := range page.Entries {
			seen = append(seen, e.DeviceToken)
		}
		pages++
		// Concurrent inserts between pages.
		if pages <= 2 {
			f.device(dt, fmt.Sprintf("late%d", pages), "")
			inserted++
		}
		if page.NextCursor == nil {
			break
		}
		require.Greater(t, *page.NextCursor, after)
		after = *page.NextCursor
	}
	require.Len(t, seen, 10+inserted)
	uniq := map[string]bool{}
	for _, s := range seen {
		assert.False(t, uniq[s], "duplicate %s", s)
		uniq[s] = true
	}
	for i := 0; i < 10; i++ {
		assert.True(t, uniq[fmt.Sprintf("d%02d", i)], "gap: d%02d", i)
	}
	// Strictly after: resuming at a cursor never repeats the cursor's own device.
	first, err := f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, 0, 1)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	second, err := f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, *first.NextCursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Entries, 1)
	assert.NotEqual(t, first.Entries[0].DeviceToken, second.Entries[0].DeviceToken)
}

// The flags each compare against the right versions:
//
//	v1: a = x*2, b = x > 1 ? 1 : 0 (DOUBLE), e = x
//	v2: a gains a lower bound (bounds only), b becomes the BOOLEAN x > 1, e dropped
//	v3: e reinstated unchanged
//
// newInVersion reads the IMMEDIATELY previous version, not the first one (e is in v1, absent
// from v2, so it is new in v3), and changedSinceEarlier sees a bounds-only edit. (A type-only edit
// cannot publish: the gate type-checks the expression against the declared type, so a type change
// always carries a different expression.)
func TestPlanDoorFlagsReadThePreviousVersionAndEveryDeclaredField(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	f.metric("a", "DOUBLE", "x * 2.0")
	f.metric("b", "DOUBLE", "x > 1.0 ? 1.0 : 0.0")
	f.metric("e", "DOUBLE", "x")
	require.NoError(t, f.publish())

	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&MetricDefinition{}).Where("metric_key = ?", "a").
		Update("min_value", 0.0).Error)
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&MetricDefinition{}).Where("metric_key = ?", "b").
		Updates(map[string]any{"data_type": "BOOLEAN", "expression": "x > 1.0"}).Error)
	f.dropMetric("e")
	require.NoError(t, f.publish())
	f.metric("e", "DOUBLE", "x")
	require.NoError(t, f.publish())

	flagsOf := func(version int32) map[string][2]bool {
		p, err := f.api.CalculatedMetricPlan(f.ctx, "meter", &version)
		require.NoError(t, err)
		out := map[string][2]bool{}
		for _, m := range p.Calculated {
			out[m.Key] = [2]bool{m.NewInVersion, m.ChangedSinceEarlier}
		}
		return out
	}
	v2 := flagsOf(2)
	assert.Equal(t, [2]bool{false, true}, v2["a"], "a bounds-only edit is a change")
	assert.Equal(t, [2]bool{false, true}, v2["b"], "a retyped formula is a change")
	assert.Equal(t, [2]bool{true, false}, flagsOf(3)["e"],
		"absent from the previous version: new, though the first version had it")
}

// A deleted device, and every device of a deleted type, are off the roster and are refused by
// token: neither is a device the live resolver would compute for.
func TestRosterLeavesOutDeletedDevicesAndTypes(t *testing.T) {
	f := backfillFixture(t)
	f.metric("x", "DOUBLE", "")
	require.NoError(t, f.publish())
	dt := f.adoptingType()
	f.device(dt, "kept", "")
	gone := f.device(dt, "gone", "")
	require.NoError(t, f.api.RDB.DB(f.ctx).Delete(gone).Error)

	var profile DeviceProfile
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("token = ?", "meter").First(&profile).Error)
	dead := &DeviceType{ProfileId: &profile.ID}
	dead.Token = "dead-type"
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(dead).Error)
	f.device(dead, "orphan", "")
	require.NoError(t, f.api.RDB.DB(f.ctx).Delete(dead).Error)

	page, err := f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, nil, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "kept", page.Entries[0].DeviceToken)
	assert.Equal(t, int64(1), *page.Total)
	for _, tok := range []string{"gone", "orphan"} {
		_, err = f.api.CalculatedBackfillRoster(f.ctx, "meter", nil, []string{tok}, 0, 10)
		assert.Equal(t, "INVALID_VALUE", codeOf(err), "%s: %v", tok, err)
	}
}

// otherProfile creates a profile in ctx's tenant with the given metrics (an expression of "" is
// measured) and publishes it when publish is set.
func (f *calcFixture) otherProfile(ctx context.Context, token string, publish bool, metrics map[string]string) {
	f.t.Helper()
	p := &DeviceProfile{}
	p.Token = token
	require.NoError(f.t, f.api.RDB.DB(ctx).Create(p).Error)
	for key, expr := range metrics {
		req := &MetricDefinitionCreateRequest{Token: token + "-" + key, DeviceProfileToken: token, MetricKey: key, DataType: "DOUBLE"}
		if expr != "" {
			e := expr
			req.Expression = &e
		}
		_, err := f.api.CreateMetricDefinition(ctx, req)
		require.NoError(f.t, err)
	}
	if publish {
		_, err := f.api.PublishDeviceProfile(ctx, token, nil, nil, "tester")
		require.NoError(f.t, err)
	}
}

func sharedFlags(t *testing.T, f *calcFixture) map[string]bool {
	t.Helper()
	got, err := f.api.CalculatedMetricPlan(f.ctx, "meter", nil)
	require.NoError(t, err)
	out := map[string]bool{}
	for _, c := range got.Calculated {
		out[c.Key] = c.NameSharedWithOtherProfiles
	}
	return out
}

// A calculated metric's name is flagged when another profile of the tenant has a metric of that
// key in its ACTIVE version, measured or calculated; the profile's own other versions, a draft
// profile, a superseded version of another profile and another tenant's profile do not count.
func TestPlanDoorFlagsNamesSharedWithOtherProfiles(t *testing.T) {
	f := backfillFixture(t)
	f.metric("voltage", "DOUBLE", "")
	for _, k := range []string{"a_measured", "b_calculated", "c_own", "d_draft", "e_superseded", "f_foreign", "g_unique", "h_pct%_"} {
		f.metric(k, "DOUBLE", "voltage * 2.0")
	}
	require.NoError(t, f.publish())
	// The profile's own earlier version carries c_own too: a second version, same keys.
	require.NoError(t, f.publish())

	f.otherProfile(f.ctx, "other-measured", true, map[string]string{"a_measured": ""})
	f.otherProfile(f.ctx, "other-calc", true, map[string]string{"voltage": "", "b_calculated": "voltage * 3.0"})
	f.otherProfile(f.ctx, "other-draft", false, map[string]string{"d_draft": ""})
	// Superseded: v1 declared e_superseded, the active v2 does not.
	f.otherProfile(f.ctx, "other-moved", true, map[string]string{"e_superseded": ""})
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("metric_key = ?", "e_superseded").Delete(&MetricDefinition{}).Error)
	_, err := f.api.PublishDeviceProfile(f.ctx, "other-moved", nil, nil, "tester")
	require.NoError(t, err)
	// Another tenant's profile.
	foreign := core.WithTenant(context.Background(), "globex")
	f.otherProfile(foreign, "foreign", true, map[string]string{"f_foreign": ""})

	got := sharedFlags(t, f)
	assert.Equal(t, map[string]bool{
		"a_measured": true, "b_calculated": true, "c_own": false, "d_draft": false,
		"e_superseded": false, "f_foreign": false, "g_unique": false, "h_pct%_": false,
	}, got)
}

// LIKE wildcards in a key are literal in the pre-filter.
func TestPlanDoorSharedNameWildcardsAreLiteral(t *testing.T) {
	f := backfillFixture(t)
	f.metric("voltage", "DOUBLE", "")
	f.metric("h_pct%_", "DOUBLE", "voltage * 2.0")
	require.NoError(t, f.publish())
	f.otherProfile(f.ctx, "other", true, map[string]string{"hXpctYZ": ""})
	assert.False(t, sharedFlags(t, f)["h_pct%_"])
	f.otherProfile(f.ctx, "other2", true, map[string]string{"h_pct%_": ""})
	assert.True(t, sharedFlags(t, f)["h_pct%_"])
}
