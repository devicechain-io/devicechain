// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ledger"
)

var fleetReleaseVersion = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+].*)?$`)
var fleetStableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// TestNoExecutableKindWithoutARolledFoundation is conditional: the foundation
// needs no tags. Scanning every non-test schema source conservatively includes
// migrations registered in Migrations and helpers they call; an unregistered
// enforcing migration also closes the gate. The readiness constants are excluded.
//
// The build version is DC_UPGRADE_TAG, the release/upgrade workflow's target,
// or an exact version tag on HEAD. An untagged development build has no release
// version to classify. The release workflow must supply its target through
// DC_UPGRADE_TAG (or test the tagged commit) to enforce patch refusal there.
func TestNoExecutableKindWithoutARolledFoundation(t *testing.T) {
	files, err := filepath.Glob("../schema/*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sources[file] = string(data)
	}
	// An empty or misdirected glob must not read as "no enforcing migration".
	if _, ok := sources["../schema/baseline.go"]; !ok {
		t.Fatalf("the gate did not read the schema package (got %d files)", len(sources))
	}
	trigger := fleetEnforcementInSources(sources)
	kinds := ledger.ExecutableKinds()
	version := ""
	if len(kinds) != 0 || trigger {
		version = os.Getenv("DC_UPGRADE_TAG")
		if version == "" {
			// No exact tag is normal on a developer build. Baseline git failures
			// are independently fatal in the active gate below.
			version, _ = fleetGit("describe", "--tags", "--exact-match", "HEAD")
		}
	}
	err = checkFleetOperationFoundation(kinds, trigger, strings.TrimSpace(version), func() (string, error) {
		if tag := strings.TrimSpace(os.Getenv("DC_BASELINE_TAG")); tag != "" {
			return tag, nil
		}
		// The baseline is the newest stable release BEHIND this build: a tag that points at HEAD is
		// the release under test, and it must never witness its own foundation.
		tags, err := fleetGit("tag", "--merged", "HEAD", "-l", "v[0-9]*.[0-9]*.[0-9]*")
		if err != nil {
			return "", err
		}
		self, err := fleetGit("tag", "--points-at", "HEAD")
		if err != nil {
			return "", err
		}
		return newestFleetStableTag(withoutFleetTags(tags, self)), nil
	}, func(tag string) error {
		_, err := fleetGit("show", tag+":backend/services/device-management/schema/migration_device_attribute_lock.go")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fleetGit(args ...string) (string, error) {
	data, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, data)
	}
	return strings.TrimSpace(string(data)), nil
}

func fleetEnforcementInSources(sources map[string]string) bool {
	for name, source := range sources {
		if strings.HasSuffix(name, "_test.go") || filepath.Base(name) == "fleet_operation_readiness_constants.go" {
			continue
		}
		if strings.Contains(source, FleetOperationEnforcementTrigger) || strings.Contains(source, FleetOperationEnforcementFunction) ||
			strings.Contains(source, "FleetOperationEnforcementTrigger") || strings.Contains(source, "FleetOperationEnforcementFunction") {
			return true
		}
	}
	return false
}

func newestFleetStableTag(output string) string {
	var tags []string
	for _, tag := range strings.Fields(output) {
		if fleetStableTag.MatchString(tag) {
			tags = append(tags, tag)
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		a, b := fleetReleaseVersion.FindStringSubmatch(tags[i]), fleetReleaseVersion.FindStringSubmatch(tags[j])
		for n := 1; n <= 3; n++ {
			x, _ := strconv.Atoi(a[n])
			y, _ := strconv.Atoi(b[n])
			if x != y {
				return x < y
			}
		}
		return false
	})
	if len(tags) == 0 {
		return ""
	}
	return tags[len(tags)-1]
}

func checkFleetOperationFoundation(kinds []string, trigger bool, version string, baseline func() (string, error), witness func(string) error) error {
	if len(kinds) == 0 && !trigger {
		return nil
	}
	if v := fleetReleaseVersion.FindStringSubmatch(version); v != nil {
		patch, err := strconv.Atoi(v[3])
		if err != nil {
			return err
		}
		if patch > 0 {
			return fmt.Errorf("fleet operation activation cannot ship in patch release %s", version)
		}
	}
	tag, err := baseline()
	if err != nil {
		return fmt.Errorf("resolve fleet operation upgrade baseline: %w", err)
	}
	if tag == "" {
		return errors.New("fleet operation activation requires a baseline tag; fetch stable tags or set DC_BASELINE_TAG")
	}
	if err := witness(tag); err != nil {
		return fmt.Errorf("baseline %s lacks the device attribute writer protocol: %w", tag, err)
	}
	return nil
}

func TestFleetOperationFoundationSourcesAndTags(t *testing.T) {
	if fleetEnforcementInSources(map[string]string{
		"fleet_operation_readiness_constants.go": FleetOperationEnforcementFunction,
		"trigger_test.go":                        FleetOperationEnforcementTrigger,
	}) {
		t.Fatal("constants or tests activated enforcement")
	}
	for _, name := range []string{FleetOperationEnforcementTrigger, FleetOperationEnforcementFunction, "FleetOperationEnforcementTrigger", "FleetOperationEnforcementFunction"} {
		if !fleetEnforcementInSources(map[string]string{"migration_enforce.go": name}) {
			t.Fatal("enforcing migration did not activate the gate")
		}
	}
	if got := newestFleetStableTag("v1.9.2 v1.10.0 v1.11.0-rc.1 v1.8.0 nonsense"); got != "v1.10.0" {
		t.Fatalf("newest stable = %q", got)
	}
	if got := newestFleetStableTag("v1.11.0-rc.1"); got != "" {
		t.Fatalf("candidate used as baseline: %q", got)
	}
	err := checkFleetOperationFoundation([]string{"fake"}, false, "", func() (string, error) {
		return "", errors.New("git failed")
	}, func(string) error { t.Fatal("witness called after git failure"); return nil })
	if err == nil {
		t.Fatal("git failure passed activation")
	}
}

// withoutFleetTags drops the tags in exclude from the whitespace-separated list in tags.
func withoutFleetTags(tags, exclude string) string {
	drop := map[string]bool{}
	for _, t := range strings.Fields(exclude) {
		drop[t] = true
	}
	kept := []string{}
	for _, t := range strings.Fields(tags) {
		if !drop[t] {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " ")
}

// A release under test is never its own baseline.
func TestFleetBaselineExcludesTheReleaseUnderTest(t *testing.T) {
	if got := newestFleetStableTag(withoutFleetTags("v1.8.0 v1.9.0 v1.10.0", "v1.10.0")); got != "v1.9.0" {
		t.Fatalf("baseline = %q, want v1.9.0: the tag at HEAD must not be its own baseline", got)
	}
}
