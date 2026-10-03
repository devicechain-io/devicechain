// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// 🔴 THE WIRE NAME IS SPELLED OUT, NOT TAKEN FROM THE CONSTANT. Every declaration a
// bootstrap has created carries this key, so renaming it would orphan each one of them —
// an unfinished instance would silently start reading as live — and a test that took the
// name from the constant would follow the rename and stay green.
const (
	unfinishedKey   = "core.devicechain.io/bootstrap-unfinished"
	unfinishedValue = "true"
)

// aDeclaration is a declaration as step 3 reads it: a phase, and the first-bootstrap
// record when unfinished is set (an empty string leaves the key out entirely).
func aDeclaration(phase, unfinished string) *dcv1beta1.Instance {
	inst := &dcv1beta1.Instance{}
	inst.Name = testInstance
	inst.Annotations = map[string]string{dcv1beta1.AnnotationPhase: phase}
	if unfinished != "" {
		inst.Annotations[unfinishedKey] = unfinished
	}
	return inst
}

// stubDeclarationErr makes the declaration read fail.
func stubDeclarationErr(t *testing.T, err error) {
	t.Helper()
	orig := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = orig })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return nil, err
	}
}

// 🔴 THE DEFECT. The configuration document is written at the START of the Helm step,
// before Helm runs, so a chart the API server rejects or a rollout that never becomes
// ready leaves the document behind over an instance that never served. Step 3 keyed on
// the document alone, refused every re-run as "already running", and left `dcctl destroy`
// as the only way out — while the docs said a re-run finishes a half-built instance.
func TestARerunFinishesAnInstanceWhoseFirstBootstrapNeverFinished(t *testing.T) {
	for _, phase := range []string{dcv1beta1.PhaseFailed, dcv1beta1.PhaseBootstrapping} {
		t.Run(phase, func(t *testing.T) {
			stubDeployedInstance(t, aLiveInstance())
			stubDeclaration(t, aDeclaration(phase, unfinishedValue))
			st := aBootstrapOf(testInstance)

			if err := stepRefuseRebuild(context.Background(), st); err != nil {
				t.Fatalf("a re-run over an instance whose first bootstrap never finished was refused, "+
					"which leaves destroy as the only way out: %v", err)
			}
			// It is BUILDING the instance, not running over a live one: the superuser seed
			// the dead run generated is still to be shown (resolveCredentials).
			if st.OverLiveInstance {
				t.Error("finishing a first bootstrap was recorded as a run over a live instance")
			}
		})
	}
}

// 🔴 THE NEGATIVE CONTROL: every shape of a LIVE instance is still refused, including
// the record beside Ready — which only a dcctl from before the record could leave, and
// which must read as live.
func TestALiveInstanceIsStillRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl *dcv1beta1.Instance
	}{
		{"no declaration at all", nil},
		{"Ready, no record", aDeclaration(dcv1beta1.PhaseReady, "")},
		{"Failed, no record (a failed upgrade of a live instance)", aDeclaration(dcv1beta1.PhaseFailed, "")},
		{"Upgrading, no record", aDeclaration(dcv1beta1.PhaseUpgrading, "")},
		{"Bootstrapping, no record (built by an earlier release)", aDeclaration(dcv1beta1.PhaseBootstrapping, "")},
		{"the record with another value", aDeclaration(dcv1beta1.PhaseFailed, "false")},
		{"the record beside Ready", aDeclaration(dcv1beta1.PhaseReady, unfinishedValue)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubDeployedInstance(t, aLiveInstance())
			stubDeclaration(t, tc.decl)
			err := stepRefuseRebuild(context.Background(), aBootstrapOf(testInstance))
			if err == nil {
				t.Fatal("a bootstrap aimed at a running instance was allowed to proceed and mint over it")
			}
			for _, want := range []string{
				"already running in this cluster",
				"dcctl upgrade local " + testInstance,
				"dcctl destroy local " + testInstance + ", then bootstrap again",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not contain %q: %v", want, err)
				}
			}
		})
	}
}

