// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// An instance that is up: the configuration document exists and parses, which is the
// boundary this refusal is drawn around.
func aLiveInstance() *config.InstanceConfiguration {
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Secrets.RootKey = "the-key-it-is-running-on"
	return deployed
}

func aBootstrapOf(instance string) *State {
	return &State{Instance: instance, Provider: "local", Values: map[string]string{}}
}

// 🔴 THE CONTRACT THIS SLICE WITHDRAWS. A bootstrap mints every credential an
// instance has; there is no ordering in which handing a running instance new ones is
// safe, and for as long as there was nowhere else for the job to go, the pipeline
// went to some lengths to reuse instead. `dcctl upgrade` is that place now.
func TestABootstrapOverALiveInstanceIsRefused(t *testing.T) {
	err := rebuildRefusalReason(aBootstrapOf(testInstance), aLiveInstance())
	if err == nil {
		t.Fatal("a bootstrap aimed at a running instance was allowed to proceed and mint over it")
	}
	if !strings.Contains(err.Error(), "dcctl upgrade") {
		t.Errorf("the refusal does not name the verb that does this instead: %v", err)
	}
	if !strings.Contains(err.Error(), "dcctl destroy") {
		t.Errorf("the refusal does not say how to rebuild deliberately: %v", err)
	}
}

// 🔴 AND THE WINDOW IT MUST LEAVE OPEN. A bootstrap builds an instance over ten steps
// and can die at any of them. The configuration document is written at the seventh —
// so before it exists there is a reachable state with a LIVE broker configured from
// credentials whose only copy is a file on this machine, and live databases whose
// owner passwords exist only in their Secrets. Re-running is the only thing that
// repairs it.
func TestAHalfBuiltInstanceCanStillBeRepairedByRunningAgain(t *testing.T) {
	if err := rebuildRefusalReason(aBootstrapOf(testInstance), nil); err != nil {
		t.Fatalf("a bootstrap that died before writing its configuration document could not be "+
			"run again, which makes a half-built instance permanently unrepairable: %v", err)
	}
}

// A restore aimed at an instance that already exists is a supported RETRY, and the
// guard that makes it safe is already there and sharper than this one: it allows the
// run only when the escrow artifact carries the key the instance is running on.
func TestARestoreOverALiveInstanceIsStillAllowed(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   *State
	}{
		{"recovering a database", func() *State {
			st := aBootstrapOf(testInstance)
			st.Restore = RestorePlan{RdbFrom: "dc-rdb-20260101"}
			return st
		}()},
		{"seeding the root key from an escrow artifact", func() *State {
			st := aBootstrapOf(testInstance)
			st.Escrow = EscrowPlan{RestoredRootKey: "a-key", RestoredFrom: "artifact.escrow"}
			return st
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := rebuildRefusalReason(tc.st, aLiveInstance()); err != nil {
				t.Errorf("a recovery could not be re-run against the instance it recovered, "+
					"which takes the retry away exactly when it matters: %v", err)
			}
		})
	}
}

// The documented path for an instance still carrying databases from before the
// managed ones: dump, then re-run with this flag so the old resources are removed. It
// is a re-run against a live instance by construction — there is nothing else the
// flag could mean.
func TestTheLegacyDatabaseRemovalRerunIsStillAllowed(t *testing.T) {
	st := aBootstrapOf(testInstance)
	st.AllowLegacyDbRemoval = true

	if err := rebuildRefusalReason(st, aLiveInstance()); err != nil {
		t.Errorf("the documented legacy-removal re-run was refused: %v", err)
	}
}

// 🔴 THE REFUSAL IS PLACED AFTER THE LOCK AND BEFORE ANYTHING IS APPLIED, and both
// edges are load-bearing. After the lock, because the answer is read from the cluster
// and a concurrent bootstrap is exactly what would make it stale between reading and
// acting. Before the operator install, because every step past that one writes to a
// cluster that may already be running the instance it would be writing over.
func TestTheRebuildRefusalRunsAfterTheLockAndBeforeAnythingIsApplied(t *testing.T) {
	claim := stepIndex(t, stepClaimCluster)
	refuse := stepIndex(t, stepRefuseRebuild)
	core := stepIndex(t, stepInstallCore)

	if refuse <= claim {
		t.Errorf("the rebuild check runs at %d and the lock is taken at %d: a concurrent "+
			"bootstrap could make the answer stale between reading it and acting on it",
			refuse, claim)
	}
	if refuse >= core {
		t.Errorf("the rebuild check runs at %d and the operator install at %d: a refused "+
			"bootstrap would already have written its own version over a live instance",
			refuse, core)
	}
}

// A dry run applies nothing, so it is not refused — but it must SAY that a real run
// would be, or the rehearsal is of a different run than the one it is rehearsing.
func TestADryRunIsNotRefusedButSaysTheRealOneWouldBe(t *testing.T) {
	st := aBootstrapOf(testInstance)
	st.DryRun = true

	// The seam the step reads through, pointed at a live instance.
	restore := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = restore })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return aLiveInstance(), nil
	}

	if err := stepRefuseRebuild(context.Background(), st); err != nil {
		t.Fatalf("a rehearsal that applies nothing was refused: %v", err)
	}
}

// 🔴 "COULD NOT TELL" MUST NOT RESOLVE TO "GO AHEAD". Reading a failure to look as an
// absent instance is the direction that mints over a live one, which is the same rule
// every other reader of this document follows.
func TestAnUnreadableClusterStopsTheBootstrapRatherThanAllowingIt(t *testing.T) {
	restore := lookupDeployedInstance
	t.Cleanup(func() { lookupDeployedInstance = restore })
	lookupDeployedInstance = func(context.Context, string, string) (*config.InstanceConfiguration, error) {
		return nil, errors.New("the API server is not answering")
	}

	if err := stepRefuseRebuild(context.Background(), aBootstrapOf(testInstance)); err == nil {
		t.Fatal("a bootstrap proceeded past an API server that would not say whether an " +
			"instance was already there")
	}
}

// The carve-outs are read off the struct rather than trusted to a list: each one is a
// documented path, and a guard whose exceptions were never constructed loses them on
// its next edit. This fails if a new field appears on RestorePlan that ought to widen
// the restore carve-out and does not.
func TestEveryWayOfNamingARestoreReachesTheCarveOut(t *testing.T) {
	sources := reflect.TypeOf(RestorePlan{})
	for i := 0; i < sources.NumField(); i++ {
		name := sources.Field(i).Name
		if !strings.HasSuffix(name, "From") {
			// Target times qualify a restore; they do not declare one.
			continue
		}
		st := aBootstrapOf(testInstance)
		plan := reflect.ValueOf(&st.Restore).Elem()
		plan.FieldByName(name).SetString("an-archive")

		if err := rebuildRefusalReason(st, aLiveInstance()); err != nil {
			t.Errorf("a restore declared through RestorePlan.%s was refused as a rebuild: %v",
				name, err)
		}
	}
}
