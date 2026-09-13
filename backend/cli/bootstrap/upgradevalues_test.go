// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"
)

// A release as the previous install would have recorded it: monitoring wired to the
// namespaces an infrastructure apply reported, an SSO client seeded with the hash of
// a secret whose cleartext went to Grafana, and a provisioned device credential.
func aPreviousRelease() map[string]interface{} {
	return map[string]interface{}{
		"metrics": map[string]interface{}{
			"enabled":           true,
			"databaseBackups":   true,
			"databaseNamespace": "dc-system",
			"cnpgNamespace":     "cnpg-system",
		},
		"extraSecrets": []interface{}{
			map[string]interface{}{"name": "dci-devicechain-lwm2m-psk"},
		},
		"functionalAreas": map[string]interface{}{
			"user-management": map[string]interface{}{
				"config": map[string]interface{}{
					"auth": map[string]interface{}{
						"issuerUrl": "https://devicechain.local",
						"seedClients": []interface{}{
							map[string]interface{}{
								"clientId":   "grafana",
								"secretHash": "$2a$10$thehashgrafanasecretmapsto",
							},
						},
					},
				},
			},
			"lwm2m-ingest": map[string]interface{}{
				"config": map[string]interface{}{
					"security": map[string]interface{}{
						"identities": []interface{}{
							map[string]interface{}{"identity": "sensor-1"},
						},
					},
				},
			},
		},
	}
}

// 🔴 THE ALERTS AN INSTANCE HAD MUST SURVIVE A VERSION BUMP. These three values came
// out of an infrastructure apply that this verb does not run, so recomputing them
// yields their zero values — and every zero value here renders cleanly as an instance
// that simply is not monitored. No error, no warning, four alerts that stop existing.
func TestAnUpgradeKeepsTheDatabaseMonitoringTheInstanceHad(t *testing.T) {
	st := &State{Values: map[string]string{}}
	carryForwardFromRelease(st, aPreviousRelease())

	for key, want := range map[string]string{
		databaseBackupsKey:   "true",
		databaseNamespaceKey: "dc-system",
		cnpgNamespaceKey:     "cnpg-system",
	} {
		if got := st.Values[key]; got != want {
			t.Errorf("%s came across as %q, want %q: an upgrade would silently drop the rules "+
				"this value gates", key, got, want)
		}
	}
}

// ...and the counterweight, because "carry everything" is only safe while it cannot
// invent anything. An instance installed without monitoring has no such values, and
// manufacturing them would render rules against series that do not exist.
func TestAnUpgradeDoesNotInventMonitoringTheInstanceNeverHad(t *testing.T) {
	st := &State{Values: map[string]string{}}
	carryForwardFromRelease(st, map[string]interface{}{})

	for _, key := range []string{databaseBackupsKey, databaseNamespaceKey, cnpgNamespaceKey} {
		if got, ok := st.Values[key]; ok {
			t.Errorf("%s was invented as %q for an instance whose release records none", key, got)
		}
	}
}

// 🔴 THE ONE CREDENTIAL THAT CANNOT BE RE-MINTED, BECAUSE ONLY ONE HALF OF IT IS
// RECOVERABLE. The cleartext went to the Grafana subchart and nothing else keeps it;
// user-management holds the hash. Minting a fresh pair on an upgrade would update
// user-management alone and break the login permanently — the failure is silent until
// somebody tries to sign in.
func TestTheGrafanaClientHashIsCarriedRatherThanReminted(t *testing.T) {
	st := &State{Values: map[string]string{}}
	carryForwardFromRelease(st, aPreviousRelease())

	if got := st.Values["grafanaOAuthSecretBcrypt"]; got != "$2a$10$thehashgrafanasecretmapsto" {
		t.Errorf("the seeded Grafana client's hash did not come across (%q), so an upgrade "+
			"would seed a client whose secret Grafana does not hold", got)
	}
}

