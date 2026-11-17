// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Fixed labels avoid exposing tenant, credential, query, or endpoint values.
// Queries measure the shared call path after the scoped-caller authorization gate,
// including validation and token acquisition. Mints count actual attempts, not
// cache hits or callers joining a single flight.
var queryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "devicechain", Subsystem: "svcclient", Name: "query_duration_seconds",
	Help: "Cross-service query duration, including token acquisition and local validation.",
})

var queryResults = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "devicechain", Subsystem: "svcclient", Name: "queries_total",
	Help: "Cross-service queries after the scoped-caller gate, by result (ok or error).",
}, []string{"result"})

var mintDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "devicechain", Subsystem: "svcclient", Name: "service_token_mint_duration_seconds",
	Help: "Duration of actual service-token mint attempts, successful or failed.",
})

func init() {
	for _, result := range []string{"ok", "error"} {
		queryResults.WithLabelValues(result)
	}
}

func observeQuery(started time.Time, err *error) {
	queryDuration.Observe(time.Since(started).Seconds())
	result := "ok"
	if *err != nil {
		result = "error"
	}
	queryResults.WithLabelValues(result).Inc()
}
