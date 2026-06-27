// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// TestRenderOperatorContents checks the rendered overlay carries the expected
// resources and that the image override was applied (the placeholder
// "controller" image is rewritten to the requested reference).
func TestRenderOperatorContents(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"kind: CustomResourceDefinition",
		"kind: ClusterRole",
		"kind: Deployment",
		"namespace: dc-k8s-system",
		"name: dc-k8s-controller-manager",
		"image: localhost:5000/operator:dev",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered operator manifests missing %q", want)
		}
	}
	if strings.Contains(s, "image: controller:latest") {
		t.Error("placeholder image was not overridden")
	}
}

// TestRenderOperatorOrdering guards the legacy reorder: the Namespace and CRDs
// must be emitted before the namespaced resources that depend on them, so the
// stream applies cleanly without kubectl's kind-sorting.
func TestRenderOperatorOrdering(t *testing.T) {
	out, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	ns := strings.Index(s, "kind: Namespace")
	sa := strings.Index(s, "kind: ServiceAccount")
	dep := strings.Index(s, "kind: Deployment")
	if ns < 0 {
		t.Fatal("no Namespace in rendered output")
	}
	if ns > sa {
		t.Errorf("Namespace (%d) must precede ServiceAccount (%d)", ns, sa)
	}
	if ns > dep {
		t.Errorf("Namespace (%d) must precede Deployment (%d)", ns, dep)
	}
}

// TestSplitImageRef covers the registry-port edge case.
func TestSplitImageRef(t *testing.T) {
	cases := []struct{ in, name, tag string }{
		{"localhost:5000/operator:dev", "localhost:5000/operator", "dev"},
		{"ghcr.io/devicechain-io/operator:1.2.3", "ghcr.io/devicechain-io/operator", "1.2.3"},
		{"controller", "controller", ""},
		{"localhost:5000/op", "localhost:5000/op", ""},
	}
	for _, c := range cases {
		name, tag := splitImageRef(c.in)
		if name != c.name || tag != c.tag {
			t.Errorf("splitImageRef(%q) = (%q,%q), want (%q,%q)", c.in, name, tag, c.name, c.tag)
		}
	}
}
