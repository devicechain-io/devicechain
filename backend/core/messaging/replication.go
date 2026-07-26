// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

// LeaseBucketName is the concrete KV bucket holding an instance's partition
// ownership leases (ADR-070).
//
// Exported because the ADR-020 A0 replication check has to name this bucket
// explicitly rather than sweep for it, and a checker that builds the name itself
// is a checker that keeps looking for a bucket the runtime stopped using. One
// spelling, two callers.
func LeaseBucketName(instanceId string) string {
	return sanitizeName(fmt.Sprintf("%s_%s", instanceId, leaseBucket))
}

// LockBucketName is the concrete KV bucket holding an instance's distributed
// locks (ADR-007).
func LockBucketName(instanceId string) string {
	return sanitizeName(fmt.Sprintf("%s_%s", instanceId, lockBucket))
}

// CacheBucketName is the concrete KV bucket for one functional area's cache. The
// area is part of the name so two areas caching the same logical thing do not
// share a bucket, and the instance is part of it so two instances on one broker
// do not either.
func CacheBucketName(instanceId, functionalArea, name string) string {
	return sanitizeName(fmt.Sprintf("%s_%s_%s", instanceId, functionalArea, name))
}

// KvStreamName is the JetStream stream that backs a KV bucket. JetStream owns
// this mapping and nats.go keeps its copy of the prefix unexported, so the one
// place it is spelled here is kvStreamPrefix.
func KvStreamName(bucket string) string { return kvStreamPrefix + bucket }

// mqttStreamNames are the streams nats-server creates for its own MQTT gateway.
//
// All four are created eagerly when the FIRST MQTT client connects to an account
// (server/mqtt.go setup), at the replica factor mqttDetermineReplicas resolved at
// that moment — a decision the broker makes once and never revisits, and the one
// replication factor on the instance that this platform does not set directly.
// That is why they are named here rather than swept for: an instance whose device
// sessions are single-replica loses every live MQTT session with one node, no
// matter how well replicated everything else is.
var mqttStreamNames = []string{
	"$MQTT_sess",   // per-account client sessions
	"$MQTT_msgs",   // inflight QoS 1 messages
	"$MQTT_qos2in", // QoS 2 messages received and not yet PUBREL-ed
	"$MQTT_out",    // outbound QoS 2 PUBREL
}

// ReplicationExpectation states, in concrete broker names, what an instance's
// JetStream objects must look like at the given replica factor (ADR-020 A0).
//
// This is the ONLY place instance ids become object names for the checker, which
// is the point: the check is meant to catch an instance whose broker does not
// match its configuration, and it can only do that if it is looking at the same
// objects the runtime created. A checker with its own naming rules fails a
// different way — it reports everything missing, or worse, it finds nothing to
// check and says so in the same green sentence it uses for success.
//
// replicas is the DECLARED factor, not an observation: 1 for a single-node
// instance, 3 for the shipped HA topology. Passing 1 is meaningful and expected —
// it is what proves the suite passes a correct non-HA instance instead of
// failing everything indiscriminately, which is the other half of the negative
// control.
func ReplicationExpectation(instanceId string, replicas int) replication.Expectation {
	exp := replication.Expectation{
		Replicas:    replicas,
		MqttStreams: append([]string(nil), mqttStreamNames...),
		LeaseBucket: KvStreamName(LeaseBucketName(instanceId)),
		RequirePods: replicas,
		Prefixes: []string{
			sanitizeName(instanceId) + "_",
			KvStreamName(sanitizeName(instanceId) + "_"),
			// The two identity buckets are instance-GLOBAL rather than instance-scoped
			// (user-management passes the same name as both logical and concrete), so
			// they do not carry the instance prefix and would otherwise fall outside
			// every sweep. Covering them by their own prefix is what keeps the refresh
			// token store — a full one fails every login — inside the check.
			KvStreamName("dc_"),
			// Sweep every MQTT stream, not only the four named above. nats-server owns
			// that set; a stream it adds in a future release would otherwise be
			// collected and judged by nothing.
			replication.MqttStreamPrefix,
		},
	}
	for _, suffix := range streams.Suffixes() {
		exp.Streams = append(exp.Streams, StreamName(instanceId, suffix))
	}
	exp.StateBuckets = []string{
		KvStreamName(LockBucketName(instanceId)),
		// The refresh-token store is created unconditionally by user-management, which
		// every instance runs. The OAuth authorization-code store is NOT required: it
		// exists only when OAuth is enabled (ADR-047), so it is left to the KV_dc_
		// prefix sweep, which checks it when it is there and does not report it
		// missing when it is not.
		KvStreamName(kv.BucketRefreshTokens),
	}
	for _, b := range kv.All {
		if b.Tier != kv.Cache {
			continue
		}
		// Matched by suffix, not by name: a cache bucket's concrete name carries the
		// functional area that owns it, so the full set depends on which areas are
		// deployed while the trailing logical name does not.
		exp.CacheBucketSuffixes = append(exp.CacheBucketSuffixes, "_"+b.Name)
	}
	return exp
}
