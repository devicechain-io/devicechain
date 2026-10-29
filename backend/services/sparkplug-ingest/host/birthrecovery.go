// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/svcclient"
)

// A Sparkplug node is registered the first time a birth (or a sample) of it resolves, with the
// device type the source names. When that cannot be done (the type does not exist yet, or
// device-management is down for longer than the in-handler retry budget) the message is dropped,
// and a clean-session Host gets no broker redelivery: a node that is already born will not announce
// itself again until it rebirths on its own, which a healthy node may never do.
//
// 🔴 THE RECOVERY IS TO ASK, NOT TO KEEP. The Host remembers which nodes it failed to register,
// and asks each of them to rebirth (an NCMD "Node Control/Rebirth", the same command the session
// machine and the liveness probe use) on a growing, capped interval until the messages that
// failed resolve. The births that answer are ingested like any other and register the node and its
// devices; nothing is replayed from memory, so there is no second copy of a birth that could
// register a node twice or outlive the broker's own record of it.
//
// It is safe across replicas because only the lease holder runs a Client at all (one owner per
// source), a new owner starts with an empty set and finds its nodes the way the reconcile probe
// does, and registration is idempotent: the device token is derived from the external id, so a
// create that races another resolves to the winner.
//
// 🔴 AN NCMD BYPASSES THE COMMAND RATE LIMITER, SO THIS HAS TO LIMIT ITSELF. It tracks WHICH ids
// failed (a sibling device's success clears nothing), remembers a node's backoff for a while after
// it is cleared (so a flapping node does not restart the cycle), asks only for the two causes a
// rebirth can cure (the device type is missing, the registration path is unavailable), drops to a
// long interval after a few requests, and asks at most a handful of nodes per tick.
//
// It is loud: the first failure for a node is logged at error with the cause, every request at
// warn, and the set is exported (birth_recovery_pending, birth_recovery_rebirths_total) so a
// source whose device type never appears reads as a standing, non-zero gauge.
const (
	// recoveryInitialDelay is how long after the first failed registration the node is first
	// asked to rebirth: long enough for the births already in flight to finish failing, short
	// enough that an operator who provisions the type right after connecting sees the node appear.
	recoveryInitialDelay = 10 * time.Second
	// recoveryMaxDelay caps the growth of the interval between requests to one node.
	recoveryMaxDelay = time.Minute
	// recoveryCeilingAfter is how many requests a node gets at the growing interval before it
	// drops to recoveryCeilingDelay: a failure that outlasts five requests is not a race with a
	// provisioning step, and it must not go on being asked once a minute for ever.
	recoveryCeilingAfter = 5
	recoveryCeilingDelay = time.Hour
	// recoveryTickEvery is how often the set is checked for nodes that are due, and
	// recoveryPerTick how many of them one check asks: the rest stay due for the next tick, so a
	// fleet that fails together is asked over a few ticks and not in one burst.
	recoveryTickEvery = 5 * time.Second
	recoveryPerTick   = 5
	// recoveryRecentTTL is how long the backoff of a node that was cleared is remembered. A
	// node cleared and failing again a moment later resumes where it was instead of starting the
	// cycle over.
	recoveryRecentTTL = 10 * time.Minute
	// maxUnregisteredNodes bounds the set, and maxFailingIDsPerNode the ids tracked under one
	// node. Past either, the id is refused tracking (logged once per quiet window).
	maxUnregisteredNodes = 1024
	maxFailingIDsPerNode = 256
	// recoveryLogQuiet is the window in which a repeated error for one node is logged once.
	recoveryLogQuiet = 10 * time.Minute
)

// unregisteredNode is one node with devices (or itself) the Host failed to register.
type unregisteredNode struct {
	firstAt time.Time
	nextAsk time.Time
	lastAsk time.Time
	delay   time.Duration
	asks    int
	// failing is the external ids whose registration failed. The node is cleared only when
	// THESE resolve: a sibling device's success says nothing about them.
	failing map[string]struct{}
}

// recentBackoff is the backoff of a node cleared a moment ago.
type recentBackoff struct {
	delay   time.Duration
	asks    int
	lastAsk time.Time
	expires time.Time
}

// birthRecovery is the set of nodes awaiting a rebirth, guarded by its own lock.
type birthRecovery struct {
	mu     sync.Mutex
	nodes  map[nodeKey]*unregisteredNode
	recent map[nodeKey]recentBackoff
	quiet  map[string]time.Time
}

// nodeKeyOf returns the node an external id ("group/node" or "group/node/device") belongs to.
// Sparkplug identifiers cannot contain "/", so the split is unambiguous.
func nodeKeyOf(externalId string) (nodeKey, bool) {
	parts := strings.Split(externalId, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nodeKey{}, false
	}
	return nodeKey{group: parts[0], node: parts[1]}, true
}

