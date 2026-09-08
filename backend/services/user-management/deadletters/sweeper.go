// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"fmt"
	"sync"
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
	Microservice *core.Microservice
	store        *Store
	retention    time.Duration
	interval     time.Duration

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup

	lifecycle core.LifecycleManager
}

// NewSweeper builds the sweeper. retention is how long a letter is kept, measured from
// when the platform gave up on it.
func NewSweeper(ms *core.Microservice, store *Store, retention, interval time.Duration,
	callbacks core.LifecycleCallbacks) *Sweeper {
	s := &Sweeper{Microservice: ms, store: store, retention: retention, interval: interval}
	s.lifecycle = core.NewLifecycleManager(
		fmt.Sprintf("%s-%s", ms.FunctionalArea, "dead-letter-sweep"), s, callbacks)
	return s
}

func (s *Sweeper) Initialize(ctx context.Context) error { return s.lifecycle.Initialize(ctx) }
func (s *Sweeper) ExecuteInitialize(context.Context) error {
	s.procCtx, s.procCancel = context.WithCancel(context.Background())
	return nil
}
func (s *Sweeper) Start(ctx context.Context) error { return s.lifecycle.Start(ctx) }
func (s *Sweeper) ExecuteStart(context.Context) error {
	s.wg.Add(1)
	go s.loop()
	return nil
}
func (s *Sweeper) Stop(ctx context.Context) error { return s.lifecycle.Stop(ctx) }
func (s *Sweeper) ExecuteStop(context.Context) error {
	if s.procCancel != nil {
		s.procCancel()
	}
	s.wg.Wait()
	return nil
}
func (s *Sweeper) Terminate(ctx context.Context) error    { return s.lifecycle.Terminate(ctx) }
func (s *Sweeper) ExecuteTerminate(context.Context) error { return nil }

func (s *Sweeper) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.procCtx.Done():
			return
		case <-ticker.C:
			s.RunOnce(s.procCtx)
		}
	}
}

// RunOnce deletes everything older than the retention window.
func (s *Sweeper) RunOnce(ctx context.Context) {
	before := time.Now().UTC().Add(-s.retention)
	n, err := s.store.Prune(ctx, before)
	if err != nil {
		log.Error().Err(err).Msg("Dead-letter retention sweep failed; retrying on the next tick.")
		return
	}
	if n > 0 {
		log.Info().Int64("removed", n).Time("before", before).
			Msg("Pruned dead letters past their retention window.")
	}
}
