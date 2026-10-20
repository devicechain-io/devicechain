// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakePlans serves one plan and records what it was asked.
type fakePlans struct {
	doc         *CalculatedPlanDoc
	missing     bool
	unreachable bool
	badDevice   string
	planCalls   int
	checked     [][]string
}

func (f *fakePlans) Plan(_ context.Context, _, profile string, version *int) (*CalculatedPlanDoc, json.RawMessage, error) {
	f.planCalls++
	if f.unreachable {
		return nil, nil, &BackfillUnavailable{Reason: "device-management could not be reached to read the plan"}
	}
	if f.missing || (version != nil && int64(*version) != f.doc.Version) {
		return nil, nil, fmt.Errorf("no published version of %q: %w", profile, gorm.ErrRecordNotFound)
	}
	raw, _ := json.Marshal(f.doc)
	return f.doc, raw, nil
}

func (f *fakePlans) CheckDevices(_ context.Context, _, _ string, tokens []string) error {
	f.checked = append(f.checked, tokens)
	for _, t := range tokens {
		if t == f.badDevice {
			return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("%q is not a device of the profile", t))
		}
	}
	return nil
}

// meterPlan is a published version 3 of "meter": power is new, kw is changed since an earlier
// version and reads power, flag reads kw and is unchanged.
func meterPlan() *CalculatedPlanDoc {
	return &CalculatedPlanDoc{
		VersionToken: "meter@3", Version: 3,
		Measured: []CalculatedPlanInput{{Key: "voltage", DataType: "DOUBLE"}, {Key: "current", DataType: "DOUBLE"}},
		Calculated: []CalculatedPlanMetric{
			{Key: "power", Expression: "voltage * current", DataType: "DOUBLE", Classifier: 1, NewInVersion: true},
			{Key: "kw", Expression: "power / 1000.0", DataType: "DOUBLE", Classifier: 2, ChangedSinceEarlier: true},
			{Key: "flag", Expression: "kw > 1.0", DataType: "BOOLEAN", Classifier: 3},
			{Key: "broken", Expression: "voltage +", DataType: "DOUBLE", Classifier: 4, NewInVersion: true},
			{Key: "bad_reader", Expression: "broken * 2.0", DataType: "DOUBLE", Classifier: 5},
		},
	}
}

// cleanPlan is meterPlan without the two metrics that cannot run: power is new, kw changed, flag
// unchanged, so the default keys are just power.
func cleanPlan() *CalculatedPlanDoc {
	d := meterPlan()
	d.Calculated = d.Calculated[:3]
	return d
}

var clock = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newRuntime(plans *fakePlans) *BackfillRuntime {
	rt := &BackfillRuntime{Runs: true, Plans: plans, Now: func() time.Time { return clock }}
	return rt
}

func goodRequest() StartBackfillRequest {
	return StartBackfillRequest{ProfileToken: "meter", From: clock.Add(-48 * time.Hour), To: clock.Add(-time.Hour)}
}

func startCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

// refusedAs asserts err is an INVALID_VALUE refusal whose sentence contains want.
func refusedAs(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	class, ok := integrity.Refused(err)
	require.True(t, ok, "not a refusal: %v", err)
	assert.Equal(t, integrity.ClassInvalid, class)
	assert.Contains(t, err.Error(), want)
}

