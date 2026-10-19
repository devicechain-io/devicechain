// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// MqttStoreBoundWatchInterval is how often MqttStoreBoundWatch looks at the gateway's streams.
// One StreamInfo per stream per pass, so the cost is three small API calls per pod per interval.
const MqttStoreBoundWatchInterval = 30 * time.Second

// The gateway creates its streams on the first MQTT client, which on a fresh install is long
// after event-sources has started and given up waiting (ReconcileMqttStores). Until something
// bounded them they were UNBOUNDED, and stayed so until the next restart of event-sources,
// which on a young instance may be weeks away. This watch is that something: it keeps looking,
// bounds a stream the first pass that finds it unbounded, and keeps the bound if the stream is
// ever recreated.

// MqttStoreBoundMetrics reports the watch.
type MqttStoreBoundMetrics struct {
	// unbounded is 1 per gateway stream the last pass found without its ceiling and could not
	// bound, 0 otherwise (absent streams included: nothing is at risk).
	unbounded *prometheus.GaugeVec
	// boundedLate counts each time a pass had to put the ceiling on a stream that was
	// unbounded while the service ran.
	boundedLate *prometheus.CounterVec
	// finishHeld is how long, in seconds, the release of the fail-closed message store has been
	// unable to finish because a replica of the store or of a platform durable is offline or not
	// current, 0 when it is not held. It lives here because the pods that finish a release are
	// the ones with the fail-closed setting off, which register no fail-closed series.
	finishHeld prometheus.Gauge
}

// NewMqttStoreBoundMetrics registers the watch's series on ms, each exported at zero.
func NewMqttStoreBoundMetrics(ms *core.Microservice) *MqttStoreBoundMetrics {
	m := &MqttStoreBoundMetrics{
		unbounded: ms.NewGaugeVec("mqtt_gateway_stream_unbounded",
			"1 when the last check found a stream of the broker's MQTT gateway without the size ceiling the "+
				"platform sets and could not set it, 0 otherwise. An unbounded gateway stream can fill the "+
				"broker's disk.",
			[]string{"stream"}),
		boundedLate: ms.NewCounterVec("mqtt_gateway_stream_bounded_late_total",
			"Times a check found a stream of the broker's MQTT gateway without its size ceiling while "+
				"event-sources was running (the gateway creates them when the first device connects) and set it.",
			[]string{"stream"}),
		finishHeld: ms.NewGauge("mqtt_gateway_finish_held_seconds",
			"Seconds the release of the MQTT fail-closed message store has been unable to finish because a "+
				"replica of the store or of one of its platform durables is offline or not current, 0 when it "+
				"is not held. While it is above 0 the released store keeps its platform durables."),
	}
	for _, s := range MqttStoreBounds(1, 1) {
		m.unbounded.WithLabelValues(s.Name).Set(0)
		m.boundedLate.WithLabelValues(s.Name)
	}
	return m
}

// MqttStoreBoundWatch runs a bound pass on an interval.
type MqttStoreBoundWatch struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// StartMqttStoreBoundWatch bounds the gateway's streams as they appear. Every event-sources pod
// runs one. msgsMaxBytes <= 0 leaves $MQTT_msgs alone, which is how the fail-closed setting says
// it owns that stream (ReconcileMqttFailClosed declares it discard-new at its own bound; this
// must not fight it). qos2MaxBytes must be positive. m may be nil. Stop it before the broker
// connection closes. The first pass runs immediately.
func (nmgr *NatsManager) StartMqttStoreBoundWatch(ctx context.Context, m *MqttStoreBoundMetrics, interval time.Duration,
	msgsMaxBytes, qos2MaxBytes int64) *MqttStoreBoundWatch {
	ctx, cancel := context.WithCancel(ctx)
	w := &MqttStoreBoundWatch{cancel: cancel}
	bounds := mqttWatchBounds(msgsMaxBytes, qos2MaxBytes)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			nmgr.checkMqttStoreBounds(ctx, m, bounds)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return w
}

// Stop ends the watch and waits for a pass in progress to return.
func (w *MqttStoreBoundWatch) Stop() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

func mqttWatchBounds(msgsMaxBytes, qos2MaxBytes int64) []MqttStoreBound {
	var out []MqttStoreBound
	for _, s := range MqttStoreBounds(msgsMaxBytes, qos2MaxBytes) {
		if s.Name == MqttMessageStore && msgsMaxBytes <= 0 {
			continue
		}
		if s.MaxBytes <= 0 {
			// Never write a ceiling of 0: it means UNLIMITED to JetStream.
			continue
		}
		out = append(out, s)
	}
	return out
}

// checkMqttStoreBounds is one pass: a StreamInfo per stream, and an update only for a stream
// whose ceiling is not the declared one.
func (nmgr *NatsManager) checkMqttStoreBounds(ctx context.Context, m *MqttStoreBoundMetrics, bounds []MqttStoreBound) {
	if m != nil {
		m.finishHeld.Set(nmgr.MqttFinishHeldFor().Seconds())
	}
	for _, s := range bounds {
		if ctx.Err() != nil {
			return
		}
		info, err := nmgr.js.StreamInfo(s.Name)
		if errors.Is(err, nats.ErrStreamNotFound) {
			setUnbounded(m, s.Name, 0)
			continue
		}
		if err != nil {
			// Could not look: keep the last value. A broker that does not answer has alerts of its own.
			log.Warn().Err(err).Str("stream", s.Name).Msg("Could not check the MQTT gateway stream's ceiling; the next check tries again")
			continue
		}
		// A message store a fail-closed pass created and did not finish, with no mark on it:
		// it refuses every QoS 1 publish until it has the gateway's subjects (see
		// boundMqttStore).
		marks := nmgr.marksOf(info)
		pending := false
		if s.Name == MqttMessageStore && !marks.owned && !marks.released {
			var perr error
			if pending, perr = nmgr.isAbandonedPendingMqttStore(ctx, info); perr != nil {
				log.Warn().Err(perr).Str("stream", s.Name).Msg("Could not tell whether the MQTT message store was abandoned by a fail-closed check; the next check tries again")
				pending = false
			}
		}
		if info.Config.MaxBytes == s.MaxBytes && !pending {
			setUnbounded(m, s.Name, 0)
			continue
		}
		cfg := info.Config
		previous := cfg.MaxBytes
		cfg.MaxBytes = s.MaxBytes
		if pending {
			cfg.Subjects = []string{mqttMsgsSubjectPrefix + ">"}
		}
		if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
			if previous <= 0 {
				setUnbounded(m, s.Name, 1)
			}
			log.Error().Err(err).Str("stream", s.Name).Int64("maxBytes", s.MaxBytes).Int64("current", previous).
				Msg("Could not bound the MQTT gateway's stream. It can consume the JetStream disk budget's headroom; the next check tries again")
			continue
		}
		setUnbounded(m, s.Name, 0)
		if m != nil && previous <= 0 {
			m.boundedLate.WithLabelValues(s.Name).Inc()
		}
		log.Info().Str("stream", s.Name).Int64("maxBytes", s.MaxBytes).Int64("previous", previous).
			Msg("Bounded MQTT gateway stream that appeared after startup so it cannot consume the JetStream disk budget's headroom")
	}
}

func setUnbounded(m *MqttStoreBoundMetrics, stream string, v float64) {
	if m != nil {
		m.unbounded.WithLabelValues(stream).Set(v)
	}
}
