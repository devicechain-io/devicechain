// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// The seam under test: cmd.Version describes the BINARY and DefaultImageVersion
// names an IMAGE TAG, and only the second has to resolve in a registry. The
// conflation is made in build tooling — a makefile or a goreleaser config, which
// no Go test runs — so this pins the backstop at the point of consumption.
//
// 🔴 Note what the table below asserts about "0.0.1": the guard treats it as
// PUBLISHED, because it is shaped exactly like a real release tag. That is
// correct and it is also why the makefile must not stamp the VERSION file here —
// nothing at this layer can tell that value apart from a genuine release, so the
// fix has to be that the tooling never produces it. See DefaultImageVersion.
func TestIsUnpublishedImageVersion(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want bool
	}{
		{
			// The unstamped `go build` fallback. Only ever exists in a local
			// registry; the published registry has no :dev tag.
			name: "the unstamped dev fallback is unpublished",
			tag:  "dev",
			want: true,
		},
		{
			// The regression this exists to catch: someone "simplifies" the
			// build tooling to stamp cmd.Version's value into
			// DefaultImageVersion. Caught here, so the published path refuses
			// instead of chasing a tag that was never pushed.
			name: "a dev build stamp is unpublished",
			tag:  "0.0.1-dev.20260716T155833Z",
			want: true,
		},
		{
			name: "a release version is published",
			tag:  "0.0.1",
			want: false,
		},
		{
			name: "a v-prefixed release tag is published",
			tag:  "v1.0.0",
			want: false,
		},
		{
			// Must not over-reach: a legitimate prerelease tag IS pushed by the
			// release pipeline, and rejecting it would break every RC bootstrap.
			// Only the "-dev." build stamp is unpublished.
			name: "a prerelease tag is published",
			tag:  "1.0.0-rc.1",
			want: false,
		},
		{
			name: "a beta tag is published",
			tag:  "1.0.0-beta.2",
			want: false,
		},
		{
			// "dev" as a substring of a real tag must not trip the guard.
			name: "a tag merely containing dev is published",
			tag:  "1.0.0-developer-preview",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnpublishedImageVersion(tc.tag); got != tc.want {
				t.Errorf("IsUnpublishedImageVersion(%q) = %v, want %v",
					tc.tag, got, tc.want)
			}
		})
	}
}

// DefaultImageVersion's in-source fallback must itself be a value the guard
// rejects: a plain `go build` has no pinned tag, and deploying published images
// from it would ImagePullBackOff minutes into the run.
func TestDefaultImageVersionFallbackIsGuarded(t *testing.T) {
	if !IsUnpublishedImageVersion(DefaultImageVersion) {
		t.Errorf("the unstamped DefaultImageVersion fallback %q is not caught by "+
			"IsUnpublishedImageVersion; an unstamped dcctl would deploy it",
			DefaultImageVersion)
	}
}
