// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the operator's view of backfill. Their labels are bounded: neither a tenant
// nor a job is ever one (the job read and the logs carry those), and a skip reason is one of
// calcfield's closed set or skipAmbiguous.
type Metrics struct {
	// Pass is the periodic task's own pass metrics.
	Pass             *core.PeriodicTaskMetrics
	RowsWritten      prometheus.Counter
	Samples          prometheus.Counter
	Skipped          *prometheus.CounterVec
	WindowsSkipped   *prometheus.CounterVec
	JobsFinished     *prometheus.CounterVec
	OldestActiveAge  prometheus.Gauge
	BatchSeconds     prometheus.Observer
	RefreshSeconds   prometheus.Observer
	RefreshConflicts prometheus.Counter
}

// NewMetrics builds the instruments exactly once for the service (a second registration
// panics), and fixes the label values so every series exists from the start.
func NewMetrics(ms *core.Microservice) *Metrics {
	m := &Metrics{
		Pass: ms.NewPeriodicTaskMetrics("calc_backfill"),
		RowsWritten: ms.NewCounter("calc_backfill_rows_written_total",
			"Derived measurement rows a calculated-metric backfill inserted."),
		Samples: ms.NewCounter("calc_backfill_samples_total",
			"Samples (the readings of one event at one instant) a backfill evaluated."),
		Skipped: ms.NewCounterVec("calc_backfill_skipped_total",
			"Calculated values a backfill could not compute, by the reason the formula gave.", []string{"reason"}),
		WindowsSkipped: ms.NewCounterVec("calc_backfill_windows_skipped_total",
			"Device-batch x chunk windows a backfill did not touch: compressed, or within reach of retention.", []string{"why"}),
		JobsFinished: ms.NewCounterVec("calc_backfill_jobs_finished_total",
			"Backfill jobs that reached a terminal status.", []string{"status"}),
		OldestActiveAge: ms.NewGauge("calc_backfill_oldest_active_job_age_seconds",
			"Age of the oldest queued or running backfill job on the instance; 0 when none is. A stuck job grows it without bound."),
		BatchSeconds: ms.NewHistogramVec("calc_backfill_batch_seconds",
			"Time to read, evaluate and commit one device batch of one chunk window.", nil,
			[]float64{.1, .5, 1, 5, 15, 60, 300}).WithLabelValues(),
		RefreshSeconds: ms.NewHistogramVec("calc_backfill_rollup_refresh_seconds",
			"Time to refresh the measurement rollup over one chunk window.", nil,
			[]float64{.1, .5, 1, 5, 15, 60, 300, 900}).WithLabelValues(),
		RefreshConflicts: ms.NewCounter("calc_backfill_rollup_refresh_conflicts_total",
			"Rollup refreshes that met the aggregate policy job's own refresh (55P03) and were retried."),
	}
	for _, s := range []string{"compressed", "retention"} {
		m.WindowsSkipped.WithLabelValues(s)
	}
	for _, s := range []string{"SUCCEEDED", "CANCELLED", "FAILED"} {
		m.JobsFinished.WithLabelValues(s)
	}
	return m
}
