// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// trustAnchorExpiryWindow is how far ahead an anchor counts as expiring, and the value of the
// gauge's window label. The window is part of the series so a second one can be added without
// renaming it.
const (
	trustAnchorExpiryWindow      = 30 * 24 * time.Hour
	trustAnchorExpiryWindowLabel = "30d"
	// trustAnchorExpiryRefresh is how long a reading is reused. An anchor's notAfter moves with
	// the calendar and an anchor's enabled flag with an operator, neither of which an alert
	// pending an hour needs to see within the minute, so a scrape every 15 s does not query.
	trustAnchorExpiryRefresh = 5 * time.Minute
	// trustAnchorExpiryRetry is how long after a failed reading the next scrape tries again.
	trustAnchorExpiryRetry   = 30 * time.Second
	trustAnchorExpiryTimeout = 3 * time.Second
)

// trustAnchorExpiryGauge reads device_trust_anchors_expiring at scrape time from a cached count.
// While the count cannot be read it reports NO series (never a stale or zero one): an alert over
// a gauge that says 0 because the database was unreachable is a quiet lie, and the availability
// alerts are what speak for an unreachable database.
type trustAnchorExpiryGauge struct {
	count   func(ctx context.Context, within time.Duration, now time.Time) (int64, error)
	now     func() time.Time
	mu      sync.Mutex
	value   int64
	have    bool
	readAt  time.Time
	failed  bool
	logOnce sync.Once
}

func (g *trustAnchorExpiryGauge) sample() []core.GaugeSample {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	wait := trustAnchorExpiryRefresh
	if g.failed {
		wait = trustAnchorExpiryRetry
	}
	if !g.readAt.IsZero() && now.Sub(g.readAt) < wait {
		return g.series()
	}
	ctx, cancel := context.WithTimeout(context.Background(), trustAnchorExpiryTimeout)
	defer cancel()
	n, err := g.count(ctx, trustAnchorExpiryWindow, now)
	g.readAt = now
	if err != nil {
		g.failed, g.have = true, false
		g.logOnce.Do(func() {
			log.Warn().Err(err).Msg("Could not count the expiring trust anchors; the gauge reports nothing until it can.")
		})
		return nil
	}
	g.failed, g.have, g.value = false, true, n
	return g.series()
}

func (g *trustAnchorExpiryGauge) series() []core.GaugeSample {
	if !g.have {
		return nil
	}
	return []core.GaugeSample{{Labels: []string{trustAnchorExpiryWindowLabel}, Value: float64(g.value)}}
}

// buildTrustAnchorExpiryGauge registers device_trust_anchors_expiring{window} on the service.
// The count is read through the plain Api when the first scrape arrives; before the Api exists
// there is nothing to report and the gauge reports no series.
func buildTrustAnchorExpiryGauge() {
	g := &trustAnchorExpiryGauge{now: time.Now,
		count: func(ctx context.Context, within time.Duration, now time.Time) (int64, error) {
			if Api == nil {
				return 0, errApiNotReady
			}
			return Api.CountTrustAnchorsExpiring(ctx, within, now)
		}}
	Microservice.NewScrapedGaugeVec("device_trust_anchors_expiring",
		"Enabled trust anchors, across every tenant, that expire within the window or already have. "+
			"Devices whose certificates chain to one of them are refused once it expires. "+
			"One count for the instance; the window label is the horizon.",
		[]string{"window"}, g.sample)
}

var errApiNotReady = errors.New("the device-management Api is not built yet")
