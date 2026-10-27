// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package bootstrap

import (
	"maps"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestEnablingAnAreaIsAcceptedByAnAPIServer drives the day-2 change the way `dcctl
// enable-area` makes it: the instance installed from its declaration, then a Helm
// upgrade of the same release with the values the enable-area run composes. The API
// server is the judge that the added area's objects are valid, and that every object
// the profile already ran is still there after the upgrade.
func TestEnablingAnAreaIsAcceptedByAnAPIServer(t *testing.T) {
	t.Parallel()
	c, kubeconfig := renderAPIServer(t)
	cfg, ch := helmInto(t, kubeconfig), chartOfItsOwn(t)

	valuesFor := func(areas ...string) map[string]interface{} {
		st := &State{Instance: "guide", Provider: "local", Values: maps.Clone(compactState(false).Values)}
		opts := UpgradeOptions{Options: Options{Instance: "guide"}, EnableAreas: areas}
		if err := applyUpgradeDeclaration(st, readyGuide(), opts); err != nil {
			t.Fatalf("settling %v: %v", areas, err)
		}
		vals, _, err := releaseValues(t.Context(), ch, st, nil)
		if err != nil {
			t.Fatalf("composing the release values for %v: %v", areas, err)
		}
		return vals
	}

	rel, err := installProfile(t, cfg, c, ch, "guide", valuesFor())
	if err != nil {
		t.Fatalf("installing the default-profile instance: %v", err)
	}
	ns := renderedDeployment(t, rel.Manifest, "device-management").Namespace

	// Negative control: before the change, the server holds no outbound-connectors.
	var d appsv1.Deployment
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: "outbound-connectors"}, &d); !apierrors.IsNotFound(err) {
		t.Fatalf("control: before enable-area the server answered %v for outbound-connectors, want NotFound", err)
	}

	rel, err = upgradeRelease(t, c, cfg, helmReleaseNameFor("guide"), ch, valuesFor("outbound-connectors"))
	if err != nil {
		t.Fatalf("the API server refused the enable-area upgrade: %v", err)
	}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: "outbound-connectors"}, &d); err != nil {
		t.Errorf("after enable-area the server does not hold the outbound-connectors Deployment: %v", err)
	}
	if missing := everyObjectIsThere(t, c, rel.Manifest); len(missing) != 0 {
		t.Errorf("the enable-area upgrade reported success but the server does not hold: %v", missing)
	}
}
