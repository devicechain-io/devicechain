// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Alarm disposition labels: an operator's verdict on one alarm CYCLE. The properties pinned here
// are the ones a backtest relies on: a label belongs to the cycle it was written on and never
// leaks onto the next one, the earlier cycles stay labelled, a label stated for a cycle the alarm
// has left is refused rather than moved, and nothing about the alarm itself changes.

type dispositionRig struct {
	api *Api
	db  *gorm.DB
}

func newDispositionRig(t *testing.T) *dispositionRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Alarm{}, &AlarmDisposition{}))
	return &dispositionRig{api: NewApi(&rdb.RdbManager{Database: db}), db: db}
}

// alarm seeds one alarm in tenant, raised at raised.
func (r *dispositionRig) alarm(t *testing.T, tenant, token string, raised time.Time) *Alarm {
	t.Helper()
	a := Alarm{
		TokenReference: rdb.TokenReference{Token: token},
		OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: token + "-key", MetricKey: "temp", State: string(AlarmStateCleared),
		Severity: string(AlarmSeverityMajor), RaisedTime: raised,
	}
	require.NoError(t, r.db.WithContext(tenantCtx(tenant)).Create(&a).Error)
	return &a
}

// reraise moves the alarm into a new cycle, as the raise path does: in place, with a new raised
// time.
func (r *dispositionRig) reraise(t *testing.T, tenant, token string, raised time.Time) {
	t.Helper()
	require.NoError(t, r.db.WithContext(tenantCtx(tenant)).Model(&Alarm{}).Where("token = ?", token).
		Updates(map[string]any{"raised_time": raised, "state": string(AlarmStateActive)}).Error)
}

func (r *dispositionRig) current(t *testing.T, tenant, token string) *Alarm {
	t.Helper()
	found, err := r.api.AlarmsByToken(tenantCtx(tenant), []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	return found[0]
}

func (r *dispositionRig) labelOf(t *testing.T, tenant, token string) *AlarmDisposition {
	t.Helper()
	label, err := r.api.AlarmDispositionOf(tenantCtx(tenant), r.current(t, tenant, token))
	require.NoError(t, err)
	return label
}

func (r *dispositionRig) list(t *testing.T, tenant string, c AlarmDispositionSearchCriteria) []AlarmDisposition {
	t.Helper()
	if c.PageSize == 0 {
		c.Pagination = rdb.Pagination{PageNumber: 1, PageSize: 100}
	}
	found, err := r.api.AlarmDispositions(tenantCtx(tenant), c)
	require.NoError(t, err)
	return found.Results
}

func (r *dispositionRig) rows(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, r.db.WithContext(core.WithSystemContext(context.Background())).
		Model(&AlarmDisposition{}).Count(&n).Error)
	return n
}

func TestAlarmDispositionLabelVocabulary(t *testing.T) {
	for _, l := range []AlarmDispositionLabel{AlarmDispositionTruePositive, AlarmDispositionFalsePositive,
		AlarmDispositionExpected} {
		require.True(t, l.Valid(), "%q must be valid", l)
	}
	for _, l := range []AlarmDispositionLabel{"", "true_positive", "TRUE_POSITIVE ", "NUISANCE", "UNKNOWN"} {
		require.False(t, l.Valid(), "%q must be refused", l)
	}
}

