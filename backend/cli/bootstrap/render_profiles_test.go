// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"sigs.k8s.io/yaml"
)

// renderProfile is one way the chart is installed: a State as a bootstrap builds it,
// plus chart values an operator may set on top (nil for a pure dcctl profile).
type renderProfile struct {
	name string
	st   func(instance string) *State
	// overrides returns a fresh map each call: chartutil.MergeTables writes into it.
	overrides func() map[string]interface{}
}

// profileState is persistenceState (a State shaped like a real bootstrap's for one
// combination of --ha, --compact and the profile) under the row's own instance id, so
// every row installs into an API server beside the others without a collision.
func profileState(ha, compact bool, profile string) func(string) *State {
	return func(instance string) *State {
		st := persistenceState(ha, compact, profile)
		st.Instance = instance
		return st
	}
}

// areaOverride is a functionalAreas.<area> block of chart values.
//
// 🔴 THE BLOCK IS COPIED ON EVERY CALL, ALL THE WAY DOWN. chartutil.MergeTables writes
// the release values' own functionalAreas.<area> keys INTO the override's block, so a
// block handed out by reference is one map every caller merges into. The two suites
// that range over renderProfiles render the same row at the same time, which made
// that a concurrent map write; run one after another it was a block that carried the
// previous render's values into the next.
func areaOverride(area string, block map[string]interface{}) func() map[string]interface{} {
	return func() map[string]interface{} {
		return map[string]interface{}{"functionalAreas": map[string]interface{}{area: copyValues(block)}}
	}
}

// copyValues deep-copies a chart values table, nested tables and lists included.
func copyValues(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = copyValue(v)
	}
	return out
}

func copyValue(v interface{}) interface{} {
	switch v := v.(type) {
	case map[string]interface{}:
		return copyValues(v)
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, e := range v {
			out[i] = copyValue(e)
		}
		return out
	default:
		return v
	}
}

