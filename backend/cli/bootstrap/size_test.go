// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// perfState is a State a real bootstrap would hand the Helm and infra steps, sized perf-12k.
func perfState(ha bool) *State {
	st := persistenceState(ha, false, "default")
	st.Size = SizePerf12k
	return st
}

func qty(t *testing.T, s string) *resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	if err != nil {
		t.Fatalf("quantity %q: %v", s, err)
	}
	return &q
}

func TestValidateSize(t *testing.T) {
	for _, ok := range []string{"", "base", "compact", "perf-12k"} {
		if err := ValidateSize(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	// perf-20k is the named TARGET, not a shipped size: the name is the claim.
	for _, bad := range []string{"perf-20k", "PERF-12K", "large", "perf"} {
		if err := ValidateSize(bad); err == nil {
			t.Errorf("%q accepted: a size ships only once a round measured it clean", bad)
		}
	}
	for _, n := range SizeNames() {
		if n == "perf-20k" {
			t.Error("perf-20k is listed as a size")
		}
	}
}

// Compact stays the cluster's setting, and the flag stays an alias.
func TestCheckSizeAgainstCluster(t *testing.T) {
	cases := []struct {
		name        string
		compact     bool
		size        string
		want        string // resulting State.Size
		wantRefused string
	}{
		{"nothing", false, "", "", ""},
		{"base", false, "base", "", ""},
		{"perf", false, "perf-12k", "perf-12k", ""},
		{"compact cluster, nothing", true, "", "", ""},
		{"compact cluster, compact", true, "compact", "", ""},
		{"compact cluster, base", true, "base", "", ""},
		{"compact name on a standard cluster", false, "compact", "", "whole cluster"},
		{"perf on a compact cluster", true, "perf-12k", "", "compact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{Compact: tc.compact, Size: tc.size}
			err := CheckSizeAgainstCluster(st)
			if tc.wantRefused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantRefused) {
					t.Fatalf("err %v, want a refusal naming %q", err, tc.wantRefused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if st.Size != tc.want {
				t.Errorf("Size %q, want %q", st.Size, tc.want)
			}
		})
	}
}

func TestSizeFor(t *testing.T) {
	if sizeFor(&State{}) != nil || sizeFor(&State{Size: SizeBase}) != nil {
		t.Error("base has a preset table row")
	}
	if sizeFor(&State{Size: SizePerf12k}) == nil {
		t.Error("perf-12k has no preset")
	}
	// A compact instance runs compactSizing, whatever the size field says.
	if sizeFor(&State{Compact: true, Size: SizePerf12k}) != nil {
		t.Error("a compact cluster renders a perf preset")
	}
	if effectiveSize(&State{Compact: true}) != SizeCompact || effectiveSize(&State{}) != SizeBase ||
		effectiveSize(&State{Size: SizePerf12k}) != SizePerf12k {
		t.Error("effectiveSize misreads the state")
	}
}

// The preset reaches the pods: every stated number is in the rendered Deployment.
func TestPerf12kReachesThePods(t *testing.T) {
	t.Parallel()
	byA := byArea(t, renderContainers(t, helmValues(perfState(false))))
	p := sizePresets[SizePerf12k]
	for area, want := range p.Areas {
		c, ok := byA[area]
		if !ok {
			t.Fatalf("%s did not render", area)
		}
		if want.CPULimit != "" && qty(t, c.limits["cpu"]).Cmp(*qty(t, want.CPULimit)) != 0 {
			t.Errorf("%s cpu limit %s, want %s", area, c.limits["cpu"], want.CPULimit)
		}
		if want.MemLimit != "" && qty(t, c.limits["memory"]).Cmp(*qty(t, want.MemLimit)) != 0 {
			t.Errorf("%s memory limit %s, want %s", area, c.limits["memory"], want.MemLimit)
		}
		if want.CPURequest != "" && qty(t, c.requests["cpu"]).Cmp(*qty(t, want.CPURequest)) != 0 {
			t.Errorf("%s cpu request %s, want %s", area, c.requests["cpu"], want.CPURequest)
		}
	}
	manifest, err := renderChart(t, helmValues(perfState(false)))
	if err != nil {
		t.Fatal(err)
	}
	areas := renderedAreas(t, manifest)
	for area, want := range p.Areas {
		if want.Replicas > 0 && areas[area].Replicas != want.Replicas {
			t.Errorf("%s replicas %d, want %d", area, areas[area].Replicas, want.Replicas)
		}
	}
}

// 🔴 A preset that states an explicit memory limit BYPASSES the chart floor, so the floor is held
// here: every preset limit is at least the floor it would otherwise have had, with envelopes on and
// off, and what GOMEMLIMIT derives from it is 75 percent.
func TestPresetLimitsHoldTheFloors(t *testing.T) {
	t.Parallel()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	floorOf := func(area, key string) string {
		fa, _ := ch.Values["functionalAreas"].(map[string]interface{})
		a, _ := fa[area].(map[string]interface{})
		s, _ := a[key].(string)
		return s
	}
	for _, envelopesOn := range []bool{false, true} {
		vals := helmValues(perfState(false))
		if envelopesOn {
			mergeOverlay(vals, envelopes(true, true))
		}
		got := byArea(t, renderContainers(t, vals))
		for area, want := range sizePresets[SizePerf12k].Areas {
			floor := floorOf(area, "memoryLimitFloor")
			if envelopesOn {
				if f := floorOf(area, "memoryLimitFloorWithEnvelopes"); f != "" {
					floor = f
				}
			}
			c := got[area]
			if floor != "" && qty(t, c.memoryLimit).Cmp(*qty(t, floor)) < 0 {
				t.Errorf("%s (envelopes %v): rendered limit %s is under the floor %s", area, envelopesOn, c.memoryLimit, floor)
			}
			if want.MemLimit != "" && floor != "" && qty(t, want.MemLimit).Cmp(*qty(t, floor)) < 0 {
				t.Errorf("%s (envelopes %v): the preset states %s, under the floor %s it bypasses", area, envelopesOn, want.MemLimit, floor)
			}
			if c.goMemLimit != "" {
				lim := qty(t, c.memoryLimit)
				if mib(t, c.goMemLimit) != lim.Value()/(1<<20)*75/100 {
					t.Errorf("%s: GOMEMLIMIT %s is not 75%% of %s", area, c.goMemLimit, c.memoryLimit)
				}
			}
		}
	}
	// device-management keeps its cache bound inside its limit: 2 x (3 x perDeviceCacheMiB 80 +
	// credentialCacheMiB 32 + 36 + 60) MiB, the chart's own sizing rule for the default bound.
	dm := byArea(t, renderContainers(t, helmValues(perfState(false))))["device-management"]
	if need := int64(2 * (3*80 + 32 + 36 + 60)); qty(t, dm.memoryLimit).Value()>>20 < need {
		t.Errorf("device-management limit %s is under %d MiB for its cache bound", dm.memoryLimit, need)
	}
}

// sizeEvidence is a measured figure a preset's number is held against: the resident memory or CPU a
// service used at the preset's rate, and the round that measured it.
type sizeEvidence struct {
	area       string
	round      string
	rssMiB     int64   // resident maximum
	cpuCores   float64 // average cores
	placeholer bool
}

// perf12kEvidence is the measured basis of perf-12k (GKE rounds 29 and 30 at 12,000 events a second).
var perf12kEvidence = []sizeEvidence{
	{area: "event-processing", round: "r30", rssMiB: 547, cpuCores: 0.443},
	{area: "device-management", round: "r30", rssMiB: 320, cpuCores: 0.97},
	{area: "event-sources", round: "r30", rssMiB: 117, cpuCores: 1.46},
	{area: "device-state", round: "r30", rssMiB: 107, cpuCores: 0.34},
	{area: "event-management", round: "r30", rssMiB: 87, cpuCores: 0.29},
}

// The limits hold the working set the rounds measured: memory at 1.5 times the resident maximum,
// and the CPU limit at 1.5 times the average use per replica.
func TestPresetLimitsHoldTheMeasuredWorkingSet(t *testing.T) {
	t.Parallel()
	got := byArea(t, renderContainers(t, helmValues(perfState(false))))
	p := sizePresets[SizePerf12k]
	for _, e := range perf12kEvidence {
		c := got[e.area]
		if lim := qty(t, c.memoryLimit).Value() >> 20; float64(lim) < 1.5*float64(e.rssMiB) {
			t.Errorf("%s (%s): memory limit %d MiB is under 1.5 x the %d MiB resident maximum", e.area, e.round, lim, e.rssMiB)
		}
		replicas := p.Areas[e.area].Replicas
		if replicas == 0 {
			replicas = 1
		}
		if cpu := qty(t, c.limits["cpu"]).AsApproximateFloat64(); cpu < 1.5*e.cpuCores/float64(replicas) {
			t.Errorf("%s (%s): cpu limit %.2f is under 1.5 x %.2f cores over %d replicas", e.area, e.round, cpu, e.cpuCores, replicas)
		}
		// A request sits at or under its limit, or the chart refuses the pod.
		if qty(t, c.requests["cpu"]).Cmp(*qty(t, c.limits["cpu"])) > 0 {
			t.Errorf("%s: cpu request %s above its limit %s", e.area, c.requests["cpu"], c.limits["cpu"])
		}
	}
	// NATS: the soft limit (80 percent) holds the 3.0 GiB working set with the documented 0.9 margin.
	if soft := qty(t, p.NatsMemoryLimit).AsApproximateFloat64() * 0.8; soft < 3.0*(1<<30)/0.9 {
		t.Errorf("nats memory limit %s: soft limit %.1f GiB is under the 3.0 GiB working set / 0.9", p.NatsMemoryLimit, soft/(1<<30))
	}
	if qty(t, p.NatsMemoryRequest).Cmp(*qty(t, p.NatsMemoryLimit)) > 0 {
		t.Error("nats memory request above its limit")
	}
}

// The design rate the preset states reaches the config a service reads, and the reservation it
// implies fits the preset's own volume, with fail-closed off and on.
func TestPresetReservationFitsItsVolume(t *testing.T) {
	p := sizePresets[SizePerf12k]
	for _, failClosed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail-closed=%v", failClosed), func(t *testing.T) {
			st := perfState(false)
			if failClosed {
				on := true
				st.MqttFailClosed = &on
			}
			n := &renderFromState(t, st).Infrastructure.Nats
			if n.StreamDesignRate != p.StreamDesignRate {
				t.Fatalf("design rate %d reached the config, want %d", n.StreamDesignRate, p.StreamDesignRate)
			}
			ceiling, err := derivedMaxFileStore(p.JetStreamStorage)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkJetStreamReservation(n, ceiling, "the "+p.JetStreamStorage+" volume", true); err != nil {
				t.Errorf("perf-12k's reservation does not fit its own volume: %v", err)
			}
			// Tight on purpose: a volume a whole 25 percent over the need means the number was not derived.
			if need := requiredVolumeGi(jetStreamReservation(n)); float64(qty(t, p.JetStreamStorage).Value()>>30) > 1.25*float64(need)+2 {
				t.Errorf("JetStream volume %s is far over the %dGi the reservation needs", p.JetStreamStorage, need)
			}
		})
	}
}

