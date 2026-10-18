// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// constDefs is a meter profile whose power is calibrated per device.
func constDefs(expr string) []*MetricDefinition {
	defs := calcTestDefs()[:2]
	p := &MetricDefinition{MetricKey: "power", DataType: "DOUBLE", Unit: sql.NullString{String: "W", Valid: true},
		Expression: sql.NullString{String: expr, Valid: true}}
	p.ID = 3
	return append(defs, p)
}

func meta(s string) *datatypes.JSON {
	j := datatypes.JSON(s)
	return &j
}

// evaluateWith is evaluate with a device's metadata, as the resolver passes it.
func evaluateWith(r ProfileResolution, md *datatypes.JSON, values map[string]string) ([]ResolvedMeasurementEntry, *recordingObserver) {
	obs := &recordingObserver{}
	calc := r.BeginCalculation(r.DeviceConstants(md))
	defer calc.Release()
	for k, v := range values {
		if m, ok := r.Metric(k); ok && !m.Calculated() {
			calc.Set(&m, v)
		}
	}
	return calc.Append(nil, obs), obs
}

// TestADeviceConstantIsReadFromTheDeviceMetadata is the slice's end to end: the same reading
// from two devices computes two values, each from its own metadata; a device without the
// constant, or with one that is not a number, gets NO value rather than a plausible one.
func TestADeviceConstantIsReadFromTheDeviceMetadata(t *testing.T) {
	r := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@1"}, constDefs("voltage * current * device.calibration"))
	in := map[string]string{"voltage": "2", "current": "3"}

	cases := []struct {
		name string
		md   *datatypes.JSON
		want string // "" = skipped
		skip string
	}{
		{"a device with a calibration", meta(`{"calibration": 1.5}`), "9", ""},
		{"another device, another value", meta(`{"calibration": 2, "site": "north"}`), "12", ""},
		{"an integer and an exponent are numbers", meta(`{"calibration": 1e0}`), "6", ""},
		{"negative", meta(`{"calibration": -0.5}`), "-3", ""},
		{"no metadata at all", nil, "", "power:missing_input"},
		{"metadata without the key", meta(`{"site": "north"}`), "", "power:missing_input"},
		{"a string is not coerced", meta(`{"calibration": "1.5"}`), "", "power:invalid_input"},
		{"a boolean", meta(`{"calibration": true}`), "", "power:invalid_input"},
		{"null", meta(`{"calibration": null}`), "", "power:invalid_input"},
		{"an object", meta(`{"calibration": {"v": 1}}`), "", "power:invalid_input"},
		{"an array", meta(`{"calibration": [1]}`), "", "power:invalid_input"},
		{"out of range", meta(`{"calibration": 1e999}`), "", "power:invalid_input"},
		{"metadata that is not an object", meta(`[1,2]`), "", "power:missing_input"},
		{"metadata that is damaged", meta(`{"calibration": 1.5`), "", "power:missing_input"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, obs := evaluateWith(r, c.md, in)
			if c.want == "" {
				assert.Empty(t, got)
				assert.Equal(t, []string{c.skip}, obs.skipped)
				return
			}
			require.Len(t, got, 1, "skipped: %v", obs.skipped)
			assert.Equal(t, c.want, got[0].Value)
			assert.True(t, got[0].Derived)
		})
	}
}

// TestAConstantIsFrozenIntoTheValue: the value computed from a device's constants is a string
// like any other, and editing the device afterwards cannot change it — nothing in the
// resolution or the entry points back at the metadata.
func TestAConstantIsFrozenIntoTheValue(t *testing.T) {
	r := NewProfileResolution(ProfileScope{}, constDefs("voltage * device.calibration"))
	md := meta(`{"calibration": 1.5}`)
	got, _ := evaluateWith(r, md, map[string]string{"voltage": "2"})
	require.Len(t, got, 1)
	require.Equal(t, "3", got[0].Value)

	*md = datatypes.JSON(`{"calibration": 100}`) // the device is edited after the value was computed
	assert.Equal(t, "3", got[0].Value, "a computed value does not follow the device")

	again, _ := evaluateWith(r, md, map[string]string{"voltage": "2"})
	require.Len(t, again, 1)
	assert.Equal(t, "200", again[0].Value, "the next reading uses the device as it is")
}