// renderProfiles is every input that changes what reaches a POD TEMPLATE, which is
// where the API server's pod-spec rules apply: each dcctl profile and the flags that
// change chart values (--ha, --compact, --no-tls, --lwm2m-identities, what the
// infrastructure reports about database monitoring), and the chart values an operator
// sets that change placement (replicas above one, the event-processing warm standby,
// the two placement switches). Both TestNoRenderedPodSpecRepeatsASpreadPair and
// TestEveryRenderedProfileIsAcceptedByAnAPIServer range over it, so the fast check and
// the API-server check cannot disagree about what "every profile" means. A new
// profile, or a new flag that changes chart values, belongs here.
//
// Not rows, deliberately: the database placement flags (--database-node-selector,
// --database-toleration). They reach only the cnpg-cluster chart, through OpenTofu,
// whose pods the CloudNativePG operator builds; helmValues reads no placement field,
// and that chart is checked against the real CRD schemas in CI.
var renderProfiles = []renderProfile{
	{name: "default", st: profileState(false, false, "default")},
	{name: "--ha", st: profileState(true, false, "default")},
	{name: "--compact", st: profileState(false, true, "default")},
	{name: "--ha --compact", st: profileState(true, true, "default")},
	{name: "--profile full", st: profileState(false, false, "full")},
	{name: "--ha --profile full", st: profileState(true, false, "full")},
	{name: "--ha --profile telemetry", st: profileState(true, false, "telemetry")},
	{name: "--ha --profile ingest-only", st: profileState(true, false, "ingest-only")},
	{
		name:      "--ha, every area at replicas 2",
		st:        profileState(true, false, "default"),
		overrides: func() map[string]interface{} { return map[string]interface{}{"replicas": 2} },
	},
	{
		name: "--ha, event-processing warm standby",
		st:   profileState(true, false, "default"),
		overrides: areaOverride("event-processing", map[string]interface{}{
			"replicas": 2, "strategy": "RollingUpdate",
		}),
	},
	{
		name:      "--ha, event-management eventPathSpread off",
		st:        profileState(true, false, "default"),
		overrides: areaOverride("event-management", map[string]interface{}{"eventPathSpread": false}),
	},
	{
		name:      "--ha, event-management avoidEventStorePrimary off",
		st:        profileState(true, false, "default"),
		overrides: areaOverride("event-management", map[string]interface{}{"avoidEventStorePrimary": false}),
	},
	{
		name: "--ha, event-management both placement switches off",
		st:   profileState(true, false, "default"),
		overrides: areaOverride("event-management", map[string]interface{}{
			"eventPathSpread": false, "avoidEventStorePrimary": false,
		}),
	},
	{
		// What a non-compact install reports back about the database (install.go,
		// steps.go): the operator's namespace, and both backup kinds on. These turn on
		// the database alerting rules and the operator's PodMonitor, which lives in a
		// namespace the release does not create.
		name: "--ha, database backups and operator monitoring reported",
		st: func(instance string) *State {
			st := profileState(true, false, "default")(instance)
			st.Values[cnpgNamespaceKey] = "cnpg-system"
			st.Values[databaseBackupsKey] = "true"
			st.Values[databaseBackupSnapshotsKey] = "true"
			return st
		},
	},
	{
		// The Gateway API front end (ingress.controller: gateway), which renders a Gateway and
		// HTTPRoutes in place of the Ingress objects. The full profile, so every route kind
		// (the API, the well-known discovery route, the console) is in the release.
		name: "--ha --profile full, Gateway API front end",
		st:   profileState(true, false, "full"),
		overrides: func() map[string]interface{} {
			return map[string]interface{}{"ingress": map[string]interface{}{"controller": "gateway"}}
		},
	},
	{
		// The Ingress front end (a cluster that still runs ingress-nginx and an instance not yet
		// moved), which renders the Ingress objects in place of the Gateway.
		name: "--ha --profile full, Ingress front end",
		st: func(instance string) *State {
			st := profileState(true, false, "full")(instance)
			st.IngressController = frontEndNginx
			return st
		},
	},
	{
		// The cutover window: the Gateway and the Ingress objects beside it, on distinct
		// certificate Secrets (the Gateway waits for a new one, the Ingress objects keep theirs).
		name: "--ha --profile full, cutover (Gateway and Ingress)",
		st: func(instance string) *State {
			st := profileState(true, false, "full")(instance)
			st.IngressController, st.LegacyIngress, st.GatewayTLSSecret = frontEndGateway, true, gatewayCutoverSecret
			return st
		},
	},
	{
		name: "--ha --no-tls",
		st: func(instance string) *State {
			st := profileState(true, false, "default")(instance)
			st.NoTLS = true
			return st
		},
	},
	{
		// The object store changes the pod template: a claim mounted into the areas that use it
		// (filesystem), or the S3 credentials projected from a Secret (s3).
		name: "--object-store filesystem",
		st: func(instance string) *State {
			st := profileState(false, false, "default")(instance)
			st.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "1Gi"}
			return st
		},
	},
	{
		name: "--object-store s3 with a credentials Secret",
		st: func(instance string) *State {
			st := profileState(false, false, "default")(instance)
			st.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3, S3: &dcv1beta1.S3ObjectStoreSpec{
				Bucket: "dc-objects", Region: "us-east-1",
				CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "storage", Name: "keys"},
			}}
			return st
		},
	},
	{
		// --lwm2m-identities implies --enable-area lwm2m-ingest, and adds a Secret and
		// a secretKeyRef env to that area's pod template.
		name: "--ha --enable-area lwm2m-ingest --lwm2m-identities",
		st: func(instance string) *State {
			st := profileState(true, false, "default")(instance)
			enabled, err := ResolveEnabledAreas("default", []string{"lwm2m-ingest"})
			if err != nil {
				panic(fmt.Sprintf("resolving the lwm2m-ingest areas: %v", err))
			}
			st.EnabledAreas = enabled
			st.Lwm2mIdentities = []Lwm2mIdentity{{
				Identity: "opaque-1", PSK: b64PSK(24), Tenant: "acme", ExternalID: "plant-a/s1",
				DeviceTypeToken: "sensor", AutoRegister: true,
			}}
			return st
		},
	},
}

// profileInstance is the instance id every suite that renders renderProfiles[i] gives it. One id
// per row, not one per suite: the render is a pure function of the values, the id is one of
// them, and suites that each invented their own (p01, f01, g01) rendered the same row again for
// nothing (render_memo_test.go remembers a render by its values, so equal ids are equal renders).
func profileInstance(i int) string { return fmt.Sprintf("p%02d", i+1) }

