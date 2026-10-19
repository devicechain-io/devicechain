// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// A release's finish waits for a cluster it can trust (mqtt_failclosed_drain.go), and a cluster
// with a replica that never comes back is never trusted. Finishing late is the safe side, but a
// finish that waits for good leaves the platform durables on the store for good, and a quiet
// Debug line was all that said so. mqttFinishHold remembers since when passes have been held by
// an unsettled cluster, so a hold that outlasts mqttFinishHeldWarnAfter is logged at Warn on
// every pass and exported as a gauge by the always-registered bound watch metrics
// (mqtt_gateway_finish_held_seconds): the pods that finish a release are the ones with the
// setting OFF, which register no fail-closed series.
var mqttFinishHeldWarnAfter = 10 * time.Minute

type mqttFinishHold struct {
	mu    sync.Mutex
	since time.Time // first pass of the current run of held passes
	last  time.Time // the latest held pass
}

// held records a pass held by an unsettled cluster and returns how long the run has lasted.
func (h *mqttFinishHold) held(now time.Time) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.since.IsZero() || now.Sub(h.last) > 3*MqttFailClosedWatchInterval {
		h.since = now
	}
	h.last = now
	return now.Sub(h.since)
}

// clear ends the run: the pass was not held by an unsettled cluster.
func (h *mqttFinishHold) clear() {
	h.mu.Lock()
	h.since, h.last = time.Time{}, time.Time{}
	h.mu.Unlock()
}

// heldFor is how long the finish has been held, 0 when it is not, or when no pass has been held
// for a few intervals (the store went back to a holder, or the finish completed elsewhere).
func (h *mqttFinishHold) heldFor(now time.Time) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.since.IsZero() || now.Sub(h.last) > 3*MqttFailClosedWatchInterval {
		return 0
	}
	return now.Sub(h.since)
}

// MqttFinishHeldFor is how long the release of the MQTT message store has been unable to
// finish because the cluster was not settled; 0 when it is not held.
func (nmgr *NatsManager) MqttFinishHeldFor() time.Duration {
	return nmgr.mqttFinish.heldFor(time.Now())
}

// noteFinishHeld is called by a finish pass that stopped because the cluster could not be
// trusted to answer.
func (nmgr *NatsManager) noteFinishHeld(what, why string) {
	d := nmgr.mqttFinish.held(time.Now())
	if d < mqttFinishHeldWarnAfter {
		log.Debug().Str("stream", MqttMessageStore).Str("why", why).Msg(what)
		return
	}
	log.Warn().Str("stream", MqttMessageStore).Str("why", why).Dur("heldFor", d).
		Msg(what + ". It has been held by an unsettled cluster for this long: the released store keeps its " +
			"platform durables until every replica of the store and the durables is online and current")
}
