// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The constant is the wire name the field holds; bootstrap_unfinished_test.go pins it.
func TestTheRecordsWireNameIsThePinnedOne(t *testing.T) {
	if dcv1beta1.AnnotationBootstrapUnfinished != unfinishedKey || dcv1beta1.BootstrapUnfinished != unfinishedValue {
		t.Fatalf("the record is %s=%q; every declaration in the field carries %s=%q",
			dcv1beta1.AnnotationBootstrapUnfinished, dcv1beta1.BootstrapUnfinished, unfinishedKey, unfinishedValue)
	}
}

// recordOf reads the first-bootstrap record off the declaration in the cluster.
func recordOf(t *testing.T, dyn *dynamicfake.FakeDynamicClient, id string) (string, bool) {
	t.Helper()
	inst := readBack(t, dyn, id)
	if inst == nil {
		t.Fatal("the declaration is gone")
	}
	v, ok := inst.Annotations[unfinishedKey]
	return v, ok
}

// 🔴 WHO MARKS, AND WHO DOES NOT. The bootstrap marks a declaration it is BUILDING
// (created or not) and clears the mark on one it is running over live; the upgrade's
// write carries whatever is there, except that leaving Ready drops an inert one.
func TestEachDeclarationWriteDoesTheRightThingWithTheRecord(t *testing.T) {
	withRecord := func(phase string) func(*dcv1beta1.Instance) {
		return func(inst *dcv1beta1.Instance) {
			setPhase(inst, phase)
			inst.Annotations[unfinishedKey] = unfinishedValue
		}
	}
	for _, tc := range []struct {
		name     string
		existing func(*dcv1beta1.Instance) // nil: no declaration yet
		write    func(dyn *dynamicfake.FakeDynamicClient) error
		want     string // "" means absent
	}{
		{"a bootstrap that creates the declaration marks it", nil,
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseBootstrapping, markUnfinished)
			}, unfinishedValue},
		{"a bootstrap building over an earlier release's declaration marks it", withPhase(dcv1beta1.PhaseFailed),
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseBootstrapping, markUnfinished)
			}, unfinishedValue},
		{"a bootstrap building over a Ready declaration with no document marks it", withPhase(dcv1beta1.PhaseReady),
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseBootstrapping, markUnfinished)
			}, unfinishedValue},
		{"a carve-out run over a live instance clears it", withRecord(dcv1beta1.PhaseFailed),
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseBootstrapping, markLive)
			}, ""},
		{"a carve-out run that creates the declaration does not mark it", nil,
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseBootstrapping, markLive)
			}, ""},
		{"the upgrade's write carries an active record", withRecord(dcv1beta1.PhaseFailed),
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseUpgrading)
			}, unfinishedValue},
		{"the upgrade's write never adds one", withPhase(dcv1beta1.PhaseFailed),
			func(dyn *dynamicfake.FakeDynamicClient) error {
				return writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v", dcv1beta1.PhaseUpgrading)
			}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dyn *dynamicfake.FakeDynamicClient
			if tc.existing == nil {
				dyn = declarationClient()
			} else {
				dyn = declarationClient(declaredInstance(t, "prod", tc.existing))
			}
			if err := tc.write(dyn); err != nil {
				t.Fatal(err)
			}
			v, ok := recordOf(t, dyn, "prod")
			if tc.want == "" && ok {
				t.Errorf("the declaration carries the record %q, want none", v)
			}
			if tc.want != "" && v != tc.want {
				t.Errorf("the record reads %q (present=%v), want %q", v, ok, tc.want)
			}
		})
	}
}

func withPhase(phase string) func(*dcv1beta1.Instance) {
	return func(inst *dcv1beta1.Instance) { setPhase(inst, phase) }
}

