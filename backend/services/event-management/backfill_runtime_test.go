// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The backfill API answers from the SAME decision the worker is wired from
// (CalculatedBackfillConfiguration.Runs), so a start is refused, with the reason, exactly when no
// worker would run the job. Nothing else runs this wiring: the model test hands the API a runtime
// it built itself, so a service that stopped passing the configuration would queue jobs nothing
// runs while every other test stayed green. Each row is a configuration document, never a struct.
func TestStartIsRefusedWhenTheConfigurationRunsNoWorker(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, doc string
		runs      bool
		logged    string // in the startup log's words
		served    string // in the words a tenant's caller is refused with
	}{
		{"defaults run it", `{}`, true, "", ""},
		{"asked for and it fits", `{"CalculatedBackfill":{"Enabled":true}}`, true, "", ""},
		{"disabled", `{"CalculatedBackfill":{"Enabled":false}}`, false, "calculatedBackfill.enabled is false",
			"the operator has switched it off"},
		// 16 writers on the default pool of 20 is 16 + 1 + 4 = 21, unset, so the service starts
		// with the worker off (asked for with true, startup would have refused).
		{"pool too small beside the writers", `{"Persistence":{"Writers":16}}`, false, "persistence.writers (16)",
			"no connection to spare"},
		{"a small pool", `{"TsdbConfiguration":{"MaxOpenConnections":12}}`, false, "tsdbConfiguration.maxOpenConnections",
			"no connection to spare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.EventManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
			rt, why := newBackfillRuntime(cfg)
			assert.Equal(t, tc.runs, rt.Runs)
			assert.Contains(t, why, tc.logged)
			assert.Contains(t, rt.WhyNot, tc.served)

			rt.Now = func() time.Time { return now }
			rt.Plans = nil // only the gate under test: a runtime that runs is stopped one step later
			_, err := (&model.Api{Backfill: rt}).StartBackfill(core.WithTenant(context.Background(), "acme"),
				model.StartBackfillRequest{ProfileToken: "meter", From: now.Add(-48 * time.Hour), To: now.Add(-time.Hour)}, "")
			var un *model.BackfillUnavailable
			require.ErrorAs(t, err, &un, "either way the request stops at UNAVAILABLE here")
			if tc.runs {
				assert.Contains(t, err.Error(), "device-management is not configured", "it got past the worker check")
			} else {
				assert.Contains(t, err.Error(), "the backfill worker is not running")
				assert.Contains(t, err.Error(), tc.served, "the reason the worker is off reaches the caller")
				// ...and the instance's settings and sizing do not: any tenant user with event:read
				// can read this reason through calculatedMetricBackfillAvailability.
				assert.NotContains(t, err.Error(), why)
				for _, internal := range []string{"persistence.", "tsdbConfiguration.", "calculatedBackfill.", "16", "12", "21"} {
					assert.NotContains(t, err.Error(), internal)
				}
			}
		})
	}
}

// The runtime carries the lifecycle the range is checked against.
func TestBackfillRuntimeCarriesTheRetention(t *testing.T) {
	cfg := &config.EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"Lifecycle":{"RetentionDays":45,"ChunkIntervalHours":12}}`), cfg))
	rt, _ := newBackfillRuntime(cfg)
	assert.Equal(t, 45, rt.RetentionDays)
	assert.Equal(t, 12*time.Hour, rt.ChunkInterval)
}

// The availability probe and a refused start speak from ONE function (Api.BackfillAvailability):
// for every configuration document, the probe's reason is byte-for-byte the reason the start is
// refused with, and a runtime the probe calls available is one a start gets past the gate of.
func TestAvailabilityProbeAndStartShareOneDecision(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, doc string
		reason    string // a fragment; empty means available
	}{
		{"defaults run it", `{}`, ""},
		{"disabled", `{"CalculatedBackfill":{"Enabled":false}}`, "the operator has switched it off"},
		{"pool too small", `{"Persistence":{"Writers":16}}`, "no connection to spare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.EventManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
			rt, _ := newBackfillRuntime(cfg)
			rt.Now = func() time.Time { return now }
			rt.Plans = nil
			api := &model.Api{Backfill: rt}

			probe := api.BackfillAvailability()
			_, err := api.StartBackfill(core.WithTenant(context.Background(), "acme"),
				model.StartBackfillRequest{ProfileToken: "meter", From: now.Add(-48 * time.Hour), To: now.Add(-time.Hour)}, "")
			var un *model.BackfillUnavailable
			require.ErrorAs(t, err, &un)
			if tc.reason == "" {
				// Plans is nil here, so the start stops at the one gate the probe also names.
				require.NotNil(t, probe)
				assert.Contains(t, probe.Reason, "device-management is not configured")
			} else {
				require.NotNil(t, probe)
				assert.Contains(t, probe.Reason, tc.reason)
			}
			assert.Equal(t, un.Reason, probe.Reason, "one function: the start's refusal IS the probe's reason")
		})
	}
}
