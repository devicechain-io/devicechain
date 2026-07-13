// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Rule-health status values (mirror the RuleStatus enum in schema.graphql).
const (
	statusActive       = "ACTIVE"
	statusCompileError = "COMPILE_ERROR"
)

// RuleHealth resolves the per-rule health of a device profile's ACTIVE published version
// (ADR-051 slice 7b): for each live DETECT rule it composes a status from a re-compile check
// and joins the durable RuleStat firing projection for last-fired / fire-count. It is
// tenant-scoped to the caller's tenant (the storage-layer tenant predicate, applied
// explicitly since these projections are not tenant-scoped models) and gated on device:read —
// the least-privilege authority for a read over the profile aggregate the rules belong to.
func (r *SchemaResolver) RuleHealth(ctx context.Context, args struct {
	ProfileToken string
}) ([]*RuleHealthResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Tenant == "" {
		// Fail closed: a tenant-scoped read with no tenant in context must not run.
		return nil, fmt.Errorf("rule health: no tenant in context")
	}
	tenant := claims.Tenant

	// Resolve the profile's active version; an unpublished profile has no live rules.
	active, found, err := r.Profiles.Load(ctx, tenant, args.ProfileToken)
	if err != nil {
		return nil, fmt.Errorf("rule health: load active profile version: %w", err)
	}
	if !found {
		return []*RuleHealthResolver{}, nil
	}

	rulesRows, err := r.DetectRules.LoadByProfileVersion(ctx, tenant, active.ActiveVersionToken)
	if err != nil {
		return nil, fmt.Errorf("rule health: load rules: %w", err)
	}
	ids := make([]string, len(rulesRows))
	for i, rr := range rulesRows {
		ids[i] = rr.RuleId
	}
	stats, err := r.RuleStats.LoadByIDs(ctx, tenant, ids)
	if err != nil {
		return nil, fmt.Errorf("rule health: load stats: %w", err)
	}

	// Share the same platform compile limits the publish gate + runtime use, so the recompile
	// check agrees with what the engine would accept.
	limits := rules.DefaultLimits()
	out := make([]*RuleHealthResolver, 0, len(rulesRows))
	for _, rr := range rulesRows {
		stat, hasStat := stats[rr.RuleId]
		out = append(out, buildRuleHealth(rr, stat, hasStat, limits))
	}
	return out, nil
}

// buildRuleHealth composes one rule's health row: its name (from the authored definition), a
// status from re-decoding+compiling the stored definition under current limits, and the joined
// firing stats. A projected rule compiled at publish (publish fails closed otherwise), so
// COMPILE_ERROR here means it stopped compiling AFTER publish — e.g. a tightened cost ceiling.
func buildRuleHealth(rr model.DetectRule, stat model.RuleStat, hasStat bool, limits rules.Limits) *RuleHealthResolver {
	rh := &RuleHealthResolver{
		ruleID:    rr.RuleId,
		ruleToken: rr.RuleToken,
		name:      rr.RuleToken,
		status:    statusActive,
	}
	decoded, derr := rules.Decode([]byte(rr.Definition))
	if derr != nil {
		rh.status = statusCompileError
		rh.message = derr.Error()
	} else {
		if decoded.Name != "" {
			rh.name = decoded.Name
		}
		decoded.ID = rr.RuleId // Compile requires a non-empty id (anchors its messages)
		if _, cerr := rules.Compile(decoded, limits); cerr != nil {
			rh.status = statusCompileError
			rh.message = cerr.Error()
		}
	}
	if hasStat {
		rh.fireCount = stat.FireCount
		rh.lastFiredAt = stat.LastFiredAt
		rh.lastSignal = stat.LastEdge
		rh.hasFired = true
	}
	return rh
}

// RuleHealthResolver resolves one RuleHealth. Firing fields are zero-valued until a rule
// fires; hasFired distinguishes "never fired" (null last-fired / null signal) from a real fire.
type RuleHealthResolver struct {
	ruleID      string
	ruleToken   string
	name        string
	status      string
	message     string
	hasFired    bool
	fireCount   int64
	lastFiredAt time.Time
	lastSignal  string
}

func (r *RuleHealthResolver) RuleId() string    { return r.ruleID }
func (r *RuleHealthResolver) RuleToken() string { return r.ruleToken }
func (r *RuleHealthResolver) Name() string      { return r.name }
func (r *RuleHealthResolver) Status() string    { return r.status }

// FireCount is int32 for the GraphQL Int scalar; a lifetime count cannot realistically
// overflow it for a health view, and it is approximate regardless.
func (r *RuleHealthResolver) FireCount() int32 { return int32(r.fireCount) }

// LastFiredAt returns the RFC3339 fire time, or null when the rule has never fired.
func (r *RuleHealthResolver) LastFiredAt() *string {
	if !r.hasFired {
		return nil
	}
	s := r.lastFiredAt.UTC().Format(time.RFC3339)
	return &s
}

// LastSignal returns the last fire's edge, or null when the rule has never fired.
func (r *RuleHealthResolver) LastSignal() *string {
	if !r.hasFired {
		return nil
	}
	s := r.lastSignal
	return &s
}

// Message returns the compile diagnostic when status is COMPILE_ERROR, else null.
func (r *RuleHealthResolver) Message() *string {
	if r.message == "" {
		return nil
	}
	s := r.message
	return &s
}
