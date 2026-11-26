// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// I-18 (reversal), unit half. The concurrent halves (one contra under racing reversals; retention racing
// a reversal's create) are in fleet_reversal_integration_test.go on a real server.

type reversalEnv struct {
	api  *Api
	ctx  context.Context
	devs []*Device
	orig *FleetOperation
}

// reversalFixture is n configured devices ("dev", "dev2", ...) declaring STRING key k, each holding
// prior (nil: no value), and an original operation "orig" that set k to v1 (or unset it) on every one
// of them, applied and DISPATCHED.
func reversalFixture(t *testing.T, prior *string, unset bool, n int) *reversalEnv {
	t.Helper()
	f := newReversalEnv(t, prior, n)
	f.orig = f.runOriginal(t, "orig", unset, true)
	return f
}

func newReversalEnv(t *testing.T, prior *string, n int) *reversalEnv {
	t.Helper()
	armed(t)
	api, ctx := configTestApi(t)
	require.NoError(t, api.RDB.DB(core.WithSystemContext(ctx)).AutoMigrate(&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{},
		&FleetOperationLine{}, &FleetOperationEvent{}))
	if sqlDB, err := api.RDB.Database.DB(); err == nil {
		pin, perr := sqlDB.Conn(context.Background())
		require.NoError(t, perr)
		t.Cleanup(func() { _ = pin.Close() })
	}
	f := &reversalEnv{api: api, ctx: ctx}
	f.devs = append(f.devs, seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}}))
	for i := 2; i <= n; i++ {
		d, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: fmt.Sprintf("dev%d", i), DeviceTypeToken: "sensor"})
		require.NoError(t, err)
		f.devs = append(f.devs, d)
	}
	if prior != nil {
		for _, d := range f.devs {
			f.setK(t, d, prior)
		}
	}
	return f
}

// runOriginal creates, starts and applies an operation over every device, then finishes it (DISPATCHED)
// or aborts it.
func (f *reversalEnv) runOriginal(t *testing.T, token string, unset, dispatch bool) *FleetOperation {
	t.Helper()
	v1 := "v1"
	tokens := make([]string, len(f.devs))
	for i, d := range f.devs {
		tokens[i] = d.Token
	}
	req := &FleetOperationCreateRequest{Token: token, Name: "n", Kind: FleetKindConfigSet, DeviceTokens: tokens,
		ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v1}}
	if unset {
		req.ConfigSet = &FleetConfigSetIntent{Key: "k", Unset: true}
	}
	op, err := f.api.CreateFleetOperation(f.ctx, req, "creator")
	require.NoError(t, err)
	op = f.start(t, op)
	for _, r := range f.postAll(t, op) {
		require.Equal(t, FleetPosted, r.Outcome, "the original applies")
	}
	if dispatch {
		res, err := f.api.finishDriven(f.ctx, &FleetDriveResult{}, op)
		require.NoError(t, err)
		require.True(t, res.Dispatched)
	} else {
		_, err = f.api.AbortFleetOperation(f.ctx, token, alice.Name)
		require.NoError(t, err)
	}
	return f.op(t, token)
}

// finish moves a RUNNING operation whose lines are all decided to DISPATCHED.
func (f *reversalEnv) finish(t *testing.T, op *FleetOperation) {
	t.Helper()
	res, err := f.api.finishDriven(f.ctx, &FleetDriveResult{}, f.op(t, op.Token))
	require.NoError(t, err)
	require.True(t, res.Dispatched)
}

func (f *reversalEnv) op(t *testing.T, token string) *FleetOperation {
	t.Helper()
	op, err := f.api.FleetOperationByToken(f.ctx, token)
	require.NoError(t, err)
	require.NotNil(t, op)
	return op
}

func (f *reversalEnv) start(t *testing.T, op *FleetOperation) *FleetOperation {
	t.Helper()
	op, err := f.api.StartFleetOperation(f.ctx, op.Token, op.ContractDigest, alice)
	require.NoError(t, err)
	return op
}

