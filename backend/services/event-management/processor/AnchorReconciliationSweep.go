// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
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
	// The embedded task supplies the lifecycle octet and the pass schedule, and its
	// metrics are how an operator sees this backstop running at all — before it, a
	// sweep that skipped every tenant because device-management was down logged a
	// warning per tenant and was otherwise indistinguishable from a clean one.
	*core.PeriodicTask

	Microservice *core.Microservice
	Api          emmodel.EventManagementApi
	client       *svcclient.Client
	dmURL        string
}

// NewAnchorReconciliationSweep builds the sweep. client resolves entity existence
// against device-management at dmURL; interval is the tick period.
func NewAnchorReconciliationSweep(ms *core.Microservice, api emmodel.EventManagementApi,
	client *svcclient.Client, dmURL string, interval time.Duration, callbacks core.LifecycleCallbacks) *AnchorReconciliationSweep {
	s := &AnchorReconciliationSweep{Microservice: ms, Api: api, client: client, dmURL: dmURL}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "anchor-sweep", interval,
		s.runOnce, callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("anchor_sweep")))
	return s
}

// runOnce sweeps every tenant that currently has anchors.
//
// 🔑 IT REPORTS WHAT IT MANAGED, and the three answers are not decoration. A sweep that
// could not reach device-management skips every tenant — by design, because deleting on an
// unconfirmed absence would erase a live tenant's whole anchor set — and before this it said
// so only in a warning per tenant. The backstop being down for a week looked exactly like the
// backstop finding nothing, which is the state it is normally in.
func (s *AnchorReconciliationSweep) runOnce(ctx context.Context) error {
	tenants, err := s.Api.DistinctAnchorTenants(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing tenants with anchors: %w", err)
	}
	var visited, failed int
	var lastErr error
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		visited++
		if err := s.sweepTenant(ctx, tenant); err != nil {
			failed++
			lastErr = err
		}
	}
	return core.PassResult(visited, failed, lastErr)
}

// sweepTenant reconciles one tenant's anchors against device-management.
func (s *AnchorReconciliationSweep) sweepTenant(ctx context.Context, tenant string) error {
	tctx := core.WithTenant(ctx, tenant)
	refs, err := s.Api.DistinctAnchorRefs(tctx)
	if err != nil {
		log.Error().Err(err).Str("tenant", tenant).Msg("Anchor sweep: failed to collect refs")
		return err
	}
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return nil
	}
	existing, err := s.resolveExisting(ctx, tenant, refs)
	if err != nil {
		// Fail safe: never delete when we can't confirm existence.
		log.Warn().Err(err).Str("tenant", tenant).Msg("Anchor sweep: device-management unreachable; skipping tenant")
		return err
	}
	var removed int64
	// A ref that could not be deleted is an orphan still present, so it counts as work the
	// pass did not do. Without this the tenant loop would tally only the tenants it could
	// ENUMERATE, and a sweep that reached every tenant and deleted nothing would report
	// itself complete.
	var deleteErr error
	for _, r := range refs {
		if existing[refKey(r.Type, r.Token)] {
			continue
		}
		// Unbounded (zero time): the sweep deletes only refs that resolve to NO
		// entity at all, so every matching anchor is a true orphan regardless of when
		// its event occurred (ADR-044 decision-4 amendment).
		n, err := s.Api.DeleteAnchorsForEntity(tctx, r.Type, r.Token, time.Time{})
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Str("type", r.Type).Str("token", r.Token).
				Msg("Anchor sweep: delete failed")
			deleteErr = err
			continue
		}
		removed += n
	}
	if removed > 0 {
		log.Info().Str("tenant", tenant).Int64("anchorsRemoved", removed).
			Msg("Anchor sweep reconciled orphaned anchors")
	}
	return deleteErr
}

func refKey(t string, token string) string { return t + "|" + token }

// maxRefsPerResolve bounds a single existence query so a large tenant's ref set
// neither overflows svcclient's response cap nor the target's SQL IN-list limit — a
// too-big request would error and (safely) skip the tenant, so the backstop would
// silently stop working for exactly the tenants that need it. Chunking keeps it live.
const maxRefsPerResolve = 500

// dedupeRefs collapses duplicate refs (a device is both an anchor source and, for a
// tracked device→device relationship, a target) so each is resolved and deleted once.
func dedupeRefs(refs []emmodel.AnchorRef) []emmodel.AnchorRef {
	seen := make(map[string]bool, len(refs))
	out := make([]emmodel.AnchorRef, 0, len(refs))
	for _, r := range refs {
		k := refKey(r.Type, r.Token)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// resolveExisting asks device-management which of refs still exist, returning a set
// keyed by refKey. It chunks the request; any chunk error is returned so the caller
// fails safe (skips the tenant) rather than treating unresolved refs as absent.
func (s *AnchorReconciliationSweep) resolveExisting(ctx context.Context, tenant string, refs []emmodel.AnchorRef) (map[string]bool, error) {
	existing := make(map[string]bool, len(refs))
	for start := 0; start < len(refs); start += maxRefsPerResolve {
		end := start + maxRefsPerResolve
		if end > len(refs) {
			end = len(refs)
		}
		if err := s.resolveChunk(ctx, tenant, refs[start:end], existing); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

// resolveChunk queries one batch and records the existing refs into `existing`. The
// refs are addressed by (type, token) (ADR-044): device-management echoes back the
// subset that still resolves, and anything it omits is treated as deleted (its
// anchors get swept), so the call fails safe by returning any transport error to the
// caller (which then skips the tenant rather than deleting on an unconfirmed set).
func (s *AnchorReconciliationSweep) resolveChunk(ctx context.Context, tenant string, refs []emmodel.AnchorRef, existing map[string]bool) error {
	inputs := make([]map[string]any, len(refs))
	for i, r := range refs {
		inputs[i] = map[string]any{"type": r.Type, "token": r.Token}
	}
	var out struct {
		ExistingEntityRefs []struct {
			Type  string `json:"type"`
			Token string `json:"token"`
		} `json:"existingEntityRefs"`
	}
	const q = `query($refs: [EntityRefInput!]!) { existingEntityRefs(refs: $refs) { type token } }`
	if err := s.client.Query(ctx, s.dmURL, tenant, q, map[string]any{"refs": inputs}, &out); err != nil {
		return err
	}
	for _, r := range out.ExistingEntityRefs {
		existing[refKey(r.Type, r.Token)] = true
	}
	return nil
}
