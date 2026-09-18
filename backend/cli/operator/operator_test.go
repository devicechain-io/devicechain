// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"strings"
	"testing"
)

// The lock lives in the operator's namespace, and that namespace is a kustomize
// setting rather than a constant. A copy of it in Go would be a second place to
// remember: a rename would leave dcctl taking its lock somewhere nothing else
// looks, which is a lock that protects nothing while appearing to work.
func TestOperatorNamespaceComesFromTheRenderedOverlay(t *testing.T) {
	t.Run("it is read out of the manifests", func(t *testing.T) {
		// The Namespace is deliberately NOT the first document: the function has to
		// search the overlay, not read its head.
		manifests := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-operator-controller-manager
  namespace: dc-operator-system
---
apiVersion: v1
kind: Namespace
metadata:
  name: dc-operator-system
`)
		got, err := NamespaceOf(manifests)
		if err != nil {
			t.Fatalf("the overlay's namespace could not be read: %v", err)
		}
		if got != "dc-operator-system" {
			t.Errorf("read namespace %q", got)
		}
	})

	t.Run("an overlay with no Namespace fails loudly", func(t *testing.T) {
		manifests := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: dc-operator-controller-manager
  namespace: dc-operator-system
`)
		got, err := NamespaceOf(manifests)
		if err == nil {
			t.Fatalf("a manifest set declaring no Namespace answered %q; the lock would be taken "+
				"in a namespace nothing else uses", got)
		}
		if !strings.Contains(err.Error(), "lock") {
			t.Errorf("the refusal does not say what it costs: %v", err)
		}
	})

	// And against the overlay dcctl actually renders, because the refusal above is
	// only worth having if the real thing passes.
	t.Run("the real operator overlay declares one", func(t *testing.T) {
		ns, err := Namespace()
		if err != nil {
			t.Fatalf("the shipped operator overlay declares no namespace to take the lock in: %v", err)
		}
		if ns == "" {
			t.Error("the shipped operator overlay declares an empty namespace")
		}
	})
}

// 🔴 A REGISTRY WITH A PORT IS THE DEVELOPER PATH'S OWN DEFAULT (localhost:5000),
// so the reference handed to the overlay has a colon in it that is NOT a tag
// separator. Three verbs compose this reference now; getting it wrong produces a
// syntactically valid image that pulls nothing.
func TestTheImageReferenceSurvivesARegistryPort(t *testing.T) {
	for _, tc := range []struct{ registry, version, want string }{
		{"localhost:5000", "dev", "localhost:5000/operator:dev"},
		{"ghcr.io/devicechain-io", "v0.17.0", "ghcr.io/devicechain-io/operator:v0.17.0"},
	} {
		if got := ImageRef(tc.registry, tc.version); got != tc.want {
			t.Errorf("ImageRef(%q, %q) = %q, want %q", tc.registry, tc.version, got, tc.want)
		}
	}
}

// 🔴 THE BUILD AND THE OVERLAY MUST NAME THE SAME IMAGE, and nothing else checks
// it: the developer path ko-builds to ImageName and the overlay's Deployment is
// patched to a reference built from the same constant, so a divergence would be an
// ImagePullBackOff on a controller nobody is watching rather than a failed build.
//
// Rendering with a known reference and finding it in the stream is what ties the
// two together — a test on the constant alone would agree with itself.
func TestTheRenderedOverlayCarriesTheImageWeName(t *testing.T) {
	ref := ImageRef("localhost:5000", "dev")
	manifests, err := Render(ref)
	if err != nil {
		t.Fatalf("rendering the operator overlay: %v", err)
	}
	if !strings.Contains(string(manifests), ref) {
		t.Fatalf("the rendered overlay does not name %q, so the image dcctl builds and the "+
			"image the cluster pulls are two different things", ref)
	}
}

// The identity slice C's guards compare is readable off what this package renders.
// Re-exported rather than reached for directly in dc-k8s, so the verbs that must
// agree ask one package about all of it — this pins that the re-export works on a
// real stream rather than merely compiling.
func TestTheRenderedOverlayCarriesAnIdentity(t *testing.T) {
	manifests, err := Render(ImageRef("localhost:5000", "dev"))
	if err != nil {
		t.Fatalf("rendering the operator overlay: %v", err)
	}
	id, err := Identity(manifests)
	if err != nil {
		t.Fatalf("the rendered overlay carries no identity: %v", err)
	}
	if id == "" {
		t.Fatal("the rendered overlay carries an EMPTY identity, which every cluster would " +
			"agree with, so the guards built on it would pass everything")
	}
}
