// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// decodePlan is a version's payload decoder, compiled once per resolution (per cache fill) and
// shared by every event of the device type until the entry is evicted. It is exactly one of:
//   - a compiled payloaddecode.Plan, or
//   - a TYPED INVALID plan: the version's decoder could not be compiled by this build.
//
// 🔴 A DECODER THAT DOES NOT COMPILE MUST NOT FAIL THE RESOLUTION. freeze returns no error, and a
// profile-read error is classified as a retryable ApiCallFailed for EVERY event type of the
// device type, so returning the compile error would turn one tenant's unusable decoder (or a
// snapshot written by a newer release, during a rollback or a downgrade) into a retry storm for
// location events, ordinary measurements and everything else that has nothing to do with it.
// The failure is stored here and surfaces only where a decode is attempted, as the terminal
// DECODE_FAILED the caller classifies from DecodePlanError.Reason.
//
// 🔴 A PLAN IS REACHABLE ONLY THROUGH ITS OWN RESOLUTION, and a resolution is cached per
// (tenant, device type). There is deliberately no process-wide cache of compiled programs keyed
// by source text (event-processing's REACT caches are the cautionary shape): two tenants that
// write the same expression compile against their OWN metrics, which decide types, formatting
// and the strict-key check, so a program compiled for one is not valid for the other.
type decodePlan struct {
	plan *payloaddecode.Plan
	err  *DecodePlanError
	// input is the input kind the document names, read leniently, so an unusable decoder can
	// still say what an uplink would have to carry. A usable one answers from its plan.
	input string
	// heap is what holding the compiled programs costs, in bytes, as decodeHeapCharge states it.
	// 0 for an unusable decoder, which holds only its error.
	heap int
}

// What a compiled decoder holds, measured (TestDecodedChargeCoversTheHeap) and rounded up. A CEL
// program is large next to the text it came from: about 7 KiB per expression however short, and
// for long expressions about 47 bytes per byte of text. Both terms are charged because neither
// alone covers both shapes: 520 one-token expressions (20 KiB of document) hold 3.8 MB, and 17
// dense ones (25 KiB) hold 1.2 MB. A maximal document (32 KiB) holds about 4.9 MB.
const (
	decodeHeapPerExpression = 8 << 10
	decodeHeapPerDocByte    = 48
)

// decodeHeapCharge is the bytes to charge the cache for the programs compiled from doc.
func decodeHeapCharge(doc *payloaddecode.Document, rawLen int) int {
	exprs := len(doc.Let)
	for i := range doc.Sections {
		sec := &doc.Sections[i]
		if sec.When != "" {
			exprs++
		}
		for j := range sec.Outputs {
			o := &sec.Outputs[j]
			for _, e := range [...]string{o.Expr, o.Map, o.When} {
				if e != "" {
					exprs++
				}
			}
		}
	}
	return exprs*decodeHeapPerExpression + rawLen*decodeHeapPerDocByte
}

// DecodePlanError is why a version's decoder is not executable by this build. Reason is the
// wire sub-reason a failed record carries (payloaddecode.ReasonEnvUnsupported or
// payloaddecode.ReasonCompileFailed).
type DecodePlanError struct {
	Reason payloaddecode.Reason
	Err    error
}

func (e *DecodePlanError) Error() string {
	return string(e.Reason) + ": " + e.Err.Error()
}

// Unwrap exposes the compile error (and through it payloaddecode.ErrEnvUnsupported).
func (e *DecodePlanError) Unwrap() error { return e.Err }

// decodePlanBuilds counts decoder compiles in this process. The compile is the expensive step of a
// cache fill (tens of milliseconds for a large decoder), so what the stampede guard promises is
// stated as this number, and tests read it.
var decodePlanBuilds atomic.Int64

// decoderCompileMetric counts decoder compiles by result (ok, env_unsupported, compile_failed). It
// is set once, at start, by InitializeCaches; nil counts nothing. Low cardinality on purpose: no
// tenant or profile label, which a tenant could grow without bound.
var decoderCompileMetric atomic.Pointer[prometheus.CounterVec]

func countCompile(result string) {
	if m := decoderCompileMetric.Load(); m != nil {
		m.WithLabelValues(result).Inc()
	}
}

// decodePlanBuildHook, when set by a test before it starts goroutines, runs at the start of every
// compile, so a test can hold one open while others arrive.
var decodePlanBuildHook func()

