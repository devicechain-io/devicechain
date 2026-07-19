// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// kvStreamPrefix is what JetStream prepends to a bucket name to form the stream
// that backs it. nats.go keeps its own copy of this unexported, and the KeyValue
// handle does not expose the underlying stream, so reconciling a bucket's ceiling
// means naming that stream ourselves.
const kvStreamPrefix = "KV_"

// kvKey encodes an arbitrary caller key into the NATS KV key charset. Caller
// keys (e.g. "tenant|token" cache keys or a lock name) are not guaranteed to
// fall within the restricted KV key charset, so they are base64url-encoded,
// which yields only [-_A-Za-z0-9] — all valid KV key characters. Shared by both
// the Cache and the DistributedLock built on this KV layer.
func kvKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// KeyValueStore returns a JetStream KV bucket, creating it with the given
// per-entry TTL, the manager's configured replica count, and the byte ceiling its
// declared tier calls for (kv.All, resolved via NatsConfiguration.KvMaxBytesFor)
// if it does not yet exist. It backs the server-side refresh-token store
// (ADR-008), the OAuth authorization-code store (ADR-047), the distributed lock,
// and every device-management cache.
//
// logical is the bucket's entry in the kv inventory — the unprefixed name that
// selects the ceiling. bucket is the concrete, instance-scoped name. They differ
// for caches and locks, which are prefixed per instance and functional area so
// two instances sharing a broker do not collide, and coincide for the buckets
// whose names are already global.
//
// A bucket is a JetStream stream, so its ceiling is reserved UP FRONT exactly
// like a message stream's and belongs in the same disk budget (ADR-023). Leaving
// it unbounded does not save that disk so much as make it unaccountable: the
// budget could only leave headroom and hope, which is what
// NatsConfiguration.KvReservation now replaces with arithmetic.
//
// Note the failure mode a ceiling buys here is better than the one message
// streams get. nats.go creates every KV bucket with DiscardNew, so a full bucket
// REFUSES the write rather than evicting an older entry — nothing already stored
// is lost. Whether the refusal is survivable is a property of the call site, and
// that is exactly what the tier in kv.All records.
func (nmgr *NatsManager) KeyValueStore(logical, bucket string, ttl time.Duration) (nats.KeyValue, error) {
	if nmgr.js == nil {
		return nil, errors.New("messaging: JetStream context not initialized")
	}
	maxBytes := nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.KvMaxBytesFor(logical)
	if maxBytes <= 0 {
		// Fail closed rather than create an unlimited bucket: 0 means UNLIMITED to
		// JetStream, so a misconfiguration that reached here would silently undo the
		// ceiling for this bucket (ADR-023 never-unlimited). ApplyDefaults already
		// coerces non-positive values, so reaching this is a bug rather than an
		// operator error — which is why it is an error and not a coercion.
		return nil, fmt.Errorf("messaging: non-positive ceiling %d for KV bucket %q", maxBytes, bucket)
	}
	if existing, err := nmgr.js.KeyValue(bucket); err == nil {
		nmgr.reconcileKvBucket(bucket, maxBytes)
		return existing, nil
	} else if !errors.Is(err, nats.ErrBucketNotFound) {
		return nil, err
	}
	return nmgr.js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:   bucket,
		TTL:      ttl,
		MaxBytes: maxBytes,
		Replicas: nmgr.streamReplicas(),
	})
}

// reconcileKvBucket applies the ceiling to a bucket that already exists.
//
// This is the whole reason KeyValueStore does not simply pass MaxBytes to
// CreateKeyValue and stop. The create path runs only when the bucket is absent,
// so on any cluster that has run before, a newly introduced ceiling would reach
// the create call on a FRESH install and nothing else — every existing install
// would keep its unbounded bucket and look perfectly healthy while being exactly
// as exposed as before. That is not hypothetical: the same shape shipped here
// once already, when ensureStream reconciled a stream's bounds but never its
// subjects, so fresh clusters got the new subject and existing ones silently did
// not.
//
// Failure is logged, not returned. An unbounded bucket is the status quo this
// change improves on, so a transient JetStream error during startup must not be
// worse than never having tried — failing service startup over it would turn a
// disk-safety improvement into an availability regression. The next restart
// retries. The warning is the signal that the budget is running ahead of reality.
func (nmgr *NatsManager) reconcileKvBucket(bucket string, maxBytes int64) {
	stream := kvStreamPrefix + bucket
	info, err := nmgr.js.StreamInfo(stream)
	if err != nil {
		log.Warn().Err(err).Str("bucket", bucket).
			Msg("Could not read KV bucket config to apply its disk ceiling; it stays as-is until the next startup")
		return
	}
	if info.Config.MaxBytes == maxBytes {
		return
	}
	cfg := info.Config
	previous := cfg.MaxBytes
	cfg.MaxBytes = maxBytes
	if _, err := nmgr.js.UpdateStream(&cfg); err != nil {
		log.Warn().Err(err).Str("bucket", bucket).Int64("maxBytes", maxBytes).Int64("previous", previous).
			Msg("Could not apply the disk ceiling to an existing KV bucket; it stays as-is until the next startup")
		return
	}
	log.Info().Str("bucket", bucket).Int64("maxBytes", maxBytes).Int64("previous", previous).
		Msg("Bounded KV bucket so it cannot consume the JetStream disk budget's headroom")
}
