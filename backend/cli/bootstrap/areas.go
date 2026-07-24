// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"

	"github.com/devicechain-io/dc-k8s/functionalarea"
)

// ResolveEnabledAreas expands the base profile into its concrete functional-area
// set and unions in the extra areas requested via --enable-area, then validates the
// result (unknown area, missing core, unmet hard dependency) using the SAME catalog
// the operator and the chart's `devicechain.enabledAreas` guard mirror. It returns
// the enabled set as strings for the chart's `enabledFunctionalAreas` values key.
//
// The returned set is deployment-selection-equivalent to `profile` PLUS the extras,
// which is why helmValues emits it in place of `profile`: the two are mutually
// exclusive in the chart (_helpers.tpl), so an additive selection has to be expressed
// as one explicit set rather than profile + a delta. The base profile's areas are
// preserved verbatim (compact keeps `default`), so this is purely additive — it never
// drops an area the profile would have deployed.
//
// It returns (nil, nil) when no extras are requested, leaving the untouched
// profile-based path (helmValues emits `profile`) exactly as it was.
func ResolveEnabledAreas(profile string, extra []string) ([]string, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	// ResolveEnabled with a nil explicit set expands the profile (or the default
	// profile when profile is empty). Extras are additive, so they are unioned on
	// top rather than passed as `explicit` (which would trip the profile/explicit
	// mutual-exclusion check for no reason).
	base, err := functionalarea.ResolveEnabled(profile, nil)
	if err != nil {
		return nil, err
	}

	seen := make(map[functionalarea.FunctionalArea]bool, len(base)+len(extra))
	enabled := make([]functionalarea.FunctionalArea, 0, len(base)+len(extra))
	add := func(a functionalarea.FunctionalArea) {
		if a == "" || seen[a] {
			return
		}
		seen[a] = true
		enabled = append(enabled, a)
	}
	for _, a := range base {
		add(a)
	}
	for _, e := range extra {
		add(functionalarea.FunctionalArea(strings.TrimSpace(e)))
	}

	// Validate the UNION (not just the extras) so an extra that is unknown, or that
	// pulls in an unmet hard dependency, fails here — before any cluster spin-up —
	// rather than at chart render (a 10-minute helm timeout, helmTimeout).
	if err := functionalarea.Validate(enabled); err != nil {
		return nil, err
	}

	out := make([]string, len(enabled))
	for i, a := range enabled {
		out[i] = string(a)
	}
	return out, nil
}
