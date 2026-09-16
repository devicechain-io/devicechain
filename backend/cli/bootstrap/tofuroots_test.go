// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// embeddedRoots is every OpenTofu root dcctl ships, by name.
//
// 🔴 TESTS READ ALL OF THEM RATHER THAN NAMING ONE, and the root split is why. Every
// assertion in this package about "the OpenTofu root" was written when there was one,
// and each names a variable or a wiring line by string. Pointing them at whichever
// root happens to hold that string today would be a second list of which-lives-where,
// kept in step by hand, going stale on exactly the edit that most needs checking: a
// variable MOVING between roots.
//
// 🔑 The question these assertions actually ask is "does the tree dcctl ships still
// wire this?", and that question has never been about a particular directory.
func embeddedRoots() map[string]fs.FS {
	return map[string]fs.FS{
		assets.ClusterRootDir:  assets.OpenTofuCluster(),
		assets.InstanceRootDir: assets.OpenTofuInstance(),
	}
}

// rootSources reads one file from every root, skipping roots that do not have it.
func rootSources(t *testing.T, file string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for name, root := range embeddedRoots() {
		body, err := fs.ReadFile(root, file)
		if err != nil {
			continue
		}
		out[name] = string(body)
	}
	if len(out) == 0 {
		t.Fatalf("no embedded OpenTofu root contains %s — the assertions that read it are "+
			"reading nothing, so they cannot fail", file)
	}
	return out
}

// rootNamesHolding returns the roots whose copy of `file` contains `want`, sorted so
// a failure message reads the same way twice.
func rootNamesHolding(t *testing.T, file, want string) []string {
	t.Helper()

	var found []string
	for name, src := range rootSources(t, file) {
		if strings.Contains(src, want) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

// requireWiredInSomeRoot fails unless some root's copy of `file` carries `want`.
//
// "Some root" rather than "the instance root" is the honest requirement for a wiring
// assertion: it asks whether the shipped tree still connects two things, and which
// root does the connecting is a layout decision the assertion has no stake in.
func requireWiredInSomeRoot(t *testing.T, file, want, why string) {
	t.Helper()

	if len(rootNamesHolding(t, file, want)) == 0 {
		t.Errorf("no OpenTofu root's %s contains %q.\n%s", file, want, why)
	}
}
