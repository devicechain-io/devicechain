// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// BACK-OFF AHEAD OF OVERFLOW: refusing new MQTT connections while the message store is nearly
// full, so the sessions already connected can drain it.
//
// With infrastructure.nats.mqttFailClosed on, a full $MQTT_msgs refuses a QoS 1 publish, and
// the gateway answers a refusal by closing the device's connection (mqtt_failclosed.go). The
// device keeps the message and reconnects; so does every other device the full store refused.
// Each reconnect is a TLS handshake on the broker and an authorization request to
// device-management (a credential check, and for a password, the attempt store and the
// database), and each reconnected device publishes into the same full store and is closed
// again. Left alone, the store's refusals become a reconnect storm on the parts of the platform
// that are already behind.
//
// This watches the store's fill and, above a high-water mark, tells the auth callout to refuse
// new device connections, until the fill falls below a low-water mark. A refused CONNECT costs
// the broker no session and device-management no credential work (the callout refuses before
// either), the devices still connected keep publishing and the platform keeps reading the store
// down, and the gap between the two marks keeps it from flapping on every sample.
//
// 🔴 IT IS A LOAD SHEDDER, NOT A GUARANTEE, and nothing depends on it for correctness. The
// store's own discard-new bound is what keeps an acknowledged publish from being lost; this only
// decides how many devices are let in to publish against it. A device already connected keeps
// publishing past the mark, so the store can still fill and refuse. Hence the failure direction:
// a fill this cannot read (the broker does not answer, or the store is not bounded discard-new)
// lifts the back-off after mqttStoreBackoffStale rather than holding it, since refusing every
// device because a sample failed would be an outage of its own.
//
// 🔴 THE DEVICE SEES "NOT AUTHORIZED", NOT "SERVER UNAVAILABLE". MQTT has a CONNACK code for
// this (3, server unavailable), but the broker's gateway (nats-server v2.15.0) has no path that
// sends it: an auth callout's refusal is answered with code 5, not authorized, as every other
// refusal is. A device that treats code 5 as permanent and stops reconnecting is a firmware
// fault this cannot work around; the documentation says so.

const (
	// MqttStoreBackoffHighWater is the fill of the message store (the larger of bytes and
	// messages against their bounds) at or above which new device connections are refused.
	// Below 1 by a margin the connected fleet takes a while to publish through: a fifth of the
	// default 1 GiB hot bound is tens of seconds of a fleet publishing at 18k/s faster than the
	// platform reads, the overload a benchmark round measured.
	MqttStoreBackoffHighWater = 0.80
	// MqttStoreBackoffLowWater is the fill below which the refusal is lifted.
	MqttStoreBackoffLowWater = 0.60
	// MqttStoreBackoffInterval is how often the fill is sampled.
	MqttStoreBackoffInterval = 2 * time.Second
)

var (
	// mqttStoreBackoffStale is how long the fill may go unread before an engaged back-off is
	// lifted. A variable so a test can shorten it.
	mqttStoreBackoffStale = 5 * MqttStoreBackoffInterval
	// mqttStoreBackoffTimeout bounds one sample.
	mqttStoreBackoffTimeout = 2 * time.Second
)

// MqttStoreFill is the message store's fill for the back-off: the larger of its bytes and its
// messages against their bounds, and whether the store refuses when full at all (discard new
// with a bound). A store that does not refuse never closes a connection for being full, so it
// gives no reason to back off.
func MqttStoreFill(info *nats.StreamInfo) (fill float64, refuses bool) {
	if info == nil || info.Config.Discard != nats.DiscardNew || (info.Config.MaxBytes <= 0 && info.Config.MaxMsgs <= 0) {
		return 0, false
	}
	return streamFillRatio(info), true
}

// NextMqttStoreBackoff is the back-off's hysteresis: engaged at or above the high-water mark,
// and once engaged, held until the fill is below the low-water mark.
func NextMqttStoreBackoff(engaged bool, fill float64) bool {
	if engaged {
		return fill >= MqttStoreBackoffLowWater
	}
	return fill >= MqttStoreBackoffHighWater
}

// MqttStoreBackoffMetrics are the back-off's series.
type MqttStoreBackoffMetrics struct {
	// Engaged is 1 while new device connections are refused.
	Engaged prometheus.Gauge
	// Fill is the store's fill at the last sample that read it.
	Fill prometheus.Gauge
	// Engagements counts each time the back-off engaged.
	Engagements prometheus.Counter
	// SampleFailures counts samples that could not read the store.
	SampleFailures prometheus.Counter
}

