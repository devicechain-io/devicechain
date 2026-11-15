// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// GroupReplaySource binds every replay position to one physical stream. Its
// fields are private so only NewGroupReplaySource can authorize a binding.
type GroupReplaySource struct {
	nmgr     *NatsManager
	base     string
	physical string
}

// NewGroupReplaySource authorizes the declared base suffix before binding its
// selected group. Physical group names are never accepted as catalog entries.
func (nmgr *NatsManager) NewGroupReplaySource(baseSuffix string, group int) (GroupReplaySource, error) {
	nmgr.freezeStreamRouting()
	s := nmgr.streamSharding()
	if !s.selects(baseSuffix) {
		return GroupReplaySource{}, fmt.Errorf("messaging: suffix %q is not selected for stream sharding", baseSuffix)
	}
	if group < 0 || group >= s.cfg.Count {
		return GroupReplaySource{}, fmt.Errorf("messaging: shard group %d out of range [0,%d)", group, s.cfg.Count)
	}
	if err := nmgr.requireReader(baseSuffix, streams.ReadEphemeral); err != nil {
		return GroupReplaySource{}, err
	}
	return GroupReplaySource{nmgr: nmgr, base: baseSuffix, physical: GroupSuffix(baseSuffix, group)}, nil
}

func (s GroupReplaySource) NewReplayReader(seq uint64) (ReplayReader, uint64, error) {
	return s.nmgr.newReplayReader(s.physical, seq)
}

func (s GroupReplaySource) NewReplayReaderFromTime(start time.Time) (ReplayReader, time.Time, error) {
	return s.nmgr.newReplayReaderFromTime(s.physical, start)
}

func (s GroupReplaySource) NewRangeReader(first, last uint64) (ReplayReader, error) {
	return s.nmgr.newRangeReader(s.base, s.physical, first, last)
}

// NewTenantReplayReaderFromTime selects the authenticated tenant's physical
// stream once for the request. The caller still filters messages by tenant.
// Unselected suffixes retain the legacy replay behavior, including no tenant requirement.
func (nmgr *NatsManager) NewTenantReplayReaderFromTime(ctx context.Context, baseSuffix string, start time.Time) (ReplayReader, time.Time, error) {
	nmgr.freezeStreamRouting()
	if !nmgr.streamSharding().selects(baseSuffix) {
		return nmgr.NewReplayReaderFromTime(baseSuffix, start)
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, time.Time{}, core.ErrNoTenant
	}
	physical, err := nmgr.tenantSuffix(tenant, baseSuffix)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := nmgr.requireReader(baseSuffix, streams.ReadEphemeral); err != nil {
		return nil, time.Time{}, err
	}
	return nmgr.newReplayReaderFromTime(physical, start)
}
