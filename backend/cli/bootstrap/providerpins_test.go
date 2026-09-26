// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// kubernetesProviderFloor is the first hashicorp/kubernetes release whose plugin SDK
// reads the all-null resource identity a failed create leaves in state as absent.
// Below it, a Deployment whose rollout timed out on create can never be refreshed
// again, so no re-run recovers the install. See the comment on the pin in versions.tf.
var kubernetesProviderFloor = [3]int{3, 2, 1}

var (
	hclComment    = regexp.MustCompile(`(?m)(#|//).*$`)
	providerBody  = regexp.MustCompile(`(\w+)\s*=\s*\{([^{}]*)\}`)
	sourceAttr    = regexp.MustCompile(`source\s*=\s*"([^"]*)"`)
	versionAttr   = regexp.MustCompile(`version\s*=\s*"([^"]*)"`)
	exactVersion  = regexp.MustCompile(`^=?\s*(\d+)\.(\d+)\.(\d+)$`)
	requiredStart = regexp.MustCompile(`required_providers\s*\{`)
)

// requiredProvidersBlock returns the body of the one required_providers block in src,
// comments removed, or fails the test.
func requiredProvidersBlock(t *testing.T, name, src string) string {
	t.Helper()
	src = hclComment.ReplaceAllString(src, "")
	locs := requiredStart.FindAllStringIndex(src, -1)
	if len(locs) != 1 {
		t.Fatalf("%s: found %d required_providers blocks, want exactly 1", name, len(locs))
	}
	depth := 1
	for i := locs[0][1]; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[locs[0][1]:i]
			}
		}
	}
	t.Fatalf("%s: the required_providers block is never closed", name)
	return ""
}

// TestShippedRootsPinEveryProviderExactly holds the provider pins every shipped root
// declares: each must be one exact version, because dcctl initialises with -upgrade
// and a range would let a routine re-run move a provider nobody chose, and the
// kubernetes one must be at or above the release that lets a failed install recover.
//
// It reads what dcctl SHIPS (the embedded tree), not the checkout.
func TestShippedRootsPinEveryProviderExactly(t *testing.T) {
	sources := rootSources(t, "versions.tf")
	var roots []string
	for name := range embeddedRoots() {
		roots = append(roots, name)
	}
	sort.Strings(roots)
	for _, root := range roots {
		src, ok := sources[root]
		if !ok {
			t.Errorf("root %q ships no versions.tf, so none of its provider pins was checked", root)
			continue
		}
		vf := root + "/versions.tf"
		block := requiredProvidersBlock(t, vf, src)

		entries := providerBody.FindAllStringSubmatch(block, -1)
		// 🔴 A SCANNER THAT SKIPS WHAT IT CANNOT READ REPORTS CLEAN. Every provider
		// entry names a source, so the sources in the block are the count the matched
		// entries must reach: an entry in a shape the pattern does not match (a nested
		// brace, say) is a mismatch here rather than a provider nobody checked.
		if n := len(sourceAttr.FindAllString(block, -1)); n != len(entries) || n == 0 {
			t.Fatalf("%s: %d source attributes but %d provider entries matched; the scanner "+
				"cannot read this block, so it cannot vouch for it", vf, n, len(entries))
		}
		sawKubernetes := false
		for _, e := range entries {
			name, body := e[1], e[2]
			srcm := sourceAttr.FindStringSubmatch(body)
			verm := versionAttr.FindStringSubmatch(body)
			if srcm == nil || verm == nil {
				t.Errorf("%s: provider %s does not declare both a source and a version", vf, name)
				continue
			}
			ver := verm[1]
			m := exactVersion.FindStringSubmatch(ver)
			if m == nil {
				t.Errorf("%s: provider %s is %q, not an exact version. dcctl initialises with "+
					"-upgrade, so a range lets any re-run move it", vf, name, ver)
				continue
			}
			if srcm[1] != "hashicorp/kubernetes" {
				continue
			}
			sawKubernetes = true
			got := [3]int{atoiT(t, m[1]), atoiT(t, m[2]), atoiT(t, m[3])}
			if versionLess(got, kubernetesProviderFloor) {
				t.Errorf("%s: kubernetes provider %s is below %d.%d.%d, whose plugin SDK cannot "+
					"refresh a Deployment left tainted by a failed rollout, so a failed install "+
					"could never be re-run", vf, ver,
					kubernetesProviderFloor[0], kubernetesProviderFloor[1], kubernetesProviderFloor[2])
			}
		}
		if !sawKubernetes {
			t.Errorf("%s: no exactly pinned hashicorp/kubernetes entry found", vf)
		}
	}
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
