// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/transport"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// routing is what the delivery gate learned about a command's device that the transport adapter
// can use to route the command without opening it. It is read from the SAME presence state the
// verdict was decided on, so the stamp and the verdict cannot come from different reads.
type routing struct {
	// transport is the transport the device's projected source names, empty when the projection
	// held no row for it (or no gate is wired) OR when the source is not one the platform mints.
	transport transport.Name
}

// routingOf reduces a device's presence state to its routing.
//
// 🔴 ONLY A MINTED TRANSPORT IS ROUTED ON. A plain MQTT or HTTP device projects the OPERATOR's
// event-source id ("mqtt1" by default), which is not a vocabulary: stamping it would put
// operator-chosen text into a message header, and an adapter has nothing to compare it with.
// Those devices carry no header, which an adapter reads as "unknown", never as "not mine".
func routingOf(known bool, source string) routing {
	if !known {
		return routing{}
	}
	if t := transport.Of(source); transport.IsMinted(t) {
		return routing{transport: t}
	}
	return routing{}
}

// impactCacheTTL bounds how long a resolved impact is reused. The impact is a property of the
// device TYPE's current profile version, so it changes only when someone publishes one; a
// minute of staleness is far inside the time an operator needs to notice. It is a bound on
// calls to device-management, not a correctness input.
const impactCacheTTL = time.Minute

// impactCacheMax bounds the cache's size. When it is reached the cache is dropped whole, which
// costs one burst of lookups and needs no eviction order.
const impactCacheMax = 4096

type impactKey struct{ tenant, device, command string }

type impactEntry struct {
	impact  string
	expires time.Time
}

// impactCache memoizes command impacts. Only SUCCESSFUL lookups are cached: a failure must be
// retried on the next command, because the header it leaves off is the fail-closed direction and
// caching it would extend an outage of device-management by the TTL.
type impactCache struct {
	mu      sync.Mutex
	entries map[impactKey]impactEntry
	now     func() time.Time
}

func (c *impactCache) get(k impactKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || !c.clock().Before(e.expires) {
		return "", false
	}
	return e.impact, true
}

func (c *impactCache) put(k impactKey, impact string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= impactCacheMax {
		c.entries = make(map[impactKey]impactEntry)
	}
	c.entries[k] = impactEntry{impact: impact, expires: c.clock().Add(impactCacheTTL)}
}

func (c *impactCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// dispatchHeaders builds the routing headers for one command's device-commands message.
//
// 🔴 NOTHING IS STAMPED UNLESS SPARKPLUG COMMANDS ARE ENABLED. With the flag off, the message is
// exactly what it was before the headers existed: no consumer reads them, and a header on every
// command of every transport is a wire change nobody asked for. That is what "behaviour
// unchanged when nothing is enabled" has to mean at the byte level.
//
//   - Dc-Transport is stamped whenever the device is on a transport the platform mints. It is
//     routing only; it authorizes nothing, and an absent header means unknown, not "not yours".
//   - Dc-Command-Impact is stamped only for a Sparkplug device, because only that adapter gates
//     on it, and resolving it costs a call to device-management. If the lookup fails the header
//     is LEFT OFF (and counted): the adapter reads absent as unknown and refuses the dangerous
//     class, so a failure here can only make a command refused, never actuated.
//
// It returns nil when there is nothing to stamp, so the message keeps a nil Headers map.
func (cproc *CommandDeliveryProcessor) dispatchHeaders(ctx context.Context, cmd *model.Command,
	route routing) map[string]string {
	if !cproc.SparkplugCommands || route.transport == "" {
		return nil
	}
	headers := map[string]string{cmddownlink.HeaderTransport: string(route.transport)}
	if route.transport == transport.Sparkplug {
		if impact, ok := cproc.commandImpact(ctx, cmd); ok {
			headers[cmddownlink.HeaderCommandImpact] = impact
		}
	}
	return headers
}

// commandImpact resolves a command's impact through the owner, memoized. It reports false when
// no resolver is wired or the owner could not decide.
func (cproc *CommandDeliveryProcessor) commandImpact(ctx context.Context, cmd *model.Command) (string, bool) {
	if cproc.CommandImpacts == nil {
		incr(cproc.ImpactLookupFailures, 1)
		return "", false
	}
	key := impactKey{tenant: cmd.TenantId, device: cmd.DeviceToken, command: cmd.Name}
	if impact, ok := cproc.impacts.get(key); ok {
		return impact, true
	}
	impact, err := cproc.CommandImpacts.CommandImpact(ctx, cmd.DeviceToken, cmd.Name)
	if err != nil {
		incr(cproc.ImpactLookupFailures, 1)
		log.Warn().Err(err).Str("command", cmd.Token).Str("device", cmd.DeviceToken).
			Msg("Could not decide a command's impact; it is dispatched without an impact stamp, which the transport adapter reads as unknown and fails closed on.")
		return "", false
	}
	cproc.impacts.put(key, impact)
	return impact, true
}