// The WAL allowance leaves the volume room to peak at about 1.9 times it.
func TestPresetWalFitsItsVolume(t *testing.T) {
	p := sizePresets[SizePerf12k]
	vol := qty(t, p.TimescaleStorage).Value() >> 20
	if int64(p.TimescaleMaxWALMiB) > vol/4 {
		t.Errorf("max_wal_size %d MiB is over a quarter of the %d MiB event store volume", p.TimescaleMaxWALMiB, vol)
	}
	if p.TimescaleMaxWALMiB < minMaxWALMiB {
		t.Errorf("max_wal_size %d MiB is under the root's floor", p.TimescaleMaxWALMiB)
	}
}

// Precedence layer 3 over layer 2: what dcctl computes beats the preset, and the preset beats the
// chart.
func TestHAReplicaCountsBeatThePreset(t *testing.T) {
	t.Parallel()
	ha := helmValues(perfState(true))
	fa := ha["functionalAreas"].(map[string]interface{})
	// event-management's replicas are dcctl's under --ha (persistenceFor), never the preset's.
	if r := fa["event-management"].(map[string]interface{})["replicas"]; fmt.Sprint(r) != "2" {
		t.Errorf("--ha event-management replicas %v, want dcctl's 2", r)
	}
	// ... while the preset's own numbers remain where dcctl states nothing.
	if r := fa["event-sources"].(map[string]interface{})["replicas"]; fmt.Sprint(r) != "3" {
		t.Errorf("event-sources replicas %v, want the preset's 3", r)
	}
	// The messaging half of HA still wins on the nats map next to the preset's design rate.
	nats := ha["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})["nats"].(map[string]interface{})
	if fmt.Sprint(nats["streamReplicas"]) != "3" {
		t.Errorf("streamReplicas %v under --ha, want 3", nats["streamReplicas"])
	}
	if fmt.Sprint(nats["streamDesignRate"]) != "12000" {
		t.Errorf("streamDesignRate %v, want the preset's 12000", nats["streamDesignRate"])
	}
	// The broker-auth material survives the preset merge.
	if _, ok := nats["auth"]; !ok {
		t.Error("the preset merge dropped the broker auth block")
	}
}

// Precedence layer 4: the --values overlay beats the preset, key by key, and leaves the rest.
func TestOverlayBeatsThePreset(t *testing.T) {
	t.Parallel()
	st := perfState(false)
	st.ValuesOverlay = map[string]interface{}{"functionalAreas": map[string]interface{}{
		"event-processing": map[string]interface{}{"resources": map[string]interface{}{
			"limits": map[string]interface{}{"memory": "2Gi"}}},
		"event-sources": map[string]interface{}{"replicas": 5},
	}}
	vals := authoringValues(st)
	fa := vals["functionalAreas"].(map[string]interface{})
	ep := fa["event-processing"].(map[string]interface{})["resources"].(map[string]interface{})["limits"].(map[string]interface{})
	if ep["memory"] != "2Gi" {
		t.Errorf("event-processing memory %v, want the overlay's 2Gi", ep["memory"])
	}
	if ep["cpu"] != "2" {
		t.Errorf("event-processing cpu %v: the overlay replaced the preset's sibling key", ep["cpu"])
	}
	if r := fmt.Sprint(fa["event-sources"].(map[string]interface{})["replicas"]); r != "5" {
		t.Errorf("event-sources replicas %s, want the overlay's 5", r)
	}
	if r := fmt.Sprint(fa["device-state"].(map[string]interface{})["replicas"]); r != "2" {
		t.Errorf("device-state replicas %s, want the preset's 2", r)
	}
	// The connection budget reads the same precedence: the overlay's replicas, then the preset's.
	st.ValuesOverlay = nil
	withPreset, err := instanceConnectionLimit(st)
	if err != nil {
		t.Fatal(err)
	}
	base := perfState(false)
	base.Size = ""
	without, err := instanceConnectionLimit(base)
	if err != nil {
		t.Fatal(err)
	}
	if withPreset <= without {
		t.Errorf("connection limit %d with perf-12k is not above base's %d: device-management runs two pods", withPreset, without)
	}
}

func sizeVars(t *testing.T, st *State) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, v := range infraVars(st) {
		name, val, _ := strings.Cut(v, "=")
		out[name] = val
	}
	return out
}

