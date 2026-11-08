// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Alarm disposition labels on the real engine: the label's table is built by the real migration
// chain (its cycle key, its alarm foreign key), a cycle is opened and closed by the one raise path,
// and the scoped reads compare text[] stamps with `&&`, which SQLite cannot run.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Disposition -v
package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func (r scopeAlarmRig) dispositionCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, r.api.RDB.DB(core.WithSystemContext(context.Background())).
		Model(&AlarmDisposition{}).Where("tenant_id = ?", r.tenant).Count(&n).Error)
	return n
}

func (r scopeAlarmRig) dispositions(t *testing.T, ctx context.Context) []AlarmDisposition {
	t.Helper()
	found, err := r.api.AlarmDispositions(ctx, AlarmDispositionSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}})
	require.NoError(t, err)
	return found.Results
}

// A real cycle: raised, labelled, resolved and raised again by the raise path. The new cycle is
// unlabelled, the first keeps its label, and both are written to the one alarm row's cycles.
func TestAlarmDispositionFollowsRealCycles(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	by := "operator"

	_, label, err := r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{
		Token: r.alarm(t, "acme-hot").Token, Disposition: string(AlarmDispositionFalsePositive)}, &by)
	require.NoError(t, err)
	require.True(t, label.RaisedTime.Equal(t0))
	require.Equal(t, []string{"customer:acme"}, []string(label.ScopeAnchors), "the label carries the cycle's stamp")

	r.resolve(t, "acme-pump", "acme-hot", t0.Add(time.Minute))
	r.raise(t, "acme-pump", "acme-hot", t0.Add(time.Hour))
	current := r.alarm(t, "acme-hot")
	require.True(t, current.RaisedTime.Equal(t0.Add(time.Hour)), "the raise path did not open a new cycle")

	got, err := r.api.AlarmDispositionOf(r.operator, current)
	require.NoError(t, err)
	require.Nil(t, got, "the first cycle's label leaked onto the second")
	require.Len(t, r.dispositions(t, r.operator), 1)

	// Stating the first cycle now is refused; the second is labelled when stated correctly.
	stale := t0.Format(time.RFC3339Nano)
	_, _, err = r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{
		Token: current.Token, Disposition: string(AlarmDispositionTruePositive), RaisedTime: &stale}, &by)
	require.ErrorIs(t, err, ErrAlarmCycleMoved)
	fresh := current.RaisedTime.Format(time.RFC3339Nano)
	_, _, err = r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{
		Token: current.Token, Disposition: string(AlarmDispositionTruePositive), RaisedTime: &fresh}, &by)
	require.NoError(t, err)
	require.EqualValues(t, 2, r.dispositionCount(t))
}

// A scoped member reads the labels of its own customers' cycles and none other, and cannot write
// one at all.
func TestAlarmDispositionUnderAScopedMember(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)
	for _, key := range []string{"acme-hot", "globex-hot"} {
		_, _, err := r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{
			Token: r.alarm(t, key).Token, Disposition: string(AlarmDispositionExpected)}, nil)
		require.NoError(t, err)
	}

	acme := r.member("customer:acme")
	mine := r.dispositions(t, acme)
	require.Len(t, mine, 1)
	require.Equal(t, r.alarm(t, "acme-hot").ID, mine[0].AlarmId)
	// Negative control: the operator reads both.
	require.Len(t, r.dispositions(t, r.operator), 2)

	// The member's own alarm's label is readable through the alarm.
	got, err := r.api.AlarmDispositionOf(acme, r.alarm(t, "acme-hot"))
	require.NoError(t, err)
	require.NotNil(t, got)
	got, err = r.api.AlarmDispositionOf(acme, r.alarm(t, "globex-hot"))
	require.NoError(t, err)
	require.Nil(t, got, "a member read another customer's label")

	// Writing is refused, on its own alarm as much as on another's (which it cannot even find).
	_, _, err = r.api.SetAlarmDisposition(acme, AlarmDispositionRequest{
		Token: r.alarm(t, "acme-hot").Token, Disposition: string(AlarmDispositionTruePositive)}, nil)
	require.True(t, errors.Is(err, auth.ErrScopeUnsupported), "a scoped member wrote a label: %v", err)
	require.Equal(t, string(AlarmDispositionExpected), r.dispositions(t, r.operator)[0].Disposition)
}

// The label goes with its alarm: deleting the device removes its alarms, and the foreign key
// takes their labels. Another device's label is untouched.
func TestAlarmDispositionGoesWithItsAlarm(t *testing.T) {
	r := newScopeAlarmRig(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)
	for _, key := range []string{"acme-hot", "globex-hot"} {
		_, _, err := r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{
			Token: r.alarm(t, key).Token, Disposition: string(AlarmDispositionTruePositive)}, nil)
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, r.dispositionCount(t))

	deleted, err := r.api.DeleteDevice(r.operator, "acme-pump")
	require.NoError(t, err)
	require.True(t, deleted)
	require.EqualValues(t, 1, r.dispositionCount(t), "the deleted device's alarm label outlived its alarm")
	left := r.dispositions(t, r.operator)
	require.Len(t, left, 1)
	require.Equal(t, r.alarm(t, "globex-hot").ID, left[0].AlarmId)
}