// buildDecodePlan compiles the version's decoder document against the version's own metrics.
// nil means the version has no decoder. It never fails: a document this build cannot compile is
// a typed invalid plan, and is logged (it passed the publish gate, so this is a backstop for a
// release that tightened the language, or a build older than the snapshot).
func buildDecodePlan(scope ProfileScope, metrics []ResolvedMetric, raw json.RawMessage) *decodePlan {
	if len(raw) == 0 {
		return nil
	}
	decodePlanBuilds.Add(1)
	if hook := decodePlanBuildHook; hook != nil {
		hook()
	}
	var head struct {
		EnvVersion int    `json:"envVersion"`
		Input      string `json:"input"`
	}
	headErr := json.Unmarshal(raw, &head)
	invalid := func(reason payloaddecode.Reason, err error) *decodePlan {
		countCompile(string(reason))
		log.Error().Err(err).Str("profileVersion", scope.ProfileVersionToken).Str("reason", string(reason)).
			Msg("A published version's payload decoder cannot be compiled by this build; integration uplinks for its device types fail with a decode error until it is replaced")
		return &decodePlan{err: &DecodePlanError{Reason: reason, Err: err}, input: head.Input}
	}
	// The version is read before the document is parsed strictly: a newer library may add
	// document keys, and a strict parse would call that a malformed document rather than the
	// downgrade it is.
	if headErr == nil && head.EnvVersion != payloaddecode.EnvVersion {
		return invalid(payloaddecode.ReasonEnvUnsupported, errors.Join(payloaddecode.ErrEnvUnsupported,
			errors.New("the decoder names an environment version this build does not know")))
	}
	doc, err := payloaddecode.ParseDocument(raw)
	if err != nil {
		return invalid(classify(err), err)
	}
	plan, err := payloaddecode.Compile(doc, decoderMetricsOf(metrics))
	if err != nil {
		return invalid(classify(err), err)
	}
	countCompile("ok")
	return &decodePlan{plan: plan, heap: decodeHeapCharge(doc, len(raw))}
}

func classify(err error) payloaddecode.Reason {
	if errors.Is(err, payloaddecode.ErrEnvUnsupported) {
		return payloaddecode.ReasonEnvUnsupported
	}
	return payloaddecode.ReasonCompileFailed
}

// HasDecoder reports whether the version declares a payload decoder, usable or not.
func (r ProfileResolution) HasDecoder() bool { return r.decode != nil }

// Decoder is the version's payload decoder as a read-only handle; the zero handle declares none.
func (r ProfileResolution) Decoder() PayloadDecoder { return PayloadDecoder{r.decode} }

// PayloadDecoder is a read-only view of a resolution's compiled decoder, the way Calculation is of
// its plan. Its field is unexported, so a holder reaches nothing it could write. The compiled plan
// is immutable and shared by every event of the device type.
type PayloadDecoder struct{ d *decodePlan }

// Declared reports whether the version has a decoder, usable or not.
func (p PayloadDecoder) Declared() bool { return p.d != nil }

// Unusable is why this build cannot run the version's decoder, as a *DecodePlanError, or nil when
// it has none or can. A caller that attempts a decode with a non-nil answer fails the uplink
// terminally with the error's Reason, and leaves every other kind of event alone.
func (p PayloadDecoder) Unusable() *DecodePlanError {
	if p.d == nil {
		return nil
	}
	return p.d.err
}

// Plan is the compiled decoder, nil when the version has none or this build cannot run it.
func (p PayloadDecoder) Plan() *payloaddecode.Plan {
	if p.d == nil {
		return nil
	}
	return p.d.plan
}

// Input is the input kind an uplink must carry for the decoder to run ("FRAME", "JSON" or
// "DECODED"), empty when the version has no decoder or an unusable one does not say.
func (p PayloadDecoder) Input() string {
	switch {
	case p.d == nil:
		return ""
	case p.d.plan != nil:
		return string(p.d.plan.InputKind())
	}
	return p.d.input
}

// MaxReadings is the most readings one decode can produce: what ingest charges before the
// decode. 0 when the version has no usable decoder.
func (p PayloadDecoder) MaxReadings() int {
	if p.Plan() == nil {
		return 0
	}
	return p.d.plan.MaxReadings()
}

// EstimatedCost is the decoder's static worst-case cost, 0 when the version has no usable one.
func (p PayloadDecoder) EstimatedCost() uint64 {
	if p.Plan() == nil {
		return 0
	}
	return p.d.plan.EstimatedCost()
}

// heapCharge is what the programs this resolution holds cost the cache, beyond the entry's
// encoding. See resolutionChargeOf.
func (r *ProfileResolution) heapCharge() int {
	if r == nil || r.decode == nil {
		return 0
	}
	return r.decode.heap
}