// postAll posts every PLANNED line of a RUNNING operation once, in ordinal order.
func (f *reversalEnv) postAll(t *testing.T, op *FleetOperation) map[int32]FleetPostResult {
	t.Helper()
	lease := FleetLease{Epoch: op.ExecutionEpoch, Grantee: alice.Name, Deadline: time.Now().Add(time.Minute)}
	var lines []FleetOperationLine
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ? AND posting = ?", op.ID, FleetPostingPlanned).Order("ordinal").Find(&lines).Error)
	out := map[int32]FleetPostResult{}
	for _, l := range lines {
		r, err := f.api.PostFleetLine(f.ctx, op.ID, l.ID, lease)
		require.NoError(t, err)
		out[l.Ordinal] = r
	}
	return out
}

func (f *reversalEnv) line(t *testing.T, op *FleetOperation, ordinal int32) FleetOperationLine {
	t.Helper()
	var l FleetOperationLine
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ? AND ordinal = ?", op.ID, ordinal).First(&l).Error)
	return l
}

func (f *reversalEnv) setK(t *testing.T, d *Device, v *string) {
	t.Helper()
	if v == nil {
		_, err := f.api.DeleteEntityAttribute(f.ctx, "device", d.Token, "SHARED", "k")
		require.NoError(t, err)
		return
	}
	_, err := f.api.SetEntityAttribute(f.ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: d.Token, Scope: "SHARED", AttrKey: "k", ValueType: "STRING", Value: v})
	require.NoError(t, err)
}

// k answers the device's current value of k, nil when absent.
func (f *reversalEnv) k(t *testing.T, d *Device) *string {
	t.Helper()
	var a EntityAttribute
	err := f.api.RDB.DB(f.ctx).Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", d.ID).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	require.NoError(t, err)
	v := a.Value.String
	return &v
}

func revReq(original, token string) *FleetReversalRequest {
	return &FleetReversalRequest{Original: original, Token: token, Name: "undo " + original}
}

// reverseAndPost creates a reversal, starts it and posts each of its lines once.
func (f *reversalEnv) reverseAndPost(t *testing.T, ctx context.Context, req *FleetReversalRequest) (*FleetOperation, map[int32]FleetPostResult) {
	t.Helper()
	rev, err := f.api.ReverseFleetOperation(ctx, req, "creator")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, rev.Dispatch)
	rev = f.start(t, rev)
	return rev, f.postAll(t, rev)
}

func TestReversalRestoresTheBeforeImageAndMintsANewRevision(t *testing.T) {
	for name, tc := range map[string]struct {
		prior *string
		unset bool
	}{"a set over a value": {ptr("v0"), false}, "a set over nothing": {nil, false}, "an unset": {ptr("v0"), true}} {
		t.Run(name, func(t *testing.T) {
			f := reversalFixture(t, tc.prior, tc.unset, 1)
			d := f.devs[0]
			var before int64
			require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", d.ID).Count(&before).Error)

			rev, res := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
			require.Equal(t, FleetPosted, res[1].Outcome)
			require.Equal(t, tc.prior, f.k(t, d), "the before-image is back")

			var after int64
			require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", d.ID).Count(&after).Error)
			require.Equal(t, before+1, after, "a reversal mints a new revision; it never rewinds to an old one")
			l := f.line(t, rev, 1)
			require.Equal(t, FleetPostingPosted, l.Posting)
			require.Greater(t, l.PostedRevision.Int64, f.line(t, f.orig, 1).PostedRevision.Int64)
			var posted fleetValueImage
			require.NoError(t, json.Unmarshal([]byte(l.PostedValue.String), &posted))
			require.Equal(t, tc.prior != nil, posted.Present)

			// The links both ways.
			require.True(t, rev.ReversalOf.Valid)
			require.Equal(t, int64(f.orig.ID), rev.ReversalOf.Int64)
			contra, err := f.api.FleetLineReversedBy(f.ctx, ptrLine(f.line(t, f.orig, 1)))
			require.NoError(t, err)
			require.NotNil(t, contra)
			require.Equal(t, l.ID, contra.ID)
			reversals, err := f.api.FleetReversalsOf(f.ctx, f.orig)
			require.NoError(t, err)
			require.Len(t, reversals, 1)
		})
	}
}

func ptrLine(l FleetOperationLine) *FleetOperationLine { return &l }

