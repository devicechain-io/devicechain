// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"maps"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// podAffinityTerm is the part of a pod (anti-)affinity term these tests read.
type podAffinityTerm struct {
	TopologyKey   string `json:"topologyKey"`
	LabelSelector *struct {
		MatchLabels      map[string]string `json:"matchLabels"`
		MatchExpressions []interface{}     `json:"matchExpressions"`
	} `json:"labelSelector"`
	Namespaces        []string    `json:"namespaces"`
	NamespaceSelector interface{} `json:"namespaceSelector"`
}

// renderedAffinity is one Deployment's pod affinity, as rendered.
type renderedAffinity struct {
	present   bool // the pod spec carries an affinity block at all
	preferred []struct {
		Weight          int             `json:"weight"`
		PodAffinityTerm podAffinityTerm `json:"podAffinityTerm"`
	}
	required []podAffinityTerm
	// podAffinity is anything under affinity other than podAntiAffinity, which
	// this chart does not set.
	other bool
}

// renderedAntiAffinity decodes the affinity of every rendered Deployment, keyed by
// area (the name of its first container). A Deployment it cannot decode is a
// failure, not a skip, as in containersOf.
func renderedAntiAffinity(t *testing.T, manifest string) map[string]renderedAffinity {
	t.Helper()

	out := map[string]renderedAffinity{}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &kind); err != nil || kind.Kind != "Deployment" {
			continue
		}
		var obj struct {
			Spec struct {
				Template struct {
					Spec struct {
						Affinity *struct {
							PodAntiAffinity *struct {
								Preferred []struct {
									Weight          int             `json:"weight"`
									PodAffinityTerm podAffinityTerm `json:"podAffinityTerm"`
								} `json:"preferredDuringSchedulingIgnoredDuringExecution"`
								Required []podAffinityTerm `json:"requiredDuringSchedulingIgnoredDuringExecution"`
							} `json:"podAntiAffinity"`
							PodAffinity  interface{} `json:"podAffinity"`
							NodeAffinity interface{} `json:"nodeAffinity"`
						} `json:"affinity"`
						Containers []struct {
							Name string `json:"name"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered Deployment: %v\n%s", err, doc)
		}
		spec := obj.Spec.Template.Spec
		if len(spec.Containers) == 0 {
			t.Fatalf("a rendered Deployment has no containers:\n%s", doc)
		}
		var ra renderedAffinity
		if a := spec.Affinity; a != nil {
			ra.present = true
			ra.other = a.PodAffinity != nil || a.NodeAffinity != nil
			if pa := a.PodAntiAffinity; pa != nil {
				ra.preferred = pa.Preferred
				ra.required = pa.Required
			}
		}
		out[spec.Containers[0].Name] = ra
	}
	if len(out) == 0 {
		t.Fatal("no Deployments rendered — every assertion below would be vacuous")
	}
	return out
}

// avoidsTheEventStorePrimary reports what is wrong with one area's affinity, or ""
// when it is exactly one PREFERRED term against this namespace's event-store
// primary. The values are asserted, not just the presence: a required term, a
// zone-wide topology or a different selector each change what the rule does.
func avoidsTheEventStorePrimary(ra renderedAffinity) string {
	switch {
	case !ra.present:
		return "no pod anti-affinity rendered, want a preferred term against cnpg.io/instanceRole=primary"
	case ra.other:
		return "renders a pod or node affinity as well as the anti-affinity"
	case len(ra.required) != 0:
		return "renders a REQUIRED anti-affinity term: on a cluster with fewer nodes than busy services the pod would stay Pending"
	case len(ra.preferred) != 1:
		return "renders a number of preferred terms other than one"
	}
	p := ra.preferred[0]
	term := p.PodAffinityTerm
	switch {
	case p.Weight != 100:
		return "the preferred term's weight is not 100"
	case term.TopologyKey != "kubernetes.io/hostname":
		return "the preferred term's topologyKey is not kubernetes.io/hostname: " + term.TopologyKey
	case term.LabelSelector == nil ||
		len(term.LabelSelector.MatchExpressions) != 0 ||
		!maps.Equal(term.LabelSelector.MatchLabels, map[string]string{"cnpg.io/instanceRole": "primary"}):
		return "the preferred term does not select exactly cnpg.io/instanceRole=primary"
	case len(term.Namespaces) != 0 || term.NamespaceSelector != nil:
		return "the preferred term names namespaces: it must match only the pod's own, the instance namespace"
	}
	return ""
}

// device-management, event-management and event-sources prefer a node that is not
// running this instance's event-store primary. Measured on GKE with every area
// requesting the same 100m, the scheduler put the primary, event-management and
// replicas of device-management and event-sources on one node (93-98% CPU under
// load). Preferred, never required, so a small cluster still schedules every pod;
// and targeted, so the areas it was not measured for keep no affinity at all.
func TestEventPathAreasPreferNodesWithoutTheEventStorePrimary(t *testing.T) {
	avoiders := []string{"device-management", "event-management", "event-sources"}
	untouched := []string{"device-state", "event-processing", "user-management"}

	for _, tc := range []struct {
		name string
		vals func() map[string]interface{}
	}{
		{"chart defaults", func() map[string]interface{} { return nil }},
		{"dcctl default", func() map[string]interface{} { return helmValues(compactState(false)) }},
		{"dcctl --compact", func() map[string]interface{} { return helmValues(compactState(true)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := renderChart(t, tc.vals())
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			got := renderedAntiAffinity(t, manifest)
			for _, area := range avoiders {
				ra, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: its placement was not checked", area)
					continue
				}
				if why := avoidsTheEventStorePrimary(ra); why != "" {
					t.Errorf("%s: %s", area, why)
				}
			}
			for _, area := range untouched {
				ra, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: that it has no affinity was not checked", area)
					continue
				}
				if ra.present {
					t.Errorf("%s renders an affinity; the rule is only for %v", area, avoiders)
				}
			}
		})
	}

	t.Run("an operator can switch it off for one area", func(t *testing.T) {
		manifest, err := renderChart(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"event-sources": map[string]interface{}{"avoidEventStorePrimary": false},
			},
		})
		if err != nil {
			t.Fatalf("rendering chart: %v", err)
		}
		got := renderedAntiAffinity(t, manifest)
		if got["event-sources"].present {
			t.Error("event-sources still renders an affinity with avoidEventStorePrimary: false")
		}
		if why := avoidsTheEventStorePrimary(got["device-management"]); why != "" {
			t.Errorf("device-management, which did not switch it off: %s", why)
		}
	})
}
