// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// A Sparkplug node is registered the first time a birth (or a sample) of it resolves, with the
// device type the source names. When that cannot be done (the type does not exist yet, or
// device-management is down for longer than the in-handler retry budget) the message is dropped,
// and a clean-session Host gets no broker redelivery: a node that is already born will not announce
// itself again until it rebirths on its own, which a healthy node may never do.
//
// 🔴 THE RECOVERY IS TO ASK, NOT TO KEEP. The Host remembers which nodes it failed to register,
// and asks each of them to rebirth (an NCMD "Node Control/Rebirth", the same command the session
// machine and the liveness probe use) on a growing, capped interval until a message of the node
// resolves. The births that answer are ingested like any other and register the node and its
// devices; nothing is replayed from memory, so there is no second copy of a birth that could
// register a node twice or outlive the broker's own record of it.
//
// It is safe across replicas because only the lease holder runs a Client at all (one owner per
// source), a new owner starts with an empty set and finds its nodes the way the reconcile probe
// does, and registration is idempotent: the device token is derived from the external id, so a
// create that races another resolves to the winner.
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
	// recoveryTickEvery is how often the set is checked for nodes that are due.
	recoveryTickEvery = 5 * time.Second
	// maxUnregisteredNodes bounds the set. A source with more nodes than this failing at once is
	// refused tracking for the rest (logged), never grown without limit.
	maxUnregisteredNodes = 1024
)

// unregisteredNode is one node whose births the Host failed to register.
type unregisteredNode struct {
	firstAt time.Time
	nextAsk time.Time
	delay   time.Duration
	asks    int
}

// birthRecovery is the set of nodes awaiting a rebirth, guarded by its own lock.
type birthRecovery struct {
	mu    sync.Mutex
	nodes map[nodeKey]*unregisteredNode
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

// noteUnregistered records that a node's messages could not be resolved, so it will be asked to
// rebirth. cause is the ingest error.
func (c *Client) noteUnregistered(externalIds []string, cause error) {
	now := c.now()
	for _, id := range externalIds {
		key, ok := nodeKeyOf(id)
		if !ok {
			continue
		}
		c.recovery.mu.Lock()
		if c.recovery.nodes == nil {
			c.recovery.nodes = map[nodeKey]*unregisteredNode{}
		}
		if _, tracked := c.recovery.nodes[key]; tracked {
			c.recovery.mu.Unlock()
			continue
		}
		if len(c.recovery.nodes) >= maxUnregisteredNodes {
			c.recovery.mu.Unlock()
			log.Error().Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
				Msg("Too many Sparkplug nodes are awaiting registration; this node will not be asked to rebirth until it births on its own.")
			continue
		}
		c.recovery.nodes[key] = &unregisteredNode{firstAt: now, nextAsk: now.Add(c.recoveryInitial), delay: c.recoveryInitial}
		c.recovery.mu.Unlock()
		if c.metrics.BirthRecoveryPending != nil {
			c.metrics.BirthRecoveryPending.Inc()
		}
		log.Error().Err(cause).Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
			Str("deviceType", c.policy.DeviceTypeToken).Dur("firstRebirthIn", c.recoveryInitial).
			Msg("A Sparkplug node's birth could not be registered and was dropped; the node stays unknown until it is registered. " +
				"Check that the source's device type exists. The node will be asked to rebirth until it registers.")
	}
}

// clearUnregistered ends the recovery of every node whose messages have now resolved.
func (c *Client) clearUnregistered(externalIds []string) {
	c.recovery.mu.Lock()
	if len(c.recovery.nodes) == 0 {
		c.recovery.mu.Unlock()
		return
	}
	var cleared []nodeKey
	for _, id := range externalIds {
		key, ok := nodeKeyOf(id)
		if !ok {
			continue
		}
		if _, tracked := c.recovery.nodes[key]; tracked {
			delete(c.recovery.nodes, key)
			cleared = append(cleared, key)
		}
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
	c.recovery.nodes = nil
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
// birth is a fresh attempt), and one that is due is asked to rebirth, with the next request pushed
// out by a doubled interval up to the cap.
func (c *Client) recoverOnce() {
	now := c.now()
	type due struct {
		key        nodeKey
		asks       int
		unregistry time.Duration
	}
	var asks []due
	var gone []nodeKey
	c.recovery.mu.Lock()
	for key, st := range c.recovery.nodes {
		if !c.sessions.NodeOnline(key.group, key.node) {
			delete(c.recovery.nodes, key)
			gone = append(gone, key)
			continue
		}
		if now.Before(st.nextAsk) {
			continue
		}
		st.asks++
		st.delay *= 2
		if st.delay > c.recoveryMax {
			st.delay = c.recoveryMax
		}
		st.nextAsk = now.Add(st.delay)
		asks = append(asks, due{key: key, asks: st.asks, unregistry: now.Sub(st.firstAt)})
	}
	c.recovery.mu.Unlock()

	for _, key := range gone {
		if c.metrics.BirthRecoveryPending != nil {
			c.metrics.BirthRecoveryPending.Dec()
		}
		log.Warn().Str("tenant", c.tenant).Str("group", key.group).Str("node", key.node).
			Msg("A Sparkplug node that could not be registered is no longer online; its next birth is a fresh attempt.")
	}
	for _, a := range asks {
		// Marked BEFORE the publish, as a commanded rebirth: the answering NBIRTH carries the
		// same bdSeq, and without the mark it would be skipped as a QoS-1 redelivery and register
		// nothing. The probe-style mark also re-asserts the node CONNECTED, which is the presence
		// transition the dropped first birth would have carried.
		c.sessions.MarkRebirthPending(a.key.group, a.key.node)
		sent := c.rebirthPub(a.key.group, a.key.node)
		if sent && c.metrics.BirthRecoveryRebirths != nil {
			c.metrics.BirthRecoveryRebirths.Inc()
		}
		log.Warn().Str("tenant", c.tenant).Str("group", a.key.group).Str("node", a.key.node).
			Str("deviceType", c.policy.DeviceTypeToken).Int("request", a.asks).Dur("unregisteredFor", a.unregistry).Bool("sent", sent).
			Msg("A Sparkplug node is still not registered; asked it to rebirth.")
	}
}
