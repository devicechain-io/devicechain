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

// preferredTerm is one rendered preferred anti-affinity term.
type preferredTerm struct {
	Weight          int             `json:"weight"`
	PodAffinityTerm podAffinityTerm `json:"podAffinityTerm"`
}

// renderedAffinity is one Deployment's pod affinity, as rendered.
type renderedAffinity struct {
	present   bool // the pod spec carries an affinity block at all
	preferred []preferredTerm
	required  []podAffinityTerm
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
								Preferred []preferredTerm   `json:"preferredDuringSchedulingIgnoredDuringExecution"`
								Required  []podAffinityTerm `json:"requiredDuringSchedulingIgnoredDuringExecution"`
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

// termsSelecting returns the preferred terms whose selector has the label key.
func termsSelecting(ra renderedAffinity, key string) []preferredTerm {
	var out []preferredTerm
	for _, p := range ra.preferred {
		if sel := p.PodAffinityTerm.LabelSelector; sel != nil {
			if _, ok := sel.MatchLabels[key]; ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// preferredHostnameTerm reports what is wrong with one preferred term, or "" when it
// is weight 100 over kubernetes.io/hostname, selecting exactly want, in the pod's own
// namespace. The values are asserted, not just the presence: a zone-wide topology, a
// different weight or a different selector each change what the rule does.
func preferredHostnameTerm(p preferredTerm, want map[string]string) string {
	term := p.PodAffinityTerm
	switch {
	case p.Weight != 100:
		return fmt.Sprintf("the preferred term's weight is %d, not 100", p.Weight)
	case term.TopologyKey != "kubernetes.io/hostname":
		return "the preferred term's topologyKey is not kubernetes.io/hostname: " + term.TopologyKey
	case term.LabelSelector == nil ||
		len(term.LabelSelector.MatchExpressions) != 0 ||
		!maps.Equal(term.LabelSelector.MatchLabels, want):
		return fmt.Sprintf("the preferred term does not select exactly %v", want)
	case len(term.Namespaces) != 0 || term.NamespaceSelector != nil:
		return "the preferred term names namespaces: it must match only the pod's own, the instance namespace"
	}
	return ""
}

// affinityShape reports what is wrong with the affinity block as a whole, whatever
// terms it holds: only a PREFERRED pod anti-affinity is allowed.
func affinityShape(ra renderedAffinity) string {
	switch {
	case !ra.present:
		return "no pod anti-affinity rendered"
	case ra.other:
		return "renders a pod or node affinity as well as the anti-affinity"
	case len(ra.required) != 0:
		return "renders a REQUIRED anti-affinity term: on a cluster with fewer nodes than busy services the pod would stay Pending"
	}
	return ""
}

// primaryLabels is what the term against the event-store primary selects.
var primaryLabels = map[string]string{"cnpg.io/instanceRole": "primary"}

// avoidsTheEventStorePrimary reports what is wrong with one area's affinity, or ""
// when it holds exactly one PREFERRED term against this namespace's event-store
// primary, and nothing required.
func avoidsTheEventStorePrimary(ra renderedAffinity) string {
	if why := affinityShape(ra); why != "" {
		return why + ", want a preferred term against cnpg.io/instanceRole=primary"
	}
	terms := termsSelecting(ra, "cnpg.io/instanceRole")
	if len(terms) != 1 {
		return fmt.Sprintf("renders %d preferred terms against the event-store primary, want exactly one", len(terms))
	}
	return preferredHostnameTerm(terms[0], primaryLabels)
}

// ownLabels is the area's own labels, which its Deployment selects on.
func ownLabels(area, instance string) map[string]string {
	return map[string]string{"devicechain.io/instance": instance, "devicechain.io/functional-area": area}
}

// prefersNodesWithoutItsOwnPods reports what is wrong with one area's affinity, or ""
// when it holds exactly one PREFERRED term against the area's own pods (exactly its
// own labels), and nothing required. It is what keeps the replicas of an area that
// set the event-path spread, and so lost the scheduler's default spread, on
// different nodes.
func prefersNodesWithoutItsOwnPods(ra renderedAffinity, area, instance string) string {
	if why := affinityShape(ra); why != "" {
		return why + ", want a preferred term against the area's own pods"
	}
	terms := termsSelecting(ra, "devicechain.io/functional-area")
	if len(terms) != 1 {
		return fmt.Sprintf("renders %d preferred terms against the area's own pods, want exactly one", len(terms))
	}
	return preferredHostnameTerm(terms[0], ownLabels(area, instance))
}

// strayTerms returns every preferred term that is neither the term against the
// event-store primary nor the one against the area's own pods.
func strayTerms(ra renderedAffinity, area, instance string) []string {
	var out []string
	for _, p := range ra.preferred {
		if preferredHostnameTerm(p, primaryLabels) == "" || preferredHostnameTerm(p, ownLabels(area, instance)) == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%+v", p))
	}
	return out
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
		{"dcctl --ha", func() map[string]interface{} { return helmValues(persistenceState(true, false, "default")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := renderChart(t, tc.vals())
			if err != nil {
				t.Fatalf("rendering chart: %v", err)
			}
			got := renderedAntiAffinity(t, manifest)
			// Only --ha runs a pod twice here (event-management), and only a pod that
			// runs twice gets the term against its own pods.
			twice := map[string]bool{"event-management": tc.name == "dcctl --ha"}
			for _, area := range avoiders {
				ra, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: its placement was not checked", area)
					continue
				}
				if why := avoidsTheEventStorePrimary(ra); why != "" {
					t.Errorf("%s: %s", area, why)
				}
				own := prefersNodesWithoutItsOwnPods(ra, area, "dctest")
				if twice[area] && own != "" {
					t.Errorf("%s at two pods: %s", area, own)
				} else if !twice[area] && own == "" {
					t.Errorf("%s at one pod renders a preferred term against its own pods", area)
				}
				if stray := strayTerms(ra, area, "dctest"); len(stray) != 0 {
					t.Errorf("%s renders preferred terms that are neither rule's: %v", area, stray)
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
		// Above one replica: event-management under --ha, and every area that takes
		// the top-level count. The event-path spread stays ONE constraint; the area's
		// own pods are kept apart by a preferred anti-affinity instead (see
		// TestEventPathAreasPreferNodesWithoutTheEventStorePrimary).
		{"dcctl --ha", func() map[string]interface{} { return helmValues(persistenceState(true, false, "default")) }},
		{"dcctl --ha, every area at replicas 2", func() map[string]interface{} {
			vals := helmValues(persistenceState(true, false, "default"))
			vals["replicas"] = 2
			return vals
		}},
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

	// Without the shared spread, the pod keeps the scheduler's default spread, which
	// already separates one Deployment's replicas (and its zones), so the area gets no
	// term against its own pods either; the event-store primary term is untouched.
	t.Run("above one replica, eventPathSpread off keeps the default spread", func(t *testing.T) {
		vals := helmValues(persistenceState(true, false, "default"))
		fa := vals["functionalAreas"].(map[string]interface{})
		fa["event-management"].(map[string]interface{})["eventPathSpread"] = false
		manifest, err := renderChart(t, vals)
		if err != nil {
			t.Fatalf("rendering chart: %v", err)
		}
		em, ok := renderedSpread(t, manifest)["event-management"]
		if !ok {
			t.Fatal("event-management did not render")
		}
		if len(em.constraints) != 0 {
			t.Errorf("event-management renders %d spread constraints with eventPathSpread: false", len(em.constraints))
		}
		ra := renderedAntiAffinity(t, manifest)["event-management"]
		if why := prefersNodesWithoutItsOwnPods(ra, "event-management", "dctest"); why == "" {
			t.Error("event-management with eventPathSpread: false renders a term against its own pods; " +
				"it keeps the default spread instead")
		}
		if why := avoidsTheEventStorePrimary(ra); why != "" {
			t.Errorf("event-management with eventPathSpread: false: %s", why)
		}
	})
}
