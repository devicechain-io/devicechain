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

// TenantDisabled publishes the generation captured by the transaction that disabled
// the tenant and bumped its head. A later re-enable bumps to a strictly greater
// generation, reopening the fence regardless of the order in which publishes land.
// During a rolling upgrade, an old pod re-enabling after a new pod disabled does not
// bump, so the fence stays closed (fail closed) until re-enabled again through a new
// pod. Every new-pod call bumps even an unchanged value, making that retry the remedy.
func (m *Manager) TenantDisabled(ctx context.Context, tenant string, generation int) error {
	return m.publishKill(ctx, tenant, generation, actiongate.KillCauseOperatorSuspended)
}

// TenantPurgeBegan follows the committed purge row write, so intervening decisions
// already refuse through tenantLive. Purge is permanent: unlike a reversible disable,
// it needs no generation bump to reopen a fence and must not create a policy head in
// a tenant being erased. Retries publish again using the current head, if any.
func (m *Manager) TenantPurgeBegan(ctx context.Context, tenant string) error {
	head, err := m.iam.AgentPolicyHead(ctx, tenant)
	if err != nil {
		return err
	}
	generation := 0
	if head != nil {
		generation = head.ActuationGeneration
	}
	return m.publishKill(ctx, tenant, generation, actiongate.KillCauseOperatorSuspended)
}
