// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/base64"
	"errors"
	"time"

	nats "github.com/nats-io/nats.go"
)

// kvKey encodes an arbitrary caller key into the NATS KV key charset. Caller
// keys (e.g. "tenant|token" cache keys or a lock name) are not guaranteed to
// fall within the restricted KV key charset, so they are base64url-encoded,
// which yields only [-_A-Za-z0-9] — all valid KV key characters. Shared by both
// the Cache and the DistributedLock built on this KV layer.
func kvKey(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// KeyValueStore returns a JetStream KeyValue bucket, creating it with the given
// per-entry TTL and the manager's configured replica count if it does not yet
// exist. It backs the server-side refresh-token store (ADR-008): each refresh
// token's jti is a key, so a token can be validated on /refresh and revoked by
// deleting the key. The TTL bounds how long an unused refresh token survives.
func (nmgr *NatsManager) KeyValueStore(bucket string, ttl time.Duration) (nats.KeyValue, error) {
	if nmgr.js == nil {
		return nil, errors.New("messaging: JetStream context not initialized")
	}
	if kv, err := nmgr.js.KeyValue(bucket); err == nil {
		return kv, nil
	} else if !errors.Is(err, nats.ErrBucketNotFound) {
		return nil, err
	}
	return nmgr.js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:   bucket,
		TTL:      ttl,
		Replicas: nmgr.streamReplicas(),
	})
}