func TestPerf12kTofuVars(t *testing.T) {
	vars := sizeVars(t, perfState(false))
	p := sizePresets[SizePerf12k]
	for name, want := range map[string]string{
		"nats_cpu_request":           p.NatsCPURequest,
		"nats_memory_request":        p.NatsMemoryRequest,
		"nats_memory_limit":          p.NatsMemoryLimit,
		"nats_jetstream_storage":     p.JetStreamStorage,
		"timescale_storage":          p.TimescaleStorage,
		"timescale_max_wal_size_mib": strconv.Itoa(p.TimescaleMaxWALMiB),
	} {
		if vars[name] != want {
			t.Errorf("%s = %q, want %q", name, vars[name], want)
		}
	}
	// Every name is one the instance root declares: splitVars refuses an orphan.
	if _, instance, err := splitVars(infraVars(perfState(false))); err != nil || len(instance) == 0 {
		t.Fatalf("splitVars: %v", err)
	}
	// Base states none of them.
	base := sizeVars(t, persistenceState(false, false, "default"))
	for name := range vars {
		if _, ok := base[name]; ok && name != "timescale_storage" && name != "nats_jetstream_storage" {
			continue
		}
	}
	for _, n := range []string{"nats_memory_limit", "timescale_max_wal_size_mib", "nats_cpu_request"} {
		if _, ok := base[n]; ok {
			t.Errorf("base passes %s", n)
		}
	}
}

