// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// The exported metric NAMES must match the regexes the shipped alerts select on.
//
// This is the seam nothing else crosses. The gauges are built by ms.NewGaugeVec,
// which composes namespace + the functional area with its dashes stripped; the
// alerts in deploy/helm/devicechain/templates/prometheusrule-replication.yaml
// select by __name__ regex. Every other test in this package reads values
// straight off the GaugeVec handle, so it never observes the rendered name at
// all — meaning a change to how that name is built (or an area whose name
// composes badly) would leave every Go test green while all four alerts silently
// selected nothing, which is the same class of silence A0 exists to end.
//
// The regexes are restated here rather than parsed out of the chart: this module
// cannot see deploy/. That is a real limitation — it pins the NAME against a copy
// of the selector, so an edit to the chart's regex alone would not fail here. It
// still catches the direction that actually happens, which is the metric name
// moving underneath a stable alert.
func TestExportedMetricNamesMatchTheAlertSelectors(t *testing.T) {
	// Names as they appear in prometheusrule-replication.yaml.
	selectors := map[string]*regexp.Regexp{
		"jetstream_replicas_desired": regexp.MustCompile(`^devicechain_.+_jetstream_replicas_desired$`),
		"jetstream_replicas_actual":  regexp.MustCompile(`^devicechain_.+_jetstream_replicas_actual$`),
		"jetstream_peers_current":    regexp.MustCompile(`^devicechain_.+_jetstream_peers_current$`),
		"jetstream_broker_clustered": regexp.MustCompile(`^devicechain_.+_jetstream_broker_clustered$`),
	}

	// Every area name the platform ships, including the ones whose names compose
	// awkwardly: dashes are stripped, so a one-word area still has to satisfy `.+`.
	for _, area := range []string{
		"device-management", "user-management", "event-processing", "event-sources",
		"event-management", "device-state", "command-delivery", "dashboard-management",
		"notification-management", "outbound-connectors", "ai-inference", "mcp",
		"lwm2m-ingest", "sparkplug-ingest",
	} {
		// promauto registers into the default registerer, so gather from there.
		// Each area yields distinct names (the area is the metric SUBSYSTEM), so
		// building one per area in a single run does not collide.
		m := newStreamMetrics(&core.Microservice{InstanceId: "test", FunctionalArea: area})
		m.brokerClustered.Set(1)
		m.replicasDesired.WithLabelValues("s").Set(1)
		m.replicasActual.WithLabelValues("s").Set(1)
		m.peersCurrent.WithLabelValues("s").Set(1)

		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gathering metrics for %q: %v", area, err)
		}
		// Only this area's families: the gatherer holds every area built so far.
		want := "devicechain_" + strings.ReplaceAll(area, "-", "") + "_"
		seen := map[string]string{}
		for _, f := range families {
			if !strings.HasPrefix(f.GetName(), want) {
				continue
			}
			for suffix := range selectors {
				if strings.HasSuffix(f.GetName(), suffix) {
					seen[suffix] = f.GetName()
				}
			}
		}
		for suffix, re := range selectors {
			name, ok := seen[suffix]
			if !ok {
				t.Errorf("area %q exported no metric ending in %q", area, suffix)
				continue
			}
			if !re.MatchString(name) {
				t.Errorf("area %q exports %q, which the alert selector %q does NOT match: "+
					"every replication alert would select nothing for this area",
					area, name, re)
			}
		}
	}
}
