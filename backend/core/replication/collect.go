// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"fmt"
	"strings"

	nats "github.com/nats-io/nats.go"
)

// Collect reads the broker and returns what it actually sees.
//
// This is the impure half of the package, and it is deliberately thin: it
// translates broker state into the Snapshot types and judges nothing. Every
// decision about whether the state is acceptable lives in Verify, where it can be
// exercised in CI against synthetic snapshots — including the ones a real cluster
// will not produce on demand, like a peer that is present but not current.
//
// COLLECTION FAILURES MUST NOT LOOK LIKE A CLEAN BILL OF HEALTH. That is the one
// thing this function has to get right, and it is not automatic: nats.go's
// listing APIs return channels and drop their errors on the floor, so a broken
// connection, an account with no JetStream, or a permissions problem all yield an
// empty sequence — which is indistinguishable from a healthy broker hosting
// nothing, and would let Verify report "PASS, 0 objects examined". Three things
// stand against that:
//
//  1. AccountInfo is called FIRST and its error is returned. It is a real
//     request/reply, so a connection or permission problem surfaces here as an
//     error rather than as silence.
//  2. The number of streams listed is compared against the account's own count.
//     A truncated or aborted sweep is an error, not a smaller snapshot.
//  3. Verify treats an empty snapshot as a finding in its own right, so even if
//     both guards were removed the suite would not go green over nothing.
func Collect(js nats.JetStreamContext, exp Expectation) (Snapshot, error) {
	var snap Snapshot

	info, err := js.AccountInfo()
	if err != nil {
		return snap, fmt.Errorf("replication: reading JetStream account info: %w", err)
	}

	all := make([]*nats.StreamInfo, 0, info.Streams)
	for si := range js.StreamsInfo() {
		if si != nil {
			all = append(all, si)
		}
	}
	if len(all) != info.Streams {
		// Not a soft warning. A partial listing silently narrows every assertion
		// below it, and the object that went missing is exactly as likely to be the
		// under-replicated one as any other.
		return snap, fmt.Errorf("replication: the broker reports %d stream(s) in this "+
			"account but the listing returned %d — the sweep was truncated (a dropped "+
			"connection mid-page, or a stream created concurrently). Re-run; a partial "+
			"snapshot cannot be judged", info.Streams, len(all))
	}

	for _, si := range all {
		if !collectible(si.Config.Name, exp) {
			continue
		}
		snap.Objects = append(snap.Objects, objectFrom(si))
		for ci := range js.ConsumersInfo(si.Config.Name) {
			if ci == nil {
				continue
			}
			snap.Consumers = append(snap.Consumers, Consumer{
				Stream: si.Config.Name,
				Name:   ci.Name,
				Leader: leaderOf(ci.Cluster),
				Peers:  peersOf(ci.Cluster),
			})
		}
	}
	return snap, nil
}

// collectible reports whether an observed stream belongs to the instance under
// test. A broker may host more than one instance (ADR-048 makes the instance the
// isolation boundary, not the broker), and judging another instance's objects
// against this one's replica factor would report failures that are not this
// instance's to fix.
//
// The MQTT streams are the exception: they are the broker's own, shared by every
// instance on it, and their replica factor is a single global decision made by
// nats-server at first connect. They are in scope for whoever is asking.
func collectible(name string, exp Expectation) bool {
	if strings.HasPrefix(name, MqttStreamPrefix) {
		return true
	}
	if inScope(name, exp.Prefixes) {
		return true
	}
	for _, n := range exp.Streams {
		if name == n {
			return true
		}
	}
	for _, n := range exp.StateBuckets {
		if name == n {
			return true
		}
	}
	if name == exp.LeaseBucket && exp.LeaseBucket != "" {
		return true
	}
	for _, suffix := range exp.CacheBucketSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// MqttStreamPrefix is what nats-server names its own MQTT session and message
// streams. They are the A0 lever: their replica factor comes from
// mqttDetermineReplicas, which runs once at the first MQTT connect after a broker
// start and never revisits its answer.
//
// Exported so an expectation can put it in Prefixes and have EVERY MQTT stream
// swept, not only the four this platform knows to name. That distinction is not
// cosmetic: nats-server owns this set and has added to it across releases, and a
// stream it introduces would otherwise be collected and then judged by nothing —
// present in the snapshot, absent from every assertion. Naming the four we
// require and sweeping the rest covers both directions.
const MqttStreamPrefix = "$MQTT_"

func objectFrom(si *nats.StreamInfo) Object {
	return Object{
		Name:     si.Config.Name,
		Replicas: si.Config.Replicas,
		Leader:   leaderOf(si.Cluster),
		Peers:    peersOf(si.Cluster),
	}
}

// leaderOf tolerates a nil Cluster, which is what an unclustered broker reports.
// That is a legitimate R1 state, not an error, so it must not panic and must not
// be mistaken for a missing leader on a clustered broker — Verify only demands a
// leader above R1.
func leaderOf(c *nats.ClusterInfo) string {
	if c == nil {
		return ""
	}
	return c.Leader
}

func peersOf(c *nats.ClusterInfo) []Peer {
	if c == nil {
		return nil
	}
	out := make([]Peer, 0, len(c.Replicas))
	for _, p := range c.Replicas {
		if p == nil {
			continue
		}
		out = append(out, Peer{Name: p.Name, Current: p.Current, Offline: p.Offline})
	}
	return out
}