// recoverableCause says whether asking the node to rebirth can help with this failure, and names
// the failure for the log. Only two causes are: the device type does not exist yet (NOT_FOUND from
// device-management, cured by creating it) and the registration path being unavailable (a
// transport failure, a timeout, a 5xx), which passes. Anything device-management ANSWERED with a
// different refusal is a decision a second birth will meet again, so it is logged and not retried.
func recoverableCause(err error) (ok bool, what string) {
	var ge *svcclient.GraphQLError
	if errors.As(err, &ge) {
		for _, c := range ge.Codes {
			if c == "NOT_FOUND" {
				return true, "device type not found"
			}
		}
		return false, "refused by device-management (" + strings.Join(ge.Codes, ",") + ")"
	}
	return true, "registration unavailable"
}

// logOnce reports whether a log line keyed by key may be written now, and records that it was.
// The caller holds recovery.mu.
func (c *Client) logOnce(key string) bool {
	now := c.now()
	if until, ok := c.recovery.quiet[key]; ok && now.Before(until) {
		return false
	}
	if c.recovery.quiet == nil {
		c.recovery.quiet = map[string]time.Time{}
	}
	if len(c.recovery.quiet) > 4*maxUnregisteredNodes {
		for k, until := range c.recovery.quiet {
			if !now.Before(until) {
				delete(c.recovery.quiet, k)
			}
		}
	}
	c.recovery.quiet[key] = now.Add(recoveryLogQuiet)
	return true
}

// noteUnregistered records that these ids could not be resolved, so their nodes will be asked to
// rebirth. cause is the ingest error.
func (c *Client) noteUnregistered(externalIds []string, cause error) {
	now := c.now()
	recoverable, what := recoverableCause(cause)
	for _, id := range externalIds {
		key, ok := nodeKeyOf(id)
		if !ok {
			continue
		}
		c.recovery.mu.Lock()
		if !recoverable {
			first := c.logOnce("permanent|" + key.group + "/" + key.node)
			c.recovery.mu.Unlock()
			if first {
				log.Error().Err(cause).Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
					Str("cause", what).Msg("A Sparkplug node could not be registered, and device-management refused it for a reason " +
					"a rebirth will not change; the node is not being asked to rebirth. Fix the source or the device type and the node's next birth registers it.")
			}
			continue
		}
		if c.recovery.nodes == nil {
			c.recovery.nodes = map[nodeKey]*unregisteredNode{}
		}
		n, tracked := c.recovery.nodes[key]
		if !tracked {
			if len(c.recovery.nodes) >= maxUnregisteredNodes {
				first := c.logOnce("cap|" + key.group + "/" + key.node)
				c.recovery.mu.Unlock()
				if first {
					log.Error().Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
						Msg("Too many Sparkplug nodes are awaiting registration; this node will not be asked to rebirth until it births on its own.")
				}
				continue
			}
			n = &unregisteredNode{firstAt: now, delay: c.recoveryInitial, nextAsk: now.Add(c.recoveryInitial), failing: map[string]struct{}{}}
			// Resume the backoff of a node cleared a moment ago.
			if r, ok := c.recovery.recent[key]; ok && now.Before(r.expires) {
				n.delay, n.asks, n.lastAsk = r.delay, r.asks, r.lastAsk
				if due := r.lastAsk.Add(r.delay); due.After(n.nextAsk) {
					n.nextAsk = due
				}
			}
			delete(c.recovery.recent, key)
			c.recovery.nodes[key] = n
		}
		if len(n.failing) < maxFailingIDsPerNode {
			n.failing[id] = struct{}{}
		}
		c.recovery.mu.Unlock()
		if !tracked {
			if c.metrics.BirthRecoveryPending != nil {
				c.metrics.BirthRecoveryPending.Inc()
			}
			log.Error().Err(cause).Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
				Str("cause", what).Str("deviceType", c.policy.DeviceTypeToken).
				Msg("A Sparkplug node's birth could not be registered and was dropped; the node stays unknown until it is registered. " +
					"Check that the source's device type exists. The node will be asked to rebirth until it registers.")
		}
	}
}

