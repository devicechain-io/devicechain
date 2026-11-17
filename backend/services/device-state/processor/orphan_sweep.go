// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const OrphanSweepInterval = 10 * time.Minute
const orphanPageSize = 500

// LiveDevice is device-management's identity for a token, independent of the projection.
type LiveDevice struct {
	Token     string
	ID        int64
	CreatedAt time.Time
}

type OrphanSource interface {
	Tenants(context.Context) ([]string, error)
	Devices(context.Context, string, []string) (map[string]LiveDevice, error)
}

type OrphanSweep struct {
	Api              *model.Api
	Source           OrphanSource
	Deleted, Stamped prometheus.Counter
}

// Run permits one replica to reconcile at a time. All projection access is tenant scoped.
func (s *OrphanSweep) Run(ctx context.Context) error {
	held, err := s.Api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey("device-state-orphan-sweep"), func() error { return s.Sweep(ctx) })
	if err != nil {
		return err
	}
	if !held {
		return core.ErrPassSkipped
	}
	return nil
}

// Sweep lists tenants through the authority, then isolates failures to their tenant.
func (s *OrphanSweep) Sweep(ctx context.Context) error {
	tenants, err := s.Source.Tenants(ctx)
	if err != nil {
		return err
	}
	var deleted, stamped int
	var failed int
	var lastErr error
	for _, tenant := range tenants {
		d, st, e := s.sweepTenant(core.WithTenant(ctx, tenant), tenant)
		deleted += d
		stamped += st
		if e != nil {
			failed++
			lastErr = e
			log.Warn().Err(e).Str("tenant", tenant).Msg("Orphan sweep skipped remaining tenant pages")
		}
	}
	if deleted > 0 || stamped > 0 {
		log.Info().Int("deleted", deleted).Int("stamped", stamped).Msg("Orphan sweep reconciled device states")
	}
	if s.Deleted != nil {
		s.Deleted.Add(float64(deleted))
	}
	if s.Stamped != nil {
		s.Stamped.Add(float64(stamped))
	}
	return core.PassResult(len(tenants), failed, lastErr)
}

func (s *OrphanSweep) sweepTenant(ctx context.Context, tenant string) (deleted, stamped int, err error) {
	var after uint
	for {
		rows, e := s.Api.OrphanPage(ctx, after, orphanPageSize)
		if e != nil {
			return deleted, stamped, e
		}
		if len(rows) == 0 {
			return
		}
		tokens := make([]string, len(rows))
		for i := range rows {
			tokens[i] = rows[i].DeviceToken
		}
		live, e := s.Source.Devices(ctx, tenant, tokens)
		if e != nil {
			return deleted, stamped, e
		}
		// A nil response is not an authoritative empty list.
		if live == nil {
			return deleted, stamped, fmt.Errorf("device-management returned no answer")
		}
		for _, row := range rows {
			device, exists := live[row.DeviceToken]
			if exists && (device.ID <= 0 || device.CreatedAt.IsZero()) {
				return deleted, stamped, fmt.Errorf("invalid device identity for %q", row.DeviceToken)
			}
		}
		for _, row := range rows {
			device := live[row.DeviceToken]
			d, st, e := s.Api.ReconcileDeviceState(ctx, row, device.ID, device.CreatedAt)
			if e != nil {
				return deleted, stamped, e
			}
			if d {
				deleted++
			}
			if st {
				stamped++
			}
		}
		after = rows[len(rows)-1].ID
		if len(rows) < orphanPageSize {
			return
		}
	}
}
