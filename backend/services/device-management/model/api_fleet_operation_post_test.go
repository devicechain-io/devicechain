// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postEnv struct {
	api   *Api
	ctx   context.Context
	dev   *Device
	op    *FleetOperation
	lease FleetLease
	line  FleetOperationLine
}

// postFixture is a device "dev" whose profile declares configuration key k, and a RUNNING operation that
// sets k to v1 on it.
func postFixture(t *testing.T, value string, unset bool) *postEnv {
	t.Helper()
	armed(t)
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{},
		&FleetOperationLine{}, &FleetOperationEvent{}))
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}})
	req := &FleetOperationCreateRequest{Token: "p1", Name: "n", Kind: FleetKindConfigSet, DeviceTokens: []string{dev.Token},
		ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &value}}
	if unset {
		req.ConfigSet = &FleetConfigSetIntent{Key: "k", Unset: true}
	}
	op, err := api.CreateFleetOperation(ctx, req, "creator")
	require.NoError(t, err)
	op, err = api.StartFleetOperation(ctx, "p1", op.ContractDigest, alice)
	require.NoError(t, err)
	e := &postEnv{api: api, ctx: ctx, dev: dev, op: op, lease: FleetLease{Epoch: op.ExecutionEpoch, Grantee: alice.Name, Deadline: time.Now().Add(time.Minute)}}
	require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).First(&e.line).Error)
	return e
}

func (e *postEnv) reload(t *testing.T) FleetOperationLine {
	t.Helper()
	var l FleetOperationLine
	require.NoError(t, e.api.RDB.DB(e.ctx).First(&l, e.line.ID).Error)
	return l
}

func (e *postEnv) post(t *testing.T) FleetPostResult {
	t.Helper()
	r, err := e.api.PostFleetLine(e.ctx, e.op.ID, e.line.ID, e.lease)
	require.NoError(t, err)
	return r
}

func attrOf(t *testing.T, e *postEnv) (*EntityAttribute, bool) {
	t.Helper()
	var a EntityAttribute
	err := e.api.RDB.DB(e.ctx).Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", e.dev.ID).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false
	}
	require.NoError(t, err)
	return &a, true
}

func TestAPostAppliesTheEffectAndClaimsTheLineTogether(t *testing.T) {
	e := postFixture(t, "v1", false)
	effects := 0
	fleetEffectHook = func() { effects++ }
	t.Cleanup(func() { fleetEffectHook = nil })

	r := e.post(t)
	require.Equal(t, FleetPosted, r.Outcome)
	require.Equal(t, 1, effects)
	a, ok := attrOf(t, e)
	require.True(t, ok)
	require.Equal(t, "v1", a.Value.String)

	l := e.reload(t)
	require.Equal(t, FleetPostingPosted, l.Posting)
	require.True(t, l.PostedAt.Valid)
	require.True(t, l.SettleBy.Valid)
	require.WithinDuration(t, l.PostedAt.Time.Add(time.Duration(e.op.SettleDeadlineS)*time.Second), l.SettleBy.Time, time.Second)
	require.Equal(t, e.op.ExecutionEpoch, l.PostedEpoch.Int64, "the epoch the post committed under")
	var prior, posted fleetValueImage
	require.NoError(t, json.Unmarshal([]byte(l.Prior.String), &prior))
	require.NoError(t, json.Unmarshal([]byte(l.PostedValue.String), &posted))
	require.False(t, prior.Present, "the key was absent before")
	require.True(t, posted.Present)
	require.Equal(t, "v1", *posted.Value)
	require.Greater(t, posted.Generation, prior.Generation, "every write advances the key's generation")
	require.True(t, l.PostedRevision.Valid, "the configuration revision the post produced")
	var rev DeviceConfigurationRevision
	require.NoError(t, e.api.RDB.DB(e.ctx).Where("device_id = ? AND revision = ?", e.dev.ID, l.PostedRevision.Int64).First(&rev).Error)

	// A second post of the same line is a lost race: no second effect and no second revision.
	var before int64
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", e.dev.ID).Count(&before).Error)
	r = e.post(t)
	require.Equal(t, FleetPostLostRace, r.Outcome)
	require.Equal(t, 1, effects, "the effect ran exactly once")
	var after int64
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", e.dev.ID).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestAnUnsetRecordsTheValueItRemoved(t *testing.T) {
	e := postFixture(t, "", true)
	// The key holds a value before the operation reaches it.
	v := "old"
	_, err := e.api.SetEntityAttribute(e.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: e.dev.Token, Scope: "SHARED", AttrKey: "k", ValueType: "STRING", Value: &v})
	require.NoError(t, err)
	require.Equal(t, FleetPosted, e.post(t).Outcome)
	_, ok := attrOf(t, e)
	require.False(t, ok)
	var prior, posted fleetValueImage
	l := e.reload(t)
	require.NoError(t, json.Unmarshal([]byte(l.Prior.String), &prior))
	require.NoError(t, json.Unmarshal([]byte(l.PostedValue.String), &posted))
	require.True(t, prior.Present)
	require.Equal(t, "old", *prior.Value, "the before-image is what a reversal restores")
	require.False(t, posted.Present)
}