// M37: a reversal is accepted only once the original can never post again.
func TestReversalIsRefusedWhileTheOriginalCanStillPost(t *testing.T) {
	f := newReversalEnv(t, ptr("v0"), 1)
	v1 := "v1"
	op, err := f.api.CreateFleetOperation(f.ctx, &FleetOperationCreateRequest{Token: "orig", Name: "n", Kind: FleetKindConfigSet,
		DeviceTokens: []string{"dev"}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v1}}, "creator")
	require.NoError(t, err)
	refused := func(state string) {
		t.Helper()
		_, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo-"+state), "creator")
		requireFleetCode(t, err, FleetCodeOriginalStillDispatching)
		_, err = f.api.PreviewFleetReversal(f.ctx, revReq("orig", "undo-"+state))
		requireFleetCode(t, err, FleetCodeOriginalStillDispatching)
		var n int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperation{}).Where("reversal_of IS NOT NULL").Count(&n).Error)
		require.Zero(t, n, "nothing was created")
	}
	refused("READY")
	op = f.start(t, op)
	for _, r := range f.postAll(t, op) {
		require.Equal(t, FleetPosted, r.Outcome)
	}
	refused("RUNNING")
	_, err = f.api.PauseFleetOperation(f.ctx, "orig", alice.Name)
	require.NoError(t, err)
	refused("PAUSED")

	// Negative control: once finished, the same request is accepted.
	op = f.op(t, "orig")
	_, err = f.api.ResumeFleetOperation(f.ctx, "orig", op.ContractDigest, nil, alice)
	require.NoError(t, err)
	res, err := f.api.finishDriven(f.ctx, &FleetDriveResult{}, f.op(t, "orig"))
	require.NoError(t, err)
	require.True(t, res.Dispatched)
	rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, rev.Dispatch)
}

// An ABORTED original keeps its applied lines, and they can be reversed.
func TestAnAbortedOriginalCanBeReversed(t *testing.T) {
	f := newReversalEnv(t, ptr("v0"), 1)
	f.orig = f.runOriginal(t, "orig", false, false)
	require.Equal(t, FleetDispatchAborted, f.orig.Dispatch)
	_, res := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
	require.Equal(t, FleetPosted, res[1].Outcome)
	require.Equal(t, "v0", *f.k(t, f.devs[0]))
}

func TestAReversalOfAReversalIsRefused(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 1)
	rev, _ := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
	f.finish(t, rev)
	_, err := f.api.ReverseFleetOperation(f.ctx, revReq("undo", "redo"), "creator")
	requireFleetCode(t, err, FleetCodeReversalNotReversible)
	_, err = f.api.PreviewFleetReversal(f.ctx, revReq("undo", "redo"))
	requireFleetCode(t, err, FleetCodeReversalNotReversible)
}

// mintRevisionForTest appends a configuration revision through the real minting function, as a device's
// next sync would after its profile changed: a change of the document that wrote no attribute.
func mintRevisionForTest(t *testing.T, f *reversalEnv, d *Device, doc string) {
	t.Helper()
	require.NoError(t, f.api.RDB.DB(f.ctx).Transaction(func(tx *gorm.DB) error {
		_, minted, err := mintConfigurationRevision(tx, d.ID, &ConfigurationDocument{Canonical: []byte(doc), Digest: "sha256:test-" + doc}, nil, "test")
		require.True(t, minted)
		return err
	}))
}

