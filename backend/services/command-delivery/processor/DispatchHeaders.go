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
	"github.com/devicechain-io/dc-command-delivery/presence"
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

// impactTimeout bounds the whole of one batch's impact resolution, however many command keys it
// names. It is deliberately short: the sweep holds the cluster-wide sweep lock while it runs, so
// a slow device-management would otherwise stall every tenant's dispatch. What a timeout costs is
// an absent impact stamp, which the adapter reads as unknown and fails closed on.
const impactTimeout = 2 * time.Second

// resolveImpacts fills the impact cache for one tenant batch BEFORE any command in it is claimed.
//
// 🔴 BEFORE THE CLAIMS, AND BATCHED, FOR THE SAME REASON. Resolving at publish time was one
// synchronous query per command, with the claim already taken and the sweep lock held: a tenant
// rebooting ten thousand Sparkplug nodes was ten thousand serial round trips, and one hung
// device-management stalled dispatch for every tenant behind it. Here a batch costs one call per
// distinct command key over the Sparkplug devices it would dispatch, under one short deadline,
// and a miss is just a missing header.
//
// Only commands the gate will DISPATCH are asked about (not those it holds or fails), only
// Sparkplug devices, and only keys the cache does not already hold.
func (cproc *CommandDeliveryProcessor) resolveImpacts(ctx context.Context, batch tenantBatch,
	states map[string]presence.State) {
	if !cproc.SparkplugCommands || cproc.CommandImpacts == nil {
		return
	}
	devicesByKey := map[string][]string{}
	var keys []string
	seen := map[impactKey]bool{}
	for _, cmd := range batch.commands {
		state := states[cmd.DeviceToken]
		if presence.Decide(state, true) != presence.Dispatch ||
			routingOf(state.Known, state.Source).transport != transport.Sparkplug {
			continue
		}
		k := impactKey{tenant: cmd.TenantId, device: cmd.DeviceToken, command: cmd.Name}
		if seen[k] {
			continue
		}
		seen[k] = true
		if _, hit := cproc.impacts.get(k); hit {
			continue
		}
		if _, ok := devicesByKey[cmd.Name]; !ok {
			keys = append(keys, cmd.Name)
		}
		devicesByKey[cmd.Name] = append(devicesByKey[cmd.Name], cmd.DeviceToken)
	}
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cproc.impactDeadline())
	defer cancel()
	for _, name := range keys {
		found, err := cproc.CommandImpacts.ImpactsFor(ctx, devicesByKey[name], name)
		if err != nil {
			log.Warn().Err(err).Str("tenant", batch.tenant).Str("command", name).
				Int("devices", len(devicesByKey[name])).
				Msg("Could not decide command impacts; these commands are dispatched without an impact stamp, which the transport adapter reads as unknown and fails closed on.")
			continue
		}
		for device, impact := range found {
			cproc.impacts.put(impactKey{tenant: batch.tenant, device: device, command: name}, impact)
		}
	}
}

func (cproc *CommandDeliveryProcessor) impactDeadline() time.Duration {
	if cproc.impactTimeout > 0 {
		return cproc.impactTimeout
	}
	return impactTimeout
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
//   - Dc-Command-Impact is stamped only for a Sparkplug device, from the cache resolveImpacts
//     filled for this batch. It makes NO network call: a miss (the owner could not decide, timed
//     out, or is not wired) leaves the header OFF and is counted, and the adapter reads absent as
//     unknown and refuses the dangerous class, so a failure here can only make a command refused,
//     never actuated.
//
// It returns nil when there is nothing to stamp, so the message keeps a nil Headers map.
func (cproc *CommandDeliveryProcessor) dispatchHeaders(cmd *model.Command, route routing) map[string]string {
	if !cproc.SparkplugCommands || route.transport == "" {
		return nil
	}
	headers := map[string]string{cmddownlink.HeaderTransport: string(route.transport)}
	if route.transport == transport.Sparkplug {
		key := impactKey{tenant: cmd.TenantId, device: cmd.DeviceToken, command: cmd.Name}
		if impact, ok := cproc.impacts.get(key); ok {
			headers[cmddownlink.HeaderCommandImpact] = impact
		} else {
			incr(cproc.ImpactLookupFailures, 1)
		}
		// The command's own expiry, so the adapter can look at it again after any wait of its own
		// before the wire. It is already on the row and costs no lookup; absent means the command
		// does not expire.
		if cmd.ExpiresAt.Valid {
			headers[cmddownlink.HeaderCommandExpires] = cmd.ExpiresAt.Time.UTC().Format(time.RFC3339)
		}
	}
	return headers
}
