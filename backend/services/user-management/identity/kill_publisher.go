// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// KillWriter publishes kill facts through the tenant-scoped NATS writer.
type KillWriter struct {
	writer   messaging.MessageWriter
	instance string
}

// NewKillWriter binds the publisher to the instance and its catalog writer.
func NewKillWriter(writer messaging.MessageWriter, instance string) *KillWriter {
	return &KillWriter{writer: writer, instance: instance}
}

// PublishKill returns only after the NATS writer acknowledges or fails the publish.
func (w *KillWriter) PublishKill(ctx context.Context, e actiongate.KillEvent) error {
	b, err := actiongate.EncodeKillEvent(e)
	if err != nil {
		return err
	}
	return w.writer.WriteMessages(core.WithTenant(ctx, e.Tenant), messaging.Message{
		Subject: messaging.ScopedSubject(w.instance, e.Tenant, streams.AgentActuationKills), Value: b, DedupID: e.EventID,
	})
}
