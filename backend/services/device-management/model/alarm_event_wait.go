// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/rs/zerolog/log"
)

// DefaultAlarmEventWait is how long a committed alarm transition waits for the relay pass it
// kicked before it answers. A healthy broker publishes well inside it, so the event normally goes
// out before the mutation answers; a broker that does not answer cannot hold the mutation longer.
const DefaultAlarmEventWait = 2 * time.Second

// alarmRelayPassTimeout bounds one post-commit relay pass, whose context its caller cannot cancel.
const alarmRelayPassTimeout = 30 * time.Second

// SetAlarmPublisher replaces the publisher the relay publishes through. The NATS manager's
// create hook calls it on every broker start while relay passes may be running, so the field is
// written under the relay's lock and each pass reads it once (alarmPublisher).
func (api *Api) SetAlarmPublisher(p AlarmEventPublisher) {
	api.alarmEventMu.Lock()
	defer api.alarmEventMu.Unlock()
	api.AlarmPublisher = p
}

// alarmPublisher is the publisher one relay pass uses from start to end.
func (api *Api) alarmPublisher() AlarmEventPublisher {
	api.alarmEventMu.Lock()
	defer api.alarmEventMu.Unlock()
	return api.AlarmPublisher
}

// relayAlarmEvents kicks a relay pass after a transition committed and waits for it at most
// api.alarmEventWait. Kicks coalesce into at most one running and one pending pass per process: a
// kick made while a pass runs waits for the follow-up pass, which starts after it. The pass runs
// on the relay's own context, never the caller's (no request claims, scope, system authority or
// cancellation). After StopAlarmEventRelay a kick does nothing: the row stays queued for the
// periodic drain of whichever replica runs next.
func (api *Api) relayAlarmEvents(ctx context.Context) {
	if api.suppressAlarmEventKickForTest {
		return
	}
	api.alarmEventMu.Lock()
	if api.alarmRelayStopped {
		api.alarmEventMu.Unlock()
		return
	}
	if api.alarmRelayCtx == nil {
		api.alarmRelayCtx, api.alarmRelayCancel = context.WithCancel(context.Background())
	}
	if api.alarmEventPending == nil {
		api.alarmEventPending = make(chan struct{})
	}
	done := api.alarmEventPending
	if !api.alarmEventRunning {
		api.alarmEventRunning = true
		api.alarmRelayIdle = make(chan struct{})
		go api.runAlarmEventRelay(api.alarmRelayCtx, api.alarmRelayIdle)
	}
	api.alarmEventMu.Unlock()
	wait := api.alarmEventWait
	if wait <= 0 {
		wait = DefaultAlarmEventWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// runAlarmEventRelay runs relay passes until no kick is pending, then closes idle.
func (api *Api) runAlarmEventRelay(base context.Context, idle chan struct{}) {
	for {
		api.alarmEventMu.Lock()
		done := api.alarmEventPending
		api.alarmEventPending = nil // every later kick shares the next pass
		api.alarmEventMu.Unlock()
		func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Str("stack", string(debug.Stack())).
						Msg("The alarm event relay panicked; the events stay queued for the next pass")
				}
			}()
			ctx, cancel := context.WithTimeout(base, alarmRelayPassTimeout)
			defer cancel()
			if err := api.drainAlarmEventsLocked(ctx, false); err != nil && base.Err() == nil {
				log.Warn().Err(err).Msg("Alarm events stay queued for the next relay pass")
			}
		}()
		api.alarmEventMu.Lock()
		if api.alarmEventPending == nil || api.alarmRelayStopped {
			if api.alarmEventPending != nil {
				close(api.alarmEventPending)
				api.alarmEventPending = nil
			}
			api.alarmEventRunning = false
			close(idle)
			api.alarmEventMu.Unlock()
			return
		}
		api.alarmEventMu.Unlock()
	}
}

// StopAlarmEventRelay ends post-commit relaying for this process: later kicks do nothing, a
// running pass is cancelled, and the call waits for it to end (at most until ctx ends). Call it
// on shutdown before the broker connection and the database close. What is still queued is
// published by the periodic drain of whichever replica runs next.
func (api *Api) StopAlarmEventRelay(ctx context.Context) error {
	api.alarmEventMu.Lock()
	api.alarmRelayStopped = true
	if api.alarmRelayCancel != nil {
		api.alarmRelayCancel()
	}
	running, idle := api.alarmEventRunning, api.alarmRelayIdle
	api.alarmEventMu.Unlock()
	if !running {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
