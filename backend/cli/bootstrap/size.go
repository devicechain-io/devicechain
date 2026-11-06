// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"sort"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The size axis: `--size base|compact|perf-12k`.
//
// A size is a PRESET, not a dial, by the same rules compact.go states: it composes levers that
// already exist, never changes which services run (that stays on the profile axis), and never
// turns a behaviour on (event envelopes, fail-closed). It is orthogonal to --ha, which multiplies
// replicas while the preset sizes each copy.
//
//   - base is the default and renders exactly what dcctl rendered before this axis existed.
//   - compact is the --compact preset (compact.go), reachable as `--size compact` as well; the flag
//     stays as a permanent alias. Its sizing is still compactSizing, so its tests are unchanged.
//   - perf-12k is the measured high-throughput shape: one cluster, no --ha, sustained 12,000
//     events a second with event envelopes on (GKE rounds 29 and 30).
//
// 🔴 THE NAME IS THE CLAIM. A size ships only once a round measured it clean, which is why there is
// no perf-20k here: round 30 collapsed at 20,000 and the preset waits for a round that passes.
//
// Precedence, lowest to highest, each layer deep-merged over the one before:
//
//  1. chart defaults and OpenTofu variable defaults;
//  2. the size preset (this file);
//  3. what dcctl computes: HA replica counts, the envelope switch, the area set, live volume sizes;
//  4. the --values overlay;
//  5. an OpenTofu variable the operator states (TF_VAR_*, terraform.tfvars, *.auto.tfvars): such a
//     variable is NOT passed by dcctl at all, because OpenTofu takes a -var over all of those.
const (
	SizeBase    = "base"
	SizeCompact = "compact"
	SizePerf12k = "perf-12k"
)

// sizeNames is every name `--size` accepts, in the order help prints them.
var sizeNames = []string{SizeBase, SizeCompact, SizePerf12k}

// SizeNames is the accepted --size values, for the command layer's help and errors.
func SizeNames() []string { return append([]string(nil), sizeNames...) }

// areaSize is what a preset states for one functional area. A zero field is NOT STATED, and the
// chart's own value (including its memory floors) applies.
type areaSize struct {
	Replicas   int
	CPULimit   string
	MemLimit   string
	CPURequest string
}

// sizePreset is one non-compact size, as typed values so the tests can read the numbers back out
// instead of parsing a rendering.
type sizePreset struct {
	Name string

	// Areas, keyed by functional area name. An area the instance does not run is inert.
	Areas map[string]areaSize

	// StreamDesignRate is the events a second the hot event streams are sized to hold for the
	// default runway, written to infrastructure.nats.streamDesignRate.
	StreamDesignRate int64

	// The broker (per server) and the volumes, as OpenTofu quantity strings.
	NatsCPURequest    string
	NatsMemoryRequest string
	NatsMemoryLimit   string
	JetStreamStorage  string
	TimescaleStorage  string
	// TimescaleMaxWALMiB is the event store's max_wal_size, in MiB.
	TimescaleMaxWALMiB int

	// EnvelopesMeasured says the figures were measured with event envelopes on, which the notices
	// repeat when they are off.
	EnvelopesMeasured bool
}

