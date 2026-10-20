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
		reason    string
	}{
		{"defaults run it", `{}`, true, ""},
		{"asked for and it fits", `{"CalculatedBackfill":{"Enabled":true}}`, true, ""},
		{"disabled", `{"CalculatedBackfill":{"Enabled":false}}`, false, "calculatedBackfill.enabled is false"},
		// 16 writers on the default pool of 20 is 16 + 1 + 4 = 21, unset, so the service starts
		// with the worker off (asked for with true, startup would have refused).
		{"pool too small beside the writers", `{"Persistence":{"Writers":16}}`, false, "persistence.writers (16)"},
		{"a small pool", `{"TsdbConfiguration":{"MaxOpenConnections":12}}`, false, "tsdbConfiguration.maxOpenConnections"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.EventManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
			rt := newBackfillRuntime(cfg)
			assert.Equal(t, tc.runs, rt.Runs)
			assert.Contains(t, rt.WhyNot, tc.reason)

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
				assert.Contains(t, err.Error(), tc.reason, "the reason the worker is off reaches the caller")
			}
		})
	}
}

// The runtime carries the lifecycle the range is checked against.
func TestBackfillRuntimeCarriesTheRetention(t *testing.T) {
	cfg := &config.EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"Lifecycle":{"RetentionDays":45,"ChunkIntervalHours":12}}`), cfg))
	rt := newBackfillRuntime(cfg)
	assert.Equal(t, 45, rt.RetentionDays)
	assert.Equal(t, 12*time.Hour, rt.ChunkInterval)
}