// Every request-level refusal happens before anything is queued: the Api has no database here, so
// a refusal that fell through to the insert would panic rather than pass.
func TestStartBackfillRefusals(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name   string
		mutate func(*StartBackfillRequest, *BackfillRuntime, *fakePlans)
		want   string
	}{
		{"no profile", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.ProfileToken = " " }, "profileToken is required"},
		{"version zero", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { v := 0; r.Version = &v }, "version must be"},
		{"missing from", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.From = time.Time{} }, "from and to are required"},
		{"empty range", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.From = r.To }, "from must be before to"},
		{"inverted range", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.From, r.To = r.To, r.From }, "from must be before to"},
		{"future to", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.To = clock.Add(time.Second) }, "future"},
		{"span over 90 days", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) {
			r.To = clock
			r.From = clock.Add(-90*day - time.Second)
		}, "at most 90 days"},
		{"older than retention", func(r *StartBackfillRequest, rt *BackfillRuntime, _ *fakePlans) {
			rt.RetentionDays, rt.ChunkInterval = 30, day
			r.From, r.To = clock.Add(-29*day-time.Hour), clock.Add(-27*day) // floor is now-30d+2d = now-28d
		}, "older than retention allows"},
		{"too many devices", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) {
			for i := 0; i <= MaxBackfillDevices; i++ {
				r.DeviceTokens = append(r.DeviceTokens, fmt.Sprintf("d%d", i))
			}
		}, "at most 1000"},
		{"blank device token", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.DeviceTokens = []string{"a", ""} }, "empty token"},
		{"unknown device", func(r *StartBackfillRequest, _ *BackfillRuntime, p *fakePlans) {
			r.DeviceTokens, p.badDevice, r.MetricKeys = []string{"a", "ghost"}, "ghost", []string{"power"}
		}, `"ghost" is not a device`},
		{"unknown key", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) {
			r.MetricKeys = []string{"power", "nope"}
		}, "meter@3 does not calculate: nope"},
		{"a measured metric is not a calculated one", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.MetricKeys = []string{"voltage"} }, "does not calculate: voltage"},
		{"a key that cannot run", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.MetricKeys = []string{"broken"} }, "broken"},
		{"a key reading one that cannot run", func(r *StartBackfillRequest, _ *BackfillRuntime, _ *fakePlans) { r.MetricKeys = []string{"bad_reader"} }, "broken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plans := &fakePlans{doc: meterPlan()}
			rt := newRuntime(plans)
			req := goodRequest()
			tc.mutate(&req, rt, plans)
			_, err := (&Api{Backfill: rt}).StartBackfill(startCtx(), req, "op@example.com")
			refusedAs(t, err, tc.want)
		})
	}
}

// The boundary values are accepted by the checks that bound them: exactly 90 days, `to` exactly
// now, and exactly the retention floor. Without these the refusals above could be refusing too much.
func TestBackfillRangeBoundaries(t *testing.T) {
	day := 24 * time.Hour
	rt := newRuntime(nil)
	rt.RetentionDays, rt.ChunkInterval = 30, day
	floor := clock.Add(-28 * day)
	assert.NoError(t, newRuntime(nil).checkRange(clock.Add(-90*day), clock), "exactly 90 days, to exactly now")
	assert.NoError(t, rt.checkRange(floor, floor.Add(time.Hour)), "from exactly at the retention floor")
	assert.Error(t, rt.checkRange(floor.Add(-time.Nanosecond), floor.Add(time.Hour)), "a nanosecond below it")
	rt.RetentionDays = 0
	assert.NoError(t, rt.checkRange(clock.Add(-89*day), clock.Add(-day)), "retention off keeps everything")

	most := make([]string, MaxBackfillDevices)
	for i := range most {
		most[i] = fmt.Sprintf("d%d", i)
	}
	got, err := checkDeviceTokens(most)
	require.NoError(t, err, "exactly the most devices a job takes")
	assert.Len(t, got, MaxBackfillDevices)
}

func TestStartBackfillNeedsAProfileAndAVersionThatExist(t *testing.T) {
	plans := &fakePlans{doc: meterPlan(), missing: true}
	_, err := (&Api{Backfill: newRuntime(plans)}).StartBackfill(startCtx(), goodRequest(), "")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "an unknown profile answers NOT_FOUND")

	plans = &fakePlans{doc: meterPlan()}
	v := 9
	req := goodRequest()
	req.Version = &v
	_, err = (&Api{Backfill: newRuntime(plans)}).StartBackfill(startCtx(), req, "")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "a version never published answers NOT_FOUND")
}

