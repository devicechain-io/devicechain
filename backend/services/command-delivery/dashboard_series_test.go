// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/processor"
	"github.com/devicechain-io/dc-microservice/core"
)

// boardSelector is one series a panel of the shipped board selects: the family name and the
// equality matchers that narrow it (other than the namespace scope every panel carries).
type boardSelector struct {
	family string
	equal  map[string]string
}

var (
	boardSeriesPattern = regexp.MustCompile(`(devicechain_commanddelivery_[a-z0-9_]+)(\{[^}]*\})?`)
	boardEqualPattern  = regexp.MustCompile(`([a-z_]+)="([^"]*)"`)
)

// boardSelectors reads every devicechain_commanddelivery_* selector out of the shipped
// command-delivery board's panel queries. The list is DERIVED from the board rather than
// retyped, so a panel added to the board is covered the moment it is added.
func boardSelectors(t *testing.T) []boardSelector {
	t.Helper()
	// The board lives outside this module, in the chart. `go test` does not track a file
	// read from out here, which is why CI and the full sweep run with -count=1.
	path := filepath.Join("..", "..", "..", "deploy", "helm", "devicechain", "dashboards", "command-delivery.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var board struct {
		Panels []struct {
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(raw, &board))

	var out []boardSelector
	for _, panel := range board.Panels {
		for _, target := range panel.Targets {
			for _, m := range boardSeriesPattern.FindAllStringSubmatch(target.Expr, -1) {
				sel := boardSelector{family: m[1], equal: map[string]string{}}
				for _, e := range boardEqualPattern.FindAllStringSubmatch(m[2], -1) {
					if e[1] != "namespace" {
						sel.equal[e[1]] = e[2]
					}
				}
				out = append(out, sel)
			}
		}
	}
	// A parse that found nothing would pass every check below by looking at nothing.
	require.NotEmpty(t, out, "no devicechain_commanddelivery_* selector found in %s", path)
	return out
}

// TestAFreshInstanceExportsEverySeriesTheBoardQueries pins the thing a first-time operator
// sees: the command-delivery board on an instance that has not handled a single command.
// A CounterVec gathers nothing until a label is first used, so a panel over one reads "No
// data" instead of zero, which is indistinguishable from a scrape that is not arriving.
//
// It builds the instruments exactly as main.go does (the same three constructors, on a
// registry it can gather) and asks, for every selector the board makes, whether a series
// exists that the selector would match.
func TestAFreshInstanceExportsEverySeriesTheBoardQueries(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "command-delivery"}
	ms.UseMetricsRegistry(reg)

	model.NewBatchMetrics(ms)
	processor.NewCommandDeliveryProcessor(ms, nil, nil, core.NewNoOpLifecycleCallbacks(),
		nil, nil, nil, nil, processor.NewDeliveryMetrics(ms))

	families, err := reg.Gather()
	require.NoError(t, err)
	byName := map[string][]*dto.Metric{}
	for _, f := range families {
		byName[f.GetName()] = f.GetMetric()
	}

	var missing []string
	for _, sel := range boardSelectors(t) {
		if !anySeriesMatches(byName[sel.family], sel.equal) {
			missing = append(missing, sel.family+selectorText(sel.equal))
		}
	}
	sort.Strings(missing)
	require.Empty(t, dedupe(missing),
		"the board selects series a fresh instance does not export, so these panels read \"No data\" "+
			"until the first event. Seed the closed label set at zero where the counter is built.")
}

func anySeriesMatches(metrics []*dto.Metric, equal map[string]string) bool {
	for _, m := range metrics {
		have := map[string]string{}
		for _, l := range m.GetLabel() {
			have[l.GetName()] = l.GetValue()
		}
		ok := true
		for k, v := range equal {
			if have[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func selectorText(equal map[string]string) string {
	if len(equal) == 0 {
		return ""
	}
	parts := make([]string, 0, len(equal))
	for k, v := range equal {
		parts = append(parts, k+`="`+v+`"`)
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

func dedupe(sorted []string) []string {
	var out []string
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
