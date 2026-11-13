// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"sort"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The size axis: `--size base|compact|perf-12k|perf-16k|perf-20k`.
//
// A size is a PRESET, not a dial, by the same rules compact.go states: it composes levers that
// already exist, never changes which services run (that stays on the profile axis), and never
// turns a behaviour on (event envelopes, fail-closed). It is orthogonal to --ha, which multiplies
// replicas while the preset sizes each copy.
//
//   - base is the default and renders exactly what dcctl rendered before this axis existed.
//
//   - compact is the --compact preset (compact.go), reachable as `--size compact` as well; the flag
//     stays as a permanent alias. Its sizing is still compactSizing, so its tests are unchanged.
//
//   - perf-12k is the measured high-throughput shape: one cluster, no --ha, sustained 12,000
//     events a second with event envelopes on (GKE rounds 29, 30 and 31).
//
//   - perf-16k is the same shape sized for 16,000 events a second (GKE round 31): a larger broker,
//     a larger write-ahead log allowance and a larger event-processing pod. 20,000 events a second
//     was stored exactly on that shape in round 31 but over the latency target, so it is a
//     throughput-only ceiling and is documented as one.
//
//   - perf-20k is the throughput target derived from round 31: more broker CPU on the same
//     unsharded shape. It does not promise the 50 ms PUBACK latency target; the measured 20k
//     run stored every event but had a 245 ms PUBACK p99. These values are not yet run as written.
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
	SizePerf16k = "perf-16k"
	SizePerf20k = "perf-20k"
)

// sizeNames is every name `--size` accepts, in the order help prints them.
var sizeNames = []string{SizeBase, SizeCompact, SizePerf12k, SizePerf16k, SizePerf20k}

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

	// RelationalMaxWALMiB is the relational store's max_wal_size, in MiB, measured with this
	// size. A bootstrap does NOT write it: the relational store belongs to the cluster and is
	// set at install (postgres_max_wal_size_mib in the cluster root; CloudNativePG refuses
	// ALTER SYSTEM, so the cluster spec is the only way). It is here so the figure has one home,
	// is held to the cluster root by a test and is published in the sizing page.
	RelationalMaxWALMiB int

	// EnvelopesMeasured says the figures were measured with event envelopes on, which the notices
	// repeat when they are off.
	EnvelopesMeasured bool
}