func TestAlarmDispositionBelongsToTheCycleItWasWrittenOn(t *testing.T) {
	r := newDispositionRig(t)
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(3 * time.Hour)
	r.alarm(t, "A", "a1", t1)
	ctx := tenantCtx("A")
	by := "operator@example.com"

	require.Nil(t, r.labelOf(t, "A", "a1"), "a fresh alarm is unlabelled")

	alarm, label, err := r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionFalsePositive), Note: ptr("  sensor iced over  "),
	}, &by)
	require.NoError(t, err)
	require.Equal(t, "a1", alarm.Token)
	require.True(t, label.RaisedTime.Equal(t1), "the label names the cycle it was written on")
	require.Equal(t, "sensor iced over", label.Note.String, "the note is trimmed")
	require.Equal(t, by, label.RecordedBy.String)

	got := r.labelOf(t, "A", "a1")
	require.NotNil(t, got)
	require.Equal(t, string(AlarmDispositionFalsePositive), got.Disposition)

	// The alarm is raised again: the new cycle starts unlabelled, and the old label is not lost.
	r.reraise(t, "A", "a1", t2)
	require.Nil(t, r.labelOf(t, "A", "a1"), "the previous cycle's label leaked onto the new cycle")
	listed := r.list(t, "A", AlarmDispositionSearchCriteria{})
	require.Len(t, listed, 1)
	require.True(t, listed[0].RaisedTime.Equal(t1))

	// Label the new cycle; both cycles are listed, newest first.
	_, _, err = r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionTruePositive)}, &by)
	require.NoError(t, err)
	require.Equal(t, string(AlarmDispositionTruePositive), r.labelOf(t, "A", "a1").Disposition)
	listed = r.list(t, "A", AlarmDispositionSearchCriteria{})
	require.Len(t, listed, 2)
	require.True(t, listed[0].RaisedTime.Equal(t2), "newest cycle first")
	require.Equal(t, string(AlarmDispositionTruePositive), listed[0].Disposition)
	require.Equal(t, string(AlarmDispositionFalsePositive), listed[1].Disposition)
}

func TestAlarmDispositionRelabelReplacesTheCyclesLabel(t *testing.T) {
	r := newDispositionRig(t)
	r.alarm(t, "A", "a1", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	ctx := tenantCtx("A")
	first, second := "first", "second"

	_, _, err := r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionTruePositive), Note: ptr("looked real")}, &first)
	require.NoError(t, err)
	_, _, err = r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionExpected)}, &second)
	require.NoError(t, err)

	require.EqualValues(t, 1, r.rows(t), "a relabel must replace the cycle's row, not add one")
	got := r.labelOf(t, "A", "a1")
	require.Equal(t, string(AlarmDispositionExpected), got.Disposition)
	require.False(t, got.Note.Valid, "a relabel with no note leaves no note")
	require.Equal(t, second, got.RecordedBy.String)
}

func TestAlarmDispositionStatedCycleMustBeCurrent(t *testing.T) {
	r := newDispositionRig(t)
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 123456000, time.UTC)
	r.alarm(t, "A", "a1", t1)
	ctx := tenantCtx("A")
	served := t1.Format(time.RFC3339Nano) // as the alarm's raisedTime is served

	// The alarm moves on to a new cycle after the operator read it.
	r.reraise(t, "A", "a1", t1.Add(time.Hour))
	_, _, err := r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionFalsePositive), RaisedTime: &served}, nil)
	require.ErrorIs(t, err, ErrAlarmCycleMoved)
	require.EqualValues(t, 0, r.rows(t), "a refused label wrote a row")

	// Stated as the cycle the alarm is in now, it is written.
	now := r.current(t, "A", "a1").RaisedTime.Format(time.RFC3339Nano)
	_, label, err := r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionFalsePositive), RaisedTime: &now}, nil)
	require.NoError(t, err)
	require.True(t, label.RaisedTime.Equal(t1.Add(time.Hour)))
}

func TestAlarmDispositionRefusesInvalidInputAndWritesNothing(t *testing.T) {
	r := newDispositionRig(t)
	r.alarm(t, "A", "a1", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	ctx := tenantCtx("A")

	for name, req := range map[string]AlarmDispositionRequest{
		"unknown label":   {Token: "a1", Disposition: "NUISANCE"},
		"lower case":      {Token: "a1", Disposition: "true_positive"},
		"empty label":     {Token: "a1", Disposition: ""},
		"note too long":   {Token: "a1", Disposition: "EXPECTED", Note: ptr(strings.Repeat("é", MaxAlarmDispositionNoteLength+1))},
		"bad raised time": {Token: "a1", Disposition: "EXPECTED", RaisedTime: ptr("yesterday")},
	} {
		_, _, err := r.api.SetAlarmDisposition(ctx, req, nil)
		require.ErrorIs(t, err, ErrInvalidAlarmDisposition, name)
	}
	require.EqualValues(t, 0, r.rows(t))

	// The bound is in characters, not bytes: a note of exactly the limit in two-byte runes passes.
	_, _, err := r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{Token: "a1", Disposition: "EXPECTED",
		Note: ptr(strings.Repeat("é", MaxAlarmDispositionNoteLength))}, nil)
	require.NoError(t, err)

	_, _, err = r.api.SetAlarmDisposition(ctx, AlarmDispositionRequest{Token: "nope", Disposition: "EXPECTED"}, nil)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "an unknown alarm is not found, got %v", err)
}

