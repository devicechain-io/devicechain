// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// sweepTenant reconciles one tenant's anchors against device-management, ONE PAGE AT A
// TIME.
//
// 🔴 IT USED TO LOAD THE WHOLE TENANT FIRST, and that is the defect this shape exists to
// close. It read every distinct target, then every distinct source device, deduped them
// into a map, then built a second map of everything that still existed — three
// structures all proportional to how much the tenant has anchored, all live at once,
// before a single orphan had been deleted. Nothing bounds that number. It chunked the
// OUTBOUND query at 500 and left the read that filled it unbounded, so the cap sat on
// the one part that was never going to be the problem.
//
// Now a page is the unit of everything: read at most one page, ask about exactly those
// refs, delete the orphans among them, forget them, advance the cursor. Peak memory is a
// function of the page size and nothing else.
//
// 🔴 ON FAILING SAFE, WHICH NOW MEANS SOMETHING SLIGHTLY DIFFERENT. An unreachable
// device-management still aborts the tenant rather than treating every ref as absent —
// that has not changed, and it is the whole reason this backstop cannot be written the
// obvious way. What has changed is that pages already reconciled STAY reconciled. That
// is not a weakening: every deletion it made was justified by a successful existence
// check on that ref, so no deletion rests on the answer that never came. The alternative
// — discarding a tenant's completed work because its last page failed — would make a
// large tenant unsweepable by a single flaky call.
func (s *AnchorReconciliationSweep) sweepTenant(ctx context.Context, tenant string) error {
	tctx := core.WithTenant(ctx, tenant)

	// Each walk is a closure over its own cursor, so the loop below does not have to
	// know that one is keyed by a pair and the other by a single token.
	//
	// 🔑 A REF IN BOTH SOURCES IS EXAMINED TWICE, ON PURPOSE. A device is an anchor
	// source, and for a tracked device→device relationship it is a target as well, so
	// the two pages overlap. Collapsing that overlap is exactly what the old in-memory
	// map bought, and it is not worth buying back: it costs one extra entry in one
	// existence query and one DELETE that matches nothing, against holding the whole
	// tenant in memory in order to know.
	var lastType, lastToken, lastDevice string
	var targets, devices cursorWatch
	walks := []struct {
		what string
		next func() ([]emmodel.AnchorRef, error)
	}{
		{"anchor targets", func() ([]emmodel.AnchorRef, error) {
			if err := targets.advancing(lastType, lastToken); err != nil {
				return nil, err
			}
			page, err := s.Api.DistinctAnchorTargetsAfter(tctx, lastType, lastToken)
			if err != nil || len(page) == 0 {
				return page, err
			}
			last := page[len(page)-1]
			lastType, lastToken = last.Type, last.Token
			return page, nil
		}},
		{"source devices", func() ([]emmodel.AnchorRef, error) {
			if err := devices.advancing(lastDevice); err != nil {
				return nil, err
			}
			page, err := s.Api.DistinctAnchorDeviceTokensAfter(tctx, lastDevice)
			if err != nil || len(page) == 0 {
				return page, err
			}
			lastDevice = page[len(page)-1].Token
			return page, nil
		}},
	}

	var removed int64
	var deleteErr error
	for _, walk := range walks {
		for {
			// An EMPTY page ends a source, not a SHORT one. A short page is the common
			// way to spell this, and it would make the processor depend on the page size
			// the model package chose — two constants that must agree, in different
			// modules, with a silently truncated sweep if they ever stop agreeing. One
			// extra query per source is the whole price of not having that.
			page, err := walk.next()
			if err != nil {
				log.Error().Err(err).Str("tenant", tenant).Str("walk", walk.what).
					Msg("Anchor sweep: failed to read a page of refs")
				return err
			}
			if len(page) == 0 {
				break
			}
			n, err := s.reconcilePage(ctx, tctx, tenant, page)
			removed += n
			if err != nil {
				// Two of the three outcomes END the tenant, and only one is accumulated.
				// An unresolved page means the next one cannot be trusted either; a
				// cancelled context means the pass is over. A failed DELETE is the only
				// one worth carrying on from — the next ref's deletion is independent of
				// it — and it still surfaces, so the pass cannot report itself complete.
				if errors.Is(err, errUnresolved) || ctx.Err() != nil {
					return err
				}
				deleteErr = err
			}
		}
	}

	if removed > 0 {
		log.Info().Str("tenant", tenant).Int64("anchorsRemoved", removed).
			Msg("Anchor sweep reconciled orphaned anchors")
	}
	return deleteErr
}

