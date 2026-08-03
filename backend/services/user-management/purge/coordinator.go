// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
)

// coordinatorLockName namespaces the advisory lock this coordinator holds while a pass
// runs. Namespaced per concern so it never blocks the migration lock or any other job's.
const coordinatorLockName = "user-management-tenant-purge"

// Coordinator drives every purging tenant towards erasure and then releases its token.
//
// # Why the tenant row is the work list
//
// There is no queue and no fact to lose. A purge is "there is a row in iam_tenants whose
// purge_state is purging", so a dropped message, a coordinator that died mid-sweep, a
// store that was down for a week, and a replica that was rescheduled all converge on the
// next pass. The work list ends when the row is removed, which is the same act that
// releases the token — so there is no state in which the platform believes a purge is
// finished and the row still says otherwise.
//
// # Why a try-lock rather than a claim
//
// Every replica runs this loop. A blocking acquire would not prevent concurrent work, it
// would QUEUE it: each replica would run the whole pass in turn, sweeping tenants a peer
// had just swept. A non-blocking acquire means one replica does the pass and the others
// return immediately and try again next tick.
//
// A per-tenant CAS claim would allow replicas to work different tenants in parallel, and
// it is deliberately not used: the claim would have to be taken BEFORE the sweep and
// released after, so a replica killed mid-sweep would leave a claim nothing clears, and
// the purge would stall until someone noticed. A pass is short, purges are rare, and a
// stalled purge is the one failure mode this design cannot tolerate quietly.
type Coordinator struct {
	Microservice *core.Microservice

	iam    *iam.Store
	db     *rdb.RdbManager
	stores []Store

	interval time.Duration
	// settle is how long every store must have been reporting clean before the purge
	// can complete. See iam.TenantPurgeStore.CleanSince: a residual scan run right after
	// a sweep sees only writes already in flight, so one clean observation is not the
	// same claim as "nothing is coming".
	settle time.Duration

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
	lifecycle  core.LifecycleManager

	// now is time.Now, replaced in tests. The settle window is a comparison between two
	// clocks' worth of timestamps, so a test that cannot move time can only assert the
	// window by sleeping through it.
	now func() time.Time
}

// NewCoordinator builds the purge loop. interval is the tick period; settle is how long
// every store must stay clean before the token is released.
func NewCoordinator(ms *core.Microservice, store *iam.Store, db *rdb.RdbManager,
	stores []Store, interval, settle time.Duration, callbacks core.LifecycleCallbacks) *Coordinator {
	c := &Coordinator{
		Microservice: ms,
		iam:          store,
		db:           db,
		stores:       stores,
		interval:     interval,
		settle:       settle,
		now:          time.Now,
	}
	c.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-tenant-purge", ms.FunctionalArea), c, callbacks)
	return c
}

func (c *Coordinator) Initialize(ctx context.Context) error { return c.lifecycle.Initialize(ctx) }

func (c *Coordinator) ExecuteInitialize(ctx context.Context) error {
	c.procCtx, c.procCancel = context.WithCancel(ctx)
	return nil
}

func (c *Coordinator) Start(ctx context.Context) error { return c.lifecycle.Start(ctx) }

func (c *Coordinator) ExecuteStart(context.Context) error {
	c.wg.Add(1)
	go c.loop()
	return nil
}

// loop runs a pass immediately and then on every tick. The first pass is not deferred to
// the first tick because an operator who just deleted a tenant is watching, and a purge
// that appears to do nothing for a minute is indistinguishable from one that is broken.
func (c *Coordinator) loop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.RunOnce(c.procCtx)
	for {
		select {
		case <-c.procCtx.Done():
			return
		case <-ticker.C:
			c.RunOnce(c.procCtx)
		}
	}
}

func (c *Coordinator) Stop(ctx context.Context) error { return c.lifecycle.Stop(ctx) }

func (c *Coordinator) ExecuteStop(context.Context) error {
	if c.procCancel != nil {
		c.procCancel()
	}
	c.wg.Wait()
	return nil
}

func (c *Coordinator) Terminate(ctx context.Context) error { return c.lifecycle.Terminate(ctx) }

func (c *Coordinator) ExecuteTerminate(context.Context) error { return nil }

// RunOnce makes one pass over every purging tenant, if no peer replica is already doing
// so. Exported so a test drives the real pass rather than a reconstruction of it.
func (c *Coordinator) RunOnce(ctx context.Context) {
	ran, err := c.db.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(coordinatorLockName), func() error {
		return c.pass(ctx)
	})
	if err != nil {
		log.Error().Err(err).Msg("Tenant purge pass failed")
		return
	}
	if !ran {
		log.Debug().Msg("Tenant purge pass skipped: another replica holds the lock")
	}
}

// pass sweeps every purging tenant once. One tenant's failure does not stop the others:
// a purge stuck on a store that is down must not hold up a purge that could finish.
func (c *Coordinator) pass(ctx context.Context) error {
	tenants, err := c.iam.TenantsPurging(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing purging tenants: %w", err)
	}
	for i := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := c.PurgeTenant(ctx, &tenants[i]); err != nil {
			log.Error().Err(err).Str("tenant", tenants[i].Token).Msg("Tenant purge did not complete this pass")
		}
	}
	return nil
}

