// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// presetRender is what one size renders to, written out as literals. It is NOT read from sizePresets:
// a test that read its expectations from the table would pass whatever the table said, so a number
// moving in size.go (or a preset vanishing) must fail here by value. The figures are the ones the
// sizing page and the release notes publish; changing one means changing both on purpose.
type presetRender struct {
	// Per area: replicas (0 = not stated), cpu limit, memory limit ("" = the chart's floor
	// applies), cpu request.
	areas map[string][4]string
	// The instance-root variables the size states.
	vars map[string]string
	// The cluster's relational max_wal_size in MiB, published beside the size.
	relationalWALMiB int
	// The events a second the hot streams are sized to hold.
	designRate int64
}

var presetRenders = map[string]presetRender{
	"perf-12k": {
		areas: map[string][4]string{
			"event-sources":     {"3", "3", "256Mi", "600m"},
			"device-management": {"2", "3", "", "400m"},
			"device-state":      {"2", "3", "512Mi", "250m"},
			"event-management":  {"", "4", "1Gi", "400m"},
			"event-processing":  {"", "2", "1Gi", "500m"},
		},
		vars: map[string]string{
			"nats_cpu_request":           "4",
			"nats_memory_request":        "4Gi",
			"nats_memory_limit":          "8Gi",
			"nats_jetstream_storage":     "28Gi",
			"timescale_storage":          "160Gi",
			"timescale_max_wal_size_mib": "20480",
		},
		relationalWALMiB: 4096,
		designRate:       12000,
	},
	"perf-16k": {
		areas: map[string][4]string{
			"event-sources":     {"3", "3", "256Mi", "750m"},
			"device-management": {"2", "3", "", "500m"},
			"device-state":      {"2", "3", "512Mi", "300m"},
			"event-management":  {"", "4", "1Gi", "450m"},
			"event-processing":  {"", "2", "1536Mi", "700m"},
		},
		vars: map[string]string{
			"nats_cpu_request":           "6",
			"nats_memory_request":        "6Gi",
			"nats_memory_limit":          "10Gi",
			"nats_jetstream_storage":     "36Gi",
			"timescale_storage":          "260Gi",
			"timescale_max_wal_size_mib": "40960",
		},
		relationalWALMiB: 8192,
		designRate:       16000,
	},
}

func perfStateNamed(name string, ha bool) *State {
	st := persistenceState(ha, false, "default")
	st.Size = name
	return st
}

// Each size's pods carry its stated replicas, limits and requests, and its broker, volumes and
// write-ahead log allowance reach OpenTofu, as literal values.
func TestEachPresetRendersItsResources(t *testing.T) {
	for name, want := range presetRenders {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSize(name); err != nil {
				t.Fatalf("--size %s is not accepted: %v", name, err)
			}
			st := perfStateNamed(name, false)
			manifest, err := renderChart(t, helmValues(st))
			if err != nil {
				t.Fatal(err)
			}
			rendered := renderedAreas(t, manifest)
			got := byArea(t, containersOf(t, manifest))
			for area, w := range want.areas {
				c, ok := got[area]
				if !ok {
					t.Fatalf("%s did not render", area)
				}
				if w[0] != "" {
					n, _ := strconv.Atoi(w[0])
					if rendered[area].Replicas != n {
						t.Errorf("%s replicas %d, want %s", area, rendered[area].Replicas, w[0])
					}
				}
				if q := qty(t, c.limits["cpu"]); q.Cmp(*qty(t, w[1])) != 0 {
					t.Errorf("%s cpu limit %s, want %s", area, c.limits["cpu"], w[1])
				}
				if w[2] != "" && qty(t, c.limits["memory"]).Cmp(*qty(t, w[2])) != 0 {
					t.Errorf("%s memory limit %s, want %s", area, c.limits["memory"], w[2])
				}
				if q := qty(t, c.requests["cpu"]); q.Cmp(*qty(t, w[3])) != 0 {
					t.Errorf("%s cpu request %s, want %s", area, c.requests["cpu"], w[3])
				}
			}
			if got := renderFromState(t, st).Infrastructure.Nats.StreamDesignRate; got != want.designRate {
				t.Errorf("streamDesignRate %d reached the config, want %d", got, want.designRate)
			}
			vars := sizeVars(t, st)
			for k, v := range want.vars {
				if vars[k] != v {
					t.Errorf("%s = %q, want %q", k, vars[k], v)
				}
			}
			// What a size states for the cluster's relational store is published, not applied.
			if _, ok := vars["postgres_max_wal_size_mib"]; ok {
				t.Error("a bootstrap passes postgres_max_wal_size_mib: the relational store is the cluster's, set at install")
			}
			p, ok := sizePresets[name]
			if !ok {
				t.Fatalf("%s has no row in the size table", name)
			}
			if p.RelationalMaxWALMiB != want.relationalWALMiB {
				t.Errorf("relational max_wal_size %d MiB, want %d", p.RelationalMaxWALMiB, want.relationalWALMiB)
			}
		})
	}
}

