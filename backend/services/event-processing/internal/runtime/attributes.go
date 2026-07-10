// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

// Scope vocabulary for a device attribute the dynamic-threshold view flattens (ADR-012). Only
// the two PLATFORM-set scopes reach the projection (the attribute consumer whitelists them and
// drops CLIENT so a device cannot set the limit it is checked against), so the view weighs only
// these. They MUST equal device-management's AttributeScope string values ("SERVER"/"SHARED");
// a processor-side test pins that equality so a rename upstream cannot silently break flattening.
const (
	AttrScopeServer = "SERVER"
	AttrScopeShared = "SHARED"
)

// AttrEntry is one live device-attribute projection row in the armer-style model-free shape: the
// processor maps a model.DeviceAttribute row (from LoadAll at startup, or LoadDevice on a live
// recheck) onto it, so the view never depends on the persistence model and its flattening is unit-
// testable in isolation.
type AttrEntry struct {
	Tenant      string
	DeviceToken string
	Scope       string
	Key         string
	Value       float64
}

// DeviceAttributeView is the loop-owned in-memory projection of every device's current numeric
// attributes, FLATTENED to the per-device key→value map a detection rule's dynamic threshold reads
// (the CEL "attr" var, ADR-051 slice 4c-3b-2). It is the read-side companion to the durable
// DeviceAttributeStore: reconciled from the store at startup and kept live by rechecks the attribute
// and entity-deleted consumers signal onto the single-writer loop — exactly parallel to the dead-man
// armer's roster view, and for the same reason. A recheck re-reads the AUTHORITATIVE projection for
// one device and REPLACES that device's entry (never a carried delta), so the two consumers (separate
// goroutines over reorderable streams) cannot install a stale value: the store's monotonic guard has
// already converged the projection, and the loop reflects whatever it says.
//
// It runs ONLY on the processor's single-writer loop (and the startup reconcile that precedes it) —
// the same goroutine that reads it on the hot path (For, in Plan) — so it needs no lock.
//
// FLATTENING is SERVER-over-SHARED (ADR-012): a server-scoped value (hidden from the device) shadows
// a shared-scoped one for the same key, matching device-management's alarm-engine resolveThreshold
// precedence — an attribute threshold is platform-set config, so the device-readable shared value is
// only a fallback. A device with no attributes has no entry (For returns nil), which the presence-
// guarded dynamic comparison reads as a clean non-match.
type DeviceAttributeView struct {
	byDevice map[devKey]map[string]float64
}

// NewDeviceAttributeView builds an empty view.
func NewDeviceAttributeView() *DeviceAttributeView {
	return &DeviceAttributeView{byDevice: map[devKey]map[string]float64{}}
}

// Reconcile rebuilds the whole view from the durable projection's live rows at startup (slice
// 4c-3b-2). It groups the flat row set by device and installs each device's flattened map, so the
// first live event after restart resolves a dynamic threshold from the same values the pre-restart
// engine saw. It runs once, on the startup goroutine, before the live loop.
func (v *DeviceAttributeView) Reconcile(rows []AttrEntry) {
	byDevice := make(map[devKey][]AttrEntry)
	for _, r := range rows {
		dk := devKey{r.Tenant, r.DeviceToken}
		byDevice[dk] = append(byDevice[dk], r)
	}
	v.byDevice = make(map[devKey]map[string]float64, len(byDevice))
	for dk, group := range byDevice {
		if m := flatten(group); len(m) > 0 {
			v.byDevice[dk] = m
		}
	}
}

// ReplaceDevice installs one device's authoritative attribute set on the single-writer loop, from
// the live rows a recheck re-read for it (slice 4c-3b-2). It REPLACES the device's whole entry —
// never merges a delta — so a removed or purged attribute disappears and a stale straggler the
// projection already rejected can never linger in memory. An empty row set (every attribute removed,
// or the device purged) drops the entry entirely rather than leaving an empty map. It allocates a
// fresh map and swaps the entry pointer, so a reference For handed to an in-flight fan-out still
// points at the prior immutable map (the loop is single-threaded, so this is belt-and-braces).
func (v *DeviceAttributeView) ReplaceDevice(tenant, deviceToken string, rows []AttrEntry) {
	dk := devKey{tenant, deviceToken}
	if m := flatten(rows); len(m) > 0 {
		v.byDevice[dk] = m
		return
	}
	delete(v.byDevice, dk)
}

// For returns a device's flattened attribute map (or nil when it has none) for the fan-out to bind
// as predicate.Input.Attr. The returned map is owned by the view and MUST NOT be mutated by the
// caller; the predicate reads it read-only, and ReplaceDevice only ever swaps in a fresh map, so a
// held reference stays valid for the duration of one synchronous fan-out.
func (v *DeviceAttributeView) For(tenant, deviceToken string) map[string]float64 {
	return v.byDevice[devKey{tenant, deviceToken}]
}

// flatten collapses one device's rows to a key→value map with SERVER-over-SHARED precedence: a
// shared-scoped value is applied first and a server-scoped value for the same key overwrites it, so
// the platform-controlled server value wins (see the type doc). A scope outside the whitelisted
// vocabulary cannot reach the projection, so it is ignored defensively rather than weighed. Returns
// an empty map for no rows; the caller (ReplaceDevice/Reconcile) treats empty as "no entry".
func flatten(rows []AttrEntry) map[string]float64 {
	m := make(map[string]float64, len(rows))
	for _, r := range rows { // shared first
		if r.Scope == AttrScopeShared {
			m[r.Key] = r.Value
		}
	}
	for _, r := range rows { // server overrides
		if r.Scope == AttrScopeServer {
			m[r.Key] = r.Value
		}
	}
	return m
}