// PurgeTenant runs one pass of one tenant's purge: every store erases, the ledger
// records what each reported, and the purge completes if everything has been clean for
// the settle window.
//
// 🔴 THE FIRST THING IT DOES IS THE INTERLOCK, AND THAT IS THIS FUNCTION'S MOST IMPORTANT
// LINE. core/tenantpurge.Sweep documents a precondition it deliberately cannot enforce —
// the token must already be in `purging` — because teaching the core package about
// iam_tenants would invert the layering for every other user of it. This is the caller
// that owes that guarantee, and it is not a formality: the sweep commits in ONE
// transaction, so a token that named a LIVE tenant would erase it completely and
// atomically, leaving no partial state for anyone to notice on the way past.
//
// Re-checking a row the query already filtered is deliberate. It makes the guarantee
// local to the code that depends on it, so it survives someone later calling this from
// somewhere that did not filter — which is exactly how the guarantee would otherwise be
// lost.
func (c *Coordinator) PurgeTenant(ctx context.Context, t *iam.Tenant) error {
	sys := core.WithSystemContext(ctx)

	// 🔴 AN EMPTY STORE SET COMPLETES INSTANTLY IF THIS IS NOT HERE, and it does so
	// through the most reassuring path in the function: no store reports anything unclean,
	// so every check below passes, and the settle window is measured from a zero-value
	// timestamp that is always long elapsed. The purge would release the token and record
	// an erasure having asked nobody. That is precisely the silence this design is built
	// to make impossible, arriving through the coordinator instead of through a store.
	if len(c.stores) == 0 {
		return fmt.Errorf("refusing to purge %q: no stores are registered, so nothing would be "+
			"asked and nothing erased — but the purge would complete and release the token", t.Token)
	}

	if t.PurgeState != iam.PurgePurging {
		return fmt.Errorf("refusing to purge %q: its lifecycle is %q, not %q — a sweep erases every "+
			"row bearing the token, in one transaction", t.Token, t.PurgeState, iam.PurgePurging)
	}
	if t.PurgeEpoch == nil {
		return fmt.Errorf("refusing to purge %q: it is purging with no epoch, so its deletion record "+
			"would have no identity and no fence could be bounded by it", t.Token)
	}
	epoch := *t.PurgeEpoch

	rec, err := c.iam.EnsurePurgeRecord(sys, t.Token, epoch)
	if err != nil {
		return fmt.Errorf("opening the deletion record for %q: %w", t.Token, err)
	}

	previous, err := c.iam.PurgeStores(sys, rec.ID)
	if err != nil {
		return fmt.Errorf("reading the ledger for %q: %w", t.Token, err)
	}
	cleanSince := cleanSinceByStore(previous)

	total := int64(0)
	allClean := true
	// The settle window has to have elapsed for EVERY store, so the constraining
	// timestamp is the most recent one — the last store to go clean. Taking the earliest
	// would let a store that only just went clean ride out on a peer that has been clean
	// for an hour.
	var lastCleanAt time.Time
	for _, store := range c.stores {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := c.eraseOne(ctx, store, t.Token, epoch, rec.ID, cleanSince[store.Name()])
		if err := c.iam.RecordPurgeStore(sys, line); err != nil {
			return fmt.Errorf("recording %q's ledger line for %q: %w", store.Name(), t.Token, err)
		}
		total += line.Rows
		if line.CleanSince == nil {
			allClean = false
			continue
		}
		if line.CleanSince.After(lastCleanAt) {
			lastCleanAt = *line.CleanSince
		}
	}

	if !allClean {
		return nil
	}
	if settled := c.now().Sub(lastCleanAt); settled < c.settle {
		log.Info().Str("tenant", t.Token).Dur("cleanFor", settled).Dur("settle", c.settle).
			Msg("Tenant purge is clean, holding for the settle window")
		return nil
	}

	if err := c.iam.CompleteTenantPurge(sys, t, rec, total, c.now()); err != nil {
		return fmt.Errorf("completing the purge of %q: %w", t.Token, err)
	}
	log.Info().Str("tenant", t.Token).Time("epoch", epoch).Int64("rows", total).
		Msg("Tenant purge complete; token released")
	return nil
}

// eraseOne runs one store and builds its ledger line, carrying the clean-since timestamp
// forward or clearing it.
//
// A store that errors keeps its rows from the previous line at zero rather than
// inventing a number, and loses its clean-since: an error means the pass could not
// establish that the store is clean, which is not the same as knowing it is dirty but
// has to be treated the same way, because completion is a claim and an unestablished
// claim cannot support one.
func (c *Coordinator) eraseOne(ctx context.Context, store Store, tenant string, epoch time.Time,
	recordID uint, wasCleanSince *time.Time) *iam.TenantPurgeStore {
	at := c.now()
	line := &iam.TenantPurgeStore{
		TenantPurgeID: recordID,
		Store:         store.Name(),
		AttemptedAt:   at,
	}

	outcome, err := store.Erase(ctx, tenant, epoch)
	line.Rows = outcome.Rows
	line.Deferred = outcome.Reason()
	if err != nil {
		line.Failure = err.Error()
		log.Warn().Err(err).Str("tenant", tenant).Str("store", store.Name()).
			Msg("Tenant purge store did not report clean")
		return line
	}
	if !outcome.Clean() {
		return line
	}

	line.Complete = true
	if wasCleanSince != nil {
		line.CleanSince = wasCleanSince
	} else {
		line.CleanSince = &at
	}
	return line
}

// cleanSinceByStore indexes the previous pass's clean-since timestamps by store name, so
// a store that has been clean all along keeps its original timestamp instead of restarting
// the settle window on every pass.
func cleanSinceByStore(lines []iam.TenantPurgeStore) map[string]*time.Time {
	out := make(map[string]*time.Time, len(lines))
	for i := range lines {
		if lines[i].Complete && lines[i].CleanSince != nil {
			out[lines[i].Store] = lines[i].CleanSince
		}
	}
	return out
}
