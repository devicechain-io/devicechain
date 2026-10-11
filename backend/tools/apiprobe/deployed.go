// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

// WHICH AREAS THE INSTANCE ACTUALLY RUNS.
//
// The coverage table is built from the working tree, so it names every area the release
// under test ships. The instance the upgrade drill runs on was installed by the BASELINE
// release's dcctl, and `dcctl upgrade` neither adds an area nor configures an object
// store, so an area that came into existence after the baseline (update-management) is
// not deployed on it and cannot be made so by anything an operator types. Probing it
// would fail on a connection to a route nothing serves, and that failure would read as a
// defect in the release.
//
// So the drill states the areas the instance runs, and everything else is skipped BY
// NAME, loudly, in every phase that would have touched it. It is never skipped quietly,
// and the table sweep checks the other direction too: an area declared undeployed must
// have no tenant-scoped table in the database, so a rig that excluded an area which is in
// fact running fails instead of measuring less than it claims.

// deployedAreas is the set of areas the instance runs; nil means every area (the
// default, and what a fresh install of the current tree is).
type deployedAreas map[string]bool

// bindDeployedAreas registers --deployed-areas on fs and returns the parse function to
// call after fs.Parse.
func bindDeployedAreas(fs *flag.FlagSet) func() (deployedAreas, error) {
	var raw string
	fs.StringVar(&raw, "deployed-areas", "",
		"comma-separated functional areas the instance actually runs; entities and reads of any other "+
			"probe area are skipped by name. Unset means every area.")
	return func() (deployedAreas, error) { return parseDeployedAreas(raw) }
}

func parseDeployedAreas(raw string) (deployedAreas, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	known := map[string]bool{}
	for _, a := range probeAreas() {
		known[a] = true
	}
	out := deployedAreas{}
	for _, a := range strings.Split(raw, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// A typo would otherwise exclude a real area without a word.
		if !known[a] {
			return nil, failWith(exitSetup, "--deployed-areas names %q, which is not a probe area (%s)",
				a, strings.Join(probeAreas(), ", "))
		}
		out[a] = true
	}
	if len(out) == 0 {
		return nil, failWith(exitSetup, "--deployed-areas named no areas; an instance that runs nothing has nothing to probe")
	}
	return out, nil
}

// has reports whether the area is deployed. A nil set deploys everything.
func (d deployedAreas) has(area string) bool { return d == nil || d[area] }

// undeployed lists the probe areas this set excludes, sorted.
func (d deployedAreas) undeployed() []string {
	if d == nil {
		return nil
	}
	var out []string
	for _, a := range probeAreas() {
		if !d[a] {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// notDeployedReason is the sentence printed beside every skipped item.
func notDeployedReason(area string) string {
	return fmt.Sprintf("the %s area is not deployed on this instance (the release it was installed from does not ship it)", area)
}

// tablesOfUndeployedAreas returns the tenant-scoped tables (`schema.table`) that live in
// an area declared undeployed. Each is a contradiction: the area has a schema, so it ran.
func tablesOfUndeployedAreas(tables []string, d deployedAreas) []string {
	if d == nil {
		return nil
	}
	var bad []string
	for _, t := range tables {
		area, _, ok := strings.Cut(t, ".")
		if !ok {
			continue
		}
		for _, u := range d.undeployed() {
			if area == u {
				bad = append(bad, t)
			}
		}
	}
	sort.Strings(bad)
	return bad
}
