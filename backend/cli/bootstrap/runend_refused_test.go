// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/client-go/dynamic"
)

func TestARunRefusedBeforeItWroteTheDeclarationLeavesThePhase(t *testing.T) {
	for _, phase := range []string{dcv1beta1.PhaseReady, dcv1beta1.PhaseBootstrapping} {
		t.Run(phase, func(t *testing.T) {
			inst := aDeclaration(phase, "")
			inst.Name = "prod"
			inst.ResourceVersion = "42"
			if phase == dcv1beta1.PhaseBootstrapping {
				inst.Annotations[unfinishedKey] = unfinishedValue
			}
			dyn := declaring(t, inst)
			before := readBack(t, dyn, "prod")
			dyn.ClearActions()
			st := aBootstrapOf("prod")
			var err error
			output := captureStdout(t, func() {
				err = finishBootstrapPhase(t.Context(), st, errors.New(`instance "prod" is already running`), func() (dynamic.Interface, error) {
					t.Error("a refused run connected to record a phase")
					return dyn, nil
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			// The fake does not advance resourceVersion, so also check for any API actions.
			if actions := dyn.Actions(); len(actions) != 0 {
				t.Errorf("a refused run accessed the declaration: %v", actions)
			}
			after := readBack(t, dyn, "prod")
			if got := after.Annotations[dcv1beta1.AnnotationPhase]; got != phase {
				t.Errorf("a refused run changed phase to %q, want %q", got, phase)
			}
			if after.ResourceVersion != before.ResourceVersion {
				t.Errorf("a refused run changed resourceVersion: %q -> %q", before.ResourceVersion, after.ResourceVersion)
			}
			if !reflect.DeepEqual(before, after) {
				t.Error("a refused run changed the declaration or its unfinished record")
			}
			if !strings.Contains(output, "The instance's recorded phase is unchanged:") {
				t.Errorf("the refusal did not explain the unchanged phase: %s", output)
			}
		})
	}
}

func TestARunThatFailedAfterWritingTheDeclarationIsStampedFailed(t *testing.T) {
	inst := aDeclaration(dcv1beta1.PhaseReady, "")
	inst.Name = "prod"
	dyn := declaring(t, inst)
	st := aBootstrapOf("prod")
	st.DeclarationWritten = true
	if err := finishBootstrapPhase(t.Context(), st, errors.New("helm"), func() (dynamic.Interface, error) { return dyn, nil }); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Annotations[dcv1beta1.AnnotationPhase]; got != dcv1beta1.PhaseFailed {
		t.Errorf("a failed run left phase %q, want Failed", got)
	}
}

func TestTheDeclareStepMarksTheDeclarationWrittenOnlyWhenItWrites(t *testing.T) {
	for _, outcome := range []string{"overlay refusal", "write failure", "dry run"} {
		t.Run(outcome, func(t *testing.T) {
			prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
			t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
			st := &State{Instance: "prod", Provider: "local", Profile: "default"}
			inst := aDeclaration(dcv1beta1.PhaseReady, "")
			inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", MQTTDeviceUplinks: boolPtr(true)}
			readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) { return inst, nil }
			written := false
			writeErr := errors.New("write failed")
			writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error {
				written = true
				if !st.DeclarationWritten {
					t.Error("DeclarationWritten was false inside the write")
				}
				return writeErr
			}
			switch outcome {
			case "overlay refusal":
				st.ValuesOverlay = map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
					"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"mqttDeviceUplinks": false}},
				}}}
			case "dry run":
				st.DryRun = true
			}
			err := stepDeclareInstance(t.Context(), st)
			switch outcome {
			case "overlay refusal":
				if err == nil || !strings.Contains(err.Error(), "records --mqtt-device-uplinks=true") {
					t.Fatalf("expected the overlay refusal, got %v", err)
				}
			case "write failure":
				if !errors.Is(err, writeErr) {
					t.Fatalf("expected write failure, got %v", err)
				}
			case "dry run":
				if err != nil {
					t.Fatal(err)
				}
			}
			want := outcome == "write failure"
			if written != want || st.DeclarationWritten != want {
				t.Errorf("write called=%v, DeclarationWritten=%v, want both %v", written, st.DeclarationWritten, want)
			}
		})
	}
}