// Every size the table has is one these literals describe, so a new size cannot ship without its
// render being written down here.
func TestEveryPresetHasALiteralRender(t *testing.T) {
	for name := range sizePresets {
		if _, ok := presetRenders[name]; !ok {
			t.Errorf("%s is in the size table but not in presetRenders", name)
		}
	}
}

// perf-16k sits strictly above perf-12k on the knobs the round 31 ramp said limit it, so the
// bigger size can never quietly be the smaller one.
func TestPerf16kIsLargerThanPerf12k(t *testing.T) {
	a, b := presetRenders["perf-12k"], presetRenders["perf-16k"]
	for _, v := range []string{"nats_cpu_request", "nats_memory_request", "nats_memory_limit", "timescale_storage", "timescale_max_wal_size_mib"} {
		if x, y := a.vars[v], b.vars[v]; x == "" || y == "" {
			t.Fatalf("%s missing", v)
		} else if qtyOrNumber(t, y) <= qtyOrNumber(t, x) {
			t.Errorf("perf-16k %s %s is not above perf-12k's %s", v, y, x)
		}
	}
	if b.relationalWALMiB <= a.relationalWALMiB {
		t.Error("perf-16k relational max_wal_size is not above perf-12k's")
	}
}

func qtyOrNumber(t *testing.T, s string) float64 {
	t.Helper()
	return qty(t, s).AsApproximateFloat64()
}

// The relational max_wal_size reaches the CloudNativePG cluster spec through the cluster root: the
// variable is declared with a default that leaves Postgres's own value, it is validated, it is
// passed to the relational cluster module as max_wal_size, and a guard holds it to a quarter of the
// data volume. CloudNativePG refuses ALTER SYSTEM, so the spec is the only route.
func TestRelationalMaxWALReachesTheClusterSpec(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(sizeDocsRoot(), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	vars := read("deploy/opentofu/cluster/variables.tf")
	block := regexp.MustCompile(`(?s)variable "postgres_max_wal_size_mib" \{.*?\n\}\n`).FindString(vars)
	if block == "" {
		t.Fatal("the cluster root does not declare postgres_max_wal_size_mib")
	}
	if !strings.Contains(block, "default     = 0") {
		t.Error("postgres_max_wal_size_mib does not default to 0 (Postgres's own value): an existing install would change")
	}
	if !strings.Contains(block, "validation {") {
		t.Error("postgres_max_wal_size_mib is not validated")
	}
	main := read("deploy/opentofu/cluster/main.tf")
	rdb := regexp.MustCompile(`(?s)module "cnpg_rdb" \{.*?\n\}\n`).FindString(main)
	if !strings.Contains(rdb, "max_wal_size = var.postgres_max_wal_size_mib > 0 ? format(\"%dMB\", var.postgres_max_wal_size_mib) : \"\"") {
		t.Error("module cnpg_rdb does not pass postgres_max_wal_size_mib as max_wal_size")
	}
	if !strings.Contains(main, `resource "terraform_data" "postgres_wal_guard"`) ||
		!strings.Contains(main, "var.postgres_max_wal_size_mib * 4 <= local.postgres_storage_mib") {
		t.Error("the cluster root does not hold the relational allowance to a quarter of postgres_storage")
	}
	// The cluster root declares it, so splitVars routes it there and nowhere else: a bootstrap
	// that passed it would be applying a cluster setting from an instance.
	cluster, instance, err := splitVars([]string{"postgres_max_wal_size_mib=4096"})
	if err != nil || len(cluster) != 1 || len(instance) != 0 {
		t.Errorf("splitVars routed postgres_max_wal_size_mib to cluster %v instance %v (err %v)", cluster, instance, err)
	}
}

// The relational allowance each size publishes passes the guard on the volume the sizing page
// tells the operator to state beside it (a quarter rule: 16Gi for 4096, 32Gi for 8192).
func TestPublishedRelationalWALFitsItsVolume(t *testing.T) {
	for name, want := range presetRenders {
		need := int64(want.relationalWALMiB) * 4 >> 10 // Gi
		if need > 32 {
			t.Errorf("%s: %d MiB needs a %dGi relational volume, over the 32Gi the page documents", name, want.relationalWALMiB, need)
		}
	}
}
