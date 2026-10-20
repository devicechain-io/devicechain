// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package backfill

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-management/model"
)

// doorPlans is device-management's plan door for the rig's profile.
type doorPlans struct{}

func (doorPlans) Plan(context.Context, string, string, *int) (*model.CalculatedPlanDoc, json.RawMessage, error) {
	doc := planDoc()
	return &doc, mustJSON(doc), nil
}
func (doorPlans) CheckDevices(context.Context, string, string, []string) error { return nil }

func (r *rig) startAPI(now time.Time) {
	r.api.Backfill = &model.BackfillRuntime{Runs: true, Plans: doorPlans{}, ChunkInterval: 24 * time.Hour,
		Now: func() time.Time { return now }}
}

// A job started through the API, not inserted by the test, is picked up by the worker, filled, and
// read back through the API's own reads with the counters the worker committed. Nothing is
// different between this job and a hand-inserted one except who created it, and that is the point:
// the API's frozen plan, keys and filter are what the worker can run.
func TestIntegrationStartedJobIsPickedUpAndFilled(t *testing.T) {
	r := newRig(t, "startrun")
	now := time.Now().UTC().Truncate(time.Second)
	r.twoDevices(now)
	r.startAPI(now)
	r.build()

	started, err := r.api.StartBackfill(r.ctx(), model.StartBackfillRequest{
		ProfileToken: "meter", From: now.Add(-72 * time.Hour), To: now}, "op@example.com")
	require.NoError(t, err)
	assert.Equal(t, model.BackfillQueued, started.Status)
	assert.JSONEq(t, `["power","kw","flag"]`, string(started.MetricKeys), "the keys new in the version")

	done := r.runUntilDone(started.Token)
	require.Equal(t, model.BackfillSucceeded, done.Status, "failure: %v", done.Failure)
	assert.NotZero(t, done.Written)
	assert.EqualValues(t, 2, done.DevicesDone)

	read, err := r.api.GetBackfill(r.ctx(), started.Token)
	require.NoError(t, err)
	assert.Equal(t, done.Written, read.Written)
	page, err := r.api.ListBackfills(r.ctx(), model.ListBackfillsRequest{})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	assert.Equal(t, started.Token, page.Jobs[0].Token)

	// A second job over the same range is accepted (the first is finished) and writes nothing.
	again, err := r.api.StartBackfill(r.ctx(), model.StartBackfillRequest{
		ProfileToken: "meter", From: now.Add(-72 * time.Hour), To: now}, "op@example.com")
	require.NoError(t, err)
	second := r.runUntilDone(again.Token)
	require.Equal(t, model.BackfillSucceeded, second.Status)
	assert.Zero(t, second.Written)
}

// A device filter the API froze reaches the worker: only the named device is filled.
func TestIntegrationStartedJobHonoursItsDeviceFilter(t *testing.T) {
	r := newRig(t, "startfilter")
	now := time.Now().UTC().Truncate(time.Second)
	r.twoDevices(now)
	r.startAPI(now)
	r.build()
	before := r.countRows("d-b")

	started, err := r.api.StartBackfill(r.ctx(), model.StartBackfillRequest{
		ProfileToken: "meter", From: now.Add(-72 * time.Hour), To: now, DeviceTokens: []string{"d-a"}}, "")
	require.NoError(t, err)
	done := r.runUntilDone(started.Token)
	require.Equal(t, model.BackfillSucceeded, done.Status, "failure: %v", done.Failure)
	assert.EqualValues(t, 1, done.DevicesDone)
	assert.Equal(t, before, r.countRows("d-b"), "the device not named is untouched")
}

// Cancelling through the API while the worker is mid-job: the request is recorded, the worker's
// next commit observes it, and the job ends CANCELLED with the rows it had written still there.
// Cancelling a QUEUED job never reaches the worker at all.
func TestIntegrationCancelThroughTheAPI(t *testing.T) {
	r := newRig(t, "startcancel")
	now := time.Now().UTC().Truncate(time.Second)
	r.twoDevices(now)
	r.startAPI(now)
	w := r.build()

	queued, err := r.api.StartBackfill(r.ctx(), model.StartBackfillRequest{
		ProfileToken: "meter", From: now.Add(-72 * time.Hour), To: now}, "")
	require.NoError(t, err)
	got, err := r.api.CancelBackfill(r.ctx(), queued.Token)
	require.NoError(t, err)
	assert.Equal(t, model.BackfillCancelled, got.Status)
	_ = r.worker.RunOnce(context.Background())
	assert.Zero(t, r.job(queued.Token).Written, "a cancelled queued job is never run")

	running, err := r.api.StartBackfill(r.ctx(), model.StartBackfillRequest{
		ProfileToken: "meter", From: now.Add(-72 * time.Hour), To: now}, "")
	require.NoError(t, err)
	commits := 0
	w.hooks.afterCommit = func(ctx context.Context) error {
		commits++
		if commits == 2 {
			_, err := r.api.CancelBackfill(r.ctx(), running.Token)
			return err
		}
		return nil
	}
	done := r.runUntilDone(running.Token)
	assert.Equal(t, model.BackfillCancelled, done.Status)
	assert.NotNil(t, done.CancelRequestedAt)
	assert.NotZero(t, done.Written, "what was written before the cancel stays")
	assert.Equal(t, done.Written, int64(len(r.derived("d-a", "d-b"))), "and the counter matches the rows")
}