// clearUnregistered removes each id from its node's failing set, and ends the recovery of a node
// whose set is now empty. An id that was not failing changes nothing: a sibling device resolving
// is not evidence that the failed ones did.
func (c *Client) clearUnregistered(externalIds []string) {
	c.recovery.mu.Lock()
	if len(c.recovery.nodes) == 0 {
		c.recovery.mu.Unlock()
		return
	}
	now := c.now()
	var cleared []nodeKey
	for _, id := range externalIds {
		key, ok := nodeKeyOf(id)
		if !ok {
			continue
		}
		n, tracked := c.recovery.nodes[key]
		if !tracked {
			continue
		}
		delete(n.failing, id)
		if len(n.failing) > 0 {
			continue
		}
		delete(c.recovery.nodes, key)
		if c.recovery.recent == nil {
			c.recovery.recent = map[nodeKey]recentBackoff{}
		}
		if len(c.recovery.recent) < maxUnregisteredNodes {
			c.recovery.recent[key] = recentBackoff{delay: n.delay, asks: n.asks, lastAsk: n.lastAsk, expires: now.Add(recoveryRecentTTL)}
		}
		cleared = append(cleared, key)
	}
	c.recovery.mu.Unlock()
	for _, key := range cleared {
		if c.metrics.BirthRecoveryPending != nil {
			c.metrics.BirthRecoveryPending.Dec()
		}
		log.Info().Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
			Msg("A Sparkplug node that could not be registered has now been registered.")
	}
}

// resetRecovery empties the set at a term boundary, keeping the gauge true.
func (c *Client) resetRecovery() {
	c.recovery.mu.Lock()
	n := len(c.recovery.nodes)
	c.recovery.nodes, c.recovery.recent, c.recovery.quiet = nil, nil, nil
	c.recovery.mu.Unlock()
	if n > 0 && c.metrics.BirthRecoveryPending != nil {
		c.metrics.BirthRecoveryPending.Sub(float64(n))
	}
}

// recoveryWorker asks the nodes that are due to rebirth, until ctx ends.
func (c *Client) recoveryWorker(ctx context.Context) {
	defer c.wg.Done()
	for sleep(ctx, c.recoveryTick) {
		c.recoverOnce()
	}
}

// recoverOnce makes one pass over the set: a node that is no longer online is forgotten (its next
// birth is a fresh attempt), and up to recoveryPerTick of the due ones are asked to rebirth, the
// longest-waiting first, with each one's next request pushed out by a doubled interval up to the
// cap (and to the ceiling after recoveryCeilingAfter requests).
func (c *Client) recoverOnce() {
	now := c.now()
	type due struct {
		key        nodeKey
		asks       int
		unregistry time.Duration
		nextAsk    time.Time
	}
	var ready []due
	var gone []nodeKey
	c.recovery.mu.Lock()
	for key, r := range c.recovery.recent {
		if !now.Before(r.expires) {
			delete(c.recovery.recent, key)
		}
	}
	for key, st := range c.recovery.nodes {
		if !c.sessions.NodeOnline(key.group, key.node) {
			delete(c.recovery.nodes, key)
			gone = append(gone, key)
			continue
		}
		if now.Before(st.nextAsk) {
			continue
		}
		ready = append(ready, due{key: key, nextAsk: st.nextAsk})
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].nextAsk.Before(ready[j].nextAsk) })
	if len(ready) > recoveryPerTick {
		ready = ready[:recoveryPerTick]
	}
	for i := range ready {
		st := c.recovery.nodes[ready[i].key]
		st.asks++
		st.lastAsk = now
		st.delay *= 2
		if st.delay > c.recoveryMax {
			st.delay = c.recoveryMax
		}
		if st.asks >= recoveryCeilingAfter {
			st.delay = recoveryCeilingDelay
		}
		st.nextAsk = now.Add(st.delay)
		ready[i].asks = st.asks
		ready[i].unregistry = now.Sub(st.firstAt)
	}
	c.recovery.mu.Unlock()

	for _, key := range gone {
		if c.metrics.BirthRecoveryPending != nil {
			c.metrics.BirthRecoveryPending.Dec()
		}
		log.Warn().Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
			Msg("A Sparkplug node that could not be registered is no longer online; its next birth is a fresh attempt.")
	}
	for _, a := range ready {
		// Marked BEFORE the publish, as a probe-style rebirth: the answering NBIRTH carries the
		// same bdSeq, and without the mark it would be skipped as a QoS-1 redelivery and register
		// nothing; the probe style also re-asserts the node CONNECTED, which is the presence
		// transition the dropped first birth would have carried. A request that did not reach the
		// wire puts the mark back, so a later redelivery is not mistaken for the answer.
		undo := c.sessions.MarkRebirthPendingRestorable(a.key.group, a.key.node)
		sent := c.rebirthPub(a.key.group, a.key.node)
		if !sent {
			undo()
		} else if c.metrics.BirthRecoveryRebirths != nil {
			c.metrics.BirthRecoveryRebirths.Inc()
		}
		log.Warn().Str("tenant", c.tenant).Str("group", a.key.group).Str("node", a.key.node).
			Str("deviceType", c.policy.DeviceTypeToken).Int("request", a.asks).Dur("unregisteredFor", a.unregistry).Bool("sent", sent).
			Msg("A Sparkplug node is still not registered; asked it to rebirth.")
	}
}
