// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
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

func boolPtr(v bool) *bool { return &v }

// documentFor composes the instance configuration document a State renders and loads it
// the way every service does. raw is the document's text, for asserting a key is ABSENT,
// which a decoded struct cannot say.
func documentFor(t *testing.T, st *State) (failClosed bool, raw string) {
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
	return loaded.Infrastructure.Nats.MqttFailClosed, string(doc)
}

// upgradeRender is what `dcctl upgrade` renders for the instance the cluster holds: the
// declaration applied over a State carrying a real instance's credentials, with the
// operator's flag (nil when not given) on top.
func upgradeRender(t *testing.T, dyn *fake.FakeDynamicClient, flag *bool) (st *State, on bool, raw string) {
	t.Helper()
	inst := readBack(t, dyn, "prod")
	st = &State{Instance: "prod", Evolving: true, Values: maps.Clone(compactState(false).Values)}
	opts := UpgradeOptions{Options: Options{Instance: "prod", MqttFailClosed: flag}}
	if err := applyUpgradeDeclaration(st, inst, opts); err != nil {
		t.Fatalf("settling the declaration: %v", err)
	}
	on, raw = documentFor(t, st)
	return st, on, raw
}

// builtWith is the declaration a bootstrap given this choice writes, put in the cluster.
func builtWith(t *testing.T, choice *bool) *fake.FakeDynamicClient {
	t.Helper()
	bootstrapSt := compactState(false)
	bootstrapSt.Instance = "prod"
	bootstrapSt.MqttFailClosed = choice
	spec := InstanceSpecFrom(bootstrapSt, ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	spec.ImageRegistry, spec.ImageVersion = "ghcr.io/devicechain-io", "v1.2.0"

	// Through the wire form the cluster stores it in.
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if choice == nil && strings.Contains(string(raw), "mqttFailClosed") {
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
func TestFailClosedReachesTheServicesDocument(t *testing.T) {
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
				st.HA, st.MqttFailClosed = shape.ha, tc.choice
				got, raw := documentFor(t, st)
				if got != tc.want {
					t.Errorf("mqttFailClosed = %v in the document the services load, want %v", got, tc.want)
				}
				if has := strings.Contains(raw, "mqttFailClosed"); has != tc.written {
					t.Errorf("document mentions mqttFailClosed = %v, want %v:\n%s", has, tc.written, raw)
				}
			})
		}
	}
}

// 🔴 THE POINT OF THE OPTION. Installed on, an instance is still on after a plain upgrade
// with no flag repeated -- and the upgrade after that.
func TestFailClosedOnSurvivesPlainUpgrades(t *testing.T) {
	t.Parallel()
	dyn := builtWith(t, boolPtr(true))
	for i := 1; i <= 2; i++ {
		_, on, _ := upgradeRender(t, dyn, nil)
		if !on {
			t.Fatalf("upgrade %d with no flag rendered fail-closed OFF over an instance installed with it on", i)
		}
		// A plain version move writes the declaration; it must not write the choice away.
		st := upgradingTo("ghcr.io/devicechain-io", "v1.3."+string(rune('0'+i)))
		if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
			t.Fatal(err)
		}
		if got := readBack(t, dyn, "prod").Spec.MQTTFailClosed; got == nil || !*got {
			t.Fatalf("a plain version move rewrote the recorded choice to %v", got)
		}
	}
}

// An explicit off stays off, and is recorded as off rather than as "never chose".
func TestFailClosedExplicitOffStaysOff(t *testing.T) {
	t.Parallel()
	dyn := builtWith(t, boolPtr(true))

	st, on, raw := upgradeRender(t, dyn, boolPtr(false))
	if on || !strings.Contains(raw, `"mqttFailClosed":false`) {
		t.Fatalf("--mqtt-fail-closed=false rendered on=%v:\n%s", on, raw)
	}
	if !st.MqttFailClosedChange {
		t.Fatal("turning it off was not marked for recording, so the next upgrade would turn it back on")
	}
	st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, dyn, "prod").Spec.MQTTFailClosed; got == nil || *got {
		t.Fatalf("the declaration records %v, want an explicit false", got)
	}

	// The next upgrade repeats nothing.
	_, on, raw = upgradeRender(t, dyn, nil)
	if on {
		t.Fatal("a flagless upgrade turned fail-closed back ON after an explicit off")
	}
	if !strings.Contains(raw, `"mqttFailClosed":false`) {
		t.Errorf("the explicit off is no longer written to the document, so it would follow the platform default:\n%s", raw)
	}
}

// A default install is off, stays off, and writes nothing; turning it on later is
// recorded and kept.
func TestFailClosedDefaultInstallIsOffAndCanBeTurnedOnLater(t *testing.T) {
	t.Parallel()
	dyn := builtWith(t, nil)

	_, on, raw := upgradeRender(t, dyn, nil)
	if on || strings.Contains(raw, "mqttFailClosed") {
		t.Fatalf("a default install rendered on=%v with the key present=%v", on,
			strings.Contains(raw, "mqttFailClosed"))
	}

	st, on, _ := upgradeRender(t, dyn, boolPtr(true))
	if !on {
		t.Fatal("--mqtt-fail-closed on an existing instance rendered off")
	}
	st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
	if err := recordUpgradedVersion(t.Context(), dyn, "prod", st); err != nil {
		t.Fatal(err)
	}
	if _, on, _ = upgradeRender(t, dyn, nil); !on {
		t.Fatal("the flag was honoured once and forgotten by the next upgrade")
	}
}

