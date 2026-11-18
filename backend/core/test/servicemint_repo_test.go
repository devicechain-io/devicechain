// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Read through the existing copy-returning API; no mutable auth map is exported.
func serviceMintCeiling() map[string][]string {
	ceiling := map[string][]string{}
	for _, caller := range auth.ServiceCallerNames() {
		grants, _ := auth.ServiceCeiling(caller)
		for _, grant := range grants {
			ceiling[caller] = append(ceiling[caller], string(grant))
		}
	}
	return ceiling
}

func TestServiceMintsMatchTheCeiling(t *testing.T) {
	sites, err := serviceMintSitesUnder(workspaceRoot(t), serviceMintForwarders)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, site := range sites {
		counts[site.Kind]++
	}
	t.Logf("mint scan: %v", counts)
	for kind, floor := range map[string]int{"direct": 39, "forwarder": 10, "area": 13} {
		if counts[kind] < floor {
			t.Errorf("%s sites: got %d, minimum %d", kind, counts[kind], floor)
		}
	}
	if problems := checkMintSites(sites, serviceMintCeiling()); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}