// M18: changed-since is ABA-proof, for a set and for an unset, and needs BOTH of its checks: the
// revision walk (a profile change wrote no attribute, so the generation did not move) and the generation
// (a rewrite of the same value minted no revision, so the walk sees nothing).
func TestChangedSinceIsABAProof(t *testing.T) {
	for name, tc := range map[string]struct {
		unset   bool
		between func(t *testing.T, f *reversalEnv, d *Device)
		changed bool
	}{
		"nothing happened": {between: func(*testing.T, *reversalEnv, *Device) {}},
		"set A-B-A": {between: func(t *testing.T, f *reversalEnv, d *Device) {
			f.setK(t, d, ptr("v2"))
			f.setK(t, d, ptr("v1"))
		}, changed: true},
		"unset A-B-A": {unset: true, between: func(t *testing.T, f *reversalEnv, d *Device) {
			f.setK(t, d, ptr("v2"))
			f.setK(t, d, nil)
		}, changed: true},
		"the same value written again (generation only)": {between: func(t *testing.T, f *reversalEnv, d *Device) {
			f.setK(t, d, ptr("v1"))
		}, changed: true},
		"the key left the document and came back (revisions only)": {between: func(t *testing.T, f *reversalEnv, d *Device) {
			mintRevisionForTest(t, f, d, `{}`)
			mintRevisionForTest(t, f, d, `{"k":"v1"}`)
		}, changed: true},
		"an unset key that appeared and left again (revisions only)": {unset: true, between: func(t *testing.T, f *reversalEnv, d *Device) {
			mintRevisionForTest(t, f, d, `{"k":"v9"}`)
			mintRevisionForTest(t, f, d, `{}`)
		}, changed: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := reversalFixture(t, ptr("v0"), tc.unset, 1)
			d := f.devs[0]
			tc.between(t, f, d)
			current := f.k(t, d)

			p, err := f.api.PreviewFleetReversal(f.ctx, revReq("orig", "undo"))
			require.NoError(t, err)
			require.Equal(t, 1, p.ReversibleCount)
			rev, res := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
			if !tc.changed {
				require.Zero(t, p.ChangedSinceCount)
				require.Equal(t, FleetPosted, res[1].Outcome)
				require.Equal(t, "v0", *f.k(t, d))
				return
			}
			require.Equal(t, 1, p.ChangedSinceCount)
			require.Equal(t, FleetPostRefused, res[1].Outcome)
			require.Equal(t, FleetLineChangedSince, res[1].Code)
			l := f.line(t, rev, 1)
			require.Equal(t, FleetPostingRefused, l.Posting)
			require.Equal(t, FleetLineChangedSince, l.Code)
			require.Equal(t, current, f.k(t, d), "a refused reversal writes nothing")
		})
	}
}

// M17: the original line and the device's state are judged under the device lock. A write that lands
// after the poster first read the line, but before it holds the device, must be seen.
func TestTheChangedSinceCheckRunsUnderTheDeviceLock(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 1)
	d := f.devs[0]
	rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
	require.NoError(t, err)
	rev = f.start(t, rev)
	fleetPostHook = func(stage string, tx *gorm.DB) {
		if stage != "read" {
			return
		}
		// An interactive writer commits between the unlocked read and the try-lock. On SQLite the
		// competing write runs on the poster's own connection; the order of the reads is what is tested.
		gen, err := bumpAttributeGenerationTx(tx, d.ID, "SHARED", "k")
		require.NoError(t, err)
		require.NotZero(t, gen)
	}
	t.Cleanup(func() { fleetPostHook = nil })
	res := f.postAll(t, rev)
	fleetPostHook = nil
	require.Equal(t, FleetPostRefused, res[1].Outcome)
	require.Equal(t, FleetLineChangedSince, res[1].Code)
	require.Equal(t, "v1", *f.k(t, d))
}