// 🔴 THE DECLARE STEP DECIDES BY WHAT STEP 3 FOUND, AND TAKES THE ANSWER BACK FROM THE
// CLUSTER. A run over a live instance must never mark it; and what the terminal stamp
// later has to clear is what the read-back carries, not what this step meant to write.
func TestTheDeclareStepMarksOnlyARunThatIsBuilding(t *testing.T) {
	for _, overLive := range []bool{false, true} {
		t.Run(map[bool]string{false: "building", true: "over a live instance"}[overLive], func(t *testing.T) {
			prev, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
			t.Cleanup(func() { readInstanceDeclaration = prev; writeInstanceDeclaration = prevWrite })

			var building *bool
			writeInstanceDeclaration = func(_ context.Context, _, _ string, _ dcv1beta1.InstanceSpec, _ string, b bool) error {
				building = &b
				return nil
			}
			readBackInst := aDeclaration(dcv1beta1.PhaseBootstrapping, "")
			if !overLive {
				readBackInst.Annotations[unfinishedKey] = unfinishedValue
			}
			readBackInst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
			readBackInst.Spec = dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default"}
			readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
				return readBackInst, nil
			}

			st := &State{Instance: "prod", Provider: "kind", Profile: "default", OverLiveInstance: overLive}
			if err := stepDeclareInstance(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			if building == nil || *building == overLive {
				t.Errorf("the declaration was written with building=%v for a run with OverLiveInstance=%v", building, overLive)
			}
			if st.UnfinishedBootstrap == overLive {
				t.Errorf("UnfinishedBootstrap is %v; the read-back says %v", st.UnfinishedBootstrap, !overLive)
			}
		})
	}
}

// failPatches makes every Patch of a declaration fail.
func failPatches(dyn *dynamicfake.FakeDynamicClient) {
	dyn.PrependReactor("patch", "instances", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server refused the patch")
	})
}

// lostClaim is a claim whose Lease has been taken away, as a reclaim looks from here.
func lostClaim(t *testing.T) *Claim {
	t.Helper()
	cs := fake.NewClientset()
	claim, err := AcquireClaim(t.Context(), cs, testClaimNS, "prod", testKubeContext)
	if err != nil {
		t.Fatalf("taking the cluster lock: %v", err)
	}
	if err := cs.CoordinationV1().Leases(testClaimNS).Delete(t.Context(), claimLeaseName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("simulating a reclaim: %v", err)
	}
	return claim
}

// 🔴 THE STAMP IS A COURTESY EXCEPT IN ONE CASE, AND THAT CASE MUST FAIL THE RUN. A run
// that ended well over an unfinished instance has made it live, and only its Ready write
// removes the record; if that write cannot be made — refused, or fenced out by a lost
// lock — the record stands over a running instance, and the command must not say
// success. Every other unrecorded stamp stays the warning it always was.
func TestAnUnrecordedFinishFailsOnlyASuccessfulRunOverAnUnfinishedInstance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		unfinished bool
		runErr     error
		fenced     bool
		wantErr    bool
	}{
		{"success over an unfinished instance, patch refused", true, nil, false, true},
		{"success over an unfinished instance, lock lost", true, nil, true, true},
		{"failure over an unfinished instance, patch refused", true, errors.New("helm"), false, false},
		{"failure over an unfinished instance, lock lost", true, errors.New("helm"), true, false},
		{"success over a live instance, patch refused", false, nil, false, false},
		{"success over a live instance, lock lost", false, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := aDeclaration(dcv1beta1.PhaseBootstrapping, "")
			inst.Name = "prod"
			if tc.unfinished {
				inst.Annotations[unfinishedKey] = unfinishedValue
			}
			dyn := declaring(t, inst)
			var claim *Claim
			if tc.fenced {
				claim = lostClaim(t)
			} else {
				failPatches(dyn)
			}
			st := &State{Instance: "prod", Provider: "local", UnfinishedBootstrap: tc.unfinished}

			var err error
			captureStdout(t, func() { err = recordRunEnded(t.Context(), dyn, "bootstrap", "prod", st, claim, tc.runErr) })
			if !tc.wantErr {
				if err != nil {
					t.Errorf("an unrecorded stamp failed a run it should only warn about: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a successful run over an unfinished instance reported success while the record " +
					"still stands over it")
			}
			for _, want := range []string{"could not record that its bootstrap finished", "dcctl bootstrap local prod"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not contain %q: %v", want, err)
				}
			}
			// Fenced or refused, the declaration still says unfinished: nothing was written.
			if v, _ := recordOf(t, dyn, "prod"); v != unfinishedValue {
				t.Errorf("the record reads %q after a stamp that did not happen", v)
			}
		})
	}
}