func TestTheFenceRefusesAPostThatIsNotUnderTheLeasesOwnState(t *testing.T) {
	for name, mutate := range map[string]func(e *postEnv, t *testing.T){
		"paused": func(e *postEnv, t *testing.T) {
			_, err := e.api.PauseFleetOperation(e.ctx, "p1", alice.Name)
			require.NoError(t, err)
		},
		"another epoch":   func(e *postEnv, t *testing.T) { e.lease.Epoch++ },
		"another grantee": func(e *postEnv, t *testing.T) { e.lease.Grantee = "someone-else@example.com" },
		"aborted": func(e *postEnv, t *testing.T) {
			_, err := e.api.AbortFleetOperation(e.ctx, "p1", alice.Name)
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := postFixture(t, "v1", false)
			mutate(e, t)
			require.Equal(t, FleetPostFenceRefused, e.post(t).Outcome)
			_, ok := attrOf(t, e)
			require.False(t, ok, "nothing was written")
			if name != "aborted" {
				require.Equal(t, FleetPostingPlanned, e.reload(t).Posting)
			}
		})
	}
	// Negative control: the lease's own state posts.
	e := postFixture(t, "v1", false)
	require.Equal(t, FleetPosted, e.post(t).Outcome)
}

func TestAPostWaitsForNothingWhenTheGuardIsNotInPlace(t *testing.T) {
	e := postFixture(t, "v1", false)
	restore := OpenFleetActivationForTest(func(*gorm.DB) error { return ErrFleetOperationNotActivated })
	r := e.post(t)
	restore()
	require.Equal(t, FleetPostNotActivated, r.Outcome)
	_, ok := attrOf(t, e)
	require.False(t, ok)
	require.Equal(t, FleetPostingPlanned, e.reload(t).Posting)
}

func TestALapsedLeaseRollsTheEffectBack(t *testing.T) {
	e := postFixture(t, "v1", false)
	e.lease.Deadline = time.Now().Add(-time.Second)
	require.Equal(t, FleetPostLeaseExpired, e.post(t).Outcome)
	_, ok := attrOf(t, e)
	require.False(t, ok)
	// A lease that lapses DURING the transaction also rolls the effect back.
	e = postFixture(t, "v1", false)
	e.lease.Deadline = time.Now().Add(700 * time.Millisecond)
	fleetEffectHook = func() { time.Sleep(900 * time.Millisecond) }
	t.Cleanup(func() { fleetEffectHook = nil })
	r := e.post(t)
	fleetEffectHook = nil
	require.Contains(t, []FleetPostOutcome{FleetPostLeaseExpired, FleetPostRetry}, r.Outcome)
	_, ok = attrOf(t, e)
	require.False(t, ok, "an effect that could not commit inside the lease left nothing behind")
	require.Equal(t, FleetPostingPlanned, e.reload(t).Posting)
}

