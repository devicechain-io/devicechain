// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// Sweeper bounds the store by age.
//
// 🔴 THE STORE IS THE ONLY THING BOUNDING THIS DATA, and that is the point of it existing.
// The stream ages out in seven days; ADR-024 asks for a record that OUTLIVES the messages
// it describes, which is exactly what the table buys — so the table is what needs a bound
// of its own, and it is a longer one than the stream's by design rather than by accident.
//
// It is the same shape as notification-management's retention sweeper and the purge
// coordinator: a ticker, a cancellable loop, and a join on stop.
type Sweeper struct {
	// The embedded task supplies the lifecycle octet, the schedule and the pass metrics.
	//
	// 🔑 IT IS BUILT WITH WithDetachedContext, WHICH PRESERVES THIS SWEEPER'S EXISTING
	// BEHAVIOUR RATHER THAN CHANGING IT. Its ExecuteInitialize took a context.Context it
	// did not name and derived its loop from context.Background() instead, so the loop
	// outlived the root cancellation that precedes teardown and ended only at Stop. That
	// was indistinguishable from an oversight; naming the option makes it a decision.
	*core.PeriodicTask

	Microservice *core.Microservice
	store        *Store
	retention    time.Duration
}

// NewSweeper builds the sweeper. retention is how long a letter is kept, measured from
// when the platform gave up on it.
func NewSweeper(ms *core.Microservice, store *Store, retention, interval time.Duration,
	callbacks core.LifecycleCallbacks) *Sweeper {
	s := &Sweeper{Microservice: ms, store: store, retention: retention}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "dead-letter-sweep", interval,
		s.RunOnce, callbacks, core.WithDetachedContext(),
		core.WithPassMetrics(ms.NewPeriodicTaskMetrics("dead_letter_sweep")))
	return s
}

// RunOnce deletes everything older than the retention window.
func (s *Sweeper) RunOnce(ctx context.Context) error {
	before := time.Now().UTC().Add(-s.retention)
	n, err := s.store.Prune(ctx, before)
	if err != nil {
		return fmt.Errorf("pruning dead letters older than %s: %w", before.Format(time.RFC3339), err)
	}
	if n > 0 {
		log.Info().Int64("removed", n).Time("before", before).
			Msg("Pruned dead letters past their retention window.")
	}
	return nil
}