// The upgrade reports the same failure through its own wrapper, and still gives the
// lock back.
func TestAnUpgradeThatCannotRecordFinishingAnUnfinishedInstanceFails(t *testing.T) {
	inst := upgrading(t)
	inst.Annotations[unfinishedKey] = unfinishedValue
	dyn := declaring(t, inst)
	failPatches(dyn)
	st := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
	st.Provider = "local"
	st.UnfinishedBootstrap = true

	err := finishUpgradePhase(t.Context(), dyn, "prod", st, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "dcctl upgrade local prod") {
		t.Fatalf("the upgrade's finish did not report the unrecorded Ready with its remedy: %v", err)
	}
	// ...and only on success: a failed upgrade's phase is a courtesy, so what comes back
	// is the run's own error and not the stamp's.
	rollout := errors.New("rollout")
	if err := finishUpgradePhase(t.Context(), dyn, "prod", st, nil, rollout); err != rollout {
		t.Errorf("a failed upgrade returned %v, want the run's own error", err)
	}
}

// 🔴 THE MERGE IS WHAT THE COMMAND EXITS WITH. Upgrade's deferred call assigns this
// function's result to its own, so the merge has to be right here: a successful upgrade
// whose Ready could not be recorded over an unfinished instance must come back as an
// error, and a failed one must come back as its own failure, not as nil.
func TestTheUpgradesFinishReturnsWhatTheCommandExitsWith(t *testing.T) {
	st := upgradingTo("ghcr.io/devicechain-io", "v1.3.0")
	st.Provider = "local"
	st.UnfinishedBootstrap = true

	inst := upgrading(t)
	inst.Annotations[unfinishedKey] = unfinishedValue
	refused := declaring(t, inst)
	failPatches(refused)
	var err error
	captureStdout(t, func() { err = finishUpgradePhase(t.Context(), refused, "prod", st, nil, nil) })
	if err == nil {
		t.Fatal("a successful upgrade whose Ready did not land over an unfinished instance exits 0")
	}

	rollout := errors.New("rollout")
	captureStdout(t, func() { err = finishUpgradePhase(t.Context(), refused, "prod", st, nil, rollout) })
	if !errors.Is(err, rollout) {
		t.Errorf("a failed upgrade exits with %v, want its own error", err)
	}

	// And a run that ended well and was recorded exits clean, with the record gone.
	inst = upgrading(t)
	inst.Annotations[unfinishedKey] = unfinishedValue
	ok := declaring(t, inst)
	captureStdout(t, func() { err = finishUpgradePhase(t.Context(), ok, "prod", st, nil, nil) })
	if err != nil {
		t.Fatalf("a recorded, successful upgrade exits with %v", err)
	}
	if v, kept := recordOf(t, ok, "prod"); kept {
		t.Errorf("a recorded Ready left the record %q behind", v)
	}
}

