// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "fmt"

// The --compact preset.
//
// compact is a PRESET, not a new tuning axis: every value below already exists as
// a lever an operator can set by hand. What the flag adds is a single vetted
// combination of them, sized against the platform's real stream/KV inventories,
// so that "DeviceChain runs on X" names a configuration instead of a hope.
//
// It deliberately does NOT change which services run. That stays on the --profile
// axis, where it is already named and visible; folding it in here would mean bug
// reports say "compact" when the real variable is "which services were deployed",
// and we would inherit a works-in-default/breaks-in-compact support matrix.
//
// It also deliberately does NOT set GOMEMLIMIT. That was expected to shrink RSS
// and measurably does not (see deploy/helm values.yaml goMemLimitPercent): the
// steady-state footprint is governed by the live set, which a soft limit cannot go
// below. Turning it on here would make the compact number rest on a lever with no
// evidence behind it.
type compactSizing struct {
	// JetStream per-stream ceilings, in bytes. These are RESERVED UP FRONT at
	// stream creation, so they are a disk floor rather than a cap on growth —
	// which is why the PV below is derived from them and not chosen separately.
	StreamMaxBytes        int64
	StreamMaxBytesCold    int64
	MqttStoreMaxBytes     int64
	MqttQoS2StoreMaxBytes int64
	KvCacheMaxBytes       int64
	KvStateMaxBytes       int64

	// Volume sizes, as OpenTofu quantity strings.
	//
	// JetStreamStorage is 2Gi and NOT 1Gi. max_file_store is floor(90% of the
	// MAGNITUDE) with the unit reattached, so a 1Gi volume yields floor(1 * 0.9) =
	// 0 -> "0Gi": a zero-byte store on which nothing starts at all. 2Gi is the
	// smallest magnitude that works, and its floor(2 * 0.9) = 1 -> "1Gi" ceiling
	// is what compactReservation is checked against.
	JetStreamStorage string
	PostgresStorage  string

	// Scheduling requests. Lowering REQUESTS fixes scheduling — pods sitting
	// Pending on a small node — and does not lower usage. Lowering LIMITS would
	// merely convert memory pressure into OOMKills, so the limits are left alone.
	CPURequest    string
	MemoryRequest string
}

// compact is the shipped preset. It is the single source this package renders
// from: helm.go, tofu.go and the budget test all read it, so the sizing cannot
// drift between the values the chart states, the volume OpenTofu provisions, and
// the assertion that the first fits the second.
var compact = compactSizing{
	StreamMaxBytes:        64 << 20,
	StreamMaxBytesCold:    16 << 20,
	MqttStoreMaxBytes:     32 << 20,
	MqttQoS2StoreMaxBytes: 8 << 20,
	KvCacheMaxBytes:       8 << 20,
	KvStateMaxBytes:       16 << 20,

	JetStreamStorage: "2Gi",
	PostgresStorage:  "2Gi",

	CPURequest:    "25m",
	MemoryRequest: "64Mi",
}

// natsValues renders the JetStream ceilings as the Helm values block they occupy
// under instance.config.infrastructure.nats.
//
// Only the ceilings are named. The block is deep-merged over the chart's own, so
// hostname/port/persistence survive — a wholesale replacement here would strip the
// broker coordinates and the failure would look nothing like a sizing mistake.
func (c compactSizing) natsValues() map[string]interface{} {
	return map[string]interface{}{
		"streamMaxBytes":        c.StreamMaxBytes,
		"streamMaxBytesCold":    c.StreamMaxBytesCold,
		"mqttStoreMaxBytes":     c.MqttStoreMaxBytes,
		"mqttQoS2StoreMaxBytes": c.MqttQoS2StoreMaxBytes,
		"kvCacheMaxBytes":       c.KvCacheMaxBytes,
		"kvStateMaxBytes":       c.KvStateMaxBytes,
	}
}

// resourceValues renders the per-container requests.
//
// `limits` is omitted rather than set: a Helm map COALESCES, so leaving it out
// keeps the chart's own limits in place. Naming it here would be the mistake this
// preset is not making — see CPURequest.
func (c compactSizing) resourceValues() map[string]interface{} {
	return map[string]interface{}{
		"requests": map[string]interface{}{
			"cpu":    c.CPURequest,
			"memory": c.MemoryRequest,
		},
	}
}

// summary is the one-line resolution printed in the bootstrap report, so an
// operator can see what was applied without reading the chart.
func (c compactSizing) summary() string {
	return fmt.Sprintf("jetstream %s, postgres %s, streams %d/%d MiB, requests %s/%s",
		c.JetStreamStorage, c.PostgresStorage,
		c.StreamMaxBytes>>20, c.StreamMaxBytesCold>>20,
		c.CPURequest, c.MemoryRequest)
}

// CompactSummary is the resolved footprint, for the command layer to echo when
// --compact is given. The sizing itself stays unexported: it is a preset, and
// exposing the struct would invite it being taken apart into per-value flags,
// which is exactly the tuning axis this deliberately is not.
func CompactSummary() string { return compact.summary() }
