// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wirePlans is a plan source that records what the resolver handed the model.
type wirePlans struct {
	version *int
	devices []string
}

func (p *wirePlans) Plan(_ context.Context, _, profile string, version *int) (*model.CalculatedPlanDoc, json.RawMessage, error) {
	p.version = version
	doc := &model.CalculatedPlanDoc{
		VersionToken: profile + "@4", Version: 4,
		Measured: []model.CalculatedPlanInput{{Key: "voltage", DataType: "DOUBLE"}},
		Calculated: []model.CalculatedPlanMetric{
			{Key: "power", Expression: "voltage * 2.0", DataType: "DOUBLE", Classifier: 1, NewInVersion: true},
			{Key: "half", Expression: "voltage / 2.0", DataType: "DOUBLE", Classifier: 2},
		},
	}
	raw, err := json.Marshal(doc)
	return doc, raw, err
}

func (p *wirePlans) CheckDevices(_ context.Context, _, _ string, tokens []string) error {
	p.devices = tokens
	return nil
}

// What the caller sends is what the job holds: the resolver carries every input field into the
// request, and attributes the job to the caller. The model tests build the request themselves, so
// a resolver that dropped a field, or parsed one from the wrong input, would pass all of them.
func TestStartBackfillCarriesTheInputIntoTheJob(t *testing.T) {
	api, _ := newBackfillApi(t)
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-50*time.Hour), now.Add(-2*time.Hour)
	plans := &wirePlans{}
	api.Backfill = &model.BackfillRuntime{Runs: true, Plans: plans}

	resp := execAs(t, api, "t1", nil, []string{string(auth.DeviceWrite)},
		`mutation($in: CalculatedMetricBackfillInput!) { startCalculatedMetricBackfill(input: $in) {
			token status profileToken profileVersion metricKeys from to createdBy } }`,
		map[string]any{"in": map[string]any{"profileToken": "meter", "version": 4, "metricKeys": []any{"half"},
			"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "deviceTokens": []any{"d1", "d2"}}})
	require.Empty(t, resp.Errors)
	var out struct {
		J struct {
			Token, Status, ProfileToken, From, To, CreatedBy string
			ProfileVersion                                   int
			MetricKeys                                       []string
		} `json:"startCalculatedMetricBackfill"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &out))
	j := out.J
	assert.NotEmpty(t, j.Token)
	assert.Equal(t, "QUEUED", j.Status)
	assert.Equal(t, "meter", j.ProfileToken)
	assert.Equal(t, 4, j.ProfileVersion)
	assert.Equal(t, []string{"half"}, j.MetricKeys, "the named key, not the default")
	assert.Equal(t, from.Format(time.RFC3339Nano), j.From)
	assert.Equal(t, to.Format(time.RFC3339Nano), j.To)
	assert.Equal(t, "op@example.com", j.CreatedBy, "the job is attributed to the caller")
	require.NotNil(t, plans.version, "the version was passed to the plan source")
	assert.Equal(t, 4, *plans.version)
	assert.Equal(t, []string{"d1", "d2"}, plans.devices, "the device tokens were checked")

	stored, err := api.GetBackfill(core.WithTenant(context.Background(), "t1"), j.Token)
	require.NoError(t, err)
	assert.JSONEq(t, `["d1","d2"]`, string(stored.DeviceFilter), "and frozen on the job")
}

// UNAVAILABLE reaches the wire as its code, with the reason, when nothing would run the job.
// (CONFLICT and NOT_FOUND of cancel need PostgreSQL's now(); the integration tests hold them.)
func TestBackfillUnavailableReachesTheWire(t *testing.T) {
	api, db := newBackfillApi(t)
	seedJob(t, db, "t1", "j1", "meter", model.BackfillSucceeded)
	write := []string{string(auth.DeviceWrite)}

	resp := execAs(t, api, "t1", nil, write, startDoc, startVars())
	require.NotEmpty(t, resp.Errors)
	assert.Equal(t, model.CodeUnavailable, resp.Errors[0].Extensions["code"], "no runtime: %s", resp.Errors[0].Message)

	api.Backfill = &model.BackfillRuntime{Runs: false, WhyNot: "calculatedBackfill.enabled is false", Plans: &wirePlans{}}
	resp = execAs(t, api, "t1", nil, write, startDoc, startVars())
	require.NotEmpty(t, resp.Errors)
	assert.Equal(t, model.CodeUnavailable, resp.Errors[0].Extensions["code"])
	assert.Contains(t, resp.Errors[0].Message, "calculatedBackfill.enabled is false")
}

// A customer-scoped caller is refused by the RESOLVER, before anything reaches the store. The
// store refuses a scoped read of this table too (it declares no scope rule), so a resolver that
// admitted scoped callers would still answer FORBIDDEN in the matrix above; this Api has no
// database, so here only the resolver's own gate can answer.
func TestBackfillScopedCallerIsRefusedBeforeTheStore(t *testing.T) {
	api := &model.Api{}
	for _, op := range []struct {
		name, query string
		vars        map[string]any
	}{
		{"start", startDoc, startVars()},
		{"cancel", cancelDoc, nil},
		{"read", readDoc, nil},
		{"list", listDoc, nil},
		{"availability", availDoc, nil},
	} {
		t.Run(op.name, func(t *testing.T) {
			resp := execAs(t, api, "t1", []string{"customer:acme"},
				[]string{string(auth.DeviceWrite), string(auth.EventRead)}, op.query, op.vars)
			require.NotEmpty(t, resp.Errors)
			assert.ErrorIs(t, resp.Errors[0].ResolverError, auth.ErrScopeUnsupported, resp.Errors[0].Message)
		})
	}
}

// The availability probe answers on the wire with the SAME decision a start is refused by: for
// each runtime the probe's reason equals the message-bearing reason of the refused start, and the
// runtime that can run answers available with a null reason.
func TestBackfillAvailabilityProbe(t *testing.T) {
	read := []string{string(auth.EventRead)}
	write := []string{string(auth.DeviceWrite)}
	probe := func(t *testing.T, api *model.Api) (bool, *string) {
		t.Helper()
		resp := execAs(t, api, "t1", nil, read, availDoc, nil)
		require.Empty(t, resp.Errors)
		var out struct {
			A struct {
				Available bool
				Reason    *string
			} `json:"calculatedMetricBackfillAvailability"`
		}
		require.NoError(t, json.Unmarshal(resp.Data, &out))
		return out.A.Available, out.A.Reason
	}
	for _, tc := range []struct {
		name string
		rt   *model.BackfillRuntime
	}{
		{"no runtime", nil},
		{"disabled", &model.BackfillRuntime{Runs: false, WhyNot: "calculatedBackfill.enabled is false", Plans: &wirePlans{}}},
		{"pool too small", &model.BackfillRuntime{Runs: false, WhyNot: "persistence.writers (16) + the backfill worker (1) + 4 spare connections is 21", Plans: &wirePlans{}}},
		{"no reason given", &model.BackfillRuntime{Runs: false, Plans: &wirePlans{}}},
		{"no device-management", &model.BackfillRuntime{Runs: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := newBackfillApi(t)
			api.Backfill = tc.rt
			avail, reason := probe(t, api)
			assert.False(t, avail)
			require.NotNil(t, reason)

			resp := execAs(t, api, "t1", nil, write, startDoc, startVars())
			require.NotEmpty(t, resp.Errors)
			assert.Equal(t, model.CodeUnavailable, resp.Errors[0].Extensions["code"])
			var un *model.BackfillUnavailable
			require.ErrorAs(t, resp.Errors[0].ResolverError, &un)
			assert.Equal(t, un.Reason, *reason, "the probe and a refused start give the same reason")
		})
	}
	t.Run("runs", func(t *testing.T) {
		api, _ := newBackfillApi(t)
		api.Backfill = &model.BackfillRuntime{Runs: true, Plans: &wirePlans{}}
		avail, reason := probe(t, api)
		assert.True(t, avail)
		assert.Nil(t, reason)
	})
}
