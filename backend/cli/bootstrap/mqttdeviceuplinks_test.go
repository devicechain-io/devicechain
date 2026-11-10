// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// 🔴 THESE TESTS DRIVE THE REAL PATH: a State goes through the declaration the cluster
// stores, comes back through applyUpgradeDeclaration, and is rendered by the embedded
// chart into the document the services load with their own strict loader. A test of the
// flag parser alone would pass just as well if the choice never reached the document, or
// was dropped by the next upgrade -- which is the bug class this option exists to close.

// deviceUplinksDocumentFor composes the instance configuration document a State renders and loads it
// the way every service does. raw is the document's text, for asserting a key is ABSENT,
// which a decoded struct cannot say.
func deviceUplinksDocumentFor(t *testing.T, st *State) (deviceUplinks bool, raw string) {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	_, doc, err := releaseValues(t.Context(), ch, st, nil)
	if err != nil {
		t.Fatalf("composing the instance configuration: %v", err)
	}
	loaded, err := loadInstanceConfigDocument(doc)
	if err != nil {
		t.Fatalf("the services would refuse this document: %v", err)
	}
	return loaded.Infrastructure.Nats.MqttDeviceUplinks, string(doc)
}

// deviceUplinksUpgradeRender is what `dcctl upgrade` renders for the instance the cluster holds: the
// declaration applied over a State carrying a real instance's credentials, with the
// operator's flag (nil when not given) on top.
func deviceUplinksUpgradeRender(t *testing.T, dyn *fake.FakeDynamicClient, flag *bool) (st *State, on bool, raw string) {
	t.Helper()
	inst := readBack(t, dyn, "prod")
	st = &State{Instance: "prod", Evolving: true, Values: maps.Clone(compactState(false).Values)}
	opts := UpgradeOptions{Options: Options{Instance: "prod", MqttDeviceUplinks: flag}}
	if err := applyUpgradeDeclaration(st, inst, opts); err != nil {
		t.Fatalf("settling the declaration: %v", err)
	}
	on, raw = deviceUplinksDocumentFor(t, st)
	return st, on, raw
}

// deviceUplinksBuiltWith is the declaration a bootstrap given this choice writes, put in the cluster.
func deviceUplinksBuiltWith(t *testing.T, choice *bool) *fake.FakeDynamicClient {
	t.Helper()
	bootstrapSt := compactState(false)
	bootstrapSt.Instance = "prod"
	bootstrapSt.MqttDeviceUplinks = choice
	spec := InstanceSpecFrom(bootstrapSt, ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	spec.ImageRegistry, spec.ImageVersion = "ghcr.io/devicechain-io", "v1.2.0"

	// Through the wire form the cluster stores it in.
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if choice == nil && strings.Contains(string(raw), "mqttDeviceUplinks") {
		t.Errorf("an instance that never chose wrote the key: %s", raw)
	}
	var wire dcv1beta1.InstanceSpec
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	inst := atVersion(t, "ghcr.io/devicechain-io", "v1.2.0")
	inst.Spec = wire
	return declaring(t, inst)
}

// The chart delivers each state to the document the services load: on and off are
// written, and never-chose writes nothing so the chart's own default stands.
func TestDeviceUplinksReachesTheServicesDocument(t *testing.T) {
	t.Parallel()
	for _, shape := range []struct {
		name        string
		ha, compact bool
	}{{"single", false, false}, {"ha", true, false}, {"compact", false, true}} {
		for _, tc := range []struct {
			name    string
			choice  *bool
			want    bool
			written bool
		}{
			{"never chose", nil, false, false},
			{"on", boolPtr(true), true, true},
			{"explicit off", boolPtr(false), false, true},
		} {
			t.Run(shape.name+"/"+tc.name, func(t *testing.T) {
				st := compactState(shape.compact)
				st.HA, st.MqttDeviceUplinks = shape.ha, tc.choice
				got, raw := deviceUplinksDocumentFor(t, st)
				if got != tc.want {
					t.Errorf("mqttDeviceUplinks = %v in the document the services load, want %v", got, tc.want)
				}
				if has := strings.Contains(raw, "mqttDeviceUplinks"); has != tc.written {
					t.Errorf("document mentions mqttDeviceUplinks = %v, want %v:\n%s", has, tc.written, raw)
				}
			})
		}
	}
}

// 🔴 THE POINT OF THE OPTION. Installed on, an instance is still on after a plain upgrade
// with no flag repeated -- and the upgrade after that.
func TestDeviceUplinksOnSurvivesPlainUpgrades(t *testing.T) {
	t.Parallel()
	dyn := deviceUplinksBuiltWith(t, boolPtr(true))
	for i := 1; i <= 2; i++ {
		_, on, _ := deviceUplinksUpgradeRender(t, dyn, nil)
		if !on {
			t.Fatalf("upgrade %d with no flag rendered device uplinks OFF over an instance installed with it on", i)
		}
		// A plain version move writes the declaration; it must not write the choice away.
		st := upgradingTo("ghcr.io/devicechain-io", "v1.3."+string(rune('0'+i)))
		if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
			t.Fatal(err)
		}
		if got := readBack(t, dyn, "prod").Spec.MQTTDeviceUplinks; got == nil || !*got {
			t.Fatalf("a plain version move rewrote the recorded choice to %v", got)
		}
	}
}

