// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// The release name carries no instance, so `destroy` has to ask the release itself
// whose it is. These tests pin the two halves of that: reading the id back out of a
// rendered value map, and deciding what to do with the answer.
//
// 🔴 The defect they exist for was measured on a live cluster: `dcctl destroy local b
// --keep-cluster`, for an instance that had never been installed, uninstalled instance
// "a"'s release — cascade-deleting its namespace and all ten of its deployments — and
// printed a success line.

func TestUninstallRefusesAReleaseBelongingToAnotherInstance(t *testing.T) {
	err := uninstallRefusalReason("a", "b")
	if err == nil {
		t.Fatal("destroying instance \"b\" against a cluster running instance \"a\" was allowed; " +
			"this is the live data-loss path and it must refuse")
	}
}

// 🔴 THE NEGATIVE CONTROL, AND IT IS THE POINT OF THE PAIR. A refusal that refuses
// everything would pass the test above while making `dcctl destroy` useless — and the
// only way anyone would find out is by running it on a real instance.
func TestUninstallProceedsForTheInstanceThatOwnsTheRelease(t *testing.T) {
	if err := uninstallRefusalReason("a", "a"); err != nil {
		t.Fatalf("destroying instance %q against the cluster running it was refused: %v", "a", err)
	}
}

// The message is what an operator acts on, and the two names are the whole content:
// without the owner they cannot tell which instance is actually there, and without
// their own they cannot tell whether they mistyped. Asserting the REASON rather than
// that there is one — a refusal can be killed by the wrong test.
func TestTheRefusalNamesBothTheOwnerAndTheInstanceAsked(t *testing.T) {
	err := uninstallRefusalReason("production", "staging")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"production", "staging"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it cannot be acted on: %v", want, err)
		}
	}
}

func TestTheInstanceIsReadFromTheReleaseValues(t *testing.T) {
	id, present, err := instanceIDFromValues(map[string]interface{}{
		"instance": map[string]interface{}{"id": "a", "createNamespace": true},
	})
	if err != nil {
		t.Fatalf("a well-formed value map was not readable: %v", err)
	}
	if !present || id != "a" {
		t.Fatalf("got (%q, %v), want (\"a\", true)", id, present)
	}
}

// 🔴 MALFORMED IS NOT ABSENT, AND ABSENT IS NOT "ANYBODY'S". Every shape below is a
// release that exists and cannot be attributed. Returning "" for any of them would let
// the caller compare "" against the requested instance, and an instance named "" is not
// a thing anyone can bootstrap — so the comparison would refuse. That happens to be
// safe TODAY, which is exactly why it must be an error instead: the safety would be an
// accident of the caller's shape, and it would evaporate the moment someone read the
// empty string as "unowned, go ahead".
func TestAnUnattributableReleaseIsRefusedRatherThanGuessed(t *testing.T) {
	for name, vals := range map[string]map[string]interface{}{
		"no instance key":    {"ingress": map[string]interface{}{"enabled": true}},
		"instance not a map": {"instance": "a"},
		"no id":              {"instance": map[string]interface{}{"createNamespace": true}},
		"id not a string":    {"instance": map[string]interface{}{"id": 7}},
		"empty id":           {"instance": map[string]interface{}{"id": ""}},
		"nil values":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			id, present, err := instanceIDFromValues(vals)
			if err == nil {
				t.Fatalf("an unattributable release was accepted as instance %q (present=%v); "+
					"the caller would then decide whether to delete it from a value this "+
					"function invented", id, present)
			}
		})
	}
}

// The chart defaults instance.id to "devicechain" (deploy/helm/devicechain/values.yaml),
// so a release installed by a plain `helm install` — still a documented path — carries
// that id in its COMPUTED values while supplying none of its own. releaseInstance reads
// the computed half for exactly this reason; this pins the consequence, which is that
// such a release is attributable and is protected from a differently-named destroy.
func TestAChartDefaultedInstanceIsStillAttributable(t *testing.T) {
	id, present, err := instanceIDFromValues(map[string]interface{}{
		"instance": map[string]interface{}{"id": "devicechain"},
	})
	if err != nil || !present || id != "devicechain" {
		t.Fatalf("got (%q, %v, %v), want (\"devicechain\", true, nil)", id, present, err)
	}
	if err := uninstallRefusalReason(id, "somethingelse"); err == nil {
		t.Fatal("a chart-defaulted release was not protected from a destroy naming another instance")
	}
}