// A volume the instance already has is kept, as on the non-preset path: an apply never resizes one.
func TestPerf12kKeepsLiveVolumes(t *testing.T) {
	st := perfState(false)
	st.LiveVolumes = liveVolumes{JetStream: "16Gi", EventStore: "400Gi"}
	vars := sizeVars(t, st)
	if vars["nats_jetstream_storage"] != "16Gi" || vars["timescale_storage"] != "400Gi" {
		t.Errorf("live volumes not kept: %v", vars)
	}
	if got := releaseVolumeSize(st, "nats_jetstream_storage"); got != "28Gi" {
		t.Errorf("release volume %s, want the preset's 28Gi", got)
	}
}

// Precedence layer 5: a variable the operator states is not passed at all.
func TestOperatorStatedVariablesBeatThePreset(t *testing.T) {
	p := sizeFor(perfState(false))
	all := []string{"kubeconfig_context=x"}
	for _, v := range p.tofuVars("", "") {
		all = append(all, v.Name+"="+v.Value)
	}
	stated := map[string]bool{"nats_memory_limit": true, "timescale_max_wal_size_mib": true, "not_a_preset_var": true}
	got := p.dropOperatorStated(all, func(n string) bool { return stated[n] })
	has := func(name string) bool { return passedIn(got, name) }
	if has("nats_memory_limit") || has("timescale_max_wal_size_mib") {
		t.Errorf("a stated variable was still passed: %v", got)
	}
	if !has("nats_cpu_request") || !has("timescale_storage") || !has("kubeconfig_context") {
		t.Errorf("an unstated variable was dropped: %v", got)
	}
	// A nil preset (base, compact) filters nothing, including a variable dcctl owns.
	if got := (*sizePreset)(nil).dropOperatorStated(all, func(string) bool { return true }); len(got) != len(all) {
		t.Error("base dropped variables")
	}
	// And through the real detector: TF_VAR_ stated, then not.
	t.Setenv("TF_VAR_nats_memory_limit", "12Gi")
	if !statedByOperator(t.TempDir(), "nats_memory_limit") {
		t.Fatal("TF_VAR_ not detected")
	}
}

