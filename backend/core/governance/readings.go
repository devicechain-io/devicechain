// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
)

// ReadingCeiling is the ONE definition of how a tenant's ingest ceiling meters READINGS,
// used by every transport that admits them: event-sources' HTTP, MQTT and capture-stream
// sources (through processor.NewReadingGate, which needs the send time, origin and
// redelivery signal those transports carry) and the LwM2M and Sparkplug gateways (through
// adapter.IngestLimiter's sample stage). The mechanisms differ because the transports know
// different things; the ceiling they meter against is this one.
//
// The ingest dimension's rate IS a rate of readings. A reading is one stored datum: one key
// of a measurement entry, one location, one alert (see eventlimit). The rate and Source
// pass through unchanged, so a tenant's tier override and the platform default mean the
// same number of readings per second on every transport.
//
// The burst is floored at eventlimit.MaxReadingsPerEvent. A token bucket never admits more
// than its burst in one call, and every caller charges at most one event's readings at a
// time, so with the floor a tier whose burst is below 256 sheds a full event on RATE, never
// permanently on its size.
//
// The floor applies only to a ceiling that admits at all (a positive rate). A ceiling of
// rate zero — the contention floor's hard drop, governance.Limits.Shed at factor 0 — stays
// exactly as it is: flooring its burst would hand a fresh bucket 256 free readings on every
// start and after every idle eviction.
//
// A nil resolve panics: a reading limiter with no ceiling is a construction error, not a
// default.
func ReadingCeiling(resolve core.TenantCeilingResolver) core.TenantCeilingResolver {
	if resolve == nil {
		panic("governance.ReadingCeiling: a ceiling resolver is required")
	}
	return func(tenant string) core.TenantCeiling {
		c := resolve(tenant)
		if c.RatePerSecond > 0 && c.Burst < eventlimit.MaxReadingsPerEvent {
			c.Burst = eventlimit.MaxReadingsPerEvent
		}
		return c
	}
}