// errUnresolved marks the one failure that must abort the tenant rather than be
// accumulated: device-management could not say which of these refs still exist, and
// deleting on an unconfirmed absence would erase a live tenant's anchors wholesale.
var errUnresolved = errors.New("entity existence could not be resolved")

// cursorWatch refuses to make the same request twice, which is this walk's termination
// argument written down and checked.
//
// 🔴 IT WATCHES THE REQUESTS, NOT THE RESPONSES, AND THE FIRST VERSION GOT THAT WRONG.
// The obvious guard compares the page that came back against the cursor that asked for
// it, and it cannot see the defect it exists for: when the cursor variable is never
// updated, every response looks like progress against a cursor frozen at the start, so
// the check passes on every iteration while the loop spins forever. Verified — the
// mutant that deleted the cursor assignment sailed through that guard and hung the test
// run. What has to be strictly increasing is the sequence of cursors actually SENT, so
// that is what this remembers.
//
// 🔴 IT COMPARES THE KEY'S PARTS, NOT A JOINED STRING. refKey's "type|token" spelling is
// built for a map, where any injective encoding will do; as an ORDER it is wrong, because
// '|' is 0x7C and sorts after every letter — so "customer|cust-003" does not compare
// greater than the initial cursor "|", and an earlier draft refused a correctly advancing
// read on its very first page. Compare the parts in order, the way the database was asked
// to sort them.
//
// The failure it prevents is not a wrong answer. It is a pass that never returns,
// re-reading page one forever against the database, inside a periodic task with no other
// bound on how long a pass may take — and a test cannot report that either, it hangs
// instead of failing.
type cursorWatch struct {
	used  []string
	begun bool
}

func (c *cursorWatch) advancing(key ...string) error {
	if c.begun && !strictlyAfter(c.used, key) {
		return fmt.Errorf("the anchor walk is about to ask for cursor %q again, having "+
			"already asked for %q — the read's ordering and its keyset filter disagree, and "+
			"sweeping on would re-read the same page forever",
			strings.Join(key, "/"), strings.Join(c.used, "/"))
	}
	c.used = append(c.used[:0:0], key...)
	c.begun = true
	return nil
}

// strictlyAfter reports whether key sorts after prev, comparing part by part.
func strictlyAfter(prev, key []string) bool {
	for i := range key {
		if key[i] != prev[i] {
			return key[i] > prev[i]
		}
	}
	return false
}

// reconcilePage resolves one page of refs and deletes the anchors of those that no
// longer exist. It returns how many anchors it removed, and an error wrapping
// errUnresolved if the existence query itself failed.
//
// A ref that could not be DELETED is an orphan still present, so it is returned as an
// error and counts as work the pass did not do — without that, a sweep that reached
// every tenant and deleted nothing would report itself complete.
func (s *AnchorReconciliationSweep) reconcilePage(ctx, tctx context.Context,
	tenant string, refs []emmodel.AnchorRef) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	existing := make(map[string]bool, len(refs))
	if err := s.resolveChunk(ctx, tenant, refs, existing); err != nil {
		// Fail safe: never delete when we cannot confirm existence.
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("Anchor sweep: device-management unreachable; skipping tenant")
		return 0, fmt.Errorf("%w: %v", errUnresolved, err)
	}

	var removed int64
	var deleteErr error
	for _, r := range refs {
		if existing[refKey(r.Type, r.Token)] {
			continue
		}
		// Unbounded (zero time): the sweep deletes only refs that resolve to NO entity
		// at all, so every matching anchor is a true orphan regardless of when its event
		// occurred (ADR-044 decision-4 amendment).
		n, err := s.Api.DeleteAnchorsForEntity(tctx, r.Type, r.Token, time.Time{})
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Str("type", r.Type).Str("token", r.Token).
				Msg("Anchor sweep: delete failed")
			deleteErr = err
			continue
		}
		removed += n
	}
	return removed, deleteErr
}

func refKey(t string, token string) string { return t + "|" + token }

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
