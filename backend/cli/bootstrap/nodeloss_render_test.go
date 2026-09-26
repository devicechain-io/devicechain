// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-k8s/functionalarea"
)

// The node-loss eviction fuse (values.yaml `nodeLossTolerationSeconds`) is the
// only thing that moves a stateless pod off a node Kubernetes has declared lost:
// at `replicas: 1` the area is down until it goes. It was chosen at 30 on purpose
// -- the reasoning is at the value -- and it is 30 here on purpose too, so that a
// change to it is a decision somebody makes in two places rather than one that
// slips through a values edit.
//
// What this guards is the thing that already went wrong once (see the helper's
// comment in _helpers.tpl): a pod-spec template that does not include the helper
// renders a pod with NO toleration, which Kubernetes then gives its 300s default.
// Nothing fails; the one pod simply stays on the dead node five minutes longer than
// every other. So the assertion is on every rendered pod template, by value -- of
// whatever kind carries it, so that a Job, CronJob or StatefulSet added to the chart
// later is checked the day it lands -- and the rendered set has floors so that it
// cannot go green over fewer.
const wantNodeLossSeconds = 30

// nodeLossTaints are the two NoExecute taints the node lifecycle controller puts on
// a node it has lost contact with (NotReady) or cannot reach (unreachable).
var nodeLossTaints = []string{"node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable"}

// fullProfileState renders every area the chart can deploy, so the pod set is the
// largest a real bootstrap produces.
func fullProfileState() *State {
	st := compactState(false)
	st.Profile = string(functionalarea.ProfileFull)
	return st
}

// podKinds are the workload kinds that carry a pod template. A rendered doc of one
// of these kinds whose pod spec cannot be found is a failure, not a skip: the
// point of reading every kind is that nothing pod-bearing goes unchecked.
var podKinds = []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"}

// podSpecOf returns the pod spec a rendered doc carries, from wherever its kind
// keeps it: spec itself (Pod), spec.jobTemplate.spec.template.spec (CronJob), or
// spec.template.spec (everything else). ok is false when the doc has none.
func podSpecOf(doc map[string]interface{}) (map[string]interface{}, bool) {
	get := func(m map[string]interface{}, k string) map[string]interface{} {
		v, _ := m[k].(map[string]interface{})
		return v
	}
	spec := get(doc, "spec")
	if spec == nil {
		return nil, false
	}
	if doc["kind"] == "Pod" {
		return spec, true
	}
	if jt := get(spec, "jobTemplate"); jt != nil {
		spec = get(jt, "spec")
	}
	pod := get(get(spec, "template"), "spec")
	return pod, pod != nil
}

// renderedPodSpecs returns the pod spec of every doc the chart renders for vals
// that carries one, keyed by "<kind>/<metadata.name>".
func renderedPodSpecs(t *testing.T, vals map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for _, doc := range renderDocs(t, vals) {
		kind, _ := doc["kind"].(string)
		pod, ok := podSpecOf(doc)
		if !ok {
			if slices.Contains(podKinds, kind) {
				t.Fatalf("a rendered %s has no pod spec where its kind keeps one: %v", kind, doc["metadata"])
			}
			continue
		}
		name, _ := doc["metadata"].(map[string]interface{})["name"].(string)
		if name == "" {
			t.Fatalf("a rendered %s has no metadata.name: %v", kind, doc["metadata"])
		}
		out[kind+"/"+name] = pod
	}
	return out
}

// podTolerations reads a pod spec's tolerations, keyed by taint key. A key that
// appears twice is a rendering defect in its own right and fails the test.
func podTolerations(t *testing.T, name string, pod map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	raw, _ := pod["tolerations"].([]interface{})
	out := map[string]map[string]interface{}{}
	for _, r := range raw {
		tol, _ := r.(map[string]interface{})
		key, _ := tol["key"].(string)
		if _, dup := out[key]; dup {
			t.Errorf("%s carries the toleration %q twice", name, key)
		}
		out[key] = tol
	}
	return out
}

func TestEveryRenderedPodCarriesTheNodeLossFuse(t *testing.T) {
	areas, err := functionalarea.ResolveEnabled(string(functionalarea.ProfileFull), nil)
	if err != nil {
		t.Fatalf("resolving the full profile: %v", err)
	}
	pods := renderedPodSpecs(t, helmValues(fullProfileState()))

	// Floors. Without them a render that dropped pods -- or one that dropped the
	// console, the template that once shipped without the fuse -- would pass over
	// whatever was left.
	if _, ok := pods["Deployment/frontend"]; !ok {
		t.Fatalf("no Deployment named frontend rendered (got %v): the console is the "+
			"one pod-spec template outside the area loop, and it must be in the set", names(pods))
	}
	for _, a := range areas {
		if _, ok := pods["Deployment/"+string(a)]; !ok {
			t.Errorf("the full profile enables %s but no Deployment of that name rendered (got %v)", a, names(pods))
		}
	}

	for name, pod := range pods {
		tols := podTolerations(t, name, pod)
		for _, key := range nodeLossTaints {
			tol, ok := tols[key]
			if !ok {
				t.Errorf("%s has no %s toleration, so Kubernetes gives it 300s on a lost node", name, key)
				continue
			}
			// %#v, not %v: a value of the wrong type (the string "30") must not
			// print exactly like the right one.
			if got := tol["tolerationSeconds"]; got != float64(wantNodeLossSeconds) {
				t.Errorf("%s: %s tolerationSeconds = %#v (%T), want %d", name, key, got, got, wantNodeLossSeconds)
			}
			if tol["effect"] != "NoExecute" || tol["operator"] != "Exists" {
				t.Errorf("%s: %s toleration is effect=%#v operator=%#v, want NoExecute/Exists "+
					"(anything else does not match the taint the node controller applies)",
					name, key, tol["effect"], tol["operator"])
			}
		}
	}
}

// The counter-case, which pins the helper's `kindIs "invalid"` over `with`:
//   - null means "leave Kubernetes' default", so NO toleration is rendered (and
//     the admission plugin supplies 300);
//   - 0 is a real request -- evict as soon as the taint lands -- and must render
//     as 0, which `with` would have treated as unset.
func TestNodeLossFuseNullRendersNoneAndZeroRendersZero(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value interface{}
	}{
		{"null", nil},
		{"zero", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vals := helmValues(fullProfileState())
			vals["nodeLossTolerationSeconds"] = tc.value
			pods := renderedPodSpecs(t, vals)
			if _, ok := pods["Deployment/frontend"]; !ok || len(pods) < 2 {
				t.Fatalf("rendered only %v; the frontend plus at least one area is the floor", names(pods))
			}
			for name, pod := range pods {
				tols := podTolerations(t, name, pod)
				for _, key := range nodeLossTaints {
					tol, ok := tols[key]
					switch {
					case tc.value == nil && ok:
						t.Errorf("%s: nodeLossTolerationSeconds=null still rendered %s: %#v", name, key, tol)
					case tc.value != nil && !ok:
						t.Errorf("%s: nodeLossTolerationSeconds=0 rendered no %s toleration, "+
							"which leaves Kubernetes' 300s in place of the requested 0", name, key)
					case tc.value != nil && tol["tolerationSeconds"] != float64(0):
						t.Errorf("%s: %s tolerationSeconds = %#v (%T), want 0", name, key, tol["tolerationSeconds"], tol["tolerationSeconds"])
					}
				}
			}
		})
	}
}

func names(pods map[string]map[string]interface{}) string {
	out := make([]string, 0, len(pods))
	for n := range pods {
		out = append(out, n)
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}
