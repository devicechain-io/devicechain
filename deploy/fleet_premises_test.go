// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-deploy/fleet/premises"
)

// fleetRequiredPremises are what the fleet roots (S1-6) need measured and HOLDING before they merge.
var fleetRequiredPremises = []string{"E-j-1", "E-j-2", "E-j-4"}

func onlyVersion(path, pattern string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(pattern).FindAllSubmatch(body, -1) {
		seen[string(m[1])] = true
	}
	if len(seen) != 1 {
		return "", errors.New(path + ": want exactly one distinct version for " + pattern)
	}
	for v := range seen {
		if !premises.IsExactVersion(v) {
			return "", errors.New(path + ": " + v + " is not one exact version")
		}
		return v, nil
	}
	return "", nil
}

// fleetPremiseProblems returns what is wrong with the recorded premises against the fleet roots'
// own pins (never deploy/gke's), or premises.ErrNoFleetRoots when there is nothing to compare yet.
func fleetPremiseProblems(rootsDir, recordPath, ciPath, kindPath string, now time.Time) ([]string, error) {
	f, err := os.Open(recordPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	record, err := premises.Parse(f)
	if err != nil {
		return nil, err
	}
	provider, err := premises.ProviderPinFromRoots(rootsDir)
	if err != nil {
		return nil, err
	}
	tofu, err := onlyVersion(ciPath, `tofu_version:\s*([^\s#]+)`)
	if err != nil {
		return nil, err
	}
	kube, err := onlyVersion(kindPath, `S0_KIND_NODE_IMAGE=kindest/node:v([^\s#@]+)`)
	if err != nil {
		return nil, err
	}
	var problems []string
	status := premises.Staleness(record, premises.Pins{Provider: provider, Tofu: tofu, Kubernetes: kube}, now)
	for _, id := range fleetRequiredPremises {
		if status[id] != premises.OK {
			problems = append(problems, id+" is "+string(status[id])+" against provider "+provider+", tofu "+tofu+", kubernetes "+kube+": re-run hack/fleet-s0 and commit the result")
		}
	}
	if err := record.RequireHolds(fleetRequiredPremises...); err != nil {
		problems = append(problems, err.Error())
	}
	return problems, nil
}

// TestFleetPremisesFresh is the scoped staleness gate: it turns the fleet gates red when a pin moves,
// and nothing else. It has nothing to compare until the fleet roots exist, and says so.
func TestFleetPremisesFresh(t *testing.T) {
	problems, err := fleetPremiseProblems("fleet", "fleet/premises/gke.json", "../.github/workflows/ci.yml", "../hack/fleet-s0/kind.sh", time.Now())
	if errors.Is(err, premises.ErrNoFleetRoots) {
		// Nothing to compare yet, which is a state to assert, not a test to skip: the gate starts
		// comparing the moment a deploy/fleet/gke-* root exists, so one existing here is a defect.
		roots, gerr := filepath.Glob("fleet/gke-*")
		if gerr != nil {
			t.Fatal(gerr)
		}
		if len(roots) != 0 {
			t.Fatalf("premises reported no fleet roots, but %v exist", roots)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The gate itself, against synthetic roots, with its negative control: it must go red when a pin moves.
func TestFleetPremiseGateRefusesMovedPins(t *testing.T) {
	dir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := func(provider string) {
		for _, name := range []string{"gke-gcs", "gke-pg"} {
			write(filepath.Join(dir, "fleet", name, "versions.tf"),
				"terraform {\n  required_providers {\n    google = {\n      source  = \"hashicorp/google\"\n      version = \""+provider+"\"\n    }\n  }\n}\n")
		}
	}
	ci := func(tofu string) { write(filepath.Join(dir, "ci.yml"), "      tofu_version: "+tofu+"\n") }
	// The real record's versions are the ones the roots must match.
	f, err := os.Open("fleet/premises/gke.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	record, err := premises.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	var p premises.Premise
	for _, e := range record.Experiments {
		if e.ID == "E-j-1" {
			p = e
		}
	}
	kind := filepath.Join(dir, "kind.sh")
	write(kind, "S0_KIND_NODE_IMAGE=kindest/node:v1.37.0\n")
	check := func() []string {
		t.Helper()
		problems, err := fleetPremiseProblems(filepath.Join(dir, "fleet"), "fleet/premises/gke.json", filepath.Join(dir, "ci.yml"), kind, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return problems
	}
	root(p.ProviderVersion)
	ci(p.TofuVersion)
	if problems := check(); len(problems) != 0 {
		t.Fatalf("pins equal to the record's were refused: %v", problems)
	}
	ci("1.10.11")
	if problems := check(); len(problems) == 0 || !strings.Contains(problems[0], "STALE") {
		t.Fatalf("a moved tofu pin did not turn the fleet gate red: %v", problems)
	}
	// A prerelease is a different version: it must not be read as the release it precedes.
	ci(p.TofuVersion + "-rc1")
	if problems := check(); len(problems) == 0 || !strings.Contains(problems[0], "STALE") {
		t.Fatalf("a prerelease tofu pin was read as the release: %v", problems)
	}
	ci(p.TofuVersion)
	root("9.9.9")
	// The three premises S1-6 needs bind the tofu version only, so a provider move reddens none of them:
	// scoped staleness, not a blanket gate.
	if problems := check(); len(problems) != 0 {
		t.Fatalf("a provider pin move reddened premises that do not bind the provider: %v", problems)
	}
	if _, err := fleetPremiseProblems(filepath.Join(dir, "nothing"), "fleet/premises/gke.json", filepath.Join(dir, "ci.yml"), kind, time.Now()); !errors.Is(err, premises.ErrNoFleetRoots) {
		t.Fatalf("missing roots must be reported: %v", err)
	}
}
