// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func tenantCtx(tenant string) context.Context { return core.WithTenant(context.Background(), tenant) }

func startAs(t *testing.T, api *Api, tenant string, mutate ...func(*StartBackfillRequest)) (*CalculatedBackfill, error) {
	t.Helper()
	req := goodRequest()
	for _, m := range mutate {
		m(&req)
	}
	return api.StartBackfill(tenantCtx(tenant), req, "op@example.com")
}

// A start queues a job that carries everything the worker and the reader need: the door's plan
// verbatim, the default keys, the warnings, the device filter, who started it, and a QUEUED status.
func TestIntegrationStartQueuesTheFrozenJob(t *testing.T) {
	mgr := newBackfillManager(t, "its82bstart")
	plans := &fakePlans{doc: meterPlan()}
	// kw and flag, so the chain warning and the changed-key warning both fire.
	api := &Api{RDB: mgr, Backfill: newRuntime(plans)}
	job, err := startAs(t, api, "acme", func(r *StartBackfillRequest) {
		r.MetricKeys = []string{"flag", "kw", "power"}
		r.DeviceTokens = []string{"d1", "d2", "d1"}
	})
	require.NoError(t, err)

	got, err := api.GetBackfill(tenantCtx("acme"), job.Token)
	require.NoError(t, err)
	assert.Equal(t, BackfillQueued, got.Status)
	assert.Equal(t, "meter", got.ProfileToken)
	assert.Equal(t, "meter@3", got.ProfileVersionToken)
	assert.EqualValues(t, 3, got.ProfileVersion)
	assert.JSONEq(t, `["flag","kw","power"]`, string(got.MetricKeys))
	assert.JSONEq(t, `["d1","d2"]`, string(got.DeviceFilter), "a repeated token is one device")
	assert.Equal(t, "op@example.com", *got.CreatedBy)
	assert.Equal(t, goodRequest().From, got.RangeFrom.UTC())
	assert.Equal(t, goodRequest().To, got.RangeTo.UTC())
	want, _ := json.Marshal(meterPlan())
	assert.JSONEq(t, string(want), string(got.Plan))
	var warnings []string
	require.NoError(t, json.Unmarshal(got.Warnings, &warnings))
	assert.Len(t, warnings, 1, "kw changed; flag's upstream kw is selected")
	assert.Equal(t, [][]string{{"d1", "d2"}}, plans.checked, "the devices were checked against the profile")

	// With no keys named, the defaults are the keys new in the version.
	api2 := &Api{RDB: mgr, Backfill: newRuntime(&fakePlans{doc: cleanPlan()})}
	d, err := startAs(t, api2, "other")
	require.NoError(t, err)
	assert.JSONEq(t, `["power"]`, string(d.MetricKeys))
	assert.Nil(t, d.DeviceFilter, "no devices named is every device")
}

// One active job per tenant: the second start is a CONFLICT that names the first, another tenant
// is unaffected, and the slot frees when the job is cancelled.
func TestIntegrationOneActiveJobPerTenant(t *testing.T) {
	mgr := newBackfillManager(t, "its82bconflict")
	api := &Api{RDB: mgr, Backfill: newRuntime(&fakePlans{doc: cleanPlan()})}
	first, err := startAs(t, api, "acme")
	require.NoError(t, err)

	_, err = startAs(t, api, "acme")
	require.Error(t, err)
	assert.True(t, conflict.Is(err), "CONFLICT: %v", err)
	assert.Contains(t, err.Error(), first.Token, "the refusal names the job holding the slot")

	// Running is still active.
	_, err = ClaimBackfill(mgr.Database.WithContext(tenantCtx("acme")), first.Token)
	require.NoError(t, err)
	_, err = startAs(t, api, "acme")
	assert.True(t, conflict.Is(err))

	_, err = startAs(t, api, "globex")
	require.NoError(t, err, "another tenant has its own slot")

	_, err = api.CancelBackfill(tenantCtx("acme"), first.Token)
	require.NoError(t, err)
	// Cancelling a RUNNING job only records the request; it is still the active one until the
	// worker observes it.
	_, err = startAs(t, api, "acme")
	assert.True(t, conflict.Is(err), "a cancel request has not freed the slot")
	require.NoError(t, FinishBackfill(mgr.Database.WithContext(tenantCtx("acme")), first.Token, 1, BackfillCancelled, ""))
	_, err = startAs(t, api, "acme")
	require.NoError(t, err, "a finished job frees the slot")
}