// 🔴 "COULD NOT TELL WHETHER ITS FIRST BOOTSTRAP FINISHED" MUST NOT READ AS "IT DID NOT".
// That is the direction that lets a run through over a live instance, so the read error
// stops the run, and says what it was trying to find out.
func TestAnUnreadableDeclarationStopsTheBootstrap(t *testing.T) {
	stubDeployedInstance(t, aLiveInstance())
	stubDeclarationErr(t, errors.New("the API server is not answering"))

	err := stepRefuseRebuild(context.Background(), aBootstrapOf(testInstance))
	// 🔴 THIS NIL CHECK IS THE ASSERTION THAT GUARDS THE DANGEROUS DIRECTION: an
	// unreadable declaration treated as unfinished is a run let through over a live
	// instance, and only an error here says it was stopped. Do not weaken it to a
	// message check. It is not sufficient alone — a read error swallowed into a nil
	// declaration falls through to the live-instance refusal, which is also an error —
	// so the message check below is what tells "stopped because unreadable" from
	// "stopped by accident".
	if err == nil {
		t.Fatal("a bootstrap proceeded past a declaration it could not read")
	}
	for _, want := range []string{"the API server is not answering", "first bootstrap finished"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not contain %q: %v", want, err)
		}
	}
}

// A rehearsal says what the real run would do, and over an unfinished instance that is
// finish it, not refuse.
func TestADryRunPredictsFinishingAnUnfinishedInstance(t *testing.T) {
	stubDeployedInstance(t, aLiveInstance())
	stubDeclaration(t, aDeclaration(dcv1beta1.PhaseFailed, unfinishedValue))
	st := aBootstrapOf(testInstance)
	st.DryRun = true

	out := captureStdout(t, func() {
		if err := stepRefuseRebuild(context.Background(), st); err != nil {
			t.Errorf("a rehearsal was refused: %v", err)
		}
	})
	if strings.Contains(out, "REFUSE") || !strings.Contains(out, "finish instance") {
		t.Errorf("the rehearsal does not predict what a real run would do:\n%s", out)
	}
	if st.OverLiveInstance {
		t.Error("the rehearsal recorded a run over a live instance")
	}
}

// 🔴 THE PASSWORD THE FAILED RUN NEVER SHOWED. The superuser's seed is generated, written
// to its Secret and only reported at the end — so a first bootstrap that died in the Helm
// step left a seed nobody has seen. The run that finishes it must show it, once, rather
// than treating it as a live instance's seed that may have changed since.
func TestFinishingABootstrapShowsTheSeedTheFailedRunNeverShowed(t *testing.T) {
	stubDeployedInstance(t, aLiveInstance())
	stubDeclaration(t, aDeclaration(dcv1beta1.PhaseFailed, unfinishedValue))
	rec := aCompleteInstall()
	st := &State{Instance: testInstance, InstanceUID: testUID, ClusterUID: testClusterUID, Values: map[string]string{}}
	FollowInstall(st, &rec)

	if err := stepRefuseRebuild(context.Background(), st); err != nil {
		t.Fatalf("the finishing run was refused: %v", err)
	}
	ref := superuserSecretRef(st.Instance)
	c := fake.NewSimpleClientset(mintedSecret(ref.Namespace, ref.Name, testUID, map[string]string{ref.Key: "the-seed"}))
	set, err := resolveCredentials(context.Background(), c, st, liveArchiveState{})
	if err != nil {
		t.Fatal(err)
	}
	if set.SuperuserPassword != "the-seed" || st.SuperuserSeed != superuserSeedRecovered {
		t.Fatalf("the seed the earlier run generated was not kept: settled %v", st.SuperuserSeed)
	}
	st.Credentials = set
	out := captureStdout(t, func() { printSuperuserReport(st) })
	if !strings.Contains(out, "the-seed") || !strings.Contains(out, "shown only this once") {
		t.Errorf("the finishing run does not show the password the failed run never reached:\n%s", out)
	}
}