// profileValues returns the values dcctl would install for p under instance id: the
// production releaseValues (helmValues, composed into the instance configuration
// document, then the install values that name that document), with p.overrides
// merged over them, the overrides winning.
func profileValues(t *testing.T, ch *chart.Chart, p renderProfile, instance string) map[string]interface{} {
	t.Helper()
	vals, err := profileValuesE(t.Context(), ch, p, instance)
	if err != nil {
		t.Fatal(err)
	}
	return vals
}

// profileValuesE is profileValues without a test, so the render warm-up (render_memo_test.go)
// composes exactly what the suites do.
func profileValuesE(ctx context.Context, ch *chart.Chart, p renderProfile, instance string) (map[string]interface{}, error) {
	vals, _, err := releaseValues(ctx, ch, p.st(instance), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: composing the release values: %w", p.name, err)
	}
	if p.overrides == nil {
		return vals, nil
	}
	return chartutil.MergeTables(p.overrides(), vals), nil
}

// podTemplateSpread is the part of a rendered object these checks read: its kind,
// name and the topology spread of the pod template it carries, wherever the kind
// keeps it.
type podTemplateSpread struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template *struct {
			Spec struct {
				TopologySpreadConstraints []spreadConstraint `json:"topologySpreadConstraints"`
			} `json:"spec"`
		} `json:"template"`
		JobTemplate *struct {
			Spec struct {
				Template *struct {
					Spec struct {
						TopologySpreadConstraints []spreadConstraint `json:"topologySpreadConstraints"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
	} `json:"spec"`
}

// podTemplateKinds are the kinds whose spec.template is a pod template; CronJob keeps
// its one under spec.jobTemplate.
var podTemplateKinds = []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"}

// repeatedSpreadPairs returns what is wrong with the topology spread of every pod
// template in the manifest, one line per finding, naming the object and the reason.
// Two shapes, from the two keys Kubernetes puts on this list:
//
//   - a repeated (topologyKey, whenUnsatisfiable) pair. The list is a map keyed on both
//     fields, so the API server refuses the object, whatever the selectors.
//   - a repeated topologyKey with distinct whenUnsatisfiable values. That is accepted
//     on create, but the strategic-merge patch key is topologyKey ALONE, so a Helm
//     upgrade addresses both entries as one and merges them into a single constraint.
//
// A pod-template kind it cannot decode is a failure, not a skip: a reader that
// skipped one would report it clean.
func repeatedSpreadPairs(t *testing.T, manifest string) []string {
	t.Helper()
	var out []string
	for _, doc := range splitManifests(manifest) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &kind); err != nil {
			t.Fatalf("decoding a rendered document's kind: %v\n%s", err, doc)
		}
		if !slices.Contains(podTemplateKinds, kind.Kind) {
			continue
		}
		var obj podTemplateSpread
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered %s: %v\n%s", kind.Kind, err, doc)
		}
		var cs []spreadConstraint
		switch {
		case obj.Kind == "CronJob" && obj.Spec.JobTemplate != nil && obj.Spec.JobTemplate.Spec.Template != nil:
			cs = obj.Spec.JobTemplate.Spec.Template.Spec.TopologySpreadConstraints
		case obj.Kind != "CronJob" && obj.Spec.Template != nil:
			cs = obj.Spec.Template.Spec.TopologySpreadConstraints
		default:
			t.Fatalf("a rendered %s %q carries no pod template where its kind keeps one:\n%s",
				obj.Kind, obj.Metadata.Name, doc)
		}
		out = append(out, spreadFindings(obj.Kind+"/"+obj.Metadata.Name, cs)...)
	}
	return out
}

// spreadFindings is repeatedSpreadPairs for one pod template's constraints.
func spreadFindings(object string, cs []spreadConstraint) []string {
	var keys []string
	byKey := map[string][]string{}
	for _, c := range cs {
		if _, seen := byKey[c.TopologyKey]; !seen {
			keys = append(keys, c.TopologyKey)
		}
		byKey[c.TopologyKey] = append(byKey[c.TopologyKey], c.WhenUnsatisfiable)
	}
	var out []string
	for _, k := range keys {
		ws := byKey[k]
		if len(ws) < 2 {
			continue
		}
		repeatedPair := false
		for _, w := range slices.Compact(slices.Sorted(slices.Values(ws))) {
			if n := countOf(ws, w); n > 1 {
				repeatedPair = true
				out = append(out, fmt.Sprintf("%s: {%s, %s} x%d: the API server refuses a repeated "+
					"(topologyKey, whenUnsatisfiable) pair", object, k, w, n))
			}
		}
		if !repeatedPair {
			out = append(out, fmt.Sprintf("%s: topologyKey %s x%d (%s): a Helm upgrade merges "+
				"constraints that share a topologyKey", object, k, len(ws), strings.Join(ws, ", ")))
		}
	}
	return out
}

func countOf(ws []string, w string) int {
	n := 0
	for _, x := range ws {
		if x == w {
			n++
		}
	}
	return n
}

// deploymentWithSpread is a minimal Deployment manifest carrying the given
// (topologyKey, whenUnsatisfiable) constraints, for the checker's own controls.
func deploymentWithSpread(name string, pairs ...[2]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: %s\nspec:\n  template:\n    spec:\n      topologySpreadConstraints:\n", name)
	for _, p := range pairs {
		fmt.Fprintf(&b, "        - maxSkew: 1\n          topologyKey: %s\n          whenUnsatisfiable: %s\n", p[0], p[1])
	}
	return b.String()
}

// No pod template the chart renders, for any profile dcctl installs, repeats a
// topology spread pair (which the API server refuses) or a topology key (which a Helm
// upgrade merges). A repeat is valid YAML, so nothing short of this, or of an API
// server, sees it.
func TestNoRenderedPodSpecRepeatsASpreadPair(t *testing.T) {
	const (
		host = "kubernetes.io/hostname"
		zone = "topology.kubernetes.io/zone"
	)
	// The checker's controls, before it is trusted with a verdict. Each is a VALUE.
	controls := []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name:     "a repeated pair is found once",
			manifest: deploymentWithSpread("dup", [2]string{host, "ScheduleAnyway"}, [2]string{host, "ScheduleAnyway"}),
			want: []string{"Deployment/dup: {kubernetes.io/hostname, ScheduleAnyway} x2: the API server " +
				"refuses a repeated (topologyKey, whenUnsatisfiable) pair"},
		},
		{
			name:     "a repeated key with distinct pairs is found too",
			manifest: deploymentWithSpread("key", [2]string{host, "ScheduleAnyway"}, [2]string{host, "DoNotSchedule"}),
			want: []string{"Deployment/key: topologyKey kubernetes.io/hostname x2 (ScheduleAnyway, " +
				"DoNotSchedule): a Helm upgrade merges constraints that share a topologyKey"},
		},
		{
			name:     "distinct keys are clean",
			manifest: deploymentWithSpread("ok", [2]string{host, "ScheduleAnyway"}, [2]string{zone, "ScheduleAnyway"}),
			want:     nil,
		},
	}
	for _, c := range controls {
		if got := repeatedSpreadPairs(t, c.manifest); !slices.Equal(got, c.want) {
			t.Fatalf("control %q: repeatedSpreadPairs = %q, want %q", c.name, got, c.want)
		}
	}

	// The profiles render in parallel, each from a chart of its own. Every render is a
	// full chart install run client-side, so a dozen of them in a row were one of the
	// package's longest tests under the race detector; the renders share nothing, and
	// a chart per profile keeps it that way whatever Helm does to the chart it is given.
	t.Parallel()
	for i, p := range renderProfiles {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			ch, err := loadEmbeddedChart()
			if err != nil {
				t.Fatalf("loading the embedded chart: %v", err)
			}
			instance := profileInstance(i)
			vals := profileValues(t, ch, p, instance)

			// The values are the INSTALL values, not the authoring ones: the instance
			// configuration is a Secret's name, never inline. A check built from
			// helmValues alone would render a different chart state from the one
			// installed.
			inst, _ := vals["instance"].(map[string]interface{})
			if _, inline := inst["config"]; inline || inst["existingSecret"] != instanceConfigSecretName(instance) {
				t.Fatalf("profileValues did not go through the install values: instance = %v", inst)
			}

			manifest, err := renderChartClientSide(t.Context(), ch, vals)
			if err != nil {
				t.Fatalf("rendering: %v", err)
			}
			// Positive control: there is something to judge.
			spread := 0
			for _, v := range renderedSpread(t, manifest) {
				if len(v.constraints) > 0 {
					spread++
				}
			}
			if spread == 0 {
				t.Fatalf("no rendered Deployment carries a topology spread: nothing to judge")
			}
			for _, f := range repeatedSpreadPairs(t, manifest) {
				t.Error(f)
			}
		})
	}
}