// 🔴 THE WHOLE LIFE OF THE RECORD, through the functions that write and read it: created
// unfinished, kept through a failure (so a re-run is let through), gone after a success
// (so the next re-run is refused).
func TestTheRecordLeavesOnlyOnSuccess(t *testing.T) {
	dyn := declarationClient()
	if err := writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v",
		dcv1beta1.PhaseBootstrapping, markUnfinished); err != nil {
		t.Fatal(err)
	}
	st := aBootstrapOf("prod")
	st.UnfinishedBootstrap = true

	if err := recordRunEnded(t.Context(), dyn, "bootstrap", "prod", st, nil, errors.New("helm")); err != nil {
		t.Fatal(err)
	}
	if err := rebuildRefusalReason(st, aLiveInstance(), readBack(t, dyn, "prod")); err != nil {
		t.Fatalf("after a failed first bootstrap, a re-run was refused: %v", err)
	}

	if err := recordRunEnded(t.Context(), dyn, "bootstrap", "prod", st, nil, nil); err != nil {
		t.Fatal(err)
	}
	decl := readBack(t, dyn, "prod")
	if decl.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseReady {
		t.Errorf("a successful run left the phase at %q", decl.Annotations[dcv1beta1.AnnotationPhase])
	}
	if err := rebuildRefusalReason(aBootstrapOf("prod"), aLiveInstance(), decl); err == nil {
		t.Fatal("after a successful bootstrap, a re-run was let through over the live instance")
	}
}

// 🔴 THE PRODUCTION MAPPING FROM "BUILDING" TO THE MARK, through the function
// WriteInstanceCR is only a kubeconfig wrapper around. The declare step's test stubs the
// write and the table above passes the mark in already chosen, so this is the one test
// that runs the mapping: a building run that wrote no record is every failed bootstrap
// refused again on its re-run, and a run over a live instance that wrote one is a live
// instance a later bootstrap is let through over.
func TestTheBootstrapsDeclarationWriteMarksExactlyWhenItIsBuilding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		building bool
		existing func(*dcv1beta1.Instance) // nil: no declaration yet
		want     string                    // "" means absent
	}{
		{"building, no declaration yet", true, nil, unfinishedValue},
		{"building over an unmarked declaration", true, withPhase(dcv1beta1.PhaseFailed), unfinishedValue},
		{"over a live instance, no declaration yet", false, nil, ""},
		{"over a live instance carrying a record", false, func(inst *dcv1beta1.Instance) {
			setPhase(inst, dcv1beta1.PhaseFailed)
			inst.Annotations[unfinishedKey] = unfinishedValue
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := declarationClient()
			if tc.existing != nil {
				dyn = declarationClient(declaredInstance(t, "prod", tc.existing))
			}
			if err := writeBootstrapDecl(t.Context(), dyn, "prod", desiredSpec(), "v", tc.building); err != nil {
				t.Fatal(err)
			}
			v, ok := recordOf(t, dyn, "prod")
			if v != tc.want || ok != (tc.want != "") {
				t.Errorf("building=%v left the record %q (present=%v), want %q", tc.building, v, ok, tc.want)
			}
			if got := readBack(t, dyn, "prod").Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseBootstrapping {
				t.Errorf("the bootstrap declared the phase %q", got)
			}
		})
	}
}