// The hash is found by client id, not by position. A release that seeds more than one
// client is ordinary, and taking the first entry would carry some other client's hash
// into Grafana's seat.
func TestTheGrafanaHashIsFoundByClientRatherThanByPosition(t *testing.T) {
	previous := aPreviousRelease()
	auth := previous["functionalAreas"].(map[string]interface{})["user-management"].(map[string]interface{})["config"].(map[string]interface{})["auth"].(map[string]interface{})
	auth["seedClients"] = []interface{}{
		map[string]interface{}{"clientId": "something-else", "secretHash": "not-grafanas"},
		map[string]interface{}{"clientId": "grafana", "secretHash": "grafanas"},
	}

	if got := seededGrafanaClientHash(previous); got != "grafanas" {
		t.Errorf("picked %q: an upgrade would seed Grafana with another client's secret", got)
	}
}

// 🔴 A VERSION BUMP MUST NOT UNPROVISION EVERY DEVICE. The LwM2M pre-shared keys come
// from a file supplied once at bootstrap, and nothing records that file. Recomputing
// the values without it does not leave the Secret alone — it takes the Secret out of
// the manifest, and Helm deletes what leaves the manifest.
func TestAnUpgradeKeepsTheProvisionedDeviceCredentials(t *testing.T) {
	vals := map[string]interface{}{"image": map[string]interface{}{"tag": "v2"}}
	carryReleaseValues(vals, aPreviousRelease())

	if _, ok := vals["extraSecrets"]; !ok {
		t.Error("the provisioned pre-shared keys left the manifest, so Helm would delete the " +
			"Secret every LwM2M device authenticates with")
	}
	areas, _ := vals["functionalAreas"].(map[string]interface{})
	if _, ok := areas["lwm2m-ingest"]; !ok {
		t.Error("the LwM2M area lost its identity bindings, so the keys that survived would " +
			"no longer be bound to anything")
	}
}

// ...and it must not overwrite what this run actually decided. An operator who
// supplied the identities file again means the newer answer; taking the release's
// copy over it would make the flag inert.
func TestWhatThisRunDecidedWinsOverWhatTheReleaseHeld(t *testing.T) {
	vals := map[string]interface{}{
		"extraSecrets": []interface{}{map[string]interface{}{"name": "the-new-one"}},
		"functionalAreas": map[string]interface{}{
			"lwm2m-ingest": map[string]interface{}{"config": "the-new-one"},
		},
	}
	carryReleaseValues(vals, aPreviousRelease())

	secrets, _ := vals["extraSecrets"].([]interface{})
	first, _ := secrets[0].(map[string]interface{})
	if first["name"] != "the-new-one" {
		t.Error("the carried block displaced the one this run computed, which would make " +
			"supplying the identities file have no effect")
	}
	areas, _ := vals["functionalAreas"].(map[string]interface{})
	if lwm2m, _ := areas["lwm2m-ingest"].(map[string]interface{}); lwm2m["config"] != "the-new-one" {
		t.Error("the carried area config displaced the one this run computed")
	}
}

// Carrying must not disturb a sibling it was not asked about. The functional-areas
// block holds every area's configuration, and a carry that replaced the map rather
// than reaching into it would take the rest of the instance's areas with it.
func TestCarryingOneAreaLeavesTheOthersAlone(t *testing.T) {
	vals := map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"user-management": map[string]interface{}{"config": "computed-this-run"},
		},
	}
	carryReleaseValues(vals, aPreviousRelease())

	areas, _ := vals["functionalAreas"].(map[string]interface{})
	um, _ := areas["user-management"].(map[string]interface{})
	if um["config"] != "computed-this-run" {
		t.Errorf("carrying the LwM2M area displaced user-management's configuration (%v)", areas)
	}
	if _, ok := areas["lwm2m-ingest"]; !ok {
		t.Error("the carried area did not land alongside it")
	}
}

// A release with nothing to carry must not leave empty scaffolding behind. An empty
// functionalAreas map is not the same as none, and the chart's schema is entitled to
// tell the difference.
func TestNothingIsCarriedFromAReleaseThatHeldNothing(t *testing.T) {
	vals := map[string]interface{}{}
	carryReleaseValues(vals, map[string]interface{}{})

	if len(vals) != 0 {
		t.Errorf("carrying from an empty release produced %v", vals)
	}
}