// sizePresets are the sizes with a measured basis. base and compact are not rows here: base states
// nothing, compact is compactSizing.
//
// perf-12k, from GKE rounds 29 and 30 (non-HA, one broker server, 48 vCPU, envelopes on; 12,000
// events a second clean: PUBACK p99 25 ms, 0.734 CPU-s per 1,000 events, exact by the oracle). The
// derivations, per knob:
//
//   - event-sources 3 x 3 CPU / 512Mi, device-management 2 x 3 CPU, device-state 2 x 3 CPU / 512Mi,
//     event-management 4 CPU / 1Gi, event-processing 2 CPU / 1Gi: the shape round 30 ran. Requests
//     are CPU-seconds per 1,000 events at 12,000 / replicas x 1.2-1.5, rounded up to 50m.
//   - device-management states no memory limit: the chart floor (768Mi) is the measured one, and
//     an explicit limit would bypass it.
//   - event-processing 1Gi: resident 547 MiB at 12,000 events a second x 1.5, rounded up.
//   - NATS 8Gi: the working set was 3.0 GiB at 12,000 (0.35 GiB per 1,000 events a second needs
//     4.2Gi) with a margin of two. The clean MEASUREMENT was at 20Gi; 8Gi is derived and the next
//     round confirms it.
//   - JetStream and event store volumes: the runway rule at 12,000 (see size_test.go), and a first
//     estimate of bytes per event pending the next round's hypertable growth figure.
//   - max_wal_size 20480 MiB: 1.9 x 900 s x 10.8 MB/s (round 30 at 12,000).
//
// What is NOT here yet, because it needs a variable the cluster root does not expose or a
// measurement: the relational max_wal_size and storage, CNPG resources, Prometheus sizing and the
// GKE node pools.
var sizePresets = map[string]sizePreset{
	SizePerf12k: {
		Name: SizePerf12k,
		Areas: map[string]areaSize{
			"event-sources":     {Replicas: 3, CPULimit: "3", MemLimit: "512Mi", CPURequest: "600m"},
			"device-management": {Replicas: 2, CPULimit: "3", CPURequest: "500m"},
			"device-state":      {Replicas: 2, CPULimit: "3", MemLimit: "512Mi", CPURequest: "250m"},
			"event-management":  {CPULimit: "4", MemLimit: "1Gi", CPURequest: "400m"},
			"event-processing":  {CPULimit: "2", MemLimit: "1Gi", CPURequest: "500m"},
		},
		StreamDesignRate:   12000,
		NatsCPURequest:     "4",
		NatsMemoryRequest:  "4Gi",
		NatsMemoryLimit:    "8Gi",
		JetStreamStorage:   "28Gi",
		TimescaleStorage:   "160Gi",
		TimescaleMaxWALMiB: 20480,
		EnvelopesMeasured:  true,
	},
}

// ValidateSize judges a --size value. "" is base.
func ValidateSize(name string) error {
	if name == "" {
		return nil
	}
	for _, n := range sizeNames {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("unknown size %q (one of %s)", name, strings.Join(sizeNames, ", "))
}

// effectiveSize is the size name an instance runs: compact when the cluster is compact, otherwise
// the recorded preset, otherwise base.
func effectiveSize(st *State) string {
	switch {
	case st.Compact:
		return SizeCompact
	case st.Size == "" || st.Size == SizeBase:
		return SizeBase
	default:
		return st.Size
	}
}

// sizeFor is the non-compact preset an instance runs, or nil for base and compact (which state
// nothing through this table).
func sizeFor(st *State) *sizePreset {
	if st.Compact {
		return nil
	}
	p, ok := sizePresets[st.Size]
	if !ok {
		return nil
	}
	return &p
}

// helmValues is the preset's chart values, to sit BELOW what dcctl computes. Nil for base.
func (p *sizePreset) helmValues() map[string]interface{} {
	if p == nil {
		return nil
	}
	areas := map[string]interface{}{}
	for name, a := range p.Areas {
		block := map[string]interface{}{}
		if a.Replicas > 0 {
			block["replicas"] = a.Replicas
		}
		limits := map[string]interface{}{}
		if a.CPULimit != "" {
			limits["cpu"] = a.CPULimit
		}
		if a.MemLimit != "" {
			limits["memory"] = a.MemLimit
		}
		if len(limits) > 0 {
			block["resources"] = map[string]interface{}{"limits": limits}
		}
		if a.CPURequest != "" {
			block["measuredRequests"] = map[string]interface{}{"cpu": a.CPURequest}
		}
		if len(block) > 0 {
			areas[name] = block
		}
	}
	return map[string]interface{}{
		"functionalAreas": areas,
		"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{
			"nats": map[string]interface{}{"streamDesignRate": p.StreamDesignRate},
		}}},
	}
}

