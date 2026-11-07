// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog/log"
)

// ServiceCeilingObserver judges every service-token mint request against the authority ceiling
// (auth.CheckServiceCeiling) and RECORDS the verdict. It never refuses: in this release the
// ceiling is observed only, and the token is minted whatever the verdict. A nil observer
// records nothing, so a test or build that does not wire one behaves exactly as before.
//
//	devicechain_<subsystem>_service_token_checks_total{caller, verdict}
//
// 🔴 `caller` IS A CLOSED SET: a name from the ceiling table, or "other". The subject is chosen
// by the sender, so it is never a label itself (unbounded cardinality is a self-inflicted denial
// of service on the metrics stack). Every (caller, verdict) series is created at zero when the
// observer is built, so a rate query is defined before the first request.
//
// It is called only AFTER the secret has been verified, so an unauthenticated sender cannot move
// the counter or fill the log.
type ServiceCeilingObserver struct {
	checks *prometheus.CounterVec

	// The warn log is rate limited: a holder of the secret in a tight loop must not be able to
	// turn the observation into a log flood.
	logMu     sync.Mutex
	logWindow time.Time
	logCount  int
	now       func() time.Time
}

const (
	// ceilingLogBurst is how many non-within verdicts are logged per window.
	ceilingLogBurst  = 10
	ceilingLogWindow = time.Minute
	// ceilingLogMaxAuthorities and ceilingLogMaxLen bound what one line may carry of
	// sender-chosen text.
	ceilingLogMaxAuthorities = 8
	ceilingLogMaxLen         = 64
)

// NewServiceCeilingObserver registers the counter on reg (nil leaves it unregistered).
// Registering twice on one registry panics, so build it once.
func NewServiceCeilingObserver(reg prometheus.Registerer, subsystem string) *ServiceCeilingObserver {
	checks := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Namespace: "devicechain", Subsystem: subsystem, Name: "service_token_checks_total",
		Help: "Service-token mint requests judged against the per-caller authority ceiling, by caller " +
			"(a service from the ceiling table, or \"other\") and verdict (within_ceiling, over_ceiling, " +
			"wildcard, unknown_caller). Recorded after the shared secret verified. OBSERVE ONLY: the token " +
			"is minted whatever the verdict; a non-within verdict is one that a later release refuses.",
	}, []string{"caller", "verdict"})
	callers := append(auth.ServiceCallerNames(), auth.UnknownCallerLabel)
	for _, c := range callers {
		for _, v := range auth.ServiceCeilingVerdicts {
			checks.WithLabelValues(c, string(v))
		}
	}
	return &ServiceCeilingObserver{checks: checks, now: time.Now}
}

// Observe judges one authenticated mint request, records the verdict and returns it.
func (o *ServiceCeilingObserver) Observe(subject string, authorities []string) auth.ServiceCeilingResult {
	res := auth.CheckServiceCeiling(subject, authorities)
	if o == nil {
		return res
	}
	o.checks.WithLabelValues(res.Caller, string(res.Verdict)).Inc()
	if res.Verdict != auth.CeilingWithin && o.allowLog() {
		off := res.Offending
		if len(off) > ceilingLogMaxAuthorities {
			off = off[:ceilingLogMaxAuthorities]
		}
		quoted := make([]string, len(off))
		for i, a := range off {
			quoted[i] = clip(a)
		}
		log.Warn().Str("subject", clip(subject)).Str("caller", res.Caller).
			Str("verdict", string(res.Verdict)).Strs("offending", quoted).
			Msg("service-token request outside the caller's authority ceiling (observe only: the mint is not refused)")
	}
	return res
}

// allowLog is a fixed-window limiter: at most ceilingLogBurst lines per window.
func (o *ServiceCeilingObserver) allowLog() bool {
	o.logMu.Lock()
	defer o.logMu.Unlock()
	now := o.now()
	if now.Sub(o.logWindow) >= ceilingLogWindow {
		o.logWindow, o.logCount = now, 0
	}
	if o.logCount >= ceilingLogBurst {
		return false
	}
	o.logCount++
	return true
}

// Counter returns one series. It exists for tests that read a count back; production code never
// calls it.
func (o *ServiceCeilingObserver) Counter(caller string, v auth.ServiceCeilingVerdict) prometheus.Counter {
	return o.checks.WithLabelValues(caller, string(v))
}

// clip bounds sender-chosen text for a log line and quotes it.
func clip(s string) string {
	if len(s) > ceilingLogMaxLen {
		s = s[:ceilingLogMaxLen] + "..."
	}
	return strconv.Quote(strings.ToValidUTF8(s, "?"))
}
