// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/devicechain-io/dc-microservice/x509trust/ocspclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// The OCSP results callout_x509_total is counted by. ocsp_revoked is a responder saying a
// certificate on the verified path is revoked; ocsp_unavailable is a REQUIRE_CURRENT refusal for a
// status that could not be confirmed (why is x509_ocsp_unavailable_allowed_total's cousin, the
// reason in the log line). An ENFORCE_KNOWN connect that went through without an answer is not a
// result: it is counted by x509_ocsp_unavailable_allowed_total{reason}.
const (
	x509ResultOcspRevoked     = "ocsp_revoked"
	x509ResultOcspUnavailable = "ocsp_unavailable"
)

// ocspReasonDisabled is the allowed-counter reason for an ENFORCE_KNOWN anchor admitted unchecked
// because the instance's OCSP switch is off.
const ocspReasonDisabled = "disabled"

// OcspChecker asks the responders. *ocspclient.Client is the real one; a test injects its own.
type OcspChecker interface {
	Check(ctx context.Context, req ocspclient.Request) ocspclient.Outcome
}

// OcspMetrics are the OCSP check's metrics.
type OcspMetrics struct {
	// Allowed counts connects an ENFORCE_KNOWN anchor admitted although a status could not be
	// learned, by why: you are not checking what you think you are when this rises.
	Allowed *prometheus.CounterVec
	// Fetch observes network fetches by outcome (ok, failed).
	Fetch *prometheus.HistogramVec
	// Cache counts answer-cache lookups (hit, miss).
	Cache *prometheus.CounterVec
	// InFlight is the fetches outstanding on this replica.
	InFlight prometheus.Gauge
	// BreakerOpened counts responders skipped after repeated failures.
	BreakerOpened prometheus.Counter
}

// NewOcspMetrics registers the OCSP metrics, every series at zero.
func NewOcspMetrics(ms *core.Microservice) *OcspMetrics {
	m := &OcspMetrics{
		Allowed: ms.NewCounterVec("x509_ocsp_unavailable_allowed_total",
			"Certificate connects an ENFORCE_KNOWN anchor admitted although a certificate's OCSP status could not be learned, by reason "+
				"(no_responder, blocked, timeout, unreachable, bad_response, breaker_open, saturated, host_budget, cache_only, unverified, "+
				"unknown_cert, stale, disabled). A rising rate means the anchor is not checking what it is believed to check.",
			[]string{"reason"}),
		Fetch: ms.NewHistogramVec("x509_ocsp_fetch_seconds",
			"Time an OCSP fetch took, by outcome (ok is a verified answer).", []string{"outcome"},
			[]float64{.005, .01, .025, .05, .1, .2, .4, .8, 1.6, 2}),
		Cache: ms.NewCounterVec("x509_ocsp_cache_lookups_total",
			"OCSP answer-cache lookups by result (hit, miss).", []string{"result"}),
		InFlight: ms.NewGauge("x509_ocsp_inflight", "OCSP fetches outstanding on this replica."),
		BreakerOpened: ms.NewCounter("x509_ocsp_breaker_opened_total",
			"Times a responder was skipped for a while after repeated endpoint failures."),
	}
	for _, r := range ocspclient.Reasons {
		m.Allowed.WithLabelValues(string(r))
	}
	m.Allowed.WithLabelValues(ocspReasonDisabled)
	for _, o := range []string{"ok", "failed"} {
		m.Fetch.WithLabelValues(o)
	}
	for _, r := range []string{"hit", "miss"} {
		m.Cache.WithLabelValues(r)
	}
	return m
}

// Hooks are the client's observers over these metrics.
func (m *OcspMetrics) Hooks() ocspclient.Hooks {
	if m == nil {
		return ocspclient.Hooks{}
	}
	return ocspclient.Hooks{
		Fetch: func(d time.Duration, ok bool) {
			outcome := "failed"
			if ok {
				outcome = "ok"
			}
			m.Fetch.WithLabelValues(outcome).Observe(d.Seconds())
		},
		Cache: func(hit bool) {
			r := "miss"
			if hit {
				r = "hit"
			}
			m.Cache.WithLabelValues(r).Inc()
		},
		InFlight:      func(delta int) { m.InFlight.Add(float64(delta)) },
		BreakerOpened: func() { m.BreakerOpened.Inc() },
	}
}

// ocspCheck is the responder's OCSP wiring: the master switch and the checker.
type ocspCheck struct {
	enabled bool
	checker OcspChecker
	metrics *OcspMetrics
	// failOpenLog rate-limits the warning for an ENFORCE_KNOWN connect admitted unchecked.
	failOpenLog rateLimitedLog
}

// WithOcsp wires the OCSP check. enabled is the instance's master switch
// (deviceManagement.callout.ocsp.enabled); with it off no checker is ever called, an
// ENFORCE_KNOWN anchor runs as OFF, and a REQUIRE_CURRENT anchor refuses (its contract is to
// refuse what it cannot confirm). A responder never given this runs every anchor as the switch
// being off.
func (c *CalloutResponder) WithOcsp(enabled bool, checker OcspChecker, metrics *OcspMetrics) *CalloutResponder {
	c.ocsp = &ocspCheck{enabled: enabled && checker != nil, checker: checker, metrics: metrics}
	return c
}

