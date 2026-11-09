// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-event-sources/processor"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
)

// The lag-credited gate's metrics (ingestMetering: lagCredit). All are additive; nothing here is read in
// legacy mode, where the vectors stay empty. None carries a tenant label.
var (
	// IngestShedCauseCounter counts the messages the lag-credited gate shed, by why. A shed with cause
	// `ceiling` on the fresh route while the lag credit is high is the #2163 class coming back.
	IngestShedCauseCounter *prometheus.CounterVec
	// IngestLagCreditHistogram is the lag credit (seconds) fresh-route admissions were judged against.
	IngestLagCreditHistogram *prometheus.HistogramVec
	// ReaderLagGauge is the corrected age of the last first delivery read from a stream.
	ReaderLagGauge *prometheus.GaugeVec
	// AppendSkewGauge is the estimated offset of the broker's append stamps from this pod's clock.
	AppendSkewGauge *prometheus.GaugeVec
	// AppendSkewClampedCounter counts first deliveries read while the skew estimate sat at its clamp.
	AppendSkewClampedCounter *prometheus.CounterVec
)

func initializeLagCreditMetrics() {
	IngestShedCauseCounter = Microservice.NewCounterVec(
		"ingest_shed_cause_total",
		"Count of messages the lag-credited ingest gate shed, by reader stream, route (fresh, backlog, live) and cause "+
			"(ceiling, contention, hard_drop). Empty unless eventSources.ingestMetering is lagCredit",
		[]string{"stream", "route", "cause"})
	IngestLagCreditHistogram = Microservice.NewHistogramVec(
		"ingest_lag_credit_seconds",
		"Lag credit the lag-credited ingest gate judged fresh-route admissions against, by reader stream",
		[]string{"stream"}, []float64{0, 0.05, 0.25, 0.5, 1, 2, 5})
	ReaderLagGauge = Microservice.NewGaugeVec(
		"reader_lag_seconds",
		"Corrected age (the broker's append stamp against this pod's clock, less the estimated skew) of the last message read from a reader stream",
		[]string{"stream"})
	AppendSkewGauge = Microservice.NewGaugeVec(
		"append_skew_seconds",
		"Estimated offset of the broker's append stamps from this pod's clock; positive means the broker's clock is behind",
		[]string{"stream"})
	AppendSkewClampedCounter = Microservice.NewCounterVec(
		"append_skew_clamped_total",
		"Count of messages read while the append-stamp skew estimate sat at its clamp (the broker's clock is more than 2s out)",
		[]string{"stream"})
	seedLagCreditMetrics(IngestMeteringMode)
}

// seedLagCreditMetrics exports the series the lag-credited gate's alerts read at 0, in lagCredit mode only,
// so the first shed after a restart is an increase from 0 rather than a series born at its final value
// (which increase() reads as no change). In legacy mode they stay empty: the gate is not running. The
// vocabulary is bounded: three stream labels, three routes and three causes.
func seedLagCreditMetrics(mode IngestMetering) {
	if mode != IngestMeteringLagCredit {
		return
	}
	streams := []string{"capture", "store", streamLabel("")}
	for _, stream := range streams {
		for _, route := range []core.IngestRoute{core.RouteFresh, core.RouteBacklog, core.RouteLive} {
			for _, cause := range []core.IngestCause{core.CauseCeiling, core.CauseContention, core.CauseHardDrop} {
				IngestShedCauseCounter.WithLabelValues(stream, route.String(), cause.String())
			}
		}
		IngestLagCreditHistogram.WithLabelValues(stream)
	}
	for _, stream := range streams[:2] {
		AppendSkewClampedCounter.WithLabelValues(stream)
	}
}

// ingestContentionFactor is a tenant's ADR-063 contention factor for the lag-credited gate: 1 when there
// is no contention, or the tenant's shed priority has not resolved (never shed the unknown), or it is gold;
// a value in (0,1) to scale its allowance; 0 to refuse its fresh traffic outright.
func ingestContentionFactor(tenant string) float64 {
	level := contentionLevel()
	if level <= 0 {
		return 1
	}
	prio, resolved := ingestShedPriority(tenant)
	if !resolved {
		return 1
	}
	return governance.ShedFactor(governance.ShedClassOf(prio), level)
}

// buildLagCreditIngest builds the lag-credited gate over the BASE ceilings buildRateLimiter resolved.
func buildLagCreditIngest() *processor.LagCreditIngest {
	return processor.NewLagCreditIngest(processor.LagCreditIngestConfig{
		Ceiling: ingestBaseCeiling,
		Reading: governance.ReadingCeiling(ingestBaseCeiling),
		Factor:  ingestContentionFactor,
		Hooks: processor.LagCreditHooks{
			Shed: func(source, tenant, stream string, route core.IngestRoute, cause core.IngestCause) {
				IngestShedCauseCounter.WithLabelValues(streamLabel(stream), route.String(), cause.String()).Inc()
				onRateShed(source, tenant)
			},
			ReadingShed: onReadingShed,
			Unresolved:  ingestUnresolved,
			Credit: func(stream string, credit time.Duration) {
				IngestLagCreditHistogram.WithLabelValues(streamLabel(stream)).Observe(credit.Seconds())
			},
			Reader: func(stream string, lag, skew time.Duration, clamped bool) {
				ReaderLagGauge.WithLabelValues(stream).Set(lag.Seconds())
				AppendSkewGauge.WithLabelValues(stream).Set(skew.Seconds())
				if clamped {
					AppendSkewClampedCounter.WithLabelValues(stream).Inc()
				}
			},
		},
	})
}

// streamLabel names the stream of a message that has none (external MQTT, presence).
func streamLabel(stream string) string {
	if stream == "" {
		return "none"
	}
	return stream
}