// NewMqttStoreBackoffMetrics registers the back-off's series on ms. Build them only with the
// fail-closed setting on, so a service with it off exports nothing new.
func NewMqttStoreBackoffMetrics(ms *core.Microservice) *MqttStoreBackoffMetrics {
	return &MqttStoreBackoffMetrics{
		Engaged: ms.NewGauge("mqtt_store_backoff_engaged",
			"1 while new MQTT device connections are refused because the broker's MQTT message store is above "+
				"its high-water mark, 0 otherwise."),
		Fill: ms.NewGauge("mqtt_store_fill_ratio",
			"How full the broker's MQTT message store is, against its bound (the larger of bytes and messages), "+
				"at the last sample that could read it."),
		Engagements: ms.NewCounter("mqtt_store_backoff_engagements_total",
			"Times new MQTT device connections started being refused because the broker's MQTT message store "+
				"crossed its high-water mark."),
		SampleFailures: ms.NewCounter("mqtt_store_backoff_sample_failures_total",
			"Samples of the broker's MQTT message store that could not read it."),
	}
}

// MqttStoreBackoff samples the message store and holds the back-off decision; see the file
// comment. Engaged is safe to call from any goroutine.
type MqttStoreBackoff struct {
	nmgr    *NatsManager
	metrics *MqttStoreBackoffMetrics
	engaged atomic.Bool
	// lastRead is when a sample last read the store; only the sampling goroutine touches it.
	lastRead time.Time
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// StartMqttStoreBackoff starts sampling the message store every interval (MqttStoreBackoffInterval
// in production). metrics may be nil. Stop it before the broker connection closes.
func (nmgr *NatsManager) StartMqttStoreBackoff(ctx context.Context, metrics *MqttStoreBackoffMetrics, interval time.Duration) *MqttStoreBackoff {
	ctx, cancel := context.WithCancel(ctx)
	b := &MqttStoreBackoff{nmgr: nmgr, metrics: metrics, cancel: cancel, lastRead: time.Now()}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		b.sample(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.sample(ctx)
			}
		}
	}()
	return b
}

// Engaged reports whether new device connections are being refused. A nil back-off never is.
func (b *MqttStoreBackoff) Engaged() bool {
	return b != nil && b.engaged.Load()
}

// Stop ends the sampling and lifts the back-off.
func (b *MqttStoreBackoff) Stop() {
	if b == nil {
		return
	}
	b.cancel()
	b.wg.Wait()
	b.set(false, "the service is stopping")
}

// sample reads the store once and moves the decision.
func (b *MqttStoreBackoff) sample(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, mqttStoreBackoffTimeout)
	info, err := b.nmgr.js.StreamInfo(MqttMessageStore, nats.Context(rctx))
	cancel()
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, nats.ErrStreamNotFound) {
		info, err = nil, nil
	}
	now := time.Now()
	if err != nil {
		if b.metrics != nil {
			b.metrics.SampleFailures.Inc()
		}
		if b.engaged.Load() && now.Sub(b.lastRead) >= mqttStoreBackoffStale {
			b.set(false, "the MQTT message store could not be read for "+mqttStoreBackoffStale.String())
		}
		return
	}
	b.lastRead = now
	fill, refuses := MqttStoreFill(info)
	if b.metrics != nil {
		b.metrics.Fill.Set(fill)
	}
	if !refuses {
		b.set(false, "the MQTT message store does not refuse when full")
		return
	}
	b.set(NextMqttStoreBackoff(b.engaged.Load(), fill), "")
}

// set moves the decision, logging and counting a change.
func (b *MqttStoreBackoff) set(engaged bool, why string) {
	if b.engaged.Swap(engaged) == engaged {
		return
	}
	if b.metrics != nil {
		if engaged {
			b.metrics.Engaged.Set(1)
			b.metrics.Engagements.Inc()
		} else {
			b.metrics.Engaged.Set(0)
		}
	}
	if engaged {
		log.Warn().Str("stream", MqttMessageStore).Float64("highWater", MqttStoreBackoffHighWater).
			Msg("The MQTT message store is above its high-water mark: refusing new MQTT device connections so the " +
				"connected devices can drain it. Devices already connected keep publishing")
		return
	}
	ev := log.Info().Str("stream", MqttMessageStore).Float64("lowWater", MqttStoreBackoffLowWater)
	if why != "" {
		ev = ev.Str("reason", why)
	}
	ev.Msg("New MQTT device connections are accepted again")
}
