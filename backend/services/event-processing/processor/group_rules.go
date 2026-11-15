// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"
)

// groupRuleRouting belongs to the process, including across leadership terms.
// Only ownership metadata is shared: registries and engines remain loop-owned.
// An observed move poisons the routing for this process; retrying a term cannot
// silently adopt the new owner while the old owner's checkpoint still has state.
type groupRuleRouting struct {
	mu      sync.Mutex
	nmgr    *messaging.NatsManager
	workers []*ResolvedEventsProcessor
	owners  map[string]int
	err     error

	// termReady is replaced before any term loop starts and closed only after
	// every worker has built, at the shared fact consumers' launch point.
	termReady chan struct{}
}

func (r *groupRuleRouting) group(tenant string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, r.err
	}
	g, err := r.nmgr.TenantShardGroup(tenant, streams.ResolvedEvents)
	if err != nil {
		return 0, err
	}
	if old, ok := r.owners[tenant]; ok && old != g {
		r.err = fmt.Errorf("DETECT frozen placement: tenant %q moved from group %d to %d; refusing state migration", tenant, old, g)
		return 0, r.err
	}
	r.owners[tenant] = g
	return g, nil
}

func (rp *ResolvedEventsProcessor) ruleWorker(tenant string) (*ResolvedEventsProcessor, error) {
	if rp.ruleRouting == nil {
		return rp, nil
	}
	g, err := rp.ruleRouting.group(tenant)
	if err != nil {
		return nil, err
	}
	if g < 0 || g >= len(rp.ruleRouting.workers) {
		return nil, fmt.Errorf("DETECT group %d has no worker", g)
	}
	return rp.ruleRouting.workers[g], nil
}

func (rp *ResolvedEventsProcessor) ownsRuleTenant(tenant string) error {
	owner, err := rp.ruleWorker(tenant)
	if err != nil {
		return err
	}
	if owner != rp {
		return fmt.Errorf("DETECT partition %q refuses tenant %q owned by group %d", rp.partitionID(), tenant, owner.worker.group)
	}
	return nil
}

func (rp *ResolvedEventsProcessor) refuseRulePlacement(err error) {
	log.Error().Err(err).Str("partition", rp.partitionID()).Msg("DETECT refuses unsafe tenant placement; ending the common term.")
	rp.pcancel()
}

// checkRulePlacement also runs while idle, before publishing a checkpoint. A
// tenant move must not let its already-armed timers publish on the former owner.
func (rp *ResolvedEventsProcessor) checkRulePlacement() error {
	if rp.ruleRouting == nil {
		return nil
	}
	r := rp.ruleRouting
	r.mu.Lock()
	tenants := make([]string, 0, len(r.owners))
	for tenant := range r.owners {
		tenants = append(tenants, tenant)
	}
	r.mu.Unlock()
	for _, tenant := range tenants {
		if _, err := r.group(tenant); err != nil {
			return err
		}
	}
	return nil
}

func (rp *ResolvedEventsProcessor) ownedRules(scoped []runtime.ScopedRule) ([]runtime.ScopedRule, error) {
	if rp.ruleRouting == nil {
		return scoped, nil
	}
	out := make([]runtime.ScopedRule, 0, len(scoped))
	for _, sr := range scoped {
		owner, err := rp.ruleWorker(sr.Tenant)
		if err != nil {
			return nil, err
		}
		if owner == rp {
			out = append(out, sr)
		}
	}
	return out, nil
}

// prepareGroupRules runs before restore and before this worker has a live loop.
// Diffing later preserves the publisher's registry pointer and never mutates a
// registry that belongs to another worker.
func (rp *ResolvedEventsProcessor) prepareGroupRules(ctx context.Context) error {
	if rp.ruleRouting == nil {
		return nil
	}
	if err := rp.checkRulePlacement(); err != nil {
		return err
	}
	for _, id := range rp.registry.IDs() {
		tenant, ok := runtime.RuleTenant(id)
		if !ok {
			return fmt.Errorf("DETECT invalid rule identity %q", id)
		}
		if err := rp.ownsRuleTenant(tenant); err != nil {
			return err
		}
	}
	if rp.RuleStore == nil || rp.registry.Count() > 0 {
		return nil
	}
	scoped, err := NewStoreRuleSource(rp.RuleStore).Load(ctx)
	if err != nil {
		return err
	}
	scoped, err = rp.ownedRules(scoped)
	if err != nil {
		return err
	}
	fresh := runtime.NewRuleRegistry(scoped)
	for _, id := range rp.registry.IDs() {
		rp.registry.Remove(id)
	}
	for _, id := range fresh.IDs() {
		sr, _ := fresh.Lookup(id)
		rp.registry.Upsert(*sr)
	}
	return nil
}

// admitGroupSnapshot checks every serialized rule reference, including timer
// generations and held samples that may have no current registry entry. It only
// inspects JSON; it never rewrites snapshot bytes or converts legacy state.
func (rp *ResolvedEventsProcessor) admitGroupSnapshot(payload []byte) error {
	if rp.ruleRouting == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if key == "rule" {
					id, ok := child.(string)
					if !ok {
						return fmt.Errorf("DETECT invalid snapshot rule reference")
					}
					tenant, ok := runtime.RuleTenant(id)
					if !ok {
						return fmt.Errorf("DETECT invalid snapshot rule %q", id)
					}
					if err := rp.ownsRuleTenant(tenant); err != nil {
						return err
					}
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}

// attachRoutedRuleWorkers installs rule and membership routing, and is private
// until the remaining fact kinds and S3/S7 are ready.
// Exactly the supervisor owns the fact durable and reconciliation client.
func (rp *ResolvedEventsProcessor) attachRoutedRuleWorkers(nmgr *messaging.NatsManager, children ...*ResolvedEventsProcessor) error {
	workers := append([]*ResolvedEventsProcessor{rp}, children...)
	r := &groupRuleRouting{nmgr: nmgr, workers: workers, owners: make(map[string]int)}
	seen := make(map[*runtime.RuleRegistry]bool)
	for g, w := range workers {
		if w == nil || w.ruleRouting != nil || w.registry == nil || seen[w.registry] {
			return fmt.Errorf("group rules require distinct registries and fresh routing")
		}
		seen[w.registry] = true
		if (w.RosterStore != nil || w.RosterReader != nil || w.EntityDeletedReader != nil) && (w.RosterStore == nil || w.ProfileActiveStore == nil) {
			return fmt.Errorf("group membership requires roster and active-version projections")
		}
		if g > 0 && (w.RuleUpdatesReader != nil || w.RosterReader != nil || w.EntityDeletedReader != nil || w.DeviceManagement != nil) {
			return fmt.Errorf("only the supervisor owns fact consumers and reconcile")
		}
		if w.RuleStore != rp.RuleStore || w.ProfileActiveStore != rp.ProfileActiveStore || w.RosterStore != rp.RosterStore {
			return fmt.Errorf("group rules require one durable projection set")
		}
	}
	for _, w := range workers {
		w.ruleRouting = r
	}
	if err := rp.attachGroupWorkers(children...); err != nil {
		for _, w := range workers {
			w.ruleRouting = nil
		}
		return err
	}
	return nil
}