// 🔴 READY CLEARS THE RECORD, IN THE SAME PATCH. Whichever verb ends successfully over an
// unfinished instance has made it live, and a record left standing beside it is a later
// bootstrap let through over a running instance.
func TestTheReadyStampRemovesTheRecordAndAFailedOneKeepsIt(t *testing.T) {
	for _, tc := range []struct {
		phase    string
		wantKept bool
	}{
		{dcv1beta1.PhaseReady, false},
		{dcv1beta1.PhaseFailed, true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			dyn := declaring(t, func() *dcv1beta1.Instance {
				inst := upgrading(t)
				inst.Annotations[unfinishedKey] = unfinishedValue
				return inst
			}())
			if err := setInstancePhase(t.Context(), dyn, "prod", tc.phase); err != nil {
				t.Fatal(err)
			}
			got := readBack(t, dyn, "prod").Annotations
			if got[dcv1beta1.AnnotationPhase] != tc.phase {
				t.Errorf("the phase reads %q, want %q", got[dcv1beta1.AnnotationPhase], tc.phase)
			}
			if v, kept := got[unfinishedKey]; kept != tc.wantKept {
				t.Errorf("after %s the record is %q (present=%v), want present=%v",
					tc.phase, v, kept, tc.wantKept)
			}
		})
	}
}

// 🔴 A SUCCESSFUL UPGRADE FINISHES AN UNFINISHED INSTANCE TOO, and must say so on the
// declaration. `dcctl upgrade` over a first bootstrap that failed in the Helm step runs the
// same Helm upgrade and readiness gate against the same document — it was the documented
// recovery — so its Ready must not leave the record standing over a live instance.
func TestAnUpgradeThatEndsWellClearsTheRecordAndOneThatFailsKeepsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		runErr   error
		wantKept bool
	}{
		{"success", nil, false},
		{"failure", errors.New("the services never rolled over"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := upgrading(t)
			inst.Annotations[unfinishedKey] = unfinishedValue
			dyn := declaring(t, inst)
			finishUpgradePhase(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"), nil, tc.runErr)

			if v, kept := readBack(t, dyn, "prod").Annotations[unfinishedKey]; kept != tc.wantKept {
				t.Errorf("after an upgrade %s the record is %q (present=%v), want present=%v",
					tc.name, v, kept, tc.wantKept)
			}
		})
	}
}

// 🔴 A RECORD BESIDE READY IS INERT, AND MOVING THE PHASE MUST NOT REVIVE IT. Only a dcctl
// from before the record can leave one there (it stamps Ready without knowing to clear it).
// The next upgrade writes Upgrading over it, and if the record survived that write a failed
// upgrade would leave Failed beside it — a live instance a later bootstrap is let through over.
func TestMovingAwayFromReadyDropsAnInertRecord(t *testing.T) {
	t.Run("the upgrade's Upgrading write", func(t *testing.T) {
		inst := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
		inst.Annotations[unfinishedKey] = unfinishedValue
		dyn := declaring(t, inst)
		if err := recordUpgradedVersion(t.Context(), dyn, "prod",
			upgradingTo("ghcr.io/devicechain-io", "v1.3.0")); err != nil {
			t.Fatal(err)
		}
		if v, kept := readBack(t, dyn, "prod").Annotations[unfinishedKey]; kept {
			t.Errorf("the record beside Ready survived the upgrade's write as %q", v)
		}
	})
	t.Run("a flagless upgrade's Failed stamp", func(t *testing.T) {
		inst := atVersion(t, "ghcr.io/devicechain-io", "v1.3.0")
		inst.Annotations[unfinishedKey] = unfinishedValue
		dyn := declaring(t, inst)
		finishUpgradePhase(t.Context(), dyn, "prod", upgradingTo("ghcr.io/devicechain-io", "v1.3.0"),
			nil, errors.New("the services never rolled over"))
		if v, kept := readBack(t, dyn, "prod").Annotations[unfinishedKey]; kept {
			t.Errorf("the record beside Ready survived a Failed stamp as %q", v)
		}
	})
}
