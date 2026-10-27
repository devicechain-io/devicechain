// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"slices"

	"github.com/devicechain-io/dc-microservice/kv"
)

// This file holds the few names a broker-side grant needs that this package alone knows how
// to spell. natsauth.AreaPermissions builds each area's allow-lists from them and from the
// stream and bucket catalogs, so a subject is written ONCE, by the code that publishes or
// subscribes to it, and the grant follows.

// InboxPrefix is the reply-inbox prefix an area's connections use in place of nats.go's
// "_INBOX": "_INBOX_<area>". Every request reply, pull fetch, KV read and push delivery on
// the connection arrives on "<prefix>.<id>", so the area's login may subscribe to
// "<prefix>.>" and to nothing broader. Under the shared "_INBOX" every login that could
// subscribe to it could read, and answer, every other login's replies.
//
// 🔴 A CONNECTION THAT DID NOT USE IT WOULD BE REFUSED ITS OWN REPLIES. Every connection an
// area opens to the platform account sets it (the manager's, the affinity leader's); a new
// one has to as well, and a raw nats.NewInbox() (which ignores it) is a defect: the reply
// goes to a subject nothing the area holds is subscribed to.
func InboxPrefix(area string) string { return "_INBOX_" + sanitizeName(area) }

// CacheEvictSubjects is the wildcard over every cache-eviction subject one area broadcasts
// on and listens to (CacheEvictSubject with any cache name).
func CacheEvictSubjects(instanceId, functionalArea string) string {
	return controlSubjectRoot + instanceId + cacheEvictSubjectSegment + sanitizeName(functionalArea) + ".>"
}

// KvBucketNames are the concrete buckets, in the shared platform account, that a declared
// bucket is when area opens it. A bucket the catalog places in the identity account has none
// here: the account is another one, entered by another login.
//
// The mapping is the one the creating code uses: a cache bucket is per instance AND per area
// (CacheBucketName), and every other bucket is per instance (the lock, lease and
// credential-attempt names). A bucket that maps to nothing is a defect in this switch, and
// TestEveryDeclaredBucketHasAConcreteName keeps it from being silent.
func KvBucketNames(instanceId string, b kv.Bucket, area string) []string {
	if b.Account != kv.App || !slices.Contains(b.Admins(), area) {
		return nil
	}
	switch b.Name {
	case kv.BucketLocks:
		return []string{LockBucketName(instanceId)}
	case kv.BucketLeases:
		return []string{LeaseBucketName(instanceId)}
	case kv.BucketCredentialAttempts:
		return []string{CredentialAttemptsBucketName(instanceId)}
	case kv.BucketDeviceCredentialAttempts:
		return []string{DeviceCredentialAttemptsBucketName(instanceId)}
	case kv.BucketSparkplugCommandsAwaiting:
		return []string{SparkplugCommandsAwaitingBucketName(instanceId)}
	}
	if b.Tier == kv.Cache {
		return []string{CacheBucketName(instanceId, area, b.Name)}
	}
	return nil
}

// MqttStoreNames are the JetStream streams nats-server's MQTT gateway keeps, which the
// event-sources area bounds and (for the message store) administers. Exported for the grant.
func MqttStoreNames() []string { return slices.Clone(mqttStreamNames) }

// MqttFailClosedConsumerNames are the durables the fail-closed loop keeps on the message store.
func MqttFailClosedConsumerNames(instanceId string) []string {
	var out []string
	for _, d := range MqttFailClosedDurables(instanceId) {
		out = append(out, d.Name)
	}
	return out
}