// sizeVar is one OpenTofu assignment a preset makes.
type sizeVar struct{ Name, Value string }

// tofuVars is the preset's instance-root variables. liveJetStream and liveEventStore are the
// volumes the instance already has ("" on a first bootstrap): a volume is never resized by an
// apply, so a live size is kept exactly as the non-preset path keeps it.
func (p *sizePreset) tofuVars(liveJetStream, liveEventStore string) []sizeVar {
	if p == nil {
		return nil
	}
	return []sizeVar{
		{"nats_cpu_request", p.NatsCPURequest},
		{"nats_memory_request", p.NatsMemoryRequest},
		{"nats_memory_limit", p.NatsMemoryLimit},
		{"nats_jetstream_storage", volumeSize(liveJetStream, p.JetStreamStorage)},
		{"timescale_storage", volumeSize(liveEventStore, p.TimescaleStorage)},
		{"timescale_max_wal_size_mib", fmt.Sprint(p.TimescaleMaxWALMiB)},
	}
}

// tofuVarNames is every variable name the preset may state, for the operator-wins filter.
func (p *sizePreset) tofuVarNames() []string {
	vars := p.tofuVars("", "")
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

// summary is the one-line resolution printed by bootstrap and upgrade.
func (p *sizePreset) summary() string {
	if p == nil {
		return ""
	}
	names := make([]string, 0, len(p.Areas))
	for n := range p.Areas {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		a := p.Areas[n]
		part := n
		if a.Replicas > 1 {
			part += fmt.Sprintf(" %dx", a.Replicas)
		}
		if a.CPULimit != "" {
			part += " " + a.CPULimit + "cpu"
		}
		if a.MemLimit != "" {
			part += "/" + a.MemLimit
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("size %s: nats %s, js %s (design rate %d/s), event store %s wal %d MiB; %s",
		p.Name, p.NatsMemoryLimit, p.JetStreamStorage, p.StreamDesignRate,
		p.TimescaleStorage, p.TimescaleMaxWALMiB, strings.Join(parts, ", "))
}

// sizeNotices are the lines bootstrap and upgrade print after the summary: what the figures were
// measured on, and what they were not.
func sizeNotices(st *State, envelopesOn bool) []string {
	p := sizeFor(st)
	if p == nil {
		return nil
	}
	var out []string
	if p.EnvelopesMeasured && !envelopesOn {
		out = append(out, fmt.Sprintf("%s figures were measured with event envelopes on; they are off on this instance", p.Name))
	}
	if st.HA {
		out = append(out, fmt.Sprintf("%s with --ha has not been measured: the published ceiling is for one broker server", p.Name))
	}
	return out
}

// dropOperatorStated removes from vars (name=value) every size-preset assignment the operator
// states themselves. OpenTofu takes a -var over TF_VAR_*, terraform.tfvars and *.auto.tfvars, so a
// variable dcctl passes is a variable the operator cannot set: the preset must not pass one they
// did. stated reports whether the operator declared a name.
func (p *sizePreset) dropOperatorStated(vars []string, stated func(string) bool) []string {
	if p == nil {
		return vars
	}
	mine := map[string]bool{}
	for _, n := range p.tofuVarNames() {
		mine[n] = true
	}
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		name, _, _ := strings.Cut(v, "=")
		if mine[name] && stated(name) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// recordedSize is the size the declaration records: empty for base, so every instance built before
// the axis existed, and every base instance since, reads and writes the same declaration.
func recordedSize(st *State) string {
	if s := effectiveSize(st); s != SizeBase {
		return s
	}
	return ""
}

// sizeFromSpec is the non-compact preset a declaration records. "compact" is carried by
// spec.compact, which stays authoritative, so it reads back as no preset here.
func sizeFromSpec(spec dcv1beta1.InstanceSpec) string {
	if spec.Size == SizeCompact || spec.Size == SizeBase {
		return ""
	}
	return spec.Size
}

// validateRecordedSize holds a declaration's size to this build's table: an unknown name is a
// declaration made by a newer dcctl, and rendering it as base would shrink a running instance.
func validateRecordedSize(spec dcv1beta1.InstanceSpec) error {
	if err := ValidateSize(spec.Size); err != nil {
		return fmt.Errorf("the instance declaration's size: %w. A newer dcctl may have recorded it: "+
			"upgrade dcctl before touching this instance", err)
	}
	if spec.Size == SizeCompact && !spec.Compact {
		return fmt.Errorf("the instance declaration says size %q but not compact", SizeCompact)
	}
	if spec.Compact && spec.Size != "" && spec.Size != SizeCompact {
		return fmt.Errorf("the instance declaration is compact and says size %q", spec.Size)
	}
	return nil
}

// StateSizeFrom is the State.Size a --size value starts as. base and compact are settled against
// the cluster by CheckSizeAgainstCluster once the install record has been followed.
func StateSizeFrom(flag string) string { return flag }

// CheckSizeAgainstCluster refuses a size the cluster the instance is built on cannot give it, and
// normalises what is left: the instance follows its cluster's compact setting, and base is the
// absence of a preset.
func CheckSizeAgainstCluster(st *State) error {
	given := st.Size
	switch {
	case given == "" || given == SizeBase:
		st.Size = ""
		return nil
	case given == SizeCompact:
		st.Size = ""
		if !st.Compact {
			return fmt.Errorf("--size compact is the whole cluster's setting, not an instance's: this " +
				"cluster was not installed compact. Install one with `dcctl install --size compact`")
		}
		return nil
	case st.Compact:
		return fmt.Errorf("--size %s needs room a compact cluster does not publish a figure for: this "+
			"cluster was installed --compact. Build it on a cluster installed without --compact", given)
	}
	return ValidateSize(given)
}

// SizeSummary is the one line a bootstrap prints for the size it applies, or "" for base.
func SizeSummary(st *State) string {
	if p := sizeFor(st); p != nil {
		return p.summary()
	}
	return ""
}

// SizeNotices are the caveats printed beside the summary.
func SizeNotices(st *State) []string {
	return sizeNotices(st, overlayEnvelopesOn(st.ValuesOverlay))
}

// docAreaRows are the body rows of the per-service table in the sizing page, one per area in
// alphabetical order. Every cell is a code span or a dash, so the same rows stand in every
// language of the docs and only the header is translated. TestPublishedSizingTables holds each
// locale's copy to these.
func (p *sizePreset) docAreaRows() []string {
	names := make([]string, 0, len(p.Areas))
	for n := range p.Areas {
		names = append(names, n)
	}
	sort.Strings(names)
	cell := func(s string) string {
		if s == "" || s == "0" {
			return "-"
		}
		return "`" + s + "`"
	}
	rows := make([]string, 0, len(names))
	for _, n := range names {
		a := p.Areas[n]
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s | %s | %s |",
			n, cell(fmt.Sprint(a.Replicas)), cell(a.CPULimit), cell(a.MemLimit), cell(a.CPURequest)))
	}
	return rows
}

// docInfraRows are the body rows of the broker and storage table: the setting's own name and value.
func (p *sizePreset) docInfraRows() []string {
	rows := [][2]string{
		{"streamDesignRate", fmt.Sprint(p.StreamDesignRate)},
	}
	for _, v := range p.tofuVars("", "") {
		rows = append(rows, [2]string{v.Name, v.Value})
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("| `%s` | `%s` |", r[0], r[1]))
	}
	return out
}

// operatorWinsOverSize is precedence layer 5 for an instance root's variables: the size's variables
// the operator states in rootdir (terraform.tfvars, *.auto.tfvars, TF_VAR_) are not passed. One
// function for the bootstrap and the upgrade, so the two cannot disagree about who wins.
func operatorWinsOverSize(st *State, vars []string, rootdir string) []string {
	return sizeFor(st).dropOperatorStated(vars, func(name string) bool { return statedByOperator(rootdir, name) })
}