// An explicit off stays off, and is recorded as off rather than as "never chose".
func TestDeviceUplinksExplicitOffStaysOff(t *testing.T) {
	t.Parallel()
	dyn := deviceUplinksBuiltWith(t, boolPtr(true))

	st, on, raw := deviceUplinksUpgradeRender(t, dyn, boolPtr(false))
	if on || !strings.Contains(raw, `"mqttDeviceUplinks":false`) {
		t.Fatalf("--mqtt-device-uplinks=false rendered on=%v:\n%s", on, raw)
	}
	if !st.MqttDeviceUplinksChange {
		t.Fatal("turning it off was not marked for recording, so the next upgrade would turn it back on")
	}
	st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Spec.MQTTDeviceUplinks; got == nil || *got {
		t.Fatalf("the declaration records %v, want an explicit false", got)
	}

	// The next upgrade repeats nothing.
	_, on, raw = deviceUplinksUpgradeRender(t, dyn, nil)
	if on {
		t.Fatal("a flagless upgrade turned device uplinks back ON after an explicit off")
	}
	if !strings.Contains(raw, `"mqttDeviceUplinks":false`) {
		t.Errorf("the explicit off is no longer written to the document, so it would follow the platform default:\n%s", raw)
	}
}

// A default install is off, stays off, and writes nothing; turning it on later is
// recorded and kept.
func TestDeviceUplinksDefaultInstallIsOffAndCanBeTurnedOnLater(t *testing.T) {
	t.Parallel()
	dyn := deviceUplinksBuiltWith(t, nil)

	_, on, raw := deviceUplinksUpgradeRender(t, dyn, nil)
	if on || strings.Contains(raw, "mqttDeviceUplinks") {
		t.Fatalf("a default install rendered on=%v with the key present=%v", on,
			strings.Contains(raw, "mqttDeviceUplinks"))
	}

	st, on, _ := deviceUplinksUpgradeRender(t, dyn, boolPtr(true))
	if !on {
		t.Fatal("--mqtt-device-uplinks on an existing instance rendered off")
	}
	st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	if _, on, _ = deviceUplinksUpgradeRender(t, dyn, nil); !on {
		t.Fatal("the flag was honoured once and forgotten by the next upgrade")
	}
}

// Repeating the flag with the value the declaration already holds writes nothing: the
// provenance annotations answer "when did something last change".
func TestDeviceUplinksRepeatingTheRecordedValueIsANoOp(t *testing.T) {
	dyn := deviceUplinksBuiltWith(t, boolPtr(true))
	st, _, _ := deviceUplinksUpgradeRender(t, dyn, boolPtr(true))
	if st.MqttDeviceUplinksChange {
		t.Error("repeating the recorded value was marked as a change")
	}
}

