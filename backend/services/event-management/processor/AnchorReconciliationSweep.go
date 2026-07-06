// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

// AnchorReconciliationSweep is the low-frequency backstop of ADR-044 decision 3: it
// periodically drops event_anchors rows whose referenced entity no longer resolves
// in device-management, catching any entity-deletion event missed during an outage
// or re-created inside the ingest cache window. The primary path is the
// entity.deleted consumer (EntityAnchorReconciler); this sweep only cleans what that
// missed.
//
// It fails SAFE: if device-management is unreachable it skips the tenant rather than
// treating every ref as absent (which would delete all its anchors) — an orphan
// lingers until the next run instead of a reachable-but-down owner nuking the set.
type AnchorReconciliationSweep struct {
	Microservice *core.Microservice
	Api          emmodel.EventManagementApi
	client       *svcclient.Client
	dmURL        string
	interval     time.Duration

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
	lifecycle  core.LifecycleManager
}

// NewAnchorReconciliationSweep builds the sweep. client resolves entity existence
// against device-management at dmURL; interval is the tick period.
func NewAnchorReconciliationSweep(ms *core.Microservice, api emmodel.EventManagementApi,
	client *svcclient.Client, dmURL string, interval time.Duration, callbacks core.LifecycleCallbacks) *AnchorReconciliationSweep {
	s := &AnchorReconciliationSweep{
		Microservice: ms, Api: api, client: client, dmURL: dmURL, interval: interval,
	}
	s.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-anchor-sweep", ms.FunctionalArea), s, callbacks)
	return s
}

func (s *AnchorReconciliationSweep) Initialize(ctx context.Context) error {
	return s.lifecycle.Initialize(ctx)
}

func (s *AnchorReconciliationSweep) ExecuteInitialize(ctx context.Context) error {
	s.procCtx, s.procCancel = context.WithCancel(ctx)
	return nil
}

func (s *AnchorReconciliationSweep) Start(ctx context.Context) error {
	return s.lifecycle.Start(ctx)
}

func (s *AnchorReconciliationSweep) ExecuteStart(context.Context) error {
	s.wg.Add(1)
	go s.loop()
	return nil
}

func (s *AnchorReconciliationSweep) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.procCtx.Done():
			return
		case <-ticker.C:
			s.runOnce(s.procCtx)
		}
	}
}

func (s *AnchorReconciliationSweep) Stop(ctx context.Context) error {
	return s.lifecycle.Stop(ctx)
}

func (s *AnchorReconciliationSweep) ExecuteStop(context.Context) error {
	if s.procCancel != nil {
		s.procCancel()
	}
	s.wg.Wait()
	return nil
}

func (s *AnchorReconciliationSweep) Terminate(ctx context.Context) error {
	return s.lifecycle.Terminate(ctx)
}

func (s *AnchorReconciliationSweep) ExecuteTerminate(context.Context) error {
	return nil
}

// runOnce sweeps every tenant that currently has anchors.
func (s *AnchorReconciliationSweep) runOnce(ctx context.Context) {
	tenants, err := s.Api.DistinctAnchorTenants(core.WithSystemContext(ctx))
	if err != nil {
		log.Error().Err(err).Msg("Anchor sweep: failed to list tenants")
		return
	}
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return
		default:
			s.sweepTenant(ctx, tenant)
		}
	}
}

// sweepTenant reconciles one tenant's anchors against device-management.
func (s *AnchorReconciliationSweep) sweepTenant(ctx context.Context, tenant string) {
	tctx := core.WithTenant(ctx, tenant)
	refs, err := s.Api.DistinctAnchorRefs(tctx)
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).Msg("Anchor sweep: failed to collect refs")
		return
	}
	if len(refs) == 0 {
		return
	}
	existing, err := s.resolveExisting(ctx, tenant, refs)
	if err != nil {
		// Fail safe: never delete when we can't confirm existence.
		log.Warn().Err(err).Str("tenant", tenant).Msg("Anchor sweep: device-management unreachable; skipping tenant")
		return
	}
	var removed int64
	for _, r := range refs {
		if existing[refKey(r.Type, r.Id)] {
			continue
		}
		n, err := s.Api.DeleteAnchorsForEntity(tctx, r.Type, r.Id)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Str("type", r.Type).Uint("id", r.Id).
				Msg("Anchor sweep: delete failed")
			continue
		}
		removed += n
	}
	if removed > 0 {
		log.Info().Str("tenant", tenant).Int64("anchorsRemoved", removed).
			Msg("Anchor sweep reconciled orphaned anchors")
	}
}

func refKey(t string, id uint) string { return t + "|" + strconv.FormatUint(uint64(id), 10) }

// resolveExisting asks device-management which of refs still exist, returning a set
// keyed by refKey. A transport/GraphQL error is returned so the caller fails safe.
func (s *AnchorReconciliationSweep) resolveExisting(ctx context.Context, tenant string, refs []emmodel.AnchorRef) (map[string]bool, error) {
	inputs := make([]map[string]any, len(refs))
	for i, r := range refs {
		inputs[i] = map[string]any{"type": r.Type, "id": strconv.FormatUint(uint64(r.Id), 10)}
	}
	var out struct {
		ExistingEntityRefs []struct {
			Type string `json:"type"`
			Id   string `json:"id"`
		} `json:"existingEntityRefs"`
	}
	const q = `query($refs: [EntityRefInput!]!) { existingEntityRefs(refs: $refs) { type id } }`
	if err := s.client.Query(ctx, s.dmURL, tenant, q, map[string]any{"refs": inputs}, &out); err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(out.ExistingEntityRefs))
	for _, r := range out.ExistingEntityRefs {
		id, err := strconv.ParseUint(r.Id, 10, 64)
		if err != nil {
			continue
		}
		existing[refKey(r.Type, uint(id))] = true
	}
	return existing, nil
}
