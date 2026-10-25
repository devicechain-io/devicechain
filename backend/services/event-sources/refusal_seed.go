// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "github.com/devicechain-io/dc-microservice/governance"

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