// The worker is switched off, or cannot fit the pool: START is refused UNAVAILABLE, with the
// reason, and nothing is asked of device-management or written. The same for an instance with no
// device-management to read the plan from, and one whose device-management is down.
func TestStartBackfillIsUnavailableWhenNothingWouldRunIt(t *testing.T) {
	plans := &fakePlans{doc: meterPlan()}
	for _, tc := range []struct {
		name string
		api  *Api
		want string
	}{
		{"no runtime", &Api{}, "no backfill configuration"},
		{"disabled", &Api{Backfill: &BackfillRuntime{Runs: false, WhyNot: "calculatedBackfill.enabled is false", Plans: plans}},
			"calculatedBackfill.enabled is false"},
		{"no reason given", &Api{Backfill: &BackfillRuntime{Runs: false, Plans: plans}}, "not running"},
		{"no device-management", &Api{Backfill: &BackfillRuntime{Runs: true}}, "device-management is not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.api.StartBackfill(startCtx(), goodRequest(), "")
			var un *BackfillUnavailable
			require.ErrorAs(t, err, &un)
			assert.Equal(t, CodeUnavailable, un.Extensions()["code"])
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.Zero(t, plans.planCalls, "an instance that cannot run the job does not even read the plan")

	down := &fakePlans{doc: meterPlan(), unreachable: true}
	_, err := (&Api{Backfill: newRuntime(down)}).StartBackfill(startCtx(), goodRequest(), "")
	var un *BackfillUnavailable
	require.ErrorAs(t, err, &un, "device-management down is UNAVAILABLE, not a failure of the request")
}

// D4: omitted, the keys are those NEW in the version; a version with none needs them named.
func TestDefaultKeysAreTheKeysNewInTheVersion(t *testing.T) {
	doc := meterPlan()
	keys, err := selectBackfillKeys(doc, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"power", "broken"}, keys, "the changed key kw and the unchanged flag are not defaults")

	keys, err = selectBackfillKeys(doc, []string{"kw", "power", "kw"})
	require.NoError(t, err)
	assert.Equal(t, []string{"kw", "power"}, keys, "a changed key is selectable, once, in the order named")

	for i := range doc.Calculated {
		doc.Calculated[i].NewInVersion = false
	}
	_, err = selectBackfillKeys(doc, nil)
	refusedAs(t, err, "no calculated metric is new in meter@3")
}

// D4 and D9: a changed key is flagged, and so is a selected key reading an unselected upstream
// that may be stored under an earlier formula. A key that is only new warns of nothing.
func TestBackfillWarnings(t *testing.T) {
	doc := meterPlan()
	plan, err := doc.Compile()
	require.NoError(t, err)

	w, err := backfillWarnings(doc, plan, []string{"power"})
	require.NoError(t, err)
	assert.Empty(t, w, "a new key reading only measured metrics has nothing to say")
	assert.NotNil(t, w, "and is an empty list, not null")

	w, err = backfillWarnings(doc, plan, []string{"kw", "power"})
	require.NoError(t, err)
	require.Len(t, w, 1, "kw is changed; its upstream power is selected, so no chain warning")
	assert.Contains(t, w[0], "kw was calculated by an earlier version under a different formula")

	w, err = backfillWarnings(doc, plan, []string{"flag"})
	require.NoError(t, err)
	require.Len(t, w, 1, "flag is unchanged but reads kw, which is changed and not selected")
	assert.Contains(t, w[0], "flag reads kw")

	w, err = backfillWarnings(doc, plan, []string{"flag", "kw", "power"})
	require.NoError(t, err)
	require.Len(t, w, 1, "everything upstream selected: only kw's own change is noted")
	assert.Contains(t, w[0], "kw was calculated")
}

// The per-key refusal does not depend on order or duplicates in the request.
func TestStartBackfillErrorsAreNotConflictsOrNotFounds(t *testing.T) {
	_, err := (&Api{Backfill: newRuntime(&fakePlans{doc: meterPlan()})}).StartBackfill(startCtx(),
		StartBackfillRequest{ProfileToken: "meter", MetricKeys: []string{"nope"}, From: goodRequest().From, To: goodRequest().To}, "")
	require.Error(t, err)
	assert.False(t, errors.Is(err, gorm.ErrRecordNotFound), "a bad key is INVALID_VALUE, not NOT_FOUND")
}