func (o *ocspCheck) countAllowed(reason string) {
	if o != nil && o.metrics != nil {
		o.metrics.Allowed.WithLabelValues(reason).Inc()
	}
}

// applyOcsp is the OCSP step of a connect whose chain has verified and passed the CRL and manual
// checks (so a certificate already refused never costs a fetch). It returns the verified paths
// that survive, or the callout result that counts a refusal.
//
// all is EVERY path that verified to the anchor, before the CRL step dropped any; survivor is
// what the CRL step left. Every certificate on all is asked about, and a revoked answer for ANY of
// them refuses: a path the CRL step dropped for completeness (no current list) may still carry an
// intermediate its CA says is revoked, and another surviving path must not hide that.
//
// 🔴 OFF RUNS NOTHING. An anchor whose mode is OFF returns before any OCSP code is reached: no
// checker call, no lookup, no byte on the wire. That is what keeps every anchor that predates
// OCSP byte-for-byte as it was.
//
// 🔴 FAIL CLOSED where the anchor asked for it, and by default everywhere else it is a REFUSAL
// unless the decision admits: a stored mode this build does not know is "error", an ApplyOCSP
// error with no classification is "error", and the only road to a nil refusal is a nil error.
func (c *CalloutResponder) applyOcsp(ctx context.Context, tenant string, anchor *model.DeviceTrustAnchor,
	all, verified *x509trust.Verified) (*x509trust.Verified, string) {
	mode, err := anchor.OcspModeOf()
	if err != nil {
		c.logAuthFailure(tenant, err)
		return nil, x509ResultError
	}
	if mode == x509trust.OCSPOff {
		return verified, ""
	}
	o := c.ocsp
	if o == nil || !o.enabled {
		if mode == x509trust.OCSPRequireCurrent {
			log.Warn().Str("tenant", tenant).Str("anchor", anchor.Token).
				Msg("Auth-callout refused a certificate connect: the anchor requires a current OCSP answer and OCSP is switched off on this instance.")
			return nil, x509ResultOcspUnavailable
		}
		o.countAllowed(ocspReasonDisabled)
		return verified, ""
	}
	out := o.checker.Check(ctx, ocspclient.Request{
		Tenant:      tenant,
		Anchor:      strconv.FormatUint(uint64(anchor.ID), 10),
		OverrideURL: anchor.OcspResponderUrlOf(),
		Targets:     all.OCSPTargets(),
	})
	for _, t := range all.OCSPTargets() {
		if out.Results[t.Key()] == x509trust.OCSPRevoked {
			return nil, x509ResultOcspRevoked
		}
	}
	decision, err := verified.ApplyOCSP(mode, out.Results)
	if err != nil {
		switch x509trust.ReasonOf(err) {
		case x509trust.ReasonRevoked:
			return nil, x509ResultOcspRevoked
		case x509trust.ReasonRevocationUnknown:
			c.logOcspUnavailable(tenant, anchor, out)
			return nil, x509ResultOcspUnavailable
		default:
			c.logAuthFailure(tenant, err)
			return nil, x509ResultError
		}
	}
	if decision.Survivor == nil {
		return nil, x509ResultError
	}
	if decision.Incomplete {
		reason := firstOcspReason(verified, out)
		o.countAllowed(string(reason))
		if ok, held := o.failOpenLog.allow(c.now()); ok {
			log.Warn().Str("tenant", tenant).Str("anchor", anchor.Token).Str("reason", string(reason)).Int("heldBack", held).
				Msg("Auth-callout admitted a certificate connect although its OCSP status could not be learned (ENFORCE_KNOWN).")
		}
	}
	return decision.Survivor, ""
}

func (c *CalloutResponder) logOcspUnavailable(tenant string, anchor *model.DeviceTrustAnchor, out ocspclient.Outcome) {
	if ok, held := c.ocsp.failOpenLog.allow(c.now()); ok {
		log.Warn().Str("tenant", tenant).Str("anchor", anchor.Token).Str("reason", string(firstOcspReasonOf(out))).Int("heldBack", held).
			Msg("Auth-callout refused a certificate connect: the anchor requires a current OCSP answer and none could be had.")
	}
}

// firstOcspReason is the reason for the first unanswered target of the verified paths, leaf first.
func firstOcspReason(v *x509trust.Verified, out ocspclient.Outcome) ocspclient.Reason {
	for _, t := range v.OCSPTargets() {
		if r, ok := out.Reasons[t.Key()]; ok {
			return r
		}
	}
	return ocspclient.ReasonNoResponder
}

func firstOcspReasonOf(out ocspclient.Outcome) ocspclient.Reason {
	for _, r := range out.Reasons {
		return r
	}
	return ocspclient.ReasonNone
}
