// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// persistenceState is a State a real bootstrap would hand the Helm and infra steps,
// for one combination of --ha, --compact and the profile.
func persistenceState(ha, compact bool, profile string) *State {
	st := compactState(compact)
	st.HA = ha
	st.Profile = profile
	return st
}

// renderedArea is what one functional area rendered as: its Deployment's replica
// count, its pod spread and anti-affinity (renderedSpread and renderedAntiAffinity,
// placement_test.go), and whether it got a PodDisruptionBudget.
type renderedArea struct {
	Replicas int
	Spread   spreadView
	Affinity renderedAffinity
	PDB      bool
}

// renderedAreas decodes every Deployment's replica count and every
// PodDisruptionBudget of a manifest, keyed by the devicechain.io/functional-area
// label, and joins each to its spread and affinity. A document it cannot decode is a failure, as
// in containersOf: a decoder that skipped one would report that area as absent,
// which is exactly the shape of the defect these tests look for.
func renderedAreas(t *testing.T, manifest string) map[string]*renderedArea {
	t.Helper()

	out := map[string]*renderedArea{}
	area := func(name string) *renderedArea {
		if out[name] == nil {
			out[name] = &renderedArea{}
		}
		return out[name]
	}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int `json:"replicas"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered document: %v\n%s", err, doc)
		}
		if obj.Kind != "Deployment" && obj.Kind != "PodDisruptionBudget" {
			continue
		}
		name := obj.Metadata.Labels["devicechain.io/functional-area"]
		if name == "" {
			t.Fatalf("a rendered %s carries no functional-area label:\n%s", obj.Kind, doc)
		}
		if obj.Kind == "PodDisruptionBudget" {
			area(name).PDB = true
			continue
		}
		if obj.Spec.Replicas == nil {
			t.Fatalf("Deployment %s renders no spec.replicas", name)
		}
		area(name).Replicas = *obj.Spec.Replicas
	}
	for name, v := range renderedSpread(t, manifest) {
		a, ok := out[name]
		if !ok {
			t.Fatalf("renderedSpread found a Deployment %q the replica reader did not", name)
		}
		a.Spread = v
	}
	for name, ra := range renderedAntiAffinity(t, manifest) {
		a, ok := out[name]
		if !ok {
			t.Fatalf("renderedAntiAffinity found a Deployment %q the replica reader did not", name)
		}
		a.Affinity = ra
	}
	return out
}

// keepsItsOwnPodsApart reports whether the area prefers nodes without another of its
// own pods (prefersNodesWithoutItsOwnPods, placement_test.go) under the test instance.
func (a *renderedArea) keepsItsOwnPodsApart(area string) bool {
	return prefersNodesWithoutItsOwnPods(a.Affinity, area, "dctest") == ""
}

func renderAreas(t *testing.T, vals map[string]interface{}) map[string]*renderedArea {
	t.Helper()
	manifest, err := renderChart(t, vals)
	if err != nil {
		t.Fatalf("rendering the chart: %v", err)
	}
	return renderedAreas(t, manifest)
}

var persistenceCases = []struct {
	name        string
	ha, compact bool
	profile     string
	want        int
}{
	{"default", false, false, "default", 1},
	{"--ha", true, false, "default", 2},
	{"--compact", false, true, "default", 1},
	{"--ha --compact", true, true, "default", 1},
	// No event-management at all: nothing to run twice, and nothing to reserve for.
	{"--ha --profile ingest-only", true, false, "ingest-only", 1},
}

// The ruling and the measurement: under --ha, without --compact, event-management runs
// as two pods; every other combination, and every other area, stays at one.
func TestEventManagementRunsTwoPodsOnlyUnderHaWithoutCompact(t *testing.T) {
	for _, c := range persistenceCases {
		t.Run(c.name, func(t *testing.T) {
			areas := renderAreas(t, helmValues(persistenceState(c.ha, c.compact, c.profile)))

			// Positive control: the decoder sees the areas this profile deploys, so an
			// absent area below is an absent area and not a blind reader.
			if !slices.Contains(slices.Collect(maps.Keys(areas)), "device-management") || len(areas) < 3 {
				t.Fatalf("the render decoded too few areas to judge: %v", slices.Sorted(maps.Keys(areas)))
			}
			if c.profile == "default" && len(areas) < 9 {
				t.Fatalf("the default profile decoded only %d areas: %v", len(areas), slices.Sorted(maps.Keys(areas)))
			}

			em, deployed := areas["event-management"]
			if c.profile == "ingest-only" {
				if deployed {
					t.Fatalf("ingest-only rendered event-management: %+v", em)
				}
			} else {
				if !deployed {
					t.Fatalf("event-management did not render: %v", slices.Sorted(maps.Keys(areas)))
				}
				if em.Replicas != c.want {
					t.Errorf("event-management replicas = %d under %s, want %d", em.Replicas, c.name, c.want)
				}
				if em.PDB != (c.want > 1) {
					t.Errorf("event-management PodDisruptionBudget = %t under %s, want %t", em.PDB, c.name, c.want > 1)
				}
				// Two pods that share a node share the CPU that starved one, and a node
				// loss takes both: a preferred anti-affinity against the area's own pods
				// is what keeps them apart. NOT a second spread constraint: the API
				// server refuses a second hostname/ScheduleAnyway entry, and an upgrade
				// would merge it with the event-path one.
				own := prefersNodesWithoutItsOwnPods(em.Affinity, "event-management", "dctest")
				if c.want > 1 {
					if why := spreadsWithTheEventPath(em.Spread, "event-management", "dctest"); why != "" {
						t.Errorf("event-management at %d pods: %s", em.Replicas, why)
					}
					if own != "" {
						t.Errorf("event-management at %d pods: %s", em.Replicas, own)
					}
				} else if own == "" {
					t.Errorf("event-management at one pod renders a preferred term against its own pods")
				}
			}
			for name, a := range areas {
				if name == "event-management" {
					continue
				}
				if a.Replicas != 1 || a.PDB || a.keepsItsOwnPodsApart(name) || len(a.Spread.constraints) > 1 {
					t.Errorf("%s under %s: replicas %d, PDB %t, term against its own pods %t, %d spread "+
						"constraints; want 1, none, none, at most one",
						name, c.name, a.Replicas, a.PDB, a.keepsItsOwnPodsApart(name), len(a.Spread.constraints))
				}
			}
		})
	}
}

// The reader can see a 2, and the PDB and spread assertions above are not vacuous:
// the tuned benchmark's values file verbatim gives two pods, a PDB and the spread;
// replicas 1 gives none of them; and a top-level replicas 2 gives every area a PDB.
func TestTheRenderedReplicaReaderSeesWhatTheChartWasGiven(t *testing.T) {
	two := renderAreas(t, map[string]interface{}{
		"functionalAreas": map[string]interface{}{"event-management": map[string]interface{}{"replicas": 2}},
	})
	if em := two["event-management"]; em == nil || em.Replicas != 2 || !em.PDB ||
		len(em.Spread.constraints) != 1 || !em.keepsItsOwnPodsApart("event-management") {
		t.Fatalf("functionalAreas.event-management.replicas: 2 decoded as %+v", em)
	}
	one := renderAreas(t, map[string]interface{}{
		"functionalAreas": map[string]interface{}{"event-management": map[string]interface{}{"replicas": 1}},
	})
	if em := one["event-management"]; em == nil || em.Replicas != 1 || em.PDB ||
		len(em.Spread.constraints) != 1 || em.keepsItsOwnPodsApart("event-management") {
		t.Fatalf("functionalAreas.event-management.replicas: 1 decoded as %+v", em)
	}
	// Areas the chart pins to one pod of its own (event-processing, frontend) keep
	// it; these three take the top-level count.
	all := renderAreas(t, map[string]interface{}{"replicas": 2})
	for _, name := range []string{"device-management", "event-management", "user-management"} {
		if a := all[name]; a == nil || a.Replicas != 2 || !a.PDB {
			t.Errorf("top-level replicas: 2 rendered %s as %+v", name, a)
		}
	}
	// The term against an area's own pods is the event-path areas' replacement for
	// the scheduler's default spread, which a pod with a spread of its own loses; the
	// spread itself stays one constraint. An area without the shared spread keeps the
	// default and gets neither.
	if dm := all["device-management"]; len(dm.Spread.constraints) != 1 || !dm.keepsItsOwnPodsApart("device-management") {
		t.Errorf("device-management at two pods: %d spread constraints, term against its own pods %t; "+
			"want one and true", len(dm.Spread.constraints), dm.keepsItsOwnPodsApart("device-management"))
	}
	if um := all["user-management"]; len(um.Spread.constraints) != 0 || um.Affinity.present {
		t.Errorf("user-management (no event-path spread) renders a spread %+v or an affinity %+v",
			um.Spread.constraints, um.Affinity)
	}
}

// Both tools that need the count get it, from the one value: the chart above, and
// the instance root here, whose event store reserves connections per pod.
func TestInfraVarsCarryTheEventManagementReplicaCount(t *testing.T) {
	for _, c := range persistenceCases {
		t.Run(c.name, func(t *testing.T) {
			st := persistenceState(c.ha, c.compact, c.profile)
			vars := infraVars(st)
			if got := valueOf(t, vars, "event_management_replicas"); got != strconv.Itoa(c.want) {
				t.Errorf("event_management_replicas = %s under %s, want %d", got, c.name, c.want)
			}
			_, inst, err := splitVars(vars)
			if err != nil {
				t.Fatalf("splitting the infra vars: %v", err)
			}
			if got := valueOf(t, inst, "event_management_replicas"); got != strconv.Itoa(c.want) {
				t.Errorf("the instance root is handed event_management_replicas = %s, want %d", got, c.want)
			}
		})
	}
}

// The numbers the reserve is built from: one pod's pool, doubled for a rollout, is
// the per-pod base, and the rollout that doubling assumes is the chart's maxSurge 1.
func TestTheEventStoreReserveIsOnePodsRolloutPerPod(t *testing.T) {
	if got := tofuVariableDefault(t, "event_management_replicas"); got != "1" {
		t.Errorf("event_management_replicas defaults to %s, want 1: a direct tofu user who "+
			"installs the chart at its default must get today's reserve", got)
	}
	if got, want := tofuVariableDefault(t, "timescale_analytics_reserved_connections"),
		strconv.Itoa(servicePoolSize*rolloutSurge); got != want {
		t.Errorf("timescale_analytics_reserved_connections defaults to %s, want %s "+
			"(one pod's pool of %d, doubled for a rollout)", got, want, servicePoolSize)
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	ru, _ := ch.Values["rollingUpdate"].(map[string]interface{})
	if s := fmt.Sprint(ru["maxSurge"]); s != "1" {
		t.Errorf("the chart's rollingUpdate.maxSurge is %s: the reserve's \"doubled for a "+
			"rollout\" assumes 1, so a wider surge must revisit it (and connbudget.go)", s)
	}
	main := rootSources(t, "main.tf")["instance"]
	if !strings.Contains(main, "reserved_application_connections = local.event_store_reserved_connections") {
		t.Errorf("the instance root does not hand the event store local.event_store_reserved_connections")
	}
}

// The event-management block is merged into functionalAreas, so another feature's
// block for another area survives it, and it survives that one.
func TestEventManagementReplicasLeaveAnotherAreasBlockAlone(t *testing.T) {
	st := persistenceState(true, false, "default")
	st.EnabledAreas = []string{"user-management", "device-management", "event-sources", "event-management", "lwm2m-ingest"}
	st.Lwm2mIdentities = []Lwm2mIdentity{{Identity: "dev-1", PSK: "c2VjcmV0", Tenant: "acme", ExternalID: "dev-1"}}
	fa, _ := helmValues(st)["functionalAreas"].(map[string]interface{})
	em, _ := fa["event-management"].(map[string]interface{})
	if em["replicas"] != 2 {
		t.Errorf("functionalAreas.event-management = %v, want replicas 2", em)
	}
	lw, _ := fa["lwm2m-ingest"].(map[string]interface{})
	cfg, _ := lw["config"].(map[string]interface{})
	sec, _ := cfg["security"].(map[string]interface{})
	if ids, _ := sec["identities"].([]interface{}); len(ids) != 1 {
		t.Errorf("functionalAreas.lwm2m-ingest lost its identities: %v", lw)
	}

	// The other direction. In helmValues nothing writes functionalAreas before
	// mergeInto runs, so the half above cannot tell a merge from an assignment that
	// replaces the whole map. Call it on a map that already holds another area's
	// block and another key of event-management's own: both must survive it.
	vals := map[string]interface{}{"functionalAreas": map[string]interface{}{
		"lwm2m-ingest":     map[string]interface{}{"enabled": true},
		"event-management": map[string]interface{}{"logLevel": "debug"},
	}}
	persistenceFor(st).mergeInto(vals)
	fa, _ = vals["functionalAreas"].(map[string]interface{})
	if lw, _ := fa["lwm2m-ingest"].(map[string]interface{}); lw["enabled"] != true {
		t.Errorf("mergeInto replaced another area's block: functionalAreas = %v", fa)
	}
	em, _ = fa["event-management"].(map[string]interface{})
	if em["replicas"] != 2 || em["logLevel"] != "debug" {
		t.Errorf("functionalAreas.event-management = %v, want replicas 2 beside the "+
			"block's existing logLevel debug", em)
	}
}

// An upgrade that skips the infrastructure moves the services and not the event
// store's reserve, so a --ha one says so.
func TestASkippedInfrastructureUpgradeSaysTheReserveDidNotMove(t *testing.T) {
	opts := UpgradeOptions{Options: Options{Instance: "prod"}}
	st := persistenceState(true, false, "default")
	st.SkipInfrastructure = true
	st.Provider = "local"
	out := captureStdout(t, func() { sayUpgradeInfrastructure(st, opts) })
	if !strings.Contains(out, "event-management now runs 2 pods") || !strings.Contains(out, "connection reserve") {
		t.Errorf("a --ha upgrade under --skip-infrastructure does not say the reserve was left:\n%s", out)
	}
	for _, other := range []*State{persistenceState(false, false, "default"), persistenceState(true, true, "default")} {
		other.SkipInfrastructure = true
		other.Provider = "local"
		if out := captureStdout(t, func() { sayUpgradeInfrastructure(other, opts) }); strings.Contains(out, "event-management") {
			t.Errorf("a one-pod instance (ha %t, compact %t) is warned about event-management:\n%s",
				other.HA, other.Compact, out)
		}
	}
}
