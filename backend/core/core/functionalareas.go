// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"os"
	"sort"
	"strings"
)

// MicroserviceConfigDir is the mount point of the per-area configuration volume.
// The Helm chart renders one ConfigMap key per ENABLED functional area
// (templates/microservice-config.yaml) and mounts the whole map here on every
// pod, so each service reads its own /etc/dct-config/{area}.
const MicroserviceConfigDir = "/etc/dct-config"

// EnabledFunctionalAreas reports the functional areas this instance deployed, by
// listing the per-area config directory.
//
// This is DERIVED, never re-declared. The area catalog already exists twice — the
// Go catalog in dc-k8s/functionalarea and the chart's `devicechain.enabledAreas`
// guard — and a third hardcoded list would drift toward exactly the failure this
// exists to prevent: the console offering a feature the cluster does not serve.
// Reading a directory the chart itself wrote adds no list to keep in step.
//
// It reports DESIRED state — what the operator deployed — not health. An area
// that is enabled but CrashLooping still appears here. That is correct for the
// question this answers ("did the operator deploy this?") and wrong for a status
// page; observed health is a separate concern and must not be blended in.
//
// 🔴 The dotfile skip is load-bearing, not defensive tidiness. Kubernetes projects
// a ConfigMap through its atomic writer, so the mount is NOT a flat directory of
// keys. Measured on a live pod:
//
//	..2026_08_12_02_48_51.185100639      <- a real directory holding the files
//	..data -> ..2026_08_12_02_48_51...   <- symlink, swapped atomically on update
//	user-management -> ..data/user-management
//	... one symlink per enabled area
//
// os.ReadDir omits "." and ".." but NOT dotfiles, so a naive listing returns two
// extra entries and would report `..data` and a timestamped directory as
// functional areas. Note what that does to testing: a hand-built fixture
// directory containing one file per area has no `..data` in it, so a test written
// that way passes while production returns the wrong answer. The test for this
// reproduces the atomic-writer layout instead.
func EnabledFunctionalAreas() ([]string, error) {
	return EnabledFunctionalAreasIn(MicroserviceConfigDir)
}

// EnabledFunctionalAreasIn is EnabledFunctionalAreas against an explicit
// directory, for callers whose mount is not at the chart's path (and for tests,
// which must supply the atomic-writer layout described above).
func EnabledFunctionalAreasIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	areas := make([]string, 0, len(entries))
	for _, e := range entries {
		// Skips ..data and ..<timestamp> (above). Kubernetes DOES permit a
		// dot-prefixed ConfigMap key — verified against a live apiserver, which
		// accepts `.hidden` and projects it as a hidden file — so the reason this
		// cannot hide a real area is narrower and worth stating exactly: no
		// FUNCTIONAL AREA NAME begins with a dot, and the chart writes one key per
		// area and nothing else.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		areas = append(areas, e.Name())
	}
	// Sorted so the response is stable across calls: directory order is not
	// guaranteed, and an unstable list would churn the console's cache and make
	// any equality assertion flaky for reasons unrelated to what it tests.
	sort.Strings(areas)
	return areas, nil
}