// sizePresets are the sizes with a measured basis. base and compact are not rows here: base states
// nothing, compact is compactSizing.
//
// perf-12k, from GKE rounds 29, 30 and 31 (non-HA, one broker server, 48 vCPU, envelopes on; 12,000
// events a second clean: PUBACK p99 3.7 ms in round 31, 0.658 CPU-s per 1,000 events, exact by the
// oracle). The derivations, per knob:
//
//   - event-sources 3 x 3 CPU / 512Mi, device-management 2 x 3 CPU, device-state 2 x 3 CPU / 512Mi,
//     event-management 4 CPU / 1Gi, event-processing 2 CPU / 1Gi: the shape round 30 ran. Requests
//     are CPU-seconds per 1,000 events at 12,000 / replicas x 1.2-1.5, rounded up to 50m.
//   - round 31 corrections: device-management's request is 400m (0.30 cores a pod at 12,000 x 1.3)
//     and event-sources' memory limit is 256Mi (81 MiB resident at most).
//   - device-management states no memory limit: the chart floor (768Mi) is the measured one, and
//     an explicit limit would bypass it.
//   - event-processing 1Gi: resident 547 MiB at 12,000 events a second x 1.5, rounded up.
//   - NATS 8Gi: the working set was 2.5 GiB at 12,000 in round 31 (a margin of 2.5 at 8Gi).
//   - JetStream volume: the runway rule at 12,000 (see size_test.go); round 31 used 7.9 GiB at 20,000.
//   - event store volume 160Gi: 756 bytes per stored event (indexes included, uncompressed) is
//     32.7 GB an hour at 12,000 a second, so it holds about three and a half hours of saturated
//     ingest after the log allowance. It is a burst volume, not retention.
//   - max_wal_size 20480 MiB: 1.9 x 900 s x 10.8 MB/s (round 30 at 12,000); round 31 saw no
//     requested checkpoint at 12,000.
//   - relational max_wal_size 4096 MiB: the stock 1 GB took four requested checkpoints a window at
//     12,000; 4 GB took one at 18,000.
//
// perf-16k, from round 31's ramp to 20,000 on the perf-12k shape. PUBACK p99 at 16,000 was 63 ms on
// the perf-12k sizing, just over the 50 ms target. The figures below are what was read off that run
// to take it under; they have NOT been run as written, and the next round confirms them.
//
//   - NATS 6 CPU request: 4.25 cores at 16,000 (5.2, peak 5.5 at 20,000) ran over a 4 CPU request.
//     Memory limit 10Gi (3.6 GiB working set at 16,000, 4.4 at 20,000; 8Gi also carried 20,000).
//   - event-processing 2 CPU / 1536Mi: 1 CPU throttled from 16,000, and 1.5Gi with 2 CPU was the
//     only arm with a clean DETECT lag at 20,000.
//   - requests are the round 31 per-pod CPU at 16,000 x 1.3, rounded up to 50m.
//   - streamDesignRate 16000 and a 36Gi JetStream volume: the runway rule (a 15 minute outage at the
//     design rate) now asks for 17,876 MiB, and the reservation with fail-closed on is 30,528 MiB,
//     which needs at least 34Gi. Round 31 ran 12,000 here and used 7.9 GiB at 20,000.
//   - max_wal_size 40960 MiB: 1.9 x 900 s x 23.8 MB/s (round 31 at 20,000), so 20,000 also
//     checkpoints on the clock; relational 8192 MiB: 1.9 x 300 s x 11.2 MB/s is 6.4 GB at 20,000.
//   - event store volume 260Gi (the volume round 30 ran): 756 bytes an event is 43.6 GB an hour at
//     16,000 a second, about four hours of saturated ingest after the log allowance and working room.
//
// What is NOT here yet, because it needs a measurement: the relational storage, CNPG resources,
// Prometheus sizing and the GKE node pools.
var sizePresets = map[string]sizePreset{
	SizePerf12k: {
		Name: SizePerf12k,
		Areas: map[string]areaSize{
			"event-sources":     {Replicas: 3, CPULimit: "3", MemLimit: "256Mi", CPURequest: "600m"},
			"device-management": {Replicas: 2, CPULimit: "3", CPURequest: "400m"},
			"device-state":      {Replicas: 2, CPULimit: "3", MemLimit: "512Mi", CPURequest: "250m"},
			"event-management":  {CPULimit: "4", MemLimit: "1Gi", CPURequest: "400m"},
			"event-processing":  {CPULimit: "2", MemLimit: "1Gi", CPURequest: "500m"},
		},
		StreamDesignRate:    12000,
		NatsCPURequest:      "4",
		NatsMemoryRequest:   "4Gi",
		NatsMemoryLimit:     "8Gi",
		JetStreamStorage:    "28Gi",
		TimescaleStorage:    "160Gi",
		TimescaleMaxWALMiB:  20480,
		RelationalMaxWALMiB: 4096,
		EnvelopesMeasured:   true,
	},
	SizePerf16k: {
		Name: SizePerf16k,
		Areas: map[string]areaSize{
			"event-sources":     {Replicas: 3, CPULimit: "3", MemLimit: "256Mi", CPURequest: "750m"},
			"device-management": {Replicas: 2, CPULimit: "3", CPURequest: "500m"},
			"device-state":      {Replicas: 2, CPULimit: "3", MemLimit: "512Mi", CPURequest: "300m"},
			"event-management":  {CPULimit: "4", MemLimit: "1Gi", CPURequest: "450m"},
			"event-processing":  {CPULimit: "2", MemLimit: "1536Mi", CPURequest: "700m"},
		},
		StreamDesignRate:    16000,
		NatsCPURequest:      "6",
		NatsMemoryRequest:   "6Gi",
		NatsMemoryLimit:     "10Gi",
		JetStreamStorage:    "36Gi",
		TimescaleStorage:    "260Gi",
		TimescaleMaxWALMiB:  40960,
		RelationalMaxWALMiB: 8192,
		EnvelopesMeasured:   true,
	},
	// GKE round 31: product/gke-perf-report-r31-2026-11.md, sections 1-3, in the
	// devicechain-strategy repo. At 20k the single broker used 5.2 cores (peak 5.5):
	// reserve 6 CPU, 6Gi memory and a 10Gi limit; no sharding or replica increase.
	// Keep perf-12k's service shape and request rule (measured cores per pod x
	// 1.2-1.5, rounded up to 50m): ES .75 x 1.2, DM .50 x 1.2, DS .29 x 1.3,
	// EM .41 x 1.5, EP .72 x 1.3. EP arm C needs 2 CPU / 1536Mi at 20k.
	// WAL 40960 MiB and 260Gi event storage follow section 2; relational WAL is
	// published for install only. 40Gi JetStream holds the 20k runway even with
	// fail-closed acknowledgements enabled. This is throughput sizing, not a latency claim.
	SizePerf20k: {
		Name: SizePerf20k,
		Areas: map[string]areaSize{
			"event-sources":     {Replicas: 3, CPULimit: "3", MemLimit: "256Mi", CPURequest: "900m"},
			"device-management": {Replicas: 2, CPULimit: "3", CPURequest: "600m"},
			"device-state":      {Replicas: 2, CPULimit: "3", MemLimit: "512Mi", CPURequest: "400m"},
			"event-management":  {CPULimit: "4", MemLimit: "1Gi", CPURequest: "650m"},
			"event-processing":  {CPULimit: "2", MemLimit: "1536Mi", CPURequest: "950m"},
		},
		StreamDesignRate:    20000,
		NatsCPURequest:      "6",
		NatsMemoryRequest:   "6Gi",
		NatsMemoryLimit:     "10Gi",
		JetStreamStorage:    "40Gi",
		TimescaleStorage:    "260Gi",
		TimescaleMaxWALMiB:  40960,
		RelationalMaxWALMiB: 8192,
		EnvelopesMeasured:   true,
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

type brokerRequest struct {
	Preset           string
	CPU, Memory      *resource.Quantity
	CPUText, MemText string
}

// brokerRequestFor checks only requests still supplied by the built-in preset.
func brokerRequestFor(st *State, stated func(name string) bool) (req *brokerRequest, unchecked string, err error) {
	return sizeFor(st).brokerRequestFor(st, stated)
}

// brokerRequestFor parses a preset's requests without changing the built-in table.
func (p *sizePreset) brokerRequestFor(st *State, stated func(name string) bool) (req *brokerRequest, unchecked string, err error) {
	if p == nil {
		return nil, "", nil
	}
	values := map[string]string{}
	for _, v := range p.tofuVarsFor(st, stated) {
		values[v.Name] = v.Value
	}
	var missing []string
	for _, name := range []string{"nats_cpu_request", "nats_memory_request"} {
		if _, ok := values[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		unchecked = "the operator states " + strings.Join(missing, " / ")
	}
	if len(missing) == 2 {
		return nil, unchecked, nil
	}
	req = &brokerRequest{Preset: p.Name, CPUText: values["nats_cpu_request"], MemText: values["nats_memory_request"]}
	if _, ok := values["nats_cpu_request"]; ok {
		cpu, err := resource.ParseQuantity(req.CPUText)
		if err != nil {
			return nil, unchecked, fmt.Errorf("parsing %s nats_cpu_request: %w", p.Name, err)
		}
		req.CPU = &cpu
	}
	if _, ok := values["nats_memory_request"]; ok {
		memory, err := resource.ParseQuantity(req.MemText)
		if err != nil {
			return nil, unchecked, fmt.Errorf("parsing %s nats_memory_request: %w", p.Name, err)
		}
		req.Memory = &memory
	}
	return req, unchecked, nil
}

// smallerBrokerPreset finds the largest preset strictly below both selected requests.
func smallerBrokerPreset(selected string) *sizePreset {
	p, ok := sizePresets[selected]
	if !ok {
		return nil
	}
	cpu, cpuErr := resource.ParseQuantity(p.NatsCPURequest)
	memory, memErr := resource.ParseQuantity(p.NatsMemoryRequest)
	if cpuErr != nil || memErr != nil {
		return nil
	}
	var best *sizePreset
	var bestCPU, bestMemory resource.Quantity
	for _, name := range sizeNames {
		candidate, ok := sizePresets[name]
		if !ok {
			continue
		}
		candidateCPU, cpuErr := resource.ParseQuantity(candidate.NatsCPURequest)
		candidateMemory, memErr := resource.ParseQuantity(candidate.NatsMemoryRequest)
		if cpuErr != nil || memErr != nil || candidateCPU.Cmp(cpu) >= 0 || candidateMemory.Cmp(memory) >= 0 {
			continue
		}
		if best == nil || candidateCPU.Cmp(bestCPU) > 0 || (candidateCPU.Cmp(bestCPU) == 0 && candidateMemory.Cmp(bestMemory) > 0) {
			best = &candidate
			bestCPU, bestMemory = candidateCPU, candidateMemory
		}
	}
	return best
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
	st.SizeGiven = given != ""
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
	// The cluster's setting, not an instance's: the sizing page says so under the table.
	rows = append(rows, [2]string{"postgres_max_wal_size_mib", fmt.Sprint(p.RelationalMaxWALMiB)})
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("| `%s` | `%s` |", r[0], r[1]))
	}
	return out
}

// volumeVars are the two preset variables that name a volume an apply must never resize.
var volumeVars = map[string]bool{"nats_jetstream_storage": true, "timescale_storage": true}

// tofuVarsFor is the preset's variables for this run with precedence layer 5 applied: a variable the
// operator states (stated) is not emitted, so what they wrote reaches OpenTofu.
//
// 🔴 THE TWO VOLUME VARIABLES ARE THE EXCEPTION ONCE THE VOLUME EXISTS. A volume's size is the live
// one on every path (volumeSize), so an upgrade cannot resize it; leaving the assignment out because
// a file states the variable would let that file's value grow or shrink a live volume. On a first
// bootstrap there is no live volume and the operator's statement is honoured like any other.
func (p *sizePreset) tofuVarsFor(st *State, stated func(string) bool) []sizeVar {
	var out []sizeVar
	for _, v := range p.tofuVars(st.LiveVolumes.JetStream, st.LiveVolumes.EventStore) {
		if volumeVars[v.Name] {
			live := st.LiveVolumes.JetStream
			if v.Name == "timescale_storage" {
				live = st.LiveVolumes.EventStore
			}
			if live != "" {
				out = append(out, v)
				continue
			}
		}
		if stated(v.Name) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// verifySizeRecorded refuses a declaration that came back without the preset this run asked it to
// keep. The API server prunes a field its CRD does not declare, so a cluster whose Instance
// definition predates spec.size accepts the write, drops the size and reports success; the run
// would then go on to build base after printing the preset's summary. Compact is not judged here:
// spec.compact carries it and has always been recorded.
func verifySizeRecorded(want, got string) error {
	if want == "" || want == got {
		return nil
	}
	return fmt.Errorf("this cluster's Instance definition does not record size (the declaration came back " +
		"without the size this run asked it to keep), so a later upgrade would build the instance as base. " +
		"Run `dcctl install` to move the cluster's definitions onto this release, then run this again")
}

// adoptRecordedSize settles a re-run's size against the declaration already in the cluster.
func adoptRecordedSize(st *State, recorded dcv1beta1.InstanceSpec) error {
	if st.Compact {
		return nil // the cluster's setting, followed from the install record
	}
	was := sizeFromSpec(recorded)
	if !st.SizeGiven {
		st.Size = was
		return nil
	}
	if st.Size != was {
		return fmt.Errorf("instance %q was bootstrapped with size %s and this run names %s: an instance "+
			"keeps the size it was bootstrapped with. Re-run without --size, or state the settings you "+
			"want in a --values overlay, which wins over any size", st.Instance, sizeNameOf(was), sizeNameOf(st.Size))
	}
	return nil
}

func sizeNameOf(s string) string {
	if s == "" {
		return SizeBase
	}
	return s
}
