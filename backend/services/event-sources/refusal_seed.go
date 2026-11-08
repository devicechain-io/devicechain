// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/governance"
)

// seedRefusalCounters exports every refusal counter of every configured source at zero
// from the start of the process.
//
// A labelled counter that is first touched by the refusal itself is BORN at its
// post-refusal value, so rate() and increase() over a window that holds that birth see no
// increase at all: the first burst of malformed requests after a pod starts is invisible
// to the "Rejected as malformed" panel and to any alert written as increase(...) > 0, and
// only the second burst shows. Creating the label values here makes the first refusal an
// increase from 0.
//
// The source label is the configured source id, which is what every refusal path stamps
// (see onEventDecodeFailed and the shed hooks). The shed counters carry the bounded shed
// class as well, so each class is seeded.
func seedRefusalCounters(sourceIDs []string) {
	for _, source := range sourceIDs {
		FailedDecodeCounter.WithLabelValues(source)
		InvalidEventTimeCounter.WithLabelValues(source)
		TooManyReadingsCounter.WithLabelValues(source)
		CredentialInMqttBodyCounter.WithLabelValues(source)
		BackpressureCounter.WithLabelValues(source)
		TenantGoneCounter.WithLabelValues(source)
		PublishFailedCounter.WithLabelValues(source)
		for _, cause := range []string{buildDropNoTenant, buildDropNoTransitKey, buildDropSealFailed, buildDropMarshal} {
			BuildDroppedCounter.WithLabelValues(source, cause)
		}
		for _, reason := range []string{processor.RefusedUnknownTenant, processor.RefusedCredential} {
			IngestRefusedCounter.WithLabelValues(source, reason)
		}
		for class := governance.ShedBestEffort; class <= governance.ShedGold; class++ {
			RateLimitedCounter.WithLabelValues(source, class.String())
			ReadingLimitedCounter.WithLabelValues(source, class.String())
			ReadingsRateLimitedCounter.WithLabelValues(source, class.String())
		}
	}
}

// configuredSourceIDs lists the ids of the configured event sources.
func configuredSourceIDs() []string {
	if Configuration == nil {
		return nil
	}
	ids := make([]string, 0, len(Configuration.EventSources))
	for _, s := range Configuration.EventSources {
		ids = append(ids, s.Id)
	}
	return ids
}

// mqttSourceIDs is the set of configured source ids whose transport is MQTT: the sources whose
// shed messages were acknowledged to the device before they were read.
func mqttSourceIDs() map[string]struct{} {
	ids := map[string]struct{}{}
	if Configuration == nil {
		return ids
	}
	for _, s := range Configuration.EventSources {
		if s.Type == processor.TYPE_MQTT {
			ids[s.Id] = struct{}{}
		}
	}
	return ids
}

// countAcknowledgedShed counts one message shed at the ingest ceiling when its source is MQTT,
// whose devices were told it arrived. Called from both shed hooks, so a message is counted once
// whichever stage (message or readings) refused it.
func countAcknowledgedShed(source string) {
	if _, ok := acknowledgedShedSources[source]; ok {
		ShedAcknowledgedCounter.WithLabelValues(source).Inc()
	}
}
