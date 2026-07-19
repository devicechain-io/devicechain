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

	// PostgresStorage is the RELATIONAL store (dc-postgresql): devices, types,
	// profiles, relationships, identities, dashboards. Catalog-scale — it does not
	// grow with message rate — with two accumulators that do grow with USE rather
	// than fleet size: one row per command issued and one per alarm, neither of
	// which is pruned today.
	//
	// Unlike JetStreamStorage this number is NOT derived from anything. It is a
	// judgement that a small fleet's catalog fits comfortably, and it is a TIME
	// budget rather than a capacity one: nothing here bounds the command and alarm
	// tables, and the module applies no postgresql.conf tuning, so pg_wal shares
	// this volume at the stock max_wal_size. C3 measures it; until then it is the
	// least evidenced value in this struct.
	PostgresStorage string

	// TimescaleStorage is the TIME-SERIES store (dc-timescaledb-single), which is
	// where telemetry actually lands — the one volume here that grows with device
	// count times message rate.
	//
	// It is set explicitly, and larger than PostgresStorage, because leaving it out
	// was a real bug: compact shrank the database that does not grow and left the
	// one that does at its full-size default, so the preset's disk claim described
	// a fraction of the disk it used.
	//
	// This is a time budget too, and more sharply so. event-management's
	// RetentionDays defaults to 0, which keeps data FOREVER (compression after 7
	// days is on, which slows the fill but does not bound it). A compact instance
	// expecting to run indefinitely wants a retention window set; the volume alone
	// only decides how long it takes to fill.
	TimescaleStorage string

	// Scheduling requests. Lowering REQUESTS fixes scheduling — pods sitting
	// Pending on a small node — and does not itself lower usage. The limits are
	// left alone because lowering them shrinks nothing: a lower MEMORY limit
	// converts memory pressure into OOMKills, and a lower CPU limit throttles.
	//
	// (Strictly, "limits do not affect usage" holds because goMemLimitPercent
	// defaults to 0. The chart CAN derive GOMEMLIMIT from the memory limit — see
	// _helpers.tpl — so the statement is about today's default, not about limits in
	// general.)
	//
	// One tradeoff worth knowing, since it is not free: widening the gap between
	// request and actual usage moves these pods UP the kubelet's eviction ranking,
	// which sorts Burstable pods by usage above request. On exactly the
	// memory-pressured small node this preset targets, compact pods are evicted
	// before they would have been at the default requests.
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
	TimescaleStorage: "4Gi",

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
	return fmt.Sprintf("jetstream %s, postgres %s, timescale %s, streams %d/%d MiB, requests %s/%s",
		c.JetStreamStorage, c.PostgresStorage, c.TimescaleStorage,
		c.StreamMaxBytes>>20, c.StreamMaxBytesCold>>20,
		c.CPURequest, c.MemoryRequest)
}

// CompactSummary is the resolved footprint, for the command layer to echo when
// --compact is given. The sizing itself stays unexported: it is a preset, and
// exposing the struct would invite it being taken apart into per-value flags,
// which is exactly the tuning axis this deliberately is not.
func CompactSummary() string { return compact.summary() }
