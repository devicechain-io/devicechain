// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Replay comes first (S1 §5.1): retrying an override reversal that was created returns it and writes
// nothing, even once the second factor that created it is no longer fresh. Step-up guards a write.
func TestReplayingAnOverrideReversalNeedsNoFreshSecondFactor(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 1)
	req := revReq("orig", "undo")
	req.OverrideChanged = true
	created, err := f.api.ReverseFleetOperation(resolveContext(f.ctx, fleetClock()), req, "creator")
	require.NoError(t, err)

	stale := resolveContext(f.ctx, fleetClock().Add(-10*time.Minute))
	again, err := f.api.ReverseFleetOperation(stale, req, "creator")
	require.NoError(t, err, "a replay writes nothing and needs no fresh factor")
	require.Equal(t, created.ID, again.ID)

	// Negative control: a NEW override reversal under a stale factor is still refused.
	_, err = f.api.ReverseFleetOperation(stale, &FleetReversalRequest{Original: "orig", Token: "undo-2", Name: "n", OverrideChanged: true}, "creator")
	requireFleetCode(t, err, FleetCodeStepUpRequired)
}

// A completed reversal's lines can pass retention while its original is still retained. The fact that the
// original line was restored must outlive the contra line: a reversal planned earlier, posting afterwards,
// is still ALREADY_REVERSED, and one planned afterwards refuses the line at plan time.
func TestAPurgedContraStillGuardsTheOriginalLine(t *testing.T) {
	f := reversalFixture(t, ptr("v0"), false, 1)
	first, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
	require.NoError(t, err)
	// Planned while line 1 is not yet reversed, with an override so changed-since cannot be what stops it.
	late, err := f.api.ReverseFleetOperation(resolveContext(f.ctx, fleetClock()),
		&FleetReversalRequest{Original: "orig", Token: "undo-late", Name: "n", OverrideChanged: true}, "creator")
	require.NoError(t, err)
	first = f.start(t, first)
	require.Equal(t, FleetPosted, f.postAll(t, first)[1].Outcome)
	f.finish(t, first)

	// The first reversal's evidence closed long ago; the original's is still open.
	now := time.Now()
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperation{}).Where("token = ?", "undo").Updates(map[string]any{"evidence": "CLOSED",
		"closed_at": sql.NullTime{Time: now.Add(-fleetClosedRetention - time.Hour), Valid: true}}).Error)
	require.NoError(t, f.api.RetainFleetOperations(f.ctx, now))
	require.True(t, f.op(t, "undo").LinesPurgedAt.Valid, "the first reversal's lines are gone")
	require.False(t, f.op(t, "orig").LinesPurgedAt.Valid, "the original is retained")

	late = f.start(t, late)
	res := f.postAll(t, late)
	require.Equal(t, FleetPostRefused, res[1].Outcome)
	require.Equal(t, FleetLineAlreadyReversed, res[1].Code)
	require.Equal(t, "v0", *f.k(t, f.devs[0]))
	p, err := f.api.PreviewFleetReversal(f.ctx, revReq("orig", "again"))
	require.NoError(t, err)
	require.Equal(t, 1, p.AlreadyReversedCount)
	require.Zero(t, p.ReversibleCount)
}

// A before-image that is a present key with a NULL value is restored as such, and the reversal settles
// from the device's own evidence: such a key is not in the device's document, so APPLIED at the posted
// revision confirms it.
func TestAReversalRestoringANullValueSettlesFromDeviceEvidence(t *testing.T) {
	f := newReversalEnv(t, ptr("v0"), 1)
	d := f.devs[0]
	db := f.api.RDB.DB(f.ctx)
	require.NoError(t, db.Model(&EntityAttribute{}).Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", d.ID).
		Update("value", nil).Error)
	f.orig = f.runOriginal(t, "orig", false, true)
	var prior fleetValueImage
	require.NoError(t, json.Unmarshal([]byte(f.line(t, f.orig, 1).Prior.String), &prior))
	require.True(t, prior.Present)
	require.Nil(t, prior.Value)

	rev, res := f.reverseAndPost(t, f.ctx, revReq("orig", "undo"))
	require.Equal(t, FleetPosted, res[1].Outcome)
	l := f.line(t, rev, 1)
	var doc DeviceConfigurationRevision
	require.NoError(t, db.Where("device_id = ? AND revision = ?", d.ID, l.PostedRevision.Int64).First(&doc).Error)
	require.NotContains(t, doc.Document, `"k"`, "a NULL value is not in the device's document")

	require.NoError(t, db.Create(&DeviceConfigurationReport{DeviceId: d.ID, Revision: l.PostedRevision.Int64, Status: "APPLIED", Source: "REPORT",
		Digest: doc.Digest, CreatedAt: time.Now()}).Error)
	_, err := f.api.ReconcileFleetLines(f.ctx, false)
	require.NoError(t, err)
	l = f.line(t, rev, 1)
	require.Equal(t, "CONFIRMED", l.Settlement.String)
}
