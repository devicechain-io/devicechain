// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCheckInstanceIdGrammar(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"foo", ""}, {"dc-local", ""}, {"a", ""}, {"a1-b2", ""},
		{strings.Repeat("a", 50), ""},
		{"foo_bar", "underscore"}, {"Foo", "uppercase"},
		{"-foo", "leading/trailing hyphen"}, {"foo-", "leading/trailing hyphen"},
		{strings.Repeat("a", 51), "too long"}, {"", "empty"}, {"foo.bar", "character class"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			err := CheckInstanceIdGrammar(tc.id)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

// gatherNoncanonical returns the value of the one gauge reg holds, failing if it is absent.
func gatherNoncanonical(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1, "startup must export the gauge even when it is zero")
	require.Equal(t, "devicechain_devicemanagement_instance_id_noncanonical", families[0].GetName())
	require.Len(t, families[0].Metric, 1)
	return families[0].Metric[0].GetGauge().GetValue()
}

// The warning goes to the logger it is handed, so it is observed here without
// reassigning the global logger (which TestNoTestInTheRepositorySwapsTheGlobalLogger forbids).
func TestReportInstanceIdGrammarWarnsAndSetsTheGauge(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"foo_bar", 1}, {"foo", 0},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var output bytes.Buffer
			logger := zerolog.New(&output)
			reg := prometheus.NewRegistry()
			ms := &Microservice{InstanceId: tc.id, FunctionalArea: "device-management"}
			ms.UseMetricsRegistry(reg)
			// Twice, to pin single-shot collector registration across a retried start.
			for range 2 {
				output.Reset()
				ms.reportInstanceIdGrammar(&logger)
				var warnings []map[string]interface{}
				for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var event map[string]interface{}
					require.NoError(t, json.Unmarshal(line, &event))
					if event["level"] == "warn" {
						warnings = append(warnings, event)
					}
				}
				if tc.want == 1 {
					require.Len(t, warnings, 1)
					require.Equal(t, tc.id, warnings[0]["instanceId"])
					require.Contains(t, warnings[0]["error"], "underscore")
					for _, phrase := range []string{"accepted for compatibility", "collide", "shared broker or database", "chart and dcctl refuse", "recreate under a canonical id"} {
						require.Contains(t, warnings[0]["message"], phrase)
					}
				} else {
					require.Empty(t, warnings)
				}
				require.Equal(t, tc.want, gatherNoncanonical(t, reg))
			}
		})
	}
}

// Test the caller: InitializeAndStart reports the grammar (and does not refuse the id)
// before the next guard, the removed-variable refusal, stops it.
func TestInitializeAndStartReportsInstanceIdGrammar(t *testing.T) {
	t.Setenv(ENV_REMOVED_SHUTDOWN_DRAIN_SECONDS, "1")
	for _, tc := range []struct {
		id   string
		want float64
	}{
		{"foo_bar", 1}, {"foo", 0},
	} {
		t.Run(tc.id, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			ms := &Microservice{InstanceId: tc.id, FunctionalArea: "device-management"}
			ms.UseMetricsRegistry(reg)
			for range 2 {
				err := ms.InitializeAndStart()
				require.ErrorContains(t, err, ENV_REMOVED_SHUTDOWN_DRAIN_SECONDS)
				require.NotContains(t, err.Error(), "invalid instance id")
				require.Equal(t, tc.want, gatherNoncanonical(t, reg))
			}
		})
	}
}
