// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/types"
)

// upgradingTo is the State an upgrade reaches recordUpgradedVersion with: the
// image source already settled by applyUpgradeDeclaration from the declaration
// plus any flags.
func upgradingTo(registry, version string) *State {
	return &State{
		Instance:      "prod",
		ImageRegistry: registry,
		ImageVersion:  version,
		DcctlVersion:  "v0.17.0",
		Values:        map[string]string{},
	}
}

// atVersion puts a declaration in the cluster recording the images it was built on.
func atVersion(t *testing.T, registry, version string) *dcv1beta1.Instance {
	t.Helper()
	inst := &dcv1beta1.Instance{}
	inst.Name = "prod"
	inst.Spec = dcv1beta1.InstanceSpec{
		Provider: "local", Cluster: "kind-devicechain", Managed: true,
		Profile: "default", Monitoring: true, CNPG: true, TLS: true,
		Host:          DefaultIngressHost,
		ImageRegistry: registry, ImageVersion: version,
	}
	// The UID is what separates this instance from a previous one of the same name,
	// and applyUpgradeDeclaration refuses a declaration without one — so a fixture
	// lacking it would fail for a reason that has nothing to do with the version.
	inst.SetUID(types.UID(testUID))
	setPhase(inst, dcv1beta1.PhaseReady)
	addFinalizer(inst)
	return inst
}

// 🔴 THE SILENT ROLLBACK, AND IT IS THE TEST THAT WOULD HAVE CAUGHT IT.
//
// WriteInstanceCR had exactly one caller — bootstrap's claim step — so an upgrade
// moved every image and left the declaration naming the version the instance was
// BUILT on. applyUpgradeDeclaration takes that declaration as the default and the
// flags as an override, so the next `dcctl upgrade` with no --version read the old
// value and rolled every service and the operator BACKWARDS, reporting success and
// correctly reporting that no credential had rotated.
//
// 🔑 This test is written as the two-run sequence rather than as "the CR was
// updated", because the single-run assertion is the weaker claim: it passes on a
// write that records the wrong field, and it does not describe the failure anyone
// would actually see. What matters is that the SECOND run inherits the version the
// FIRST one moved to.
func TestASecondUpgradeInheritsTheVersionTheFirstOneMovedTo(t *testing.T) {
	obj, err := instanceToUnstructured(atVersion(t, "ghcr.io/devicechain-io", "v1.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	dyn := declarationClient(obj)

	// Run one: an explicit --version, the way an operator upgrades.
	first := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", first); err != nil {
		t.Fatalf("recording the version this upgrade moved to: %v", err)
	}

	// Run two: no flags at all, so every image comes from the declaration.
	inst := readBack(t, dyn, "prod")
	st := &State{Instance: "prod", Values: map[string]string{}}
	if err := applyUpgradeDeclaration(st, inst, UpgradeOptions{}); err != nil {
		t.Fatalf("settling what the declaration says: %v", err)
	}

	if st.ImageVersion != "v1.3.0" {
		t.Errorf("a flagless upgrade resolved %q, so it would roll every service and the "+
			"operator back to the version this instance was BUILT on rather than the one it "+
			"is running — reporting success while doing it", st.ImageVersion)
	}
	if st.ImageRegistry != "ghcr.io/devicechain-io" {
		t.Errorf("the registry did not survive: %q", st.ImageRegistry)
	}
}

// The counterweight: recording the new version must not disturb anything else in
// the declaration. An upgrade moves a version, not an instance's shape — and the
// immutable half of the spec would refuse the write outright, which is a failure
// mode worth seeing here rather than on a cluster.
func TestRecordingAVersionLeavesTheRestOfTheDeclarationAlone(t *testing.T) {
	before := atVersion(t, "localhost:5000", "my-build")
	before.Spec.HA = true
	before.Spec.ExtraFunctionalAreas = []string{"lwm2m-ingest"}
	obj, err := instanceToUnstructured(before)
	if err != nil {
		t.Fatal(err)
	}
	dyn := declarationClient(obj)

	if err := recordUpgradedVersion(t.Context(), dyn, "prod", upgradingTo("localhost:5000", "my-build-2")); err != nil {
		t.Fatalf("recording the version: %v", err)
	}

	after := readBack(t, dyn, "prod")
	if after.Spec.ImageVersion != "my-build-2" {
		t.Errorf("the version was not recorded: %q", after.Spec.ImageVersion)
	}
	if !after.Spec.HA || after.Spec.Provider != "local" || after.Spec.Cluster != "kind-devicechain" {
		t.Errorf("the declaration's shape moved: ha=%v provider=%q cluster=%q",
			after.Spec.HA, after.Spec.Provider, after.Spec.Cluster)
	}
	if len(after.Spec.ExtraFunctionalAreas) != 1 || after.Spec.ExtraFunctionalAreas[0] != "lwm2m-ingest" {
		t.Errorf("the enabled areas moved: %v", after.Spec.ExtraFunctionalAreas)
	}
}

// A dry run must not write. It is the whole contract of --dry-run, and this write
// happens before the operator apply — early enough that a rehearsal reaching it
// would leave the declaration claiming a version nothing ever deployed.
func TestADryRunRecordsNothing(t *testing.T) {
	obj, err := instanceToUnstructured(atVersion(t, "ghcr.io/devicechain-io", "v1.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	dyn := declarationClient(obj)

	st := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
	st.DryRun = true
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatalf("a dry run reported an error: %v", err)
	}

	if got := readBack(t, dyn, "prod").Spec.ImageVersion; got != "v1.2.0" {
		t.Errorf("a dry run moved the declaration to %q", got)
	}
}

// 🔴 A DECLARATION THAT VANISHED IS NOT AN EMPTY ONE. hydrateUpgradeState already
// refused a missing declaration, so reaching this function with none means somebody
// deleted it between the two reads. Recreating it here would resurrect an instance
// record somebody had just removed, from a spec this verb never authored.
func TestAVanishedDeclarationIsRefusedRatherThanRecreated(t *testing.T) {
	dyn := declarationClient()

	err := recordUpgradedVersion(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"))
	if err == nil {
		t.Fatal("a missing declaration was accepted, so this upgrade would have written a new one")
	}
	if readBack(t, dyn, "prod") != nil {
		t.Error("a declaration was created for an instance that had none")
	}
}
