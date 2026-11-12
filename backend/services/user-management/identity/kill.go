// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/devicechain-io/dc-microservice/actiongate"
)

// KillPublisher notifies area fences after the policy kill has committed.
type KillPublisher interface {
	PublishKill(context.Context, actiongate.KillEvent) error
}

// SetKillPublisher installs the publisher during startup. Nil skips notification.
func (m *Manager) SetKillPublisher(p KillPublisher) { m.killPublisher = p }

// KillNotificationError means the policy kill committed but notification failed.
// Repeating the action retries notification even when the policy is unchanged.
type KillNotificationError struct{ Err error }

func (e *KillNotificationError) Error() string {
	return "the kill is in force but the area fences could not be notified; the operator must repeat the action"
}
func (e *KillNotificationError) Unwrap() error { return e.Err }

func (m *Manager) publishKill(ctx context.Context, tenant string, generation int, cause actiongate.KillCause) error {
	if m.killPublisher == nil {
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return &KillNotificationError{Err: err}
	}
	e := actiongate.KillEvent{EventID: hex.EncodeToString(id[:]), Tenant: tenant, Cause: cause, ActuationGeneration: generation, At: m.clock()}
	if err := m.killPublisher.PublishKill(ctx, e); err != nil {
		return &KillNotificationError{Err: err}
	}
	return nil
}
