// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Per-device constants at resolve (ADR-082, S6b): the formula reads a number from the device
// record the resolver already holds, and the value is frozen into the computed measurement.

func meterWithMetadata(md string) *model.Device {
	d := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
	if md != "" {
		j := datatypes.JSON(md)
		d.Metadata = &j
	}
	return d
}

func calibratedPower() model.ProfileResolution {
	return meterResolution(calculatedDef(5, "power_cal", model.MetricDouble, "kW",
		"voltage * current / 1000 * device.calibration"))
}

func value(t *testing.T, p *resolvedevent.ResolvedMeasurementsPayload, sample int, name string) (string, bool) {
	t.Helper()
	for _, e := range p.Entries[sample].Entries {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestTwoDevicesComputeFromTheirOwnConstants(t *testing.T) {
	profile := calibratedPower()
	reading := map[string]string{"voltage": "200", "current": "10"}

	a := resolveSamplesFor(t, meterWithMetadata(`{"calibration": 1.5}`), profile, testCalcMetrics(), reading)
	b := resolveSamplesFor(t, meterWithMetadata(`{"calibration": 0.5, "site": "north"}`), profile, testCalcMetrics(), reading)

	va, ok := value(t, a, 0, "power_cal")
	require.True(t, ok)
	vb, ok := value(t, b, 0, "power_cal")
	require.True(t, ok)
	assert.Equal(t, "3", va)
	assert.Equal(t, "1", vb)

	// The computed entry is stamped like any calculated value: derived, classifier, unit.
	for _, e := range a.Entries[0].Entries {
		if e.Name == "power_cal" {
			assert.True(t, e.Derived)
			assert.EqualValues(t, 5, *e.Classifier)
			assert.Equal(t, "kW", *e.Unit)
		}
	}
}

func TestEverySampleEntryOfAnEventUsesTheDevicesConstants(t *testing.T) {
	p := resolveSamplesFor(t, meterWithMetadata(`{"calibration": 2}`), calibratedPower(), testCalcMetrics(),
		map[string]string{"voltage": "100", "current": "10"},
		map[string]string{"voltage": "200", "current": "10"},
		map[string]string{"voltage": "300", "current": "10"})
	for i, want := range []string{"2", "4", "6"} {
		got, ok := value(t, p, i, "power_cal")
		require.True(t, ok, "sample %d", i)
		assert.Equal(t, want, got, "sample %d", i)
	}
}

func TestAnAbsentOrUnusableConstantSkipsTheValueAndKeepsTheReading(t *testing.T) {
	reading := map[string]string{"voltage": "200", "current": "10"}
	for name, c := range map[string]struct {
		md     string
		reason calcfield.SkipReason
	}{
		"no metadata":      {"", calcfield.SkipMissingInput},
		"another key":      {`{"site": "north"}`, calcfield.SkipMissingInput},
		"a string":         {`{"calibration": "1.5"}`, calcfield.SkipInvalidInput},
		"a boolean":        {`{"calibration": false}`, calcfield.SkipInvalidInput},
		"damaged metadata": {`{"calibration": 1.5`, calcfield.SkipMissingInput},
	} {
		t.Run(name, func(t *testing.T) {
			metrics := testCalcMetrics()
			p := resolveSamplesFor(t, meterWithMetadata(c.md), calibratedPower(), metrics, reading)
			_, computed := value(t, p, 0, "power_cal")
			assert.False(t, computed, "no value is substituted")
			v, kept := value(t, p, 0, "voltage")
			require.True(t, kept, "the reading itself is stored")
			assert.Equal(t, "200", v)
			assert.Equal(t, 1.0, metrics.skips(c.reason))
		})
	}
}

// A computed value does not follow the device: the same reading resolved again after the
// device's metadata changed computes from the new constant, and the payload resolved before
// is untouched.
func TestAConstantIsFrozenAtResolve(t *testing.T) {
	profile := calibratedPower()
	reading := map[string]string{"voltage": "200", "current": "10"}
	device := meterWithMetadata(`{"calibration": 1}`)
	before := resolveSamplesFor(t, device, profile, testCalcMetrics(), reading)

	edited := datatypes.JSON(`{"calibration": 9}`)
	device.Metadata = &edited

	got, _ := value(t, before, 0, "power_cal")
	assert.Equal(t, "2", got, "history is not rewritten")
	after := resolveSamplesFor(t, device, profile, testCalcMetrics(), reading)
	got, _ = value(t, after, 0, "power_cal")
	assert.Equal(t, "18", got)
}

// A profile whose formulas read no constant resolves identically whatever the device's
// metadata holds, including metadata that is not even JSON.
func TestAFormulaThatReadsNoConstantNeverReadsMetadata(t *testing.T) {
	profile := meterResolution(calculatedDef(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"))
	reading := map[string]string{"voltage": "200", "current": "10"}
	want := resolveSamplesFor(t, meterWithMetadata(""), profile, testCalcMetrics(), reading)
	for _, md := range []string{`{"calibration": 5}`, `not json`, `[1]`} {
		assert.Equal(t, want, resolveSamplesFor(t, meterWithMetadata(md), profile, testCalcMetrics(), reading), md)
	}
}