// 🔴 BOOTSTRAP'S READY IS WHAT CLEARS THE RECORD, so the function the command calls at
// the end of a run is tested through, not around: a success over a marked declaration
// leaves Ready and no record; a run whose claim was lost writes nothing at all; a dry run
// does not even connect.
func TestTheBootstrapsFinishClearsTheRecordOnlyWhenItMay(t *testing.T) {
	marked := func() *dcv1beta1.Instance {
		inst := aDeclaration(dcv1beta1.PhaseBootstrapping, "")
		inst.Name = "prod"
		inst.Annotations[unfinishedKey] = unfinishedValue
		return inst
	}
	connectTo := func(dyn *dynamicfake.FakeDynamicClient) func() (dynamic.Interface, error) {
		return func() (dynamic.Interface, error) { return dyn, nil }
	}

	t.Run("a successful run over a marked declaration", func(t *testing.T) {
		dyn := declaring(t, marked())
		st := aBootstrapOf("prod")
		st.UnfinishedBootstrap = true
		if err := finishBootstrapPhase(t.Context(), st, nil, connectTo(dyn)); err != nil {
			t.Fatal(err)
		}
		decl := readBack(t, dyn, "prod")
		if got := decl.Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseReady {
			t.Errorf("a successful bootstrap left the phase at %q", got)
		}
		if v, kept := decl.Annotations[unfinishedKey]; kept {
			t.Errorf("a successful bootstrap left the record %q over the instance it made live", v)
		}
	})

	t.Run("a run whose claim was lost", func(t *testing.T) {
		dyn := declaring(t, marked())
		st := aBootstrapOf("prod")
		st.Claim = lostClaim(t)
		var err error
		captureStdout(t, func() { err = finishBootstrapPhase(t.Context(), st, errors.New("helm"), connectTo(dyn)) })
		if err != nil {
			t.Fatalf("a fenced, failed run reported %v; its stamp is a courtesy", err)
		}
		decl := readBack(t, dyn, "prod")
		if got := decl.Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseBootstrapping {
			t.Errorf("a fenced run stamped %q over the reclaimer's declaration", got)
		}
		if v := decl.Annotations[unfinishedKey]; v != unfinishedValue {
			t.Errorf("a fenced run changed the record to %q", v)
		}
	})

	t.Run("a dry run", func(t *testing.T) {
		st := aBootstrapOf("prod")
		st.DryRun = true
		if err := finishBootstrapPhase(t.Context(), st, nil, func() (dynamic.Interface, error) {
			t.Error("a dry run connected to the cluster to record a phase")
			return nil, errors.New("no cluster")
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no connection after a success over an unfinished instance", func(t *testing.T) {
		st := aBootstrapOf("prod")
		st.Provider = "local"
		st.UnfinishedBootstrap = true
		err := finishBootstrapPhase(t.Context(), st, nil, func() (dynamic.Interface, error) {
			return nil, errors.New("no cluster")
		})
		if err == nil || !strings.Contains(err.Error(), "dcctl bootstrap local prod") {
			t.Errorf("an unreachable cluster after a success over an unfinished instance reported %v", err)
		}
	})
}

// 🔴 THE UPGRADE READS WHETHER IT IS FINISHING A FIRST BOOTSTRAP, through the function
// Upgrade calls. The finish's error (above) is only raised when this is true, so a
// hydration that left it false would let a successful upgrade over an unfinished
// instance exit 0 with its Ready — and so the record's removal — unrecorded.
func TestTheUpgradeHydrationReadsTheFirstBootstrapRecord(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		phase string
		want  bool
	}{
		{"marked and not Ready", dcv1beta1.PhaseFailed, true},
		{"marked beside Ready", dcv1beta1.PhaseReady, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevDecl, prevDeployed := readInstanceDeclaration, lookupDeployedInstance
			t.Cleanup(func() { readInstanceDeclaration, lookupDeployedInstance = prevDecl, prevDeployed })
			readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
				inst := atVersion(t, "ghcr.io/devicechain-io", "v0.17.0")
				setPhase(inst, tc.phase)
				inst.Annotations[unfinishedKey] = unfinishedValue
				return inst, nil
			}
			lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
				return &config.InstanceConfiguration{}, nil
			}
			written := aWritableState()
			written.Instance = "prod"
			c := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "kube-system", UID: types.UID(testClusterUID)}})
			writeInstallThenBootstrapSecrets(t, c, written)
			settleStringDataLikeAnAPIServer(t, c)
			if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
				t.Fatal(err)
			}
			stubOperatorCheck(t, nil)

			st, err := hydrateUpgradeState(context.Background(), c, provider,
				ClusterBinding{KubeContext: "kind-devicechain", Cluster: "devicechain"},
				UpgradeOptions{Options: Options{Instance: "prod"}})
			if err != nil {
				t.Fatal(err)
			}
			if st.UnfinishedBootstrap != tc.want {
				t.Errorf("the upgrade read UnfinishedBootstrap=%v from a %s declaration carrying the record, want %v",
					st.UnfinishedBootstrap, tc.phase, tc.want)
			}
		})
	}
}
