// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type killMessageWriter struct {
	messaging.MessageWriter
	messages []messaging.Message
	err      error
	tenant   string
}

func (w *killMessageWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.tenant, _ = core.TenantFromContext(ctx)
	w.messages = append(w.messages, msgs...)
	return w.err
}
func TestKillWriter(t *testing.T) {
	w := &killMessageWriter{}
	p := NewKillWriter(w, "instance")
	e := actiongate.KillEvent{EventID: "id", Tenant: "acme", Cause: actiongate.KillCauseTenantDisabled, PolicyVersion: 2, At: time.Now().UTC()}
	require.NoError(t, p.PublishKill(context.Background(), e))
	require.Len(t, w.messages, 1)
	require.Equal(t, "acme", w.tenant)
	require.Equal(t, messaging.ScopedSubject("instance", "acme", streams.AgentActuationKills), w.messages[0].Subject)
	require.Equal(t, e.EventID, w.messages[0].DedupID)
	decoded, err := actiongate.DecodeKillEvent(w.messages[0].Value)
	require.NoError(t, err)
	require.Equal(t, e, decoded)
	w.err = errors.New("publish failed")
	require.ErrorIs(t, p.PublishKill(context.Background(), e), w.err)
	e.Cause = "invalid"
	require.Error(t, p.PublishKill(context.Background(), e))
	require.Len(t, w.messages, 2)
}
