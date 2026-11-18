// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Count primitive exchanges, including session renewals, rather than cache hits
// or single-flight waiters. A login challenge is not a completed sign-in;
// VerifyMfa and SelectTenant are separate operations outside these counters.
var loginResults = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "devicechain", Subsystem: "userclient", Name: "logins_total",
	Help: "Login exchanges by result (ok, error, or mfa_challenge).",
}, []string{"result"})

var refreshResults = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "devicechain", Subsystem: "userclient", Name: "refreshes_total",
	Help: "Refresh exchanges by result (ok or error), before any login fallback.",
}, []string{"result"})

func init() {
	for _, result := range []string{"ok", "error", "mfa_challenge"} {
		loginResults.WithLabelValues(result)
	}
	for _, result := range []string{"ok", "error"} {
		refreshResults.WithLabelValues(result)
	}
}

func observeLogin(err error) {
	result := "ok"
	var mfa *MfaRequiredError
	if errors.As(err, &mfa) {
		result = "mfa_challenge"
	} else if err != nil {
		result = "error"
	}
	loginResults.WithLabelValues(result).Inc()
}

func observeRefresh(err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	refreshResults.WithLabelValues(result).Inc()
}
