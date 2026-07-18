// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package functionalarea

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCatalogMatchesPublishedServiceImages is the release image-ref contract.
//
// A cut release is only deployable if the images release.yml publishes are
// exactly the images the deploy path expects to pull. The two sides are:
//
//   - PUBLISHED (release.yml + hack/list-services.sh): every backend/services/*
//     directory that has a main.go is ko-built and pushed as
//     ghcr.io/devicechain-io/<dir-name>:<tag>. release.yml uses `ko --bare` with
//     an explicit repo == the directory basename (NOT --base-import-paths, which
//     would name the image after the dc-<name> Go module base and mismatch this).
//   - EXPECTED (this catalog + the Helm chart): the chart renders each enabled
//     functional area as "{registry}/{area}:{tag}" (templates/_helpers.tpl,
//     devicechain.image), where {area} is the FunctionalArea string.
//
// If a service directory and its FunctionalArea key ever diverge — a new service
// added without a catalog entry, an area renamed on one side only — a tag would
// publish an image no area deploys, or (worse) deploy an area whose image was
// never published, and the break would only surface on a fresh install of that
// release. This test fails at PR time instead.
//
// The operator (backend/k8s -> .../operator, ko --bare) and the console
// (frontend/Dockerfile -> .../frontend) are published under fixed names by
// dedicated release jobs and are not functional areas, so they are excluded from
// both sides by construction.
func TestCatalogMatchesPublishedServiceImages(t *testing.T) {
	// PUBLISHED side: mirror hack/list-services.sh exactly — backend/services/*
	// dirs containing a main.go. Tests run with cwd = the package directory, so
	// ../../services resolves to backend/services.
	servicesDir := filepath.Join("..", "..", "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("reading %s: %v", servicesDir, err)
	}
	published := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(servicesDir, e.Name(), "main.go")); err == nil {
			published[e.Name()] = true
		}
	}
	if len(published) == 0 {
		t.Fatalf("found no publishable services under %s — path or filter is wrong", servicesDir)
	}

	// EXPECTED side: every functional area in the catalog. The chart can deploy
	// any of these, so every one must have a published image.
	expected := map[string]bool{}
	for area := range catalog {
		expected[string(area)] = true
	}

	for name := range published {
		if !expected[name] {
			t.Errorf("service %q is published by release.yml but has no functional area in the catalog — "+
				"the chart cannot deploy it. Add a FunctionalArea + catalog manifest, or exclude the service.", name)
		}
	}
	for name := range expected {
		if !published[name] {
			t.Errorf("functional area %q is in the catalog (the chart renders %s/<area>:<tag> for it) but no "+
				"backend/services/%s/main.go exists, so release.yml never publishes its image — a fresh install "+
				"of a tagged release would fail to pull it.", name, "{registry}", name)
		}
	}
}