// A bootstrap re-run that names no flag keeps the recorded choice; one that names a flag
// replaces it.
func TestARerunWithNoDeviceUplinksFlagKeepsTheDeclaredChoice(t *testing.T) {
	run := func(t *testing.T, declared, flag *bool) (written *bool) {
		t.Helper()
		prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
		t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
		current := declared
		readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			inst := aDeclaration(dcv1beta1.PhaseReady, "")
			inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
			inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", MQTTDeviceUplinks: current}
			return inst, nil
		}
		writeInstanceDeclaration = func(_ context.Context, _, _ string, spec dcv1beta1.InstanceSpec, _ string, _ bool) error {
			written, current = spec.MQTTDeviceUplinks, spec.MQTTDeviceUplinks
			return nil
		}
		st := &State{Instance: "prod", Provider: "local", Profile: "default", MqttDeviceUplinks: flag}
		if err := stepDeclareInstance(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		return written
	}
	if got := run(t, boolPtr(true), nil); got == nil || !*got {
		t.Errorf("a flagless re-run wrote %v over a declared on", got)
	}
	if got := run(t, boolPtr(false), nil); got == nil || *got {
		t.Errorf("a flagless re-run wrote %v over a declared explicit off", got)
	}
	if got := run(t, nil, nil); got != nil {
		t.Errorf("a choice was invented for an instance that never made one: %v", *got)
	}
	if got := run(t, boolPtr(true), boolPtr(false)); got == nil || *got {
		t.Errorf("an explicit flag lost to the declared value: %v", got)
	}
}

func TestMqttDeviceUplinksFromFlagDistinguishesNotGivenFromFalse(t *testing.T) {
	if MqttDeviceUplinksFromFlag(false, true) != nil || MqttDeviceUplinksFromFlag(false, false) != nil {
		t.Error("a flag that was not given produced a choice")
	}
	if v := MqttDeviceUplinksFromFlag(true, false); v == nil || *v {
		t.Errorf("--mqtt-device-uplinks=false produced %v", v)
	}
	if v := MqttDeviceUplinksFromFlag(true, true); v == nil || !*v {
		t.Errorf("--mqtt-device-uplinks produced %v", v)
	}
}

// 🔴 A CLUSTER WHOSE INSTANCE DEFINITION PREDATES THE FIELD PRUNES IT AND REPORTS SUCCESS. The
// run must say so, or the next flagless upgrade renders the default over the operator's choice.
func TestDeviceUplinksBootstrapRefusesAPrunedChoice(t *testing.T) {
	prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		inst := aDeclaration(dcv1beta1.PhaseReady, "")
		inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
		inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default"} // the field was pruned
		return inst, nil
	}
	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error { return nil }
	st := &State{Instance: "prod", Provider: "local", Profile: "default", MqttDeviceUplinks: boolPtr(true)}
	err := stepDeclareInstance(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), "does not record mqttDeviceUplinks") || !strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("a pruned choice was not refused: %v", err)
	}
	// Never choosing is never a mismatch.
	st = &State{Instance: "prod", Provider: "local", Profile: "default"}
	if err := stepDeclareInstance(context.Background(), st); err != nil {
		t.Errorf("an instance that never chose was refused: %v", err)
	}
}

func TestDeviceUplinksUpgradeRefusesAPrunedChoice(t *testing.T) {
	for _, choice := range []bool{true, false} {
		t.Run(fmt.Sprint(choice), func(t *testing.T) {
			dyn := deviceUplinksBuiltWith(t, nil)
			dyn.PrependReactor("update", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if u, ok := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured); ok {
					unstructured.RemoveNestedField(u.Object, "spec", "mqttDeviceUplinks")
				}
				return false, nil, nil
			})
			st, _, _ := deviceUplinksUpgradeRender(t, dyn, boolPtr(choice))
			if st.MqttFailClosedChange {
				t.Fatal("premise: fail-closed must be unchanged")
			}
			st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
			err := recordUpgradedVersion(t.Context(), dyn, "prod", st)
			if err == nil || !strings.Contains(err.Error(), "does not record mqttDeviceUplinks") || !strings.Contains(err.Error(), "dcctl install") {
				t.Fatalf("a pruned choice was not refused before apply: %v", err)
			}
		})
	}
}

func TestDeviceUplinksOverlayPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		overlay bool
		flag    *bool
		want    bool
	}{
		{"flag off", true, boolPtr(false), false},
		{"omitted", true, nil, true},
		{"flag on", false, boolPtr(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := compactState(false)
			st.MqttDeviceUplinks = tc.flag
			st.ValuesOverlay = map[string]interface{}{
				"instance": map[string]interface{}{"config": map[string]interface{}{
					"infrastructure": map[string]interface{}{"nats": map[string]interface{}{
						"mqttDeviceUplinks": tc.overlay,
					}},
				}},
			}
			before, err := encodeValuesOverlay(st.ValuesOverlay)
			if err != nil {
				t.Fatal(err)
			}
			if got := natsValuesOf(authoringValues(st))["mqttDeviceUplinks"]; got != tc.want {
				t.Fatalf("rendered %v, want %v", got, tc.want)
			}
			_, raw := deviceUplinksDocumentFor(t, st)
			if !strings.Contains(raw, fmt.Sprintf(`"mqttDeviceUplinks":%t`, tc.want)) {
				t.Fatalf("rendered document disagrees with choice: %s", raw)
			}
			after, err := encodeValuesOverlay(st.ValuesOverlay)
			if err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("rendering mutated the recorded overlay")
			}
		})
	}
}

// Drive the claim itself through the dynamic client's wire conversion, including
// an API server that silently prunes the new field before read-back.
func TestDeviceUplinksClaimRoundTrip(t *testing.T) {
	for _, prune := range []bool{false, true} {
		for _, choice := range []*bool{nil, boolPtr(true), boolPtr(false)} {
			t.Run(fmt.Sprintf("prune=%t/choice=%v", prune, choice), func(t *testing.T) {
				st := &State{Instance: "prod", Provider: "local", Profile: "default", MqttDeviceUplinks: choice,
					ImageRegistry: "ghcr.io/devicechain-io", ImageVersion: "v1.2.0"}
				inst := aDeclaration(dcv1beta1.PhaseReady, "")
				inst.Name = "prod"
				inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
				inst.Spec = InstanceSpecFrom(st, st.Binding, st.Provider)
				inst.Spec.MQTTDeviceUplinks = nil
				dyn := declaring(t, inst)
				if prune {
					dyn.PrependReactor("update", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
						u := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
						unstructured.RemoveNestedField(u.Object, "spec", "mqttDeviceUplinks")
						return false, nil, nil
					})
				}
				prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
				t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
				readInstanceDeclaration = func(ctx context.Context, _, id string) (*dcv1beta1.Instance, error) {
					return readInstanceCR(ctx, dyn, id)
				}
				writeInstanceDeclaration = func(ctx context.Context, _, id string, spec dcv1beta1.InstanceSpec, version string, _ bool) error {
					return writeInstanceCR(ctx, dyn, id, spec, version, dcv1beta1.PhaseBootstrapping)
				}
				err := stepDeclareInstance(t.Context(), st)
				if prune && choice != nil {
					if err == nil || !strings.Contains(err.Error(), "does not record mqttDeviceUplinks") || !strings.Contains(err.Error(), "dcctl install") {
						t.Fatalf("claim did not refuse before apply: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !sameBoolPtr(choice, readBack(t, dyn, "prod").Spec.MQTTDeviceUplinks) {
					t.Fatal("claim did not record the choice")
				}
				up, _, _ := deviceUplinksUpgradeRender(t, dyn, nil)
				if !sameBoolPtr(choice, up.MqttDeviceUplinks) {
					t.Fatal("flagless upgrade lost the claim's choice")
				}
				value, present := natsValuesOf(helmValues(up))["mqttDeviceUplinks"]
				if present != (choice != nil) || (choice != nil && value != *choice) {
					t.Fatalf("helmValues choice = %v, present=%t", value, present)
				}
			})
		}
	}
}
