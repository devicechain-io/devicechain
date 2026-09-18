// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// 🔴 THE SAME HOLE TestTheRestorePlanReachesTheStateAnInstallAppliesFrom EXISTS FOR.
// installState is a struct literal that no test reaches through Install — Install
// needs a provider and a live cluster — and a literal is exactly where a settled
// option gets dropped.
//
// Dropped here, `dcctl install --version v0.17.0 --registry ghcr.io/example` installs
// the operator from dcctl's compiled-in defaults instead, and says so nowhere: the
// apply is green, the controller runs, and the cluster is carrying a release nobody
// chose. On the --build path the failure is louder but no better — an empty registry
// makes requireResolvedImages refuse a cluster that has already been created.
func TestTheImageSourceReachesTheStateTheOperatorIsInstalledFrom(t *testing.T) {
	st := installState(ClusterBinding{KubeContext: "kind-devicechain"}, "local", InstallOptions{
		Options: Options{
			ImageRegistry: "ghcr.io/example",
			ImageVersion:  "v0.17.0",
			BuildImages:   true,
		},
	})

	if st.ImageRegistry != "ghcr.io/example" || st.ImageVersion != "v0.17.0" {
		t.Errorf("the install would deploy the operator from %q at %q, not the source it was given",
			st.ImageRegistry, st.ImageVersion)
	}
	if !st.BuildImages {
		t.Error("--build was dropped, so the install would pull a published operator image " +
			"instead of the one it was asked to build, and never build it")
	}
	if got, want := operatorImageRef(st), "ghcr.io/example/operator:v0.17.0"; got != want {
		t.Errorf("the operator image resolved to %q, want %q", got, want)
	}
}

// 🔴 THE OPERATOR'S VERSION MUST NOT BECOME A RECORDED CLUSTER SETTING, and this is
// the counterweight to the whole slice rather than a tidiness check.
//
// refuseAReinstallThatWouldHurt compares this run's InstallSettings against the last
// completed install's and refuses a CHANGE while instances are running. Moving the
// operator forward is now done by re-running `dcctl install` at a new --version — so
// the moment the version lands in InstallSettings, the one command that exists to
// move it is refused on every cluster that has an instance on it, which is every
// cluster anyone would want to move.
//
// Recording it somewhere is still worth doing one day; InstallSettings is the one
// place it must not be, because this field set is a LOCK, not a log.
func TestTheOperatorVersionIsNotRecordedAsAClusterSetting(t *testing.T) {
	atOld, _ := reinstallState()
	atOld.ImageRegistry, atOld.ImageVersion = "ghcr.io/devicechain-io", "v0.16.0"

	atNew, _ := reinstallState()
	atNew.ImageRegistry, atNew.ImageVersion = "ghcr.io/devicechain-io", "v0.17.0"

	if installSettingsFor(atOld) != installSettingsFor(atNew) {
		t.Fatalf("moving the operator's version changed the cluster's recorded settings:\n"+
			"  at v0.16.0 %+v\n  at v0.17.0 %+v\n"+
			"An install that moves the operator would then read as a settings CHANGE and be "+
			"refused under the instances it exists to serve, leaving no way to move it at all.",
			installSettingsFor(atOld), installSettingsFor(atNew))
	}
}

// 🔴 A BUILD IS THE DEVELOPER PATH, AND ON EVERY OTHER PATH IT MUST DO NOTHING —
// not "must succeed", MUST NOT LOOK.
//
// buildOperatorImageForInstall runs before the operator is applied on every
// install, published ones included, and behind the guard it reaches for a source
// checkout, starts a registry container and writes a ConfigMap to whatever cluster
// the current context names. So a nil return proves nothing on a developer's
// machine, where all of that would succeed: this asserts the guard by pointing the
// run at a kube context that does not exist. Past the guard, ensureLocalRegistry
// must fail on it; returning nil means it never got that far.
func TestAPublishedInstallBuildsNothing(t *testing.T) {
	st := &State{
		ImageRegistry: "ghcr.io/devicechain-io",
		ImageVersion:  "v0.17.0",
		KubeContext:   "no-such-context-" + t.Name(),
	}
	if err := buildOperatorImageForInstall(t.Context(), st); err != nil {
		t.Fatalf("an install that was not asked to build anything tried to: %v", err)
	}
}
