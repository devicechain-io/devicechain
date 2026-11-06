// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	tfjson "github.com/hashicorp/terraform-json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// writeStated puts variable files in a fresh instance-root directory, the way an operator does.
func writeStated(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hasVar(vars []string, name string) bool { return passedIn(vars, name) }

func hasExactly(vars []string, want string) bool {
	for _, v := range vars {
		if v == want {
			return true
		}
	}
	return false
}

// Precedence layer 5: a variable the operator states is not emitted by the preset at all.
func TestOperatorStatedVariablesAreNotEmittedByThePreset(t *testing.T) {
	dir := writeStated(t, map[string]string{
		"terraform.tfvars":                      "nats_memory_limit = \"12Gi\"\n",
		"zz-dcctl-event-store.auto.tfvars.json": `{"timescale_max_wal_size_mib": 4096}`,
	})
	got := infraVarsIn(perfState(false), dir)
	if hasVar(got, "nats_memory_limit") || hasVar(got, "timescale_max_wal_size_mib") {
		t.Errorf("a stated variable is still emitted: %v", got)
	}
	for _, n := range []string{"nats_cpu_request", "nats_memory_request", "nats_jetstream_storage", "timescale_storage"} {
		if !hasVar(got, n) {
			t.Errorf("%s was dropped though the operator stated nothing", n)
		}
	}
	// A non-preset instance is never filtered.
	b := persistenceState(false, true, "default")
	if len(infraVarsIn(b, dir)) != len(infraVars(b)) {
		t.Error("a non-preset instance had variables dropped")
	}
}

// An explicit --event-store-max-wal-mib on this run still reaches OpenTofu, over a recorded file.
func TestExplicitWalFlagReachesTofuOverAStatedFile(t *testing.T) {
	dir := writeStated(t, map[string]string{"zz-dcctl-event-store.auto.tfvars.json": `{"timescale_max_wal_size_mib": 4096}`})
	st := perfState(false)
	wal := 1024
	st.EventStoreMaxWALMiB = &wal
	var got []string
	for _, v := range infraVarsIn(st, dir) {
		if strings.HasPrefix(v, "timescale_max_wal_size_mib=") {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != "timescale_max_wal_size_mib=1024" {
		t.Errorf("WAL assignments %v, want only the flag's 1024", got)
	}
}

// A file that states a volume variable must not resize a LIVE volume: the live size is passed, as on
// the base path. Before the volume exists, the operator's statement stands.
func TestStatedVolumeVariablesNeverResizeALiveVolume(t *testing.T) {
	dir := writeStated(t, map[string]string{"terraform.tfvars": "nats_jetstream_storage = \"64Gi\"\ntimescale_storage = \"500Gi\"\n"})
	live := perfState(false)
	live.LiveVolumes = liveVolumes{JetStream: "28Gi", EventStore: "160Gi"}
	got := infraVarsIn(live, dir)
	if !hasExactly(got, "nats_jetstream_storage=28Gi") || !hasExactly(got, "timescale_storage=160Gi") {
		t.Errorf("a live volume's assignment was dropped for a stated file: %v", got)
	}
	got = infraVarsIn(perfState(false), dir)
	if hasVar(got, "nats_jetstream_storage") || hasVar(got, "timescale_storage") {
		t.Errorf("a first bootstrap overrode the operator's volume sizes: %v", got)
	}
}

// The UPGRADE caller: prepareUpgradePlan composes its -var list from the root directory it was
// given, so a stated variable is absent from what the plan runs with.
func TestUpgradePlanLeavesOperatorStatedVariablesAlone(t *testing.T) {
	rec := aCompleteInstall()
	st := perfState(false)
	st.Instance, st.KubeContext, st.Install = "prod", "gke_p_z_c", &rec
	st.Values["natsCA"] = "ca"
	dir := writeStated(t, map[string]string{"terraform.tfvars": "nats_memory_limit = \"12Gi\"\n"})
	tf := &fakeUpgradeTofu{plans: []*tfjson.Plan{{}}, state: &tfjson.State{}}
	p, err := prepareUpgradePlan(context.Background(), st, tf, dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasVar(p.vars, "nats_memory_limit") {
		t.Errorf("the upgrade passes a variable the operator stated: %v", p.vars)
	}
	if !hasVar(p.vars, "nats_cpu_request") {
		t.Errorf("the upgrade lost the preset's other variables: %v", p.vars)
	}
}

// The BOOTSTRAP caller needs a real tofu to run, so this holds its shape: applyInfra composes the
// variables from the opened root's directory, after opening it.
func TestApplyInfraComposesItsVariablesFromTheOpenedRoot(t *testing.T) {
	raw, err := os.ReadFile("tofu.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	a := strings.Index(src, "func applyInfra(")
	b := strings.Index(src[a:], "\n}\n")
	body := src[a : a+b]
	open := strings.Index(body, "openInstanceRoot(ctx, st)")
	compose := strings.Index(body, "instanceRootVarsIn(st, inst.rootdir)")
	if open < 0 || compose < 0 || compose < open {
		t.Errorf("applyInfra must call instanceRootVarsIn(st, inst.rootdir) after openInstanceRoot (open %d, compose %d)", open, compose)
	}
	if strings.Contains(body, "instanceRootVars(st)") {
		t.Error("applyInfra composes variables without the root directory")
	}
}

// statedByOperator reads the environment the way OpenTofu does, and file names the way the host
// file system does.
func TestStatedByOperatorMatchesTofu(t *testing.T) {
	t.Setenv("TF_VAR_nats_memory_limit", "12Gi")
	if !statedByOperator("", "nats_memory_limit") {
		t.Error("TF_VAR_ not detected")
	}
	if statedByOperator("", "NATS_MEMORY_LIMIT") {
		t.Error("variable names are case sensitive")
	}
	t.Setenv("TF_VAR_timescale_storage", "")
	if statedByOperator("", "timescale_storage") {
		t.Error("an empty TF_VAR_ counts as stated")
	}
	if runtime.GOOS != "windows" {
		t.Setenv("tf_var_nats_cpu_request", "9")
		if statedByOperator("", "nats_cpu_request") {
			t.Error("a lower-case tf_var_ prefix is not read by tofu")
		}
	}
	dir := writeStated(t, map[string]string{"Terraform.tfvars": "nats_cpu_request = \"9\"\n"})
	want := runtime.GOOS == "windows"
	if got := statedByOperator(dir, "nats_cpu_request"); got != want {
		t.Errorf("Terraform.tfvars stated=%v on %s, want %v", got, runtime.GOOS, want)
	}
}

// A pruned spec.size must stop the declare step, not build base after printing perf-12k.
func TestADeclarationThatPrunedTheSizeStopsTheRun(t *testing.T) {
	prev, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prev, prevWrite })
	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error { return nil }
	mk := func(size string) *dcv1beta1.Instance {
		return &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "7f0d4f2e-0000-4000-8000-000000000001"},
			Spec: dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default", Size: size}}
	}
	readBack := func(size string) {
		calls := 0
		readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			calls++
			if calls == 1 {
				return nil, nil // nothing declared yet
			}
			return mk(size), nil
		}
	}
	// The cluster's definition predates the field: it comes back with no size.
	readBack("")
	err := stepDeclareInstance(context.Background(), &State{Instance: "prod", Provider: "kind", Profile: "default", Size: SizePerf12k})
	if err == nil || !strings.Contains(err.Error(), "dcctl install") {
		t.Fatalf("a pruned size was accepted: %v", err)
	}
	// A cluster that records it is fine.
	readBack(SizePerf12k)
	if err := stepDeclareInstance(context.Background(), &State{Instance: "prod", Provider: "kind", Profile: "default", Size: SizePerf12k}); err != nil {
		t.Fatal(err)
	}
	// Base, and a compact instance on an old definition, are not judged: nothing was asked to be kept.
	readBack("")
	if err := stepDeclareInstance(context.Background(), &State{Instance: "prod", Provider: "kind", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
}

// An upgrade stops on a size this build does not know, rather than rendering base.
func TestUpgradeRefusesAnUnknownRecordedSize(t *testing.T) {
	inst := &dcv1beta1.Instance{Spec: dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", Size: "perf-99k"}}
	if err := applyUpgradeDeclaration(&State{Instance: "i"}, inst, UpgradeOptions{}); err == nil ||
		!strings.Contains(err.Error(), "perf-99k") {
		t.Fatalf("an unknown recorded size was accepted: %v", err)
	}
}
