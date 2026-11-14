// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/rs/zerolog/log"
)

const (
	maxPositionFilterEntries = 1024
	// positionDecisionTTL bounds how long a decision is trusted. A published version's rules
	// never change, but a rule id is NOT unique for all time: "{profile}@{n}" restarts when a
	// profile is deleted and created again under the same token, so the same id can come back
	// holding a geofence rule. A decision is therefore re-read after this long rather than
	// held for the life of the subscription.
	positionDecisionTTL = 30 * time.Second
)

type positionDecision struct {
	allowed bool
	at      time.Time
}

// positionFilter belongs to one subscription goroutine. It caches decided answers by rule id
// for positionDecisionTTL; FIFO eviction bounds memory across profile versions, and an
// evicted or expired decision is simply recomputed.
type positionFilter struct {
	store   *model.DetectRuleStore
	now     func() time.Time // nil: time.Now
	allowed map[string]positionDecision
	order   []string
	next    int
}

func (f *positionFilter) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *positionFilter) allow(ctx context.Context, ruleID string) bool {
	if d, ok := f.allowed[ruleID]; ok && f.clock().Sub(d.at) < positionDecisionTTL {
		return d.allowed
	}
	if f.store == nil {
		log.Debug().Str("rule", ruleID).Msg("detection subscription: rule store unavailable; withholding event")
		return false
	}
	row, ok, err := f.store.LoadByID(ctx, ruleID)
	if err != nil || !ok {
		log.Debug().Err(err).Str("rule", ruleID).Msg("detection subscription: rule unavailable; withholding event")
		return false
	}
	compiled, failed := runtime.CompilePublishedRules(row.Tenant, row.ProfileVersionToken, []dmmodel.PublishedDetectionRule{{
		Token: row.RuleToken, Definition: row.Definition,
		EntityGroupToken: row.EntityGroupToken, EntityGroupVersion: row.EntityGroupVersion,
	}})
	if failed != 0 || len(compiled) != 1 || compiled[0].Compiled.ID != ruleID {
		log.Debug().Str("rule", ruleID).Msg("detection subscription: rule cannot be compiled; withholding event")
		f.remember(ruleID, false) // a stored definition compiles the same way every time
		return false
	}
	// DETECT rules currently consume resolved device events only (RuleRegistry.Plan), not
	// other rules' derived signals. The strict rule decoder has no derived-input field.
	// If derived inputs are added, their position dependency must be checked transitively here.
	allowed := !compiled[0].Compiled.RequiresPosition
	f.remember(ruleID, allowed)
	return allowed
}

// remember caches a DECIDED answer only. A missing row or a failed read withholds the event
// it was asked for but is not remembered, so a transient store error does not silence a
// rule for the rest of the subscription; it is asked again on the rule's next detection.
func (f *positionFilter) remember(ruleID string, allowed bool) {
	if f.allowed == nil {
		f.allowed = make(map[string]positionDecision)
	}
	if _, ok := f.allowed[ruleID]; ok {
		f.allowed[ruleID] = positionDecision{allowed: allowed, at: f.clock()}
		return
	}
	if len(f.allowed) == maxPositionFilterEntries {
		delete(f.allowed, f.order[f.next])
		f.order[f.next] = ruleID
		f.next = (f.next + 1) % maxPositionFilterEntries
	} else {
		f.order = append(f.order, ruleID)
	}
	f.allowed[ruleID] = positionDecision{allowed: allowed, at: f.clock()}
}
