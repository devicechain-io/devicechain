// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "testing"

func TestHarborpulseS7DeclaresStableTenantWitnessAndControls(t *testing.T) {
	s := NewHarborpulse(1, Load{}).(*harborpulse)
	if err := s.ConfigureScenario(ScenarioOptions{Defects: []string{HarborpulseDefectIsolationCrossRead, HarborpulseDefectMCPWrongAnswer}}); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, profile := range s.Manifest().Profiles {
		for _, metric := range profile.Metrics {
			if metric.Key == HarborpulseTenantMarkerKey {
				if profile.Token != HarborpulsePumpProfile || metric.DataType != "DOUBLE" {
					t.Fatal("witness on wrong profile or type")
				}
				found++
			}
		}
	}
	if found != 1 {
		t.Fatalf("tenant witness declarations=%d", found)
	}
	if HarborpulseTenantMarker("a") != HarborpulseTenantMarker("a") || HarborpulseTenantMarker("a") == HarborpulseTenantMarker("b") {
		t.Fatal("tenant witnesses are not stable and distinguishable")
	}
	if HarborpulseTenantDescription("a") == HarborpulseTenantDescription("b") {
		t.Fatal("inventory witnesses collide")
	}
}
