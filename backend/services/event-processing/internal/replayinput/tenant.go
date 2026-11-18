// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replayinput

import (
	"context"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

func sharded(m *messaging.NatsManager, suffix string) bool {
	cfg := m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups
	return cfg != nil && cfg.Count >= 2 && slices.Contains(cfg.Suffixes, suffix)
}

// TenantPreview preserves the exact legacy opener unless the mounted document
// selects this stream. The authenticated context reaches the tenant replay API.
func TenantPreview(ctx context.Context, m *messaging.NatsManager, suffix string) Opener {
	if !sharded(m, suffix) {
		return m
	}
	return tenantTimeSource{ctx: ctx, m: m}
}

type tenantTimeSource struct {
	ctx context.Context
	m   *messaging.NatsManager
}

func (s tenantTimeSource) NewReplayReaderFromTime(suffix string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	return s.m.NewTenantReplayReaderFromTime(s.ctx, suffix, start)
}

// SequenceOpener supports both time and sequence positions on one source.
type SequenceOpener interface {
	Opener
	NewReplayReader(string, uint64) (messaging.ReplayReader, uint64, error)
}

// TenantReview pins a single physical source for the entire comparison. Its
// suffix qualifies saved bounds, so base positions can never bind a g1 reader.
// TODO(R7 S4/S6 fenced cutover): moved tenants need ordered base<=H then own-group
// segments here. Fresh sharded installs have no pre-cutover base history.
func TenantReview(ctx context.Context, m *messaging.NatsManager, suffix string) (SequenceOpener, string, error) {
	if !sharded(m, suffix) {
		return m, suffix, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, "", core.ErrNoTenant
	}
	g, err := m.TenantShardGroup(tenant, suffix)
	if err != nil {
		return nil, "", err
	}
	source, err := m.NewGroupReplaySource(suffix, g)
	if err != nil {
		return nil, "", err
	}
	physical := messaging.GroupSuffix(suffix, g)
	return groupSource{source: source}, physical, nil
}

type groupSource struct{ source messaging.GroupReplaySource }

func (s groupSource) NewReplayReader(_ string, seq uint64) (messaging.ReplayReader, uint64, error) {
	return s.source.NewReplayReader(seq)
}

func (s groupSource) NewReplayReaderFromTime(_ string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	return s.source.NewReplayReaderFromTime(start)
}