func TestAlarmDispositionLeavesTheAlarmAlone(t *testing.T) {
	r := newDispositionRig(t)
	r.alarm(t, "A", "a1", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	before := r.current(t, "A", "a1")

	_, _, err := r.api.SetAlarmDisposition(tenantCtx("A"), AlarmDispositionRequest{
		Token: "a1", Disposition: string(AlarmDispositionTruePositive)}, ptr("op"))
	require.NoError(t, err)

	after := r.current(t, "A", "a1")
	require.Equal(t, before.State, after.State)
	require.Equal(t, before.Acknowledged, after.Acknowledged)
	require.Equal(t, before.Severity, after.Severity)
	require.True(t, before.RaisedTime.Equal(after.RaisedTime))
	require.False(t, after.AcknowledgedBy.Valid, "labelling is not acknowledging")
}

func TestAlarmDispositionsAreTenantIsolatedAndFiltered(t *testing.T) {
	r := newDispositionRig(t)
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.alarm(t, "A", "a1", t0)
	r.alarm(t, "A", "a2", t0.Add(24*time.Hour))
	r.alarm(t, "A", "a3", t0.Add(48*time.Hour))
	r.alarm(t, "B", "b1", t0)
	a := tenantCtx("A")

	for token, label := range map[string]AlarmDispositionLabel{
		"a1": AlarmDispositionTruePositive, "a2": AlarmDispositionFalsePositive, "a3": AlarmDispositionFalsePositive,
	} {
		_, _, err := r.api.SetAlarmDisposition(a, AlarmDispositionRequest{Token: token, Disposition: string(label)}, nil)
		require.NoError(t, err)
	}

	// Tenant B cannot label tenant A's alarm, and sees none of its labels.
	_, _, err := r.api.SetAlarmDisposition(tenantCtx("B"), AlarmDispositionRequest{Token: "a1",
		Disposition: string(AlarmDispositionExpected)}, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Empty(t, r.list(t, "B", AlarmDispositionSearchCriteria{}))
	require.Len(t, r.list(t, "A", AlarmDispositionSearchCriteria{}), 3)

	fp := string(AlarmDispositionFalsePositive)
	require.Len(t, r.list(t, "A", AlarmDispositionSearchCriteria{Disposition: &fp}), 2)
	require.Len(t, r.list(t, "A", AlarmDispositionSearchCriteria{AlarmToken: ptr("a2")}), 1)
	require.Empty(t, r.list(t, "A", AlarmDispositionSearchCriteria{AlarmToken: ptr("b1")}),
		"another tenant's alarm token found labels")
	require.Empty(t, r.list(t, "A", AlarmDispositionSearchCriteria{AlarmToken: ptr("missing")}))

	start := t0.Add(24 * time.Hour).Format(time.RFC3339)
	end := t0.Add(24 * time.Hour).Format(time.RFC3339)
	inRange := r.list(t, "A", AlarmDispositionSearchCriteria{StartTime: &start, EndTime: &end})
	require.Len(t, inRange, 1, "both bounds are inclusive")
	require.Equal(t, fp, inRange[0].Disposition)

	_, err = r.api.AlarmDispositions(a, AlarmDispositionSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, Disposition: ptr("false_positive")})
	require.ErrorIs(t, err, ErrInvalidAlarmDisposition, "a misspelt filter must not read as an empty answer")
	_, err = r.api.AlarmDispositions(a, AlarmDispositionSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}, StartTime: ptr("last week")})
	require.Error(t, err)
}