// TestAProfileWithoutConstantsNeverTouchesMetadata is the pay-nothing pin: a resolution whose
// formulas read no constant returns nil from DeviceConstants whatever the metadata holds, so
// the document is not even looked at.
func TestAProfileWithoutConstantsNeverTouchesMetadata(t *testing.T) {
	for name, r := range map[string]ProfileResolution{
		"no formulas":            NewProfileResolution(ProfileScope{}, calcTestDefs()[:2]),
		"formulas, no constants": NewProfileResolution(ProfileScope{}, calcTestDefs()),
	} {
		assert.Nil(t, r.DeviceConstants(meta(`{"calibration": 1.5}`)), name)
		assert.Empty(t, r.ConstantKeys(), name)
	}
	r := NewProfileResolution(ProfileScope{}, constDefs("voltage * device.calibration"))
	assert.Equal(t, []string{"calibration"}, r.ConstantKeys())
	assert.Nil(t, r.DeviceConstants(nil), "a device with no metadata has no constants")
}

// TestConstantKeysAreTheUnionOfTheFormulas: every constant any formula reads is read once.
func TestConstantKeysAreTheUnionOfTheFormulas(t *testing.T) {
	defs := constDefs("voltage * device.b + device.a")
	q := &MetricDefinition{MetricKey: "amps2", DataType: "DOUBLE",
		Expression: sql.NullString{String: "current * device.b * device[\"c-d\"]", Valid: true}}
	q.ID = 4
	r := NewProfileResolution(ProfileScope{}, append(defs, q))
	assert.Equal(t, []string{"a", "b", "c-d"}, r.ConstantKeys())
}

// TestConstantsSurviveTheCache: a decoded resolution compiles its own plan and reads the
// same constants; the cached wire form carries no constants.
func TestConstantsSurviveTheCache(t *testing.T) {
	built := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@3"}, constDefs("voltage * device.calibration"))
	wire, err := json.Marshal(built)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "calibration\":", "no constant value is ever cached")
	decoded, err := decodeProfileResolution(wire)
	require.NoError(t, err)
	assert.Equal(t, []string{"calibration"}, decoded.ConstantKeys())
	got, _ := evaluateWith(*decoded, meta(`{"calibration": 4}`), map[string]string{"voltage": "2"})
	require.Len(t, got, 1)
	assert.Equal(t, "8", got[0].Value)
}

// TestRecalculateUsesTheConstantsItIsGiven: the draft recalculation computes with the
// device constants its caller supplies (the device's current ones), and a caller that supplies
// none gets missing_input, never a default.
func TestRecalculateUsesTheConstantsItIsGiven(t *testing.T) {
	draft := NewProfileResolution(ProfileScope{}, constDefs("voltage * device.calibration"))
	stored := []ResolvedMeasurementEntry{{Name: "voltage", Value: "2"}}

	got := draft.Recalculate(stored, draft.DeviceConstants(meta(`{"calibration": 3}`)), nopObserver{})
	require.Len(t, got, 2)
	assert.Equal(t, "power", got[0].Name)
	assert.Equal(t, "6", got[0].Value)

	obs := &recordingObserver{}
	got = draft.Recalculate(stored, nil, obs)
	assert.Len(t, got, 1)
	assert.Equal(t, []string{"power:missing_input"}, obs.skipped)
}

// TestReadConstantsDuplicateAndEscapedKeys pins the two corners a byte scanner could get
// wrong: a key repeated in the document (the last wins) and a key written with escapes.
func TestReadConstantsDuplicateAndEscapedKeys(t *testing.T) {
	keys := []string{"cal", "é"}
	got := readConstants([]byte(`{"cal": 1, "x": {"cal": 99, "a\"b": [1, {"cal": 98}]}, "cal": 2, "é": 3}`), keys)
	require.Len(t, got, 2)
	byKey := map[string]calcfield.Constant{}
	for _, c := range got {
		byKey[c.Key()] = c
	}
	require.Contains(t, byKey, "cal")
	require.Contains(t, byKey, "é")
	ctx := calcfield.ConstantFromString("cal", "2")
	assert.Equal(t, fmt.Sprint(ctx), fmt.Sprint(byKey["cal"]), "the last repeated key wins, and a nested key is not a top-level one")
	assert.Equal(t, fmt.Sprint(calcfield.ConstantFromString("é", "3")), fmt.Sprint(byKey["é"]))
}