// Cancel: a QUEUED job at once; a RUNNING one by request (status unchanged until the worker
// commits); a finished one is a CONFLICT; an unknown or another tenant's token is NOT_FOUND.
func TestIntegrationCancel(t *testing.T) {
	mgr := newBackfillManager(t, "its82bcancel")
	api := &Api{RDB: mgr, Backfill: newRuntime(&fakePlans{doc: cleanPlan()})}

	queued, err := startAs(t, api, "acme")
	require.NoError(t, err)
	got, err := api.CancelBackfill(tenantCtx("acme"), queued.Token)
	require.NoError(t, err)
	assert.Equal(t, BackfillCancelled, got.Status)
	assert.NotNil(t, got.FinishedAt)

	_, err = api.CancelBackfill(tenantCtx("acme"), queued.Token)
	require.Error(t, err)
	assert.True(t, conflict.Is(err), "cancelling a finished job is a CONFLICT: %v", err)
	assert.Contains(t, err.Error(), "CANCELLED")

	running, err := startAs(t, api, "acme")
	require.NoError(t, err)
	_, err = ClaimBackfill(mgr.Database.WithContext(tenantCtx("acme")), running.Token)
	require.NoError(t, err)
	got, err = api.CancelBackfill(tenantCtx("acme"), running.Token)
	require.NoError(t, err)
	assert.Equal(t, BackfillRunning, got.Status, "the worker finishes the transition")
	assert.NotNil(t, got.CancelRequestedAt)
	again, err := api.CancelBackfill(tenantCtx("acme"), running.Token)
	require.NoError(t, err, "asking twice is harmless")
	assert.Equal(t, got.CancelRequestedAt.UTC(), again.CancelRequestedAt.UTC(), "and does not move the request")

	_, err = api.CancelBackfill(tenantCtx("acme"), "nope")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = api.CancelBackfill(tenantCtx("globex"), running.Token)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "another tenant's job does not exist for this one")
	same, err := api.GetBackfill(tenantCtx("acme"), running.Token)
	require.NoError(t, err)
	assert.Equal(t, BackfillRunning, same.Status, "and was not touched")
}

// The list is newest first and keyset-paged, filters by profile and status, and is the tenant's own.
func TestIntegrationListOrderingPagingAndIsolation(t *testing.T) {
	mgr := newBackfillManager(t, "its82blist")
	api := &Api{RDB: mgr, Backfill: newRuntime(&fakePlans{doc: cleanPlan()})}
	var tokens []string
	for i := 0; i < 5; i++ {
		j, err := startAs(t, api, "acme")
		require.NoError(t, err)
		tokens = append([]string{j.Token}, tokens...) // newest first
		if i < 4 {
			_, err = api.CancelBackfill(tenantCtx("acme"), j.Token)
			require.NoError(t, err)
		}
	}
	other, err := startAs(t, api, "globex")
	require.NoError(t, err)

	page := func(req ListBackfillsRequest) ([]string, string) {
		t.Helper()
		p, err := api.ListBackfills(tenantCtx("acme"), req)
		require.NoError(t, err)
		var out []string
		for _, j := range p.Jobs {
			out = append(out, j.Token)
		}
		return out, p.NextCursor
	}
	all, cursor := page(ListBackfillsRequest{})
	assert.Equal(t, tokens, all)
	assert.Empty(t, cursor)
	assert.NotContains(t, all, other.Token)

	p1, c1 := page(ListBackfillsRequest{First: 2})
	assert.Equal(t, tokens[:2], p1)
	require.NotEmpty(t, c1)
	p2, c2 := page(ListBackfillsRequest{First: 2, After: c1})
	assert.Equal(t, tokens[2:4], p2)
	p3, c3 := page(ListBackfillsRequest{First: 2, After: c2})
	assert.Equal(t, tokens[4:], p3)
	assert.Empty(t, c3)

	active, _ := page(ListBackfillsRequest{Statuses: []string{BackfillQueued}})
	assert.Equal(t, tokens[:1], active)
	cancelled, _ := page(ListBackfillsRequest{Statuses: []string{BackfillCancelled}, ProfileToken: "meter"})
	assert.Equal(t, tokens[1:], cancelled)
	none, _ := page(ListBackfillsRequest{ProfileToken: "pump"})
	assert.Empty(t, none)

	_, err = api.GetBackfill(tenantCtx("acme"), other.Token)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
