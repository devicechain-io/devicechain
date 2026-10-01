// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
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

// spreadConstraint is one rendered topologySpreadConstraint, with every field the
// API has decoded, so a field this chart does not set is visible when it appears.
type spreadConstraint struct {
	MaxSkew           int    `json:"maxSkew"`
	TopologyKey       string `json:"topologyKey"`
	WhenUnsatisfiable string `json:"whenUnsatisfiable"`
	LabelSelector     *struct {
		MatchLabels      map[string]string `json:"matchLabels"`
		MatchExpressions []interface{}     `json:"matchExpressions"`
	} `json:"labelSelector"`
	MatchLabelKeys     []string    `json:"matchLabelKeys"`
	MinDomains         interface{} `json:"minDomains"`
	NodeAffinityPolicy interface{} `json:"nodeAffinityPolicy"`
	NodeTaintsPolicy   interface{} `json:"nodeTaintsPolicy"`
}

// spreadView is what one rendered Deployment says about the event-path spread: the
// Deployment's selector, its pod template's labels, and the template's constraints.
type spreadView struct {
	selector    map[string]string
	podLabels   map[string]string
	constraints []spreadConstraint
}

// renderedSpread decodes the spread of every rendered Deployment, keyed by area (the
// name of its first container). A Deployment it cannot decode is a failure, not a
// skip, as in renderedAntiAffinity.
func renderedSpread(t *testing.T, manifest string) map[string]spreadView {
	t.Helper()

	out := map[string]spreadView{}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &kind); err != nil || kind.Kind != "Deployment" {
			continue
		}
		var obj struct {
			Spec struct {
				Selector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
				Template struct {
					Metadata struct {
						Labels map[string]string `json:"labels"`
					} `json:"metadata"`
					Spec struct {
						TopologySpreadConstraints []spreadConstraint `json:"topologySpreadConstraints"`
						Containers                []struct {
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
		out[spec.Containers[0].Name] = spreadView{
			selector:    obj.Spec.Selector.MatchLabels,
			podLabels:   obj.Spec.Template.Metadata.Labels,
			constraints: spec.TopologySpreadConstraints,
		}
	}
	if len(out) == 0 {
		t.Fatal("no Deployments rendered — every assertion below would be vacuous")
	}
	return out
}

// eventPathLabel is the pod label the event-path spread counts.
var eventPathLabel = map[string]string{"devicechain.io/event-path": "true"}

// spreadsWithTheEventPath reports what is wrong with one area's spread, or "" when
// it is exactly one PREFERRED hostname spread among the pods carrying the event-path
// label, the pod carries that label, and the Deployment's selector is untouched.
// The values are asserted, not just the presence: DoNotSchedule leaves pods Pending
// on a small cluster, a zone key spreads nothing on a one-zone cluster, and
// pod-template-hash in matchLabelKeys narrows the count to one Deployment.
func spreadsWithTheEventPath(v spreadView, area, instance string) string {
	wantSelector := map[string]string{
		"devicechain.io/instance":        instance,
		"devicechain.io/functional-area": area,
	}
	if !maps.Equal(v.selector, wantSelector) {
		return fmt.Sprintf("the Deployment selector is %v, want exactly %v: a selector is immutable, "+
			"so a new key there fails every upgrade of an existing instance", v.selector, wantSelector)
	}
	if len(v.constraints) != 1 {
		return fmt.Sprintf("renders %d topology spread constraints, want exactly one", len(v.constraints))
	}
	c := v.constraints[0]
	switch {
	case c.MaxSkew != 1:
		return fmt.Sprintf("maxSkew is %d, want 1", c.MaxSkew)
	case c.TopologyKey != "kubernetes.io/hostname":
		return "topologyKey is not kubernetes.io/hostname: " + c.TopologyKey
	case c.WhenUnsatisfiable != "ScheduleAnyway":
		return "whenUnsatisfiable is not ScheduleAnyway: " + c.WhenUnsatisfiable +
			"; a required spread leaves pods Pending on a cluster with fewer nodes than these services"
	case c.LabelSelector == nil || len(c.LabelSelector.MatchExpressions) != 0 ||
		!maps.Equal(c.LabelSelector.MatchLabels, eventPathLabel):
		return fmt.Sprintf("the constraint does not select exactly %v", eventPathLabel)
	case len(c.MatchLabelKeys) != 0:
		return fmt.Sprintf("the constraint sets matchLabelKeys %v, which narrows the count to one Deployment", c.MatchLabelKeys)
	case c.MinDomains != nil || c.NodeAffinityPolicy != nil || c.NodeTaintsPolicy != nil:
		return "the constraint sets minDomains or a node policy, which this chart does not"
	}
	for k, want := range eventPathLabel {
		if v.podLabels[k] != want {
			return fmt.Sprintf("the pod template does not carry %s=%s, so the constraint does not count "+
				"the pod it is on", k, want)
		}
	}
	return ""
}

// device-management, event-management, device-state, event-sources and
// event-processing prefer nodes running fewer of the five, which guards against three
// or more of them on one node. (The 2/1/2 placement measured on GKE at shipped
// defaults already satisfies maxSkew 1; the measured requests, not this spread, are
// what address it.) Preferred, never required, so a small cluster still
// schedules every pod; and targeted, so the other areas carry neither the label nor
// a constraint.
func TestEventPathPodsSpreadAcrossNodes(t *testing.T) {
	spreaders := []string{"device-management", "event-management", "device-state", "event-sources", "event-processing"}
	untouched := []string{"user-management", "command-delivery", "dashboard-management", "notification-management"}

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
			got := renderedSpread(t, manifest)
			for _, area := range spreaders {
				v, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: its placement was not checked", area)
					continue
				}
				if why := spreadsWithTheEventPath(v, area, "dctest"); why != "" {
					t.Errorf("%s: %s", area, why)
				}
			}
			for _, area := range untouched {
				v, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: that it has no spread was not checked", area)
					continue
				}
				if len(v.constraints) != 0 {
					t.Errorf("%s renders a topology spread; the rule is only for %v", area, spreaders)
				}
				if _, has := v.podLabels["devicechain.io/event-path"]; has {
					t.Errorf("%s carries the event-path label, so the five would count it", area)
				}
			}
		})
	}

	t.Run("an operator can switch it off for one area", func(t *testing.T) {
		manifest, err := renderChart(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"event-sources": map[string]interface{}{"eventPathSpread": false},
			},
		})
		if err != nil {
			t.Fatalf("rendering chart: %v", err)
		}
		got := renderedSpread(t, manifest)
		es, ok := got["event-sources"]
		if !ok {
			t.Fatal("event-sources did not render")
		}
		if len(es.constraints) != 0 {
			t.Errorf("event-sources still renders %d spread constraints with eventPathSpread: false", len(es.constraints))
		}
		if _, has := es.podLabels["devicechain.io/event-path"]; has {
			t.Error("event-sources still carries the event-path label with eventPathSpread: false")
		}
		// The check can fail: it does not pass the area that was switched off.
		if why := spreadsWithTheEventPath(es, "event-sources", "dctest"); why == "" {
			t.Error("spreadsWithTheEventPath passed an area with no spread: the check cannot fail")
		}
		if why := spreadsWithTheEventPath(got["device-management"], "device-management", "dctest"); why != "" {
			t.Errorf("device-management, which did not switch it off: %s", why)
		}
	})
}