// D-L10 and M19 (application half): a CHANGED_SINCE refusal does not consume the original line; a later
// reversal that overrides only that line applies it and records OVERRIDE_CHANGED; a line not overridden
// is still refused; and once one reversal line is applied, any other is ALREADY_REVERSED, naming the winner.
func TestAnOverrideAppliesOnlyItsLineAndARefusalDoesNotConsumeTheOriginal(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 2)
	for _, d := range f.devs {
		f.setK(t, d, ptr("v2"))
	}
	first, res := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
	require.Equal(t, FleetLineChangedSince, res[1].Code)
	require.Equal(t, FleetLineChangedSince, res[2].Code)
	f.finish(t, first)

	// An override needs a fresh second factor.
	req := revReq("orig", "undo-1")
	req.OverrideOrdinals = []int32{1}
	_, err := f.api.ReverseFleetOperation(f.ctx, req, "creator")
	require.Error(t, err)
	stale := resolveContext(f.ctx, fleetClock().Add(-10*time.Minute))
	_, err = f.api.ReverseFleetOperation(stale, req, "creator")
	requireFleetCode(t, err, FleetCodeStepUpRequired)

	stepped := resolveContext(f.ctx, fleetClock())
	// A competitor planned before the override is applied.
	loser, err := f.api.ReverseFleetOperation(stepped, &FleetReversalRequest{Original: "orig", Token: "undo-late", Name: "n", Ordinals: &[]int32{1}, OverrideChanged: true}, "creator")
	require.NoError(t, err)
	override, res := f.reverseAndPost(t, stepped, req)
	f.finish(t, override)
	require.Equal(t, FleetPosted, res[1].Outcome, "the refused first reversal did not consume line 1")
	require.Equal(t, FleetLineChangedSince, res[2].Code, "an override applies only the lines it names")
	require.Equal(t, "v0", *f.k(t, f.devs[0]))
	require.Equal(t, "v2", *f.k(t, f.devs[1]))
	l1 := f.line(t, override, 1)
	require.True(t, l1.OverrideChanged)
	require.False(t, f.line(t, override, 2).OverrideChanged)
	var events []FleetOperationEvent
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ? AND kind = ?", override.ID, fleetEventOverrideChanged).Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, int64(l1.ID), events[0].LineId.Int64)

	// The competitor planned line 1 before it was reversed; at post it loses, and says to whom.
	loser = f.start(t, loser)
	res = f.postAll(t, loser)
	require.Equal(t, FleetPostRefused, res[1].Outcome)
	require.Equal(t, FleetLineAlreadyReversed, res[1].Code)
	require.Contains(t, f.line(t, loser, 1).Detail, `"undo-1"`)
	require.Equal(t, "v0", *f.k(t, f.devs[0]))

	// A reversal planned now refuses the reversed line at plan time.
	p, err := f.api.PreviewFleetReversal(f.ctx, revReq("orig", "again"))
	require.NoError(t, err)
	require.Equal(t, 1, p.AlreadyReversedCount)
	require.Equal(t, 1, p.ReversibleCount)
	_, err = f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "again-1", Name: "n", Ordinals: &[]int32{1}}, "creator")
	requireFleetCode(t, err, FleetCodeNothingToReverse)
}

func TestReversalOfExpiredEvidenceIsRefused(t *testing.T) {
	t.Run("purged", func(t *testing.T) {
		f := reversalFixture(t, ptr("v0"), false, 1)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(f.orig).Update("lines_purged_at", time.Now()).Error)
		_, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
		requireFleetCode(t, err, FleetCodeEvidenceExpired)
	})
	t.Run("retention has begun", func(t *testing.T) {
		f := reversalFixture(t, ptr("v0"), false, 2)
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ? AND ordinal = 2", f.orig.ID).Delete(&FleetOperationLine{}).Error)
		_, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
		requireFleetCode(t, err, FleetCodeEvidenceExpired)
	})
	t.Run("the original line was erased", func(t *testing.T) {
		f := reversalFixture(t, ptr("v0"), false, 2)
		rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
		require.NoError(t, err)
		// Redacted after the reversal was planned: refused at post, under the device lock.
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperationLine{}).Where("operation_id = ? AND ordinal = 1", f.orig.ID).
			Updates(map[string]any{"prior": nil, "posted_value": nil, "target_key": "", "redacted_at": time.Now()}).Error)
		rev = f.start(t, rev)
		res := f.postAll(t, rev)
		require.Equal(t, FleetLinePriorRedacted, res[1].Code)
		require.Equal(t, FleetPosted, res[2].Outcome)
		p, err := f.api.PreviewFleetReversal(f.ctx, revReq("orig", "again"))
		require.NoError(t, err)
		require.Equal(t, 1, p.RedactedCount)
	})
}

