// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

func TestRegistryIdentityAcrossImageSources(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	savedDigest, savedVersion := DefaultBrokerImageDigest, DefaultImageVersion
	t.Cleanup(func() { DefaultBrokerImageDigest, DefaultImageVersion = savedDigest, savedVersion })
	DefaultBrokerImageDigest, DefaultImageVersion = digest, "v1.4.0"

	for _, tc := range []struct {
		registry, normalized string
		published            bool
	}{
		{DefaultImageRegistry, DefaultImageRegistry, true},
		{"GHCR.IO/DEVICECHAIN-IO", DefaultImageRegistry, true},
		{"https://ghcr.io/devicechain-io", DefaultImageRegistry, true},
		{"ghcr.io:443/devicechain-io", DefaultImageRegistry, true},
		{"ghcr.io/devicechain-io/", DefaultImageRegistry, true},
		{" GHCR.io:443/devicechain-io/ ", DefaultImageRegistry, true},
		{"HTTPS://GHCR.IO:443/DEVICECHAIN-IO//", DefaultImageRegistry, true},
		{"localhost:5000", "localhost:5000", false},
		{"registry.example.com/dc", "registry.example.com/dc", false},
		{"ghcr.io:5000/devicechain-io", "ghcr.io:5000/devicechain-io", false},
		{"ghcr.io/someone-else", "ghcr.io/someone-else", false},
		{"https://GHCR.IO:5000/someone-else/", "ghcr.io:5000/someone-else", false},
		{"ghcr.io", "ghcr.io", false},
		{"", "", false},
	} {
		t.Run(tc.registry, func(t *testing.T) {
			if got := normalizeRegistry(tc.registry); got != tc.normalized {
				t.Errorf("normalizeRegistry = %q, want %q", got, tc.normalized)
			}
			if got := isPublishedRegistry(tc.registry); got != tc.published {
				t.Errorf("isPublishedRegistry = %v, want %v", got, tc.published)
			}
			// An empty argv registry defaults to the published registry; an empty
			// state registry still has no resolved image source or broker digest.
			for _, build := range []bool{false, true} {
				got, err := ResolveImageSource(tc.registry, "1.4.0", build)
				if err != nil {
					t.Fatal(err)
				}
				wantVersion := "1.4.0"
				if !build && (tc.published || tc.registry == "") {
					wantVersion = "v1.4.0"
				}
				if got.Version != wantVersion {
					t.Errorf("build=%v: version = %q, want %q", build, got.Version, wantVersion)
				}
				if tc.registry != "" && (got.Registry != tc.registry || !strings.Contains(got.Label, tc.registry+"/<area>:")) {
					t.Errorf("registry spelling changed: %+v", got)
				}
				st := &State{ImageRegistry: tc.registry, ImageVersion: DefaultImageVersion, BuildImages: build}
				wantDigest := ""
				if tc.published && !build {
					wantDigest = digest
				}
				if got := brokerImageDigest(st); got != wantDigest {
					t.Errorf("build=%v: broker digest = %q, want %q", build, got, wantDigest)
				}
				if st.ImageRegistry != tc.registry {
					t.Errorf("state registry spelling changed to %q", st.ImageRegistry)
				}
			}
			if tc.published {
				if _, err := ResolveImageSource(tc.registry, "dev", false); err == nil {
					t.Error("accepted an unpublished image version")
				}
			}
		})
	}
}
