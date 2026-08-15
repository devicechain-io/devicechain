// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Label values. Every one is a fixed string owned by this package, and every label a
// small closed enum — the bounded cardinality the governance lesson requires (ADR-023
// G.3). 🔴 NO TENANT LABEL ON ANY OF THESE, and none may be added: a tenant is an
// unbounded, tenant-influenceable cardinality vector, so per-tenant questions are
// answered by a warn log or a count-of-tenants gauge, never by a series per tenant.
//
// These strings are a metric dimension, so renaming one silently breaks an operator's
// dashboards and alert rules. Treat them as wire format.
const (
	targetKindDeviceListLabel = "device_list"
	targetKindGroupLabel      = "group"
	// targetKindUnknownLabel is for a refusal decided before any target was
	// established. An ambiguous request names both a list and a group, or neither, so
	// there is no honest kind to report — and reporting one of the two would invent a
	// fact about a request that never got far enough to have it.
	targetKindUnknownLabel = "unknown"

	outcomeCreated  = "created"
	outcomeReplayed = "replayed"
	outcomeRefused  = "refused"
	// outcomeUndecided is the platform failing to reach a verdict, as opposed to
	// reaching an unwelcome one. Kept apart from "refused" because they call for
	// opposite responses — see recordBatchOutcome.
	outcomeUndecided = "undecided"

	dispositionAccepted = "accepted"
	dispositionRefused  = "refused"

	// boundCeiling / boundReserve answer WHICH limit refused a device, and they are a
	// counterfactual rather than a state read — see batchHeadroom.boundAt.
	boundCeiling = "ceiling"
	boundReserve = "reserve"
	boundNone    = "none"

	cancelDispositionCancelled       = "cancelled"
	cancelDispositionAlreadySent     = "already_sent"
	cancelDispositionAlreadyFinished = "already_finished"

	// codeOtherLabel is where a refusal code this build does not know lands.
	codeOtherLabel = "other"
)

// metricSafeCodes is the closed set of refusal codes allowed to appear in a metric
// label. Anything else records as "other".
//
// 🔴 IT IS A LABEL ALLOWLIST, NOT A CLAIM TO ENUMERATE RejectionCode. The type's own
// doc is explicit that the set is not closed — codes from device-management's enqueue
// gate are relayed with their own values, and it deliberately does not re-declare them
// because two constant sets naming one enum drift silently. The reasoning holds for
// RELAYING and does not transfer here, for one reason: a code that falls through to
// "other" is not a silent failure, it is a visible one. An "other" series that starts
// climbing is the signal that this list wants an entry, and until someone adds it the
// counter is merely coarse rather than wrong.
//
// Why an allowlist at all: without it, a peer's code string reaches a Prometheus label
// directly — an unbounded label wearing a bounded label's clothes, which is the exact
// cardinality vector the tenant-label rule exists to prevent. The bound has to be
// structural, not a convention we intend to honour.
//
// The three relayed entries are the ones device-management produces today. They earn
// their place because they are the most common refusals a real fleet write produces —
// folding COMMAND_NOT_IN_VOCABULARY into "other" would blind the one panel an operator
// most needs after a heterogeneous fan-out.
var metricSafeCodes = map[RejectionCode]struct{}{
	// This package's own codes. hack/check-rejection-code-labels.sh asserts every
	// locally declared code appears here, so OUR drift is caught at CI time; a peer's
	// new code is caught by the "other" bucket at runtime.
	RejectPayloadNotJSON:       {},
	RejectMetadataNotJSON:      {},
	RejectExpiresAtInvalid:     {},
	RejectHeldCeilingExceeded:  {},
	RejectUnclassified:         {},
	RejectBatchTargetAmbiguous: {},
	RejectBatchTooLarge:        {},
	RejectBatchPartialRefused:  {},
	RejectTokenInUse:           {},
	RejectGroupUnusable:        {},

	// Relayed from device-management's enqueue gate. Listed for the label only; this
	// is not a mirror of that service's enum and must not be treated as one.
	"DEVICE_NOT_FOUND":          {},
	"COMMAND_NOT_IN_VOCABULARY": {},
	"PAYLOAD_SCHEMA_VIOLATION":  {},
}

