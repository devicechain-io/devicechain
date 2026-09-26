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
// renders a Deployment with NO toleration, which Kubernetes then gives its 300s
// default. Nothing fails; the one pod simply stays on the dead node five minutes
// longer than every other. So the assertion is on every rendered Deployment, by
// value, and the rendered set has floors so that it cannot go green over fewer.
const wantNodeLossSeconds = 30

// nodeLossTaints are the two NoExecute taints the node lifecycle controller puts on
// a node it has lost contact with (NotReady) or cannot reach (unreachable).
var nodeLossTaints = []string{"node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable"}

// fullProfileState renders every area the chart can deploy, so the Deployment set
// is the largest a real bootstrap produces.
func fullProfileState() *State {
	st := compactState(false)
	st.Profile = string(functionalarea.ProfileFull)
	return st
}

// renderedDeployments returns every Deployment the chart renders for vals, keyed by
// metadata.name.
func renderedDeployments(t *testing.T, vals map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for _, doc := range renderDocs(t, vals) {
		if doc["kind"] != "Deployment" {
			continue
		}
		name, _ := doc["metadata"].(map[string]interface{})["name"].(string)
		if name == "" {
			t.Fatalf("a rendered Deployment has no metadata.name: %v", doc["metadata"])
		}
		out[name] = doc
	}
	return out
}

// podTolerations reads spec.template.spec.tolerations, keyed by taint key. A key
// that appears twice is a rendering defect in its own right and fails the test.
func podTolerations(t *testing.T, name string, dep map[string]interface{}) map[string]map[string]interface{} {
	t.Helper()
	spec, _ := dep["spec"].(map[string]interface{})
	tmpl, _ := spec["template"].(map[string]interface{})
	pod, _ := tmpl["spec"].(map[string]interface{})
	raw, _ := pod["tolerations"].([]interface{})
	out := map[string]map[string]interface{}{}
	for _, r := range raw {
		tol, _ := r.(map[string]interface{})
		key, _ := tol["key"].(string)
		if _, dup := out[key]; dup {
			t.Errorf("Deployment %s carries the toleration %q twice", name, key)
		}
		out[key] = tol
	}
	return out
}

func TestEveryRenderedDeploymentCarriesTheNodeLossFuse(t *testing.T) {
	areas, err := functionalarea.ResolveEnabled(string(functionalarea.ProfileFull), nil)
	if err != nil {
		t.Fatalf("resolving the full profile: %v", err)
	}
	deps := renderedDeployments(t, helmValues(fullProfileState()))

	// Floors. Without them a render that dropped Deployments -- or one that
	// dropped the console, the template that once shipped without the fuse --
	// would pass over whatever was left.
	if _, ok := deps["frontend"]; !ok {
		t.Fatalf("no Deployment named frontend rendered (got %v): the console is the "+
			"one pod-spec template outside the area loop, and it must be in the set", names(deps))
	}
	for _, a := range areas {
		if _, ok := deps[string(a)]; !ok {
			t.Errorf("the full profile enables %s but no Deployment of that name rendered (got %v)", a, names(deps))
		}
	}

	for name, dep := range deps {
		tols := podTolerations(t, name, dep)
		for _, key := range nodeLossTaints {
			tol, ok := tols[key]
			if !ok {
				t.Errorf("Deployment %s has no %s toleration, so Kubernetes gives it 300s on a lost node", name, key)
				continue
			}
			if got := tol["tolerationSeconds"]; got != float64(wantNodeLossSeconds) {
				t.Errorf("Deployment %s: %s tolerationSeconds = %v, want %d", name, key, got, wantNodeLossSeconds)
			}
			if tol["effect"] != "NoExecute" || tol["operator"] != "Exists" {
				t.Errorf("Deployment %s: %s toleration is effect=%v operator=%v, want NoExecute/Exists "+
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
			deps := renderedDeployments(t, vals)
			if _, ok := deps["frontend"]; !ok || len(deps) < 2 {
				t.Fatalf("rendered only %v; the frontend plus at least one area is the floor", names(deps))
			}
			for name, dep := range deps {
				tols := podTolerations(t, name, dep)
				for _, key := range nodeLossTaints {
					tol, ok := tols[key]
					switch {
					case tc.value == nil && ok:
						t.Errorf("Deployment %s: nodeLossTolerationSeconds=null still rendered %s: %v", name, key, tol)
					case tc.value != nil && !ok:
						t.Errorf("Deployment %s: nodeLossTolerationSeconds=0 rendered no %s toleration, "+
							"which leaves Kubernetes' 300s in place of the requested 0", name, key)
					case tc.value != nil && tol["tolerationSeconds"] != float64(0):
						t.Errorf("Deployment %s: %s tolerationSeconds = %v, want 0", name, key, tol["tolerationSeconds"])
					}
				}
			}
		})
	}
}

func names(deps map[string]map[string]interface{}) string {
	out := make([]string, 0, len(deps))
	for n := range deps {
		out = append(out, n)
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}