// TestReadConstantsRefusesWhatIsNotAnObject: nothing but a well-formed object supplies any
// constant, so a damaged document makes every constant absent rather than some of them.
func TestReadConstantsRefusesWhatIsNotAnObject(t *testing.T) {
	keys := []string{"a"}
	for _, doc := range []string{``, ` `, `null`, `1`, `"a"`, `[]`, `{`, `{"a"`, `{"a":`, `{"a":1`, `{"a":1,`, `{"a":1,}`,
		`{"a" 1}`, `{a:1}`, `{"a":1 "b":2}`, `{"a":"unterminated}`, `{"a":[1}`, `{"a":,}`, `{,}`} {
		assert.Nil(t, readConstants([]byte(doc), keys), "%q", doc)
	}
	assert.NotNil(t, readConstants([]byte(`{}`), keys), "an empty object is readable and has none")
	assert.Empty(t, readConstants([]byte(` { } `), keys))
}

// TestReadConstantsAgreesWithEncodingJSON holds the hand-written scanner to encoding/json on
// generated documents: nested containers, strings full of braces, quotes, commas and escapes,
// repeated keys, every scalar kind, odd whitespace. For each wanted key the outcome must be
// the one a standard decode gives.
func TestReadConstantsAgreesWithEncodingJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(20261017))
	names := []string{"cal", "k1", "k-2", "é", "a b", "", "q\"uote", "back\\slash", "{", "]"}
	keys := []string{"cal", "k1", "k-2", "é", "q\"uote"}
	var scalar func() any
	var value func(depth int) any
	scalar = func() any {
		switch rng.Intn(9) {
		case 0:
			return rng.Intn(1000) - 500
		case 1:
			return rng.Float64()*200 - 100
		case 2:
			return rng.Float64() * 1e-8
		case 3:
			return "str, with } and ] and \" and \\ and é"
		case 4:
			return true
		case 5:
			return nil
		case 6:
			return "1.5"
		case 7:
			return 1e21
		default:
			return rng.Int63()
		}
	}
	value = func(depth int) any {
		if depth > 3 || rng.Intn(3) > 0 {
			return scalar()
		}
		if rng.Intn(2) == 0 {
			arr := []any{}
			for range rng.Intn(4) {
				arr = append(arr, value(depth+1))
			}
			return arr
		}
		obj := map[string]any{}
		for range rng.Intn(4) {
			obj[names[rng.Intn(len(names))]] = value(depth + 1)
		}
		return obj
	}
	space := []string{"", " ", "\n\t ", "  "}
	for n := range 3000 {
		var sb strings.Builder
		sb.WriteString(space[rng.Intn(len(space))] + "{")
		members := rng.Intn(7)
		for i := range members {
			name := names[rng.Intn(len(names))]
			enc, err := json.Marshal(value(0))
			require.NoError(t, err)
			kb, _ := json.Marshal(name)
			if i > 0 {
				sb.WriteString(space[rng.Intn(len(space))] + "," + space[rng.Intn(len(space))])
			}
			sb.WriteString(space[rng.Intn(len(space))] + string(kb) + space[rng.Intn(len(space))] + ":" +
				space[rng.Intn(len(space))] + string(enc) + space[rng.Intn(len(space))])
		}
		sb.WriteString("}" + space[rng.Intn(len(space))])
		doc := sb.String()

		var ref map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(doc), &ref), "generator produced invalid JSON: %s", doc)

		got := map[string]string{}
		for _, c := range readConstants([]byte(doc), keys) {
			got[c.Key()] = fmt.Sprint(c)
		}
		expect := map[string]string{}
		for _, k := range keys {
			if raw, ok := ref[k]; ok {
				expect[k] = fmt.Sprint(constantOf(k, raw))
			}
		}
		require.Equal(t, expect, got, "case %d: %s", n, doc)
	}
}

// TestConstantAccessorsHandOutCopies backs the exemption of ConstantKeys and DeviceConstants
// in TestAResolutionHandsOutNothingWritable: what they return is the caller's to change.
func TestConstantAccessorsHandOutCopies(t *testing.T) {
	r := NewProfileResolution(ProfileScope{}, constDefs("voltage * device.calibration"))
	keys := r.ConstantKeys()
	keys[0] = "mutated"
	assert.Equal(t, []string{"calibration"}, r.ConstantKeys())

	md := meta(`{"calibration": 1.5}`)
	a, b := r.DeviceConstants(md), r.DeviceConstants(md)
	require.Len(t, a, 1)
	a[0] = calcfield.InvalidConstant("calibration")
	assert.Equal(t, "calibration", b[0].Key())
	got, _ := evaluateWith(r, md, map[string]string{"voltage": "2"})
	require.Len(t, got, 1, "a caller changing its constants cannot change the resolution")
	assert.Equal(t, "3", got[0].Value)
}
