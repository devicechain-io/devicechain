// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/kv"
	nats "github.com/nats-io/nats.go"
)

// CredentialAttemptStore returns the instance's credential-attempt bucket — the shared
// backoff state credential.Checker keeps so every replica of a service sees the same
// count of failed sign-ins — creating it if it does not exist.
//
// The bucket's name and TTL are decided here and nowhere else, so the replication
// check (ReplicationExpectation, via CredentialAttemptsBucketName) and the runtime
// cannot disagree about which bucket this is. The TTL is credential.AttemptTTL, which
// the checker's own policy validation keeps at least twice every kind's delay cap.
func (nmgr *NatsManager) CredentialAttemptStore() (nats.KeyValue, error) {
	return nmgr.KeyValueStore(kv.BucketCredentialAttempts,
		CredentialAttemptsBucketName(nmgr.Microservice.InstanceId), credential.AttemptTTL)
}