func TestReversalSelectionNeverPostedLinesAndReplay(t *testing.T) {
	f := newReversalEnv(t, ptr("v0"), 2)
	v1 := "v1"
	op, err := f.api.CreateFleetOperation(f.ctx, &FleetOperationCreateRequest{Token: "orig", Name: "n", Kind: FleetKindConfigSet,
		DeviceTokens: []string{"dev", "dev2", "ghost"}, ConfigSet: &FleetConfigSetIntent{Key: "k", ValueType: "STRING", Value: &v1}}, "creator")
	require.NoError(t, err)
	op = f.start(t, op)
	lease := FleetLease{Epoch: op.ExecutionEpoch, Grantee: alice.Name, Deadline: time.Now().Add(time.Minute)}
	l1 := f.line(t, op, 1)
	r, err := f.api.PostFleetLine(f.ctx, op.ID, l1.ID, lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
	_, err = f.api.AbortFleetOperation(f.ctx, "orig", alice.Name) // line 2 is cancelled, line 3 was refused at plan
	require.NoError(t, err)

	// Every applied line by default: one.
	p, err := f.api.PreviewFleetReversal(f.ctx, revReq("orig", "undo"))
	require.NoError(t, err)
	require.Equal(t, FleetReversalPreview{ReversibleCount: 1, RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ContractDigest: p.ContractDigest}, *p)
	// A selection naming never-applied lines stores them SKIPPED/NOTHING_TO_REVERSE.
	sel := &FleetReversalRequest{Original: "orig", Token: "undo", Name: "n", Ordinals: &[]int32{3, 1, 2, 1}}
	p2, err := f.api.PreviewFleetReversal(f.ctx, sel)
	require.NoError(t, err)
	require.Equal(t, 2, p2.NotPostedCount)
	require.NotEqual(t, p.ContractDigest, p2.ContractDigest, "the selection is part of the contract")
	rev, err := f.api.ReverseFleetOperation(f.ctx, sel, "creator")
	require.NoError(t, err)
	require.Equal(t, p2.ContractDigest, rev.ContractDigest, "what was previewed is what is created")
	for _, o := range []int32{2, 3} {
		l := f.line(t, rev, o)
		require.Equal(t, "SKIPPED", l.Posting)
		require.Equal(t, FleetLineNothingToReverse, l.Code)
		require.True(t, l.RefusedAtPlan)
	}
	require.Equal(t, FleetPostingPlanned, f.line(t, rev, 1).Posting)

	// Replay: the same request returns the original; another under the token is a CONFLICT.
	again, err := f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "undo", Name: "n", Ordinals: &[]int32{1, 2, 3}}, "creator")
	require.NoError(t, err)
	require.Equal(t, rev.ID, again.ID)
	_, err = f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "undo", Name: "n", Ordinals: &[]int32{1}}, "creator")
	requireFleetCode(t, err, FleetCodeConflict)

	// Selections that reverse nothing.
	_, err = f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "u2", Name: "n", Ordinals: &[]int32{}}, "creator")
	requireFleetCode(t, err, FleetCodeNothingToReverse)
	_, err = f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "u3", Name: "n", Ordinals: &[]int32{2, 3}}, "creator")
	requireFleetCode(t, err, FleetCodeNothingToReverse)
	_, err = f.api.ReverseFleetOperation(f.ctx, &FleetReversalRequest{Original: "orig", Token: "u4", Name: "n", Ordinals: &[]int32{9}}, "creator")
	requireFleetCode(t, err, FleetCodeInvalidRequest)
	stepped := resolveContext(f.ctx, fleetClock())
	_, err = f.api.ReverseFleetOperation(stepped, &FleetReversalRequest{Original: "orig", Token: "u5", Name: "n", Ordinals: &[]int32{1}, OverrideOrdinals: []int32{2}}, "creator")
	requireFleetCode(t, err, FleetCodeInvalidRequest)
	_, err = f.api.ReverseFleetOperation(f.ctx, revReq("missing", "u6"), "creator")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// M30: retention never purges an original while a reversal of it is live; once the reversal is over the
// original is purged, and a later reversal is EVIDENCE_EXPIRED.
func TestRetentionSkipsAnOriginalWithALiveReversal(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 1)
	now := time.Now()
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(f.orig).Updates(map[string]any{"evidence": "CLOSED",
		"closed_at": sql.NullTime{Time: now.Add(-fleetClosedRetention - time.Hour), Valid: true}}).Error)
	rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
	require.NoError(t, err)
	require.NoError(t, f.api.RetainFleetOperations(f.ctx, now))
	require.False(t, f.op(t, "orig").LinesPurgedAt.Valid, "a READY reversal keeps the original's evidence")
	var lines int64
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", f.orig.ID).Count(&lines).Error)
	require.EqualValues(t, 1, lines)

	// Negative control: the reversal ends without applying anything; the original is purged.
	_, err = f.api.AbortFleetOperation(f.ctx, rev.Token, alice.Name)
	require.NoError(t, err)
	require.Equal(t, "CLOSED", f.op(t, "undo").Evidence)
	require.NoError(t, f.api.RetainFleetOperations(f.ctx, now))
	require.True(t, f.op(t, "orig").LinesPurgedAt.Valid)
	_, err = f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo-again"), "creator")
	requireFleetCode(t, err, FleetCodeEvidenceExpired)
}