// Repeating the flag with the value the declaration already holds writes nothing: the
// provenance annotations answer "when did something last change".
func TestFailClosedRepeatingTheRecordedValueIsANoOp(t *testing.T) {
	dyn := builtWith(t, boolPtr(true))
	st, _, _ := upgradeRender(t, dyn, boolPtr(true))
	if st.MqttFailClosedChange {
		t.Error("repeating the recorded value was marked as a change")
	}
}

// The refusal comes from the CLI, before anything exists, and names the way out.
func TestFailClosedIsRefusedWithoutTheAreasThatRunIt(t *testing.T) {
	if err := ValidateMqttFailClosed(boolPtr(true), "default", nil); err != nil {
		t.Errorf("the default profile was refused: %v", err)
	}
	for _, p := range []string{"telemetry", "ingest-only", "full"} {
		if err := ValidateMqttFailClosed(boolPtr(true), p, []string{"lwm2m-ingest"}); err != nil {
			t.Errorf("profile %s was refused: %v", p, err)
		}
	}
	// Off and never-chose are never refused, whatever the area set.
	for _, c := range []*bool{nil, boolPtr(false)} {
		if err := ValidateMqttFailClosed(c, "no-such-profile", nil); err != nil {
			t.Errorf("a choice that is not 'on' was judged: %v", err)
		}
	}
	// A profile this build does not know is refused rather than assumed to be fine.
	if err := ValidateMqttFailClosed(boolPtr(true), "no-such-profile", nil); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

// A bootstrap re-run that names no flag keeps the recorded choice; one that names a flag
// replaces it.
func TestARerunWithNoFailClosedFlagKeepsTheDeclaredChoice(t *testing.T) {
	run := func(t *testing.T, declared, flag *bool) (written *bool) {
		t.Helper()
		prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
		t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
		current := declared
		readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			inst := aDeclaration(dcv1beta1.PhaseReady, "")
			inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
			inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", MQTTFailClosed: current}
			return inst, nil
		}
		writeInstanceDeclaration = func(_ context.Context, _, _ string, spec dcv1beta1.InstanceSpec, _ string, _ bool) error {
			written, current = spec.MQTTFailClosed, spec.MQTTFailClosed
			return nil
		}
		st := &State{Instance: "prod", Provider: "local", Profile: "default", MqttFailClosed: flag}
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

func TestMqttFailClosedFromFlagDistinguishesNotGivenFromFalse(t *testing.T) {
	if MqttFailClosedFromFlag(false, true) != nil || MqttFailClosedFromFlag(false, false) != nil {
		t.Error("a flag that was not given produced a choice")
	}
	if v := MqttFailClosedFromFlag(true, false); v == nil || *v {
		t.Errorf("--mqtt-fail-closed=false produced %v", v)
	}
	if v := MqttFailClosedFromFlag(true, true); v == nil || !*v {
		t.Errorf("--mqtt-fail-closed produced %v", v)
	}
}

// 🔴 A CLUSTER WHOSE INSTANCE DEFINITION PREDATES THE FIELD PRUNES IT AND REPORTS SUCCESS. The
// run must say so, or the next flagless upgrade renders the default over the operator's choice.
func TestBootstrapRefusesADeclarationThatCameBackWithoutTheChoice(t *testing.T) {
	prevRead, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prevRead, prevWrite })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		inst := aDeclaration(dcv1beta1.PhaseReady, "")
		inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
		inst.Spec = dcv1beta1.InstanceSpec{Provider: "local", Profile: "default"} // the field was pruned
		return inst, nil
	}
	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error { return nil }
	st := &State{Instance: "prod", Provider: "local", Profile: "default", MqttFailClosed: boolPtr(true)}
	err := stepDeclareInstance(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), "does not record mqttFailClosed") || !strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("a pruned choice was not refused: %v", err)
	}
	// Never choosing is never a mismatch.
	st = &State{Instance: "prod", Provider: "local", Profile: "default"}
	if err := stepDeclareInstance(context.Background(), st); err != nil {
		t.Errorf("an instance that never chose was refused: %v", err)
	}
}

func TestUpgradeRefusesADeclarationThatCameBackWithoutTheChoice(t *testing.T) {
	dyn := builtWith(t, nil)
	dyn.PrependReactor("update", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if u, ok := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured); ok {
			unstructured.RemoveNestedField(u.Object, "spec", "mqttFailClosed")
		}
		return false, nil, nil
	})
	st, _, _ := upgradeRender(t, dyn, boolPtr(true))
	st.DcctlVersion, st.ImageRegistry, st.ImageVersion = "v0.25.0", "ghcr.io/devicechain-io", "v1.2.0"
	err := recordUpgradedVersion(t.Context(), dyn, "prod", st)
	if err == nil || !strings.Contains(err.Error(), "does not record mqttFailClosed") {
		t.Fatalf("a pruned choice was not refused: %v", err)
	}
}
