// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The local registry image dcctl's --build path starts must be addressed by
// DIGEST, with the tag kept beside it.
//
// hack/check-image-pins.sh enforces this for every tracked shell script, and the
// two other places that start this same container are shell (hack/upgrade-rig.sh,
// deploy/local/up.sh). This one is Go, so it gets its guard here rather than by
// teaching a shell tokenizer to read Go.
//
// WHY IT MATTERS HERE AND NOT ONLY IN THE RIG: all three sites only create the
// container when it is not already running, so whichever runs first decides what
// the other two reuse. A pin that only the rig carries is a pin dcctl can defeat
// on a developer box by getting there first.
//
// 🔴 THE PATTERN IS ASSERTED, NOT THE VALUE. Pinning the exact digest here would
// make every bump a two-file edit whose second half is a copy of the first — a
// fixture built from the thing under test, which cannot notice that thing moving
// and only teaches people to update it without reading it. What must not change
// is the SHAPE: a digest, and a tag in front of it.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*@sha256:[0-9a-f]{64}$`)

func TestLocalRegistryImageIsDigestPinned(t *testing.T) {
	if !strings.Contains(localRegistryImage, "@sha256:") {
		t.Fatalf("localRegistryImage = %q, which is a bare tag: a name Docker Hub can repoint under a build. Pin it as image:tag@sha256:...", localRegistryImage)
	}
	if !pinnedImage.MatchString(localRegistryImage) {
		t.Fatalf("localRegistryImage = %q does not read as image:tag@sha256:<64 hex>. The tag half is not decoration — it is the only thing that tells a reader which version the digest is", localRegistryImage)
	}
}

// The constant is only worth guarding while it is the thing that reaches `docker
// run`. This is the half a constant-only assertion cannot make: someone re-adds a
// literal at the call site, the constant stays perfectly pinned, and dcctl pulls a
// moving tag again. That is exactly how the site this came from was written.
func TestNoBareRegistryImageLiteralInSteps(t *testing.T) {
	f, err := os.Open("steps.go")
	if err != nil {
		t.Fatalf("open steps.go: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	seen := 0
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if !strings.Contains(text, `"registry:`) {
			continue
		}
		seen++
		if !strings.Contains(text, "@sha256:") {
			t.Errorf("steps.go:%d carries a bare registry image literal: %s", line, strings.TrimSpace(text))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read steps.go: %v", err)
	}

	// An enumeration that finds nothing is how this check would go quietly
	// vacuous — a renamed file, a moved constant, a changed quoting style — and
	// it would report a clean file it never read.
	if seen == 0 {
		t.Fatalf("found no registry image literal in steps.go at all; this test is no longer looking at the thing it guards")
	}
}
