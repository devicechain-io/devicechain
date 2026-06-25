// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// lockBucket is the JetStream KV bucket holding lock keys for an instance.
	lockBucket = "dc_locks"
	// lockRetries bounds how many times an acquire is retried before giving up,
	// and lockBackoff is the wait between attempts. Together they reproduce the
	// former redislock LimitRetry(LinearBackoff(5s), 5) behavior (ADR-007).
	lockRetries = 5
	lockBackoff = 5 * time.Second
)

// DistributedLock provides a mutex across all replicas of a microservice,
// backed by NATS JetStream KV optimistic concurrency (ADR-007 replaces the
// Redis redislock). Acquisition is a KV Create, which fails when the key
// already exists; the bucket's TTL auto-expires a lock whose holder crashed
// without releasing it, so a dead holder cannot wedge the lock forever.
type DistributedLock struct {
	kv  nats.KeyValue
	ttl time.Duration
}

// NewDistributedLock returns a DistributedLock over the instance lock bucket,
// creating it with the given TTL if needed. The TTL bounds how long a lock
// survives without being released (a crashed holder), so it must comfortably
// exceed the guarded critical section.
func (nmgr *NatsManager) NewDistributedLock(ttl time.Duration) (*DistributedLock, error) {
	bucket := sanitizeName(fmt.Sprintf("%s_%s", nmgr.Microservice.InstanceId, lockBucket))
	kv, err := nmgr.KeyValueStore(bucket, ttl)
	if err != nil {
		return nil, err
	}
	return &DistributedLock{kv: kv, ttl: ttl}, nil
}

// WithLock acquires the named lock, runs logic while holding it, and releases
// it afterward. It retries acquisition with a bounded backoff while the lock is
// held by another replica; if it cannot be obtained within the retry budget the
// error is returned and logic never runs (fail-closed). The lock is released
// even if logic returns an error.
func (l *DistributedLock) WithLock(ctx context.Context, name string, logic func(ctx context.Context) error) error {
	key := cacheKey(name)
	holder := uuid.NewString()

	log.Info().Str("lock", name).Msg("Acquiring distributed lock...")
	acquired := false
	for attempt := 0; attempt <= lockRetries; attempt++ {
		if _, err := l.kv.Create(key, []byte(holder)); err == nil {
			acquired = true
			break
		} else if !errors.Is(err, nats.ErrKeyExists) {
			return err
		}
		if attempt == lockRetries {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockBackoff):
		}
	}
	if !acquired {
		return fmt.Errorf("messaging: could not obtain distributed lock %q within %d attempts", name, lockRetries+1)
	}

	defer func() {
		// Release only our own hold: if the TTL already expired our entry and
		// another replica took the lock, deleting would drop theirs. Purge our
		// key only when it still carries our holder id.
		if entry, err := l.kv.Get(key); err == nil && string(entry.Value()) == holder {
			if derr := l.kv.Delete(key); derr != nil {
				log.Error().Err(derr).Str("lock", name).Msg("Error releasing distributed lock.")
			}
		}
	}()

	log.Info().Str("lock", name).Msg("Lock obtained. Running guarded logic.")
	return logic(ctx)
}