// BatchMetrics are the fleet-write counters.
//
// 🔴 IT IS NIL-TOLERANT BY CONSTRUCTION, AND THAT IS NOT DEFENSIVE STYLE. promauto
// registers against the process-global registry and PANICS on a duplicate, while this
// package's tests build an Api by literal, many times per run, with no Microservice.
// So the constructor answers a nil Microservice with a nil *BatchMetrics and every
// recorder tolerates a nil receiver — the same shape the outbound-connectors dispatch
// metrics use, for the same reason.
type BatchMetrics struct {
	enqueues *prometheus.CounterVec
	devices  *prometheus.CounterVec
	refusals *prometheus.CounterVec
	cancels  *prometheus.CounterVec
}

// NewBatchMetrics registers the fleet-write counters, or returns nil when there is no
// microservice to register against.
//
// The names deliberately do NOT repeat the functional area. The registrar already
// prefixes every metric with the namespace and the area, so these serve as
// devicechain_commanddelivery_batch_*. The six counters this service registered
// earlier do repeat it — devicechain_commanddelivery_command_delivery_claims_lost_total
// — which is a wart, not a convention; every other service names them the short way.
// Those six are left alone rather than renamed under a new feature.
func NewBatchMetrics(ms *core.Microservice) *BatchMetrics {
	if ms == nil {
		return nil
	}
	return &BatchMetrics{
		enqueues: ms.NewCounterVec("batch_enqueues_total",
			"Command batches decided, by how the target was named and the outcome.",
			[]string{"target_kind", "outcome"}),
		devices: ms.NewCounterVec("batch_devices_total",
			"Devices a command batch resolved to, by whether the batch enqueued to them.",
			[]string{"disposition"}),
		refusals: ms.NewCounterVec("batch_refusals_total",
			"Per-device batch refusals, by code and — for a ceiling refusal — which bound "+
				"caused it. bound=reserve means the device would have fitted against the "+
				"tenant's own ceiling and was refused only by the delivery reserve.",
			[]string{"code", "bound"}),
		cancels: ms.NewCounterVec("batch_cancel_commands_total",
			"Commands examined by a batch cancel, by what the cancel was able to do with "+
				"each. A standing already_sent share is a brake that keeps arriving late.",
			[]string{"disposition"}),
	}
}

func (m *BatchMetrics) recordEnqueue(targetKind, outcome string) {
	if m == nil {
		return
	}
	m.enqueues.WithLabelValues(targetKind, outcome).Inc()
}

func (m *BatchMetrics) recordDevices(disposition string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.devices.WithLabelValues(disposition).Add(float64(n))
}

// recordRefusal adds n refusals sharing one (code, bound) pair.
//
// It takes a count rather than being called per refusal because the caller tallies first:
// a ten-thousand-device batch refused at the ceiling is ten thousand refusals and exactly
// one label pair, so calling this once per device would take the vector's lock ten
// thousand times to add ten thousand ones.
func (m *BatchMetrics) recordRefusal(code RejectionCode, bound string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.refusals.WithLabelValues(metricCodeLabel(code), bound).Add(float64(n))
}

func (m *BatchMetrics) recordCancel(disposition string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.cancels.WithLabelValues(disposition).Add(float64(n))
}

// metricCodeLabel folds a refusal code to a label value this build knows.
func metricCodeLabel(code RejectionCode) string {
	if _, known := metricSafeCodes[code]; known {
		return string(code)
	}
	return codeOtherLabel
}

// targetKindLabel names how a batch's target was specified, for a metric label.
func targetKindLabel(kind BatchTargetKind) string {
	switch kind {
	case BatchTargetDeviceList:
		return targetKindDeviceListLabel
	case BatchTargetGroup:
		return targetKindGroupLabel
	default:
		return targetKindUnknownLabel
	}
}

// requestTargetKindLabel names the target kind a REQUEST asked for, before anything has
// been resolved. It is only for refusals raised that early: an ambiguous request has no
// kind, and a well-formed one is reported from the resolved target instead.
func requestTargetKindLabel(request *CommandBatchCreateRequest) string {
	hasList := len(request.NamedDevices()) > 0
	hasGroup := request.GroupToken != nil && *request.GroupToken != ""
	switch {
	case hasList && !hasGroup:
		return targetKindDeviceListLabel
	case hasGroup && !hasList:
		return targetKindGroupLabel
	default:
		return targetKindUnknownLabel
	}
}
