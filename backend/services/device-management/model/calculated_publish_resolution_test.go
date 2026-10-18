// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A calculated metric reaches the resolve path through the PUBLISHED SNAPSHOT: publish
// writes the definitions into the version row, and ProfileResolutionByDeviceType rebuilds
// the resolution from that row, not from the live drafts. Every other calculated-metric test
// builds its resolution from definitions in hand, so none of them could see a snapshot that
// dropped the expression — which would publish the metric, then resolve it as a MEASURED
// one: nothing computed, and a device's value under the key stored as if it were the
// computed one.
func TestAPublishedCalculatedMetricResolvesAsCalculated(t *testing.T) {
	f := newCalcFixture(t)
	require.NoError(t, f.api.RDB.Database.AutoMigrate(&GeoFence{}, &GeoFenceSetVersion{}, &GeoFenceGeometryBlob{}))
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: 1}
	f.metric("voltage", "DOUBLE", "")
	f.metric("current", "DOUBLE", "")
	f.metric("power_kw", "DOUBLE", "voltage * current / 1000.0")
	require.NoError(t, f.publish())

	var profile DeviceProfile
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("token = ?", "meter").First(&profile).Error)
	dt := &DeviceType{ProfileId: &profile.ID}
	dt.Token = "meter-type"
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(dt).Error)

	r, err := f.api.ProfileResolutionByDeviceType(f.ctx, dt.ID)
	require.NoError(t, err)
	assert.Equal(t, "meter@1", r.Scope().ProfileVersionToken)
	require.Equal(t, 1, r.CalculatedCount(), "the published snapshot must carry the formula to the resolver")
	def, ok := r.Metric("power_kw")
	require.True(t, ok)
	assert.True(t, def.Calculated())

	// And it computes, from the published version alone.
	calc := r.BeginCalculation(nil)
	defer calc.Release()
	for key, raw := range map[string]string{"voltage": "230", "current": "10"} {
		m, ok := r.Metric(key)
		require.True(t, ok)
		calc.Set(&m, raw)
	}
	out := calc.Append(nil, nopObserver{})
	require.Len(t, out, 1)
	assert.Equal(t, "power_kw", out[0].Name)
	assert.Equal(t, "2.3", out[0].Value)
	assert.True(t, out[0].Derived)
}

type nopObserver struct{}

func (nopObserver) Computed(string)                             {}
func (nopObserver) Skipped(string, calcfield.SkipReason, error) {}

// One cached resolution serves every resolver worker of a pod at once: the compiled plan is
// shared and each sample entry takes its own pooled sample. Each goroutine here computes from
// values no other goroutine sets, so a sample shared between two workers — or a plan that
// kept anything of one evaluation — shows up as a value computed from another worker's
// reading, with or without the race detector.
func TestOneResolutionCalculatesForManyWorkersAtOnce(t *testing.T) {
	r := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@2"}, calcTestDefs())
	require.Equal(t, 1, r.CalculatedCount())
	voltage, _ := r.Metric("voltage")
	current, _ := r.Metric("current")

	const workers, rounds = 8, 400
	errs := make(chan string, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				v, c := w+1, i+1
				calc := r.BeginCalculation(nil)
				calc.Set(&voltage, strconv.Itoa(v))
				calc.Set(&current, strconv.Itoa(c))
				out := calc.Append(nil, nopObserver{})
				calc.Release()
				if want := strconv.Itoa(v * c); len(out) != 1 || out[0].Value != want {
					errs <- fmt.Sprintf("worker %d round %d: got %v, want power=%s", w, i, out, want)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// A derived entry travels to every consumer, so its classifier, unit and data type are its
// own: an entry that pointed into the shared plan would let one consumer's write change the
// stamp of every later value of that metric, on every worker.
func TestADerivedEntryOwnsItsStamp(t *testing.T) {
	r := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@2"}, calcTestDefs())
	values := map[string]string{"voltage": "2", "current": "3"}
	first, _ := evaluate(r, values)
	second, _ := evaluate(r, values)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.NotNil(t, first[0].Unit)
	assert.NotSame(t, first[0].Classifier, second[0].Classifier)
	assert.NotSame(t, first[0].Unit, second[0].Unit)
	assert.NotSame(t, first[0].DataType, second[0].DataType)

	*first[0].Classifier, *first[0].Unit, *first[0].DataType = 99, "kW", "INT"
	third, _ := evaluate(r, values)
	assert.Equal(t, uint64(3), *third[0].Classifier, "a write to one entry's stamp reached the plan")
	assert.Equal(t, "W", *third[0].Unit)
	assert.Equal(t, "DOUBLE", *third[0].DataType)
}
