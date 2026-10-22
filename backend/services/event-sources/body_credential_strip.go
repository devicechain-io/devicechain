// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sync"
	"time"

	processor "github.com/devicechain-io/dc-event-sources/processor"
	"github.com/rs/zerolog/log"
)

const (
	// bodyCredentialWarnSetCap bounds how many (tenant, device) pairs one pod remembers having
	// warned about inside the window. Past it a strip is still counted and still stripped; it is
	// just not named, so a fleet of millions cannot grow this set without bound.
	bodyCredentialWarnSetCap = 10000
	// bodyCredentialWarnWindow is how long a (tenant, device) is quiet after its WARN.
	bodyCredentialWarnWindow = time.Hour
)

// bodyCredentialWarner rate-limits the per-device WARN that capture.bodyCredential: strip
// raises: once per (tenant, device) per window per pod, from a set bounded at
// bodyCredentialWarnSetCap and cleared when the window ends. It carries the tenant and the
// topic-derived device token and nothing the device presented, so no credential field can reach
// a log line through it.
type bodyCredentialWarner struct {
	mu     sync.Mutex
	seen   map[string]struct{}
	start  time.Time
	now    func() time.Time
	window time.Duration
	cap    int
}

func newBodyCredentialWarner() *bodyCredentialWarner {
	return &bodyCredentialWarner{
		seen:   make(map[string]struct{}),
		now:    time.Now,
		window: bodyCredentialWarnWindow,
		cap:    bodyCredentialWarnSetCap,
	}
}

// shouldWarn reports whether this (tenant, device) is due a WARN now, and records it.
func (w *bodyCredentialWarner) shouldWarn(tenant, device string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if w.start.IsZero() || now.Sub(w.start) >= w.window {
		w.seen = make(map[string]struct{})
		w.start = now
	}
	key := tenant + "\x00" + device
	if _, dup := w.seen[key]; dup {
		return false
	}
	if len(w.seen) >= w.cap {
		return false
	}
	w.seen[key] = struct{}{}
	return true
}

// onBodyCredentialStripped is the callback the capture source's decode workers call for each
// event whose body credential was removed (capture.bodyCredential: strip). It counts on the
// SAME series a refusal does, so one rate tells the operator how many devices still send a
// credential in either mode, and warns once per device per hour.
func onBodyCredentialStripped(warner *bodyCredentialWarner) func(source, tenant, device string, stripped bool) {
	return func(source, tenant, device string, stripped bool) {
		if CredentialInMqttBodyCounter != nil {
			CredentialInMqttBodyCounter.WithLabelValues(source).Inc()
		}
		if warner.shouldWarn(tenant, device) {
			log.Warn().Str("source", source).Str("tenant", tenant).Str("device", device).
				Msg("Device sent a credential in the body of an MQTT event; it was removed under capture.bodyCredential: strip. " +
					"Update the device's firmware, then rotate its credential: the broker had already stored it.")
		}
	}
}

// wireBodyCredential applies capture.bodyCredential to the platform broker's two sources (the
// capture-stream source and the message-store source that backs it up) and records the setting
// on the gauge. It is called from exactly one place, the branch that builds those two, and it
// takes only *GatewayJetStreamSource: the external-broker MQTT and HTTP sources cannot be
// handed it, which is what keeps "only the platform capture source may drop a body credential"
// a property of the types rather than of a reviewer's attention.
func wireBodyCredential(sources ...*processor.GatewayJetStreamSource) {
	strip := Configuration.Capture.StripsBodyCredential()
	if BodyCredentialStripGauge != nil {
		v := 0.0
		if strip {
			v = 1
		}
		BodyCredentialStripGauge.Set(v)
	}
	if !strip {
		return
	}
	log.Warn().Msg("capture.bodyCredential is strip: this is TRANSITIONAL. Devices still sending a credential in the body " +
		"of an MQTT event are accepted with it removed. Set it to refuse when total_msg_credential_in_mqtt_body stops " +
		"rising. The setting is removed in v0.26.0, and event-sources will refuse to start while it is set.")
	warner := newBodyCredentialWarner()
	for _, s := range sources {
		s.StripBodyCredential(onBodyCredentialStripped(warner))
	}
}
