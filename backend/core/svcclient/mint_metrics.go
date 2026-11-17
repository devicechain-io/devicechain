// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Count completed shared mint operations, not callers waiting on singleflight or
// cache hits. Fixed results avoid labels containing identities, tenants or URLs.
// Seed both so an increase can observe the first failure after an initial scrape.
var serviceTokenMints = newMintCounter(prometheus.DefaultRegisterer)

func newMintCounter(reg prometheus.Registerer) *prometheus.CounterVec {
	c := promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Namespace: "devicechain", Subsystem: "svcclient",
		Name: "service_token_mints_total",
		Help: "Completed shared service-token mint operations, by result (ok or error); excludes cache hits and pre-mint refusals.",
	}, []string{"result"})
	for _, result := range []string{"ok", "error"} {
		c.WithLabelValues(result)
	}
	return c
}
