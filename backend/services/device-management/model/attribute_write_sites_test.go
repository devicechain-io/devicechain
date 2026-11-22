// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"
)

// Every attribute/generation write entrance, including generic destructive paths.
// The frozen dedupe migration predates the protocol and is a named exception.
var attributeWriteSites = []struct {
	Name, Function, Exception string
}{
	{"setEntityAttribute", "Api.SetEntityAttribute", ""},
	{"deleteEntityAttribute", "Api.DeleteEntityAttribute", ""},
	{"restore", "Api.RestoreDeviceConfiguration", ""},
	{"copy", "Api.CopyDeviceConfiguration", ""},
	{"facet import", "Api.ImportFacetValues", ""},
	{"repair", "Api.repairAttribute", "locks and recomputes the historic dedupe result"},
	{"device delete", "Api.DeleteDevice", "hard cascade under the device lock"},
	{"tenant purge", "purge.Relational.Erase", "destructive marker without per-device locks"},
	{"orphan sweep", "Api.SweepDeviceAttributeOrphans", "the device no longer exists"},
	{"dedupe migration", "schema.NewEntityAttributeUniqueKeySchema", "historic 20261201090000; runs before protocol activation"},
}

func checkAttributeWriteSiteCoverage(exercised map[string]bool) error {
	for _, site := range attributeWriteSites {
		if !exercised[site.Name] {
			return fmt.Errorf("attribute write site %q (%s) was not exercised", site.Name, site.Function)
		}
	}
	return nil
}

func TestAttributeWriteSiteCompleteness(t *testing.T) {
	covered := map[string]bool{}
	for _, site := range attributeWriteSites {
		covered[site.Name] = true
	}
	if err := checkAttributeWriteSiteCoverage(covered); err != nil {
		t.Fatal(err)
	}
	delete(covered, "tenant purge")
	if err := checkAttributeWriteSiteCoverage(covered); err == nil {
		t.Fatal("missing purge entrance escaped the completeness check")
	}
}