func TestGatesRefuseWithACodeAndAReplannedLineNeverRetries(t *testing.T) {
	// The key is not declared by the profile.
	e := postFixture(t, "v1", false)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{ID: e.op.ID}).Update("intent", `{"key":"other","valueType":"STRING","value":"x","unset":false}`).Error)
	r := e.post(t)
	require.Equal(t, FleetPostRefused, r.Outcome)
	require.Equal(t, FleetLineKeyUndeclared, r.Code)
	l := e.reload(t)
	require.Equal(t, FleetPostingRefused, l.Posting)
	require.Equal(t, FleetLineKeyUndeclared, l.Code)
	require.False(t, l.RefusedAtPlan, "a post-time refusal counts toward the failure limit")
	require.False(t, l.PostedAt.Valid)
	// A refused line is not retried: another post of it finds it settled.
	require.Equal(t, FleetPostLostRace, e.post(t).Outcome)
	require.NoError(t, e.api.RecordFleetLineRetry(e.ctx, e.line.ID, "DEADLOCK", time.Now()))
	l = e.reload(t)
	require.Equal(t, int32(0), l.Attempts, "bookkeeping never touches a line that is no longer PLANNED")

	// A value the declared type cannot carry.
	e = postFixture(t, "v1", false)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{ID: e.op.ID}).Update("intent", `{"key":"k","valueType":"LONG","value":"not-a-number","unset":false}`).Error)
	r = e.post(t)
	require.Equal(t, FleetPostRefused, r.Outcome)
	require.Equal(t, CodeConfigurationValueType, r.Code)
	_, ok := attrOf(t, e)
	require.False(t, ok, "the refused effect rolled back")

	// The device is gone.
	e = postFixture(t, "v1", false)
	require.NoError(t, e.api.RDB.DB(e.ctx).Delete(&Device{}, e.dev.ID).Error)
	r = e.post(t)
	require.Equal(t, FleetPostRefused, r.Outcome)
	require.Equal(t, FleetLineTargetGone, r.Code)
	require.Equal(t, string(ledger.PostingSkipped), e.reload(t).Posting)
}

func TestARefusalCannotLandAfterAPause(t *testing.T) {
	e := postFixture(t, "v1", false)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&FleetOperation{ID: e.op.ID}).Update("intent", `{"key":"other","valueType":"STRING","value":"x","unset":false}`).Error)
	// The pause lands between the decision and its recording.
	_, err := e.api.PauseFleetOperation(e.ctx, "p1", alice.Name)
	require.NoError(t, err)
	r, err := e.api.recordFleetRefusal(e.ctx, "acme", e.op.ID, e.line.ID, e.lease, fleetDecision{FleetPostingRefused, "X", "d"})
	require.NoError(t, err)
	require.Equal(t, FleetPostFenceRefused, r.Outcome)
	require.Equal(t, FleetPostingPlanned, e.reload(t).Posting)
}

func TestADisabledDeviceIsStillWritten(t *testing.T) {
	e := postFixture(t, "v1", false)
	require.NoError(t, e.api.RDB.DB(e.ctx).Model(&Device{}).Where("id = ?", e.dev.ID).Update("disabled_at", time.Now()).Error)
	require.Equal(t, FleetPosted, e.post(t).Outcome, "configuration is desired state; the line settles when the device returns")
}

func TestRetryBookkeepingBacksOffAndNeverChangesThePosting(t *testing.T) {
	e := postFixture(t, "v1", false)
	now := time.Now()
	require.NoError(t, e.api.RecordFleetLineRetry(e.ctx, e.line.ID, "DEADLOCK", now))
	l := e.reload(t)
	require.Equal(t, int32(1), l.Attempts)
	require.Equal(t, "DEADLOCK", l.LastErrorCode)
	require.Equal(t, FleetPostingPlanned, l.Posting)
	require.True(t, l.NextAttemptAt.Valid)
	d := l.NextAttemptAt.Time.Sub(now)
	require.GreaterOrEqual(t, d, time.Second)
	require.Less(t, d, 2*time.Second)
	require.NoError(t, e.api.RecordFleetLineRetry(e.ctx, e.line.ID, "LOCK_TIMEOUT", now))
	require.Equal(t, int32(2), e.reload(t).Attempts)

	require.Equal(t, time.Second, fleetRetryDelay(1))
	require.Equal(t, 2*time.Second, fleetRetryDelay(2))
	require.Equal(t, 32*time.Second, fleetRetryDelay(6))
	require.Equal(t, time.Minute, fleetRetryDelay(7))
	require.Equal(t, time.Minute, fleetRetryDelay(500))
	require.Equal(t, time.Second, fleetRetryDelay(0))
}

func TestOnlyRetryableFailuresAreRetried(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, &AttributeWriteBusyError{}} {
		_, ok := fleetRetryableCode(err)
		require.True(t, ok, err)
	}
	for _, err := range []error{errors.New("boom"), gorm.ErrRecordNotFound, &ConfigurationRefusal{Code: "X"}} {
		_, ok := fleetRetryableCode(err)
		require.False(t, ok, "%v must surface, never be retried or refused", err)
	}
}