func TestSizeNoticesNameWhatWasNotMeasured(t *testing.T) {
	st := perfState(true)
	n := strings.Join(sizeNotices(st, false), "|")
	if !strings.Contains(n, "envelopes") || !strings.Contains(n, "--ha") {
		t.Errorf("notices %q miss the envelope or HA caveat", n)
	}
	if n := sizeNotices(perfState(false), true); len(n) != 0 {
		t.Errorf("unexpected notices %v", n)
	}
	if sizeNotices(persistenceState(false, false, "default"), false) != nil {
		t.Error("base prints notices")
	}
	if !strings.Contains(sizeFor(st).summary(), "perf-12k") {
		t.Error("summary does not name the size")
	}
}

// The declaration records the size, reads it back, and refuses what this build cannot judge.
func TestSizeIsRecordedAndReadBack(t *testing.T) {
	st := perfState(false)
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	if spec.Size != SizePerf12k {
		t.Fatalf("spec.size %q, want perf-12k", spec.Size)
	}
	back := &State{}
	applyDeclaration(back, spec)
	if back.Size != SizePerf12k || back.Compact {
		t.Errorf("read back size %q compact %v", back.Size, back.Compact)
	}
	// Base records nothing: the declaration of every instance built before the axis existed.
	if InstanceSpecFrom(persistenceState(false, false, "default"), ClusterBinding{}, "local").Size != "" {
		t.Error("base records a size")
	}
	// Compact records its name beside compact: true, and reads back with no preset.
	cspec := InstanceSpecFrom(persistenceState(false, true, "default"), ClusterBinding{}, "local")
	if cspec.Size != SizeCompact || !cspec.Compact {
		t.Errorf("compact spec: size %q compact %v", cspec.Size, cspec.Compact)
	}
	cback := &State{}
	applyDeclaration(cback, cspec)
	if cback.Size != "" || !cback.Compact {
		t.Errorf("compact read back: size %q compact %v", cback.Size, cback.Compact)
	}
	// A declaration made before the field existed has no size and stays valid.
	old := dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", Compact: true}
	if err := ValidateInstanceSpec(old); err != nil {
		t.Errorf("a pre-size compact declaration is refused: %v", err)
	}
	for name, bad := range map[string]dcv1beta1.InstanceSpec{
		"unknown size":             {Provider: "local", Profile: "default", Size: "perf-99k"},
		"compact size, not":        {Provider: "local", Profile: "default", Size: "compact"},
		"compact with a perf size": {Provider: "local", Profile: "default", Compact: true, Size: "perf-12k"},
	} {
		if err := ValidateInstanceSpec(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// An upgrade re-applies the recorded size: the same state, rebuilt from the declaration, renders the
// same chart values.
func TestUpgradeReappliesTheRecordedSize(t *testing.T) {
	t.Parallel()
	st := perfState(false)
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	up := persistenceState(false, false, "default")
	applyDeclaration(up, spec)
	a, _ := json.Marshal(helmValues(st))
	b, _ := json.Marshal(helmValues(up))
	if string(a) != string(b) {
		t.Error("an upgrade rebuilt from the declaration renders differently from the bootstrap")
	}
	if jetStreamStorageFor(up) != "28Gi" {
		t.Errorf("jetStreamStorageFor %s, want the preset's 28Gi", jetStreamStorageFor(up))
	}
}

// The caller, not just the function: the variables both applies run with, filtered against a real
// directory of the operator's files.
func TestOperatorWinsOverSizeReadsTheRootDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte("nats_memory_limit = \"12Gi\""+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zz-dcctl-event-store.auto.tfvars.json"), []byte(`{"timescale_max_wal_size_mib": 4096}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := perfState(false)
	got := operatorWinsOverSize(st, infraVars(st), dir)
	if passedIn(got, "nats_memory_limit") || passedIn(got, "timescale_max_wal_size_mib") {
		t.Errorf("a variable the operator states is still passed: %v", got)
	}
	for _, n := range []string{"nats_cpu_request", "nats_jetstream_storage", "timescale_storage"} {
		if !passedIn(got, n) {
			t.Errorf("%s was dropped though the operator stated nothing", n)
		}
	}
	// Base is never filtered: the variables it passes (the compact preset's, a live volume) are dcctl's.
	b := persistenceState(false, true, "default")
	if all := infraVars(b); len(operatorWinsOverSize(b, all, dir)) != len(all) {
		t.Error("a non-preset instance had variables dropped")
	}
}

// The merge ORDER, tested where the two layers really do disagree. perf-12k states no key dcctl also
// states, so a test on it alone cannot tell "dcctl over the preset" from "preset over dcctl". This one
// registers a throwaway preset that states event-management's replicas, which --ha decides itself.
// Serial on purpose: it edits the table, and the parallel tests only resume after the serial ones.
func TestDcctlComputedValuesBeatAPresetThatDisagrees(t *testing.T) {
	sizePresets["zz-conflict"] = sizePreset{Name: "zz-conflict", Areas: map[string]areaSize{
		"event-management": {Replicas: 1},
		"event-sources":    {Replicas: 7},
	}, StreamDesignRate: 99}
	t.Cleanup(func() { delete(sizePresets, "zz-conflict") })

	st := persistenceState(true, false, "default")
	st.Size = "zz-conflict"
	fa := helmValues(st)["functionalAreas"].(map[string]interface{})
	if r := fmt.Sprint(fa["event-management"].(map[string]interface{})["replicas"]); r != "2" {
		t.Errorf("--ha event-management replicas %s: the preset's 1 beat dcctl's 2", r)
	}
	if r := fmt.Sprint(fa["event-sources"].(map[string]interface{})["replicas"]); r != "7" {
		t.Errorf("event-sources replicas %s: the preset's own 7 was lost", r)
	}
	// The design rate is a preset value dcctl states through the same key: the preset's, not the default.
	nats := helmValues(st)["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})["nats"].(map[string]interface{})
	if fmt.Sprint(nats["streamDesignRate"]) != "99" {
		t.Errorf("streamDesignRate %v, want the preset's 99", nats["streamDesignRate"])
	}
}

// An explicit --event-store-max-wal-mib on this run is the operator speaking now: it survives the
// filter that removes the preset's own assignment, even when a file also states the variable.
func TestExplicitWalFlagSurvivesTheSizeFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zz-dcctl-event-store.auto.tfvars.json"), []byte(`{"timescale_max_wal_size_mib": 4096}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := perfState(false)
	wal := 1024
	st.EventStoreMaxWALMiB = &wal
	var got []string
	for _, v := range operatorWinsOverSize(st, infraVars(st), dir) {
		if strings.HasPrefix(v, "timescale_max_wal_size_mib=") {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != "timescale_max_wal_size_mib=1024" {
		t.Errorf("WAL assignments %v, want only the flag's 1024", got)
	}
	// Without the flag, the recorded file value is left to win: nothing is passed.
	st.EventStoreMaxWALMiB = nil
	for _, v := range operatorWinsOverSize(st, infraVars(st), dir) {
		if strings.HasPrefix(v, "timescale_max_wal_size_mib=") {
			t.Errorf("the preset's WAL allowance is still passed over a recorded one: %s", v)
		}
	}
}

// A retry of an interrupted bootstrap keeps the instance's size; naming another is refused.
func TestARerunKeepsTheRecordedSize(t *testing.T) {
	rec := dcv1beta1.InstanceSpec{Provider: "local", Profile: "default", Size: SizePerf12k}
	cases := []struct {
		name    string
		given   string
		compact bool
		want    string
		refused bool
	}{
		{"no flag keeps the size", "", false, SizePerf12k, false},
		{"same size", SizePerf12k, false, SizePerf12k, false},
		{"base over a perf instance", SizeBase, false, "", true},
		{"compact cluster is not judged here", "", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{Instance: "i", Compact: tc.compact, Size: tc.given}
			if tc.given != "" && !tc.compact {
				if err := CheckSizeAgainstCluster(st); err != nil {
					t.Fatal(err)
				}
			}
			err := adoptRecordedSize(st, rec)
			if (err != nil) != tc.refused {
				t.Fatalf("err %v, refused want %v", err, tc.refused)
			}
			if !tc.refused && st.Size != tc.want {
				t.Errorf("Size %q, want %q", st.Size, tc.want)
			}
		})
	}
	// An instance recorded with no size, re-run with no flag, stays base.
	st := &State{Instance: "i"}
	if err := adoptRecordedSize(st, dcv1beta1.InstanceSpec{Provider: "local", Profile: "default"}); err != nil || st.Size != "" {
		t.Errorf("a base instance re-run: size %q err %v", st.Size, err)
	}
}

// The CALLER of adoptRecordedSize: a bootstrap retry over a perf-12k declaration, through the real
// declare step, writes perf-12k back and not base.
func TestTheDeclareStepKeepsARecordedSize(t *testing.T) {
	prev, prevWrite := readInstanceDeclaration, writeInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration = prev, prevWrite })
	var written []dcv1beta1.InstanceSpec
	writeInstanceDeclaration = func(_ context.Context, _, _ string, spec dcv1beta1.InstanceSpec, _ string, _ bool) error {
		written = append(written, spec)
		return nil
	}
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return &dcv1beta1.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "7f0d4f2e-0000-4000-8000-000000000001"},
			Spec:       dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default", Size: SizePerf12k},
		}, nil
	}
	st := &State{Instance: "prod", Provider: "kind", Profile: "default"}
	if err := stepDeclareInstance(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 || written[len(written)-1].Size != SizePerf12k {
		t.Errorf("a retry wrote %+v over a perf-12k declaration", written)
	}
	if st.Size != SizePerf12k {
		t.Errorf("the run's size is %q after the read-back", st.Size)
	}
}
