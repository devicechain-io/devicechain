// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// objectStoreModulePath is the object store module as the EMBEDDED tree holds it,
// i.e. what dcctl extracts and applies — not a copy on disk that could differ.
const objectStoreModulePath = "modules/object-store/main.tf"

// pinnedImage is image:tag@sha256:<64 lowercase hex>, anchored at both ends.
//
// The tag half is required too, for the reason hack/check-image-pins.sh gives: a
// bare digest is a fact nobody can act on, and the bumper resolves the TAG to find
// the next digest, so a reference without one cannot be moved forward.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*@sha256:[0-9a-f]{64}$`)

func objectStoreModule(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(OpenTofu(), objectStoreModulePath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", objectStoreModulePath, err)
	}
	return string(b)
}

// imageDefault returns the default of `variable "image"`, with the grammar
// hack/lib/object-store-image.sh uses: start at the line `variable "image" {`,
// stop at the first line that is exactly `}`, take the first `default = "…"`.
// A missing block or default is fatal, never an empty string that a later
// assertion could misread.
func imageDefault(t *testing.T, src string) string {
	t.Helper()
	defaultLine := regexp.MustCompile(`^[ \t]*default[ \t]*=[ \t]*"([^"]*)"[ \t]*$`)
	in := false
	for _, line := range strings.Split(src, "\n") {
		switch {
		case !in && strings.TrimRight(line, " \t") == `variable "image" {`:
			in = true
		case in && strings.TrimRight(line, " \t") == "}":
			t.Fatalf("%s: variable \"image\" has no default", objectStoreModulePath)
		case in:
			if m := defaultLine.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	t.Fatalf("%s: no `variable \"image\" {` block found", objectStoreModulePath)
	return ""
}

// TestObjectStoreImageIsDigestPinned: the object store's image must be addressed
// by digest.
//
// A tag-only reference is exactly how every fresh install came to fail: the
// default named a release tag on a registry that later stopped serving it, and
// only nodes that had already cached it could start the store. A digest does not
// make a registry keep serving an image — hack/check-image-pulls.sh, run by the
// weekly bumper, watches that — but it makes the pull reproducible, lets a
// mirrored copy satisfy it, and gives the bumper a pin to advance.
//
// Asserts the SHAPE, not a particular digest: a fixture copied from the value
// under test cannot notice it move, and the digest is meant to move.
func TestObjectStoreImageIsDigestPinned(t *testing.T) {
	got := imageDefault(t, objectStoreModule(t))
	if !pinnedImage.MatchString(got) {
		t.Fatalf("object-store image default = %q, not image:tag@sha256:<64 hex>", got)
	}
}

// TestObjectStoreContainersTakeTheVariable: every container in the module runs
// var.image — the init container that creates the buckets AND the server. A
// literal on either one would be a second pin nothing advances and nothing
// checks, and the init container is the one a failed pull stops first.
func TestObjectStoreContainersTakeTheVariable(t *testing.T) {
	src := objectStoreModule(t)
	imageLine := regexp.MustCompile(`^[ \t]*image[ \t]*=`)
	n := 0
	for i, line := range strings.Split(src, "\n") {
		if !imageLine.MatchString(line) {
			continue
		}
		n++
		if got := strings.Join(strings.Fields(line), " "); got != "image = var.image" {
			t.Errorf("%s:%d: container image is %q, want `image = var.image`", objectStoreModulePath, i+1, got)
		}
	}
	// Two: create-buckets and minio. Zero would mean this stopped looking at
	// the thing it guards; one would mean a container lost its image line.
	if n != 2 {
		t.Fatalf("%s: found %d container image lines, want 2 (create-buckets + minio)", objectStoreModulePath, n)
	}
}
