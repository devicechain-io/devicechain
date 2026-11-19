// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/rs/zerolog/log"
)

const DefaultAlarmEventWait = 2 * time.Second

// relayAlarmEvents coalesces kicks into at most one running and one pending pass.
// A kick during a pass waits for the follow-up, which starts after that kick.
func (api *Api) relayAlarmEvents(ctx context.Context) {
	if api.suppressAlarmEventKickForTest {
		return
	}
	api.alarmEventMu.Lock()
	if api.alarmEventPending == nil {
		api.alarmEventPending = make(chan struct{})
	}
	done := api.alarmEventPending
	if !api.alarmEventRunning {
		api.alarmEventRunning = true
		go api.runAlarmEventRelay(context.Background())
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

func (api *Api) runAlarmEventRelay(base context.Context) {
	for {
		api.alarmEventMu.Lock()
		done := api.alarmEventPending
		api.alarmEventPending = nil // all later kicks share the next pass
		api.alarmEventMu.Unlock()
		func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Str("stack", string(debug.Stack())).Msg("Alarm event relay panicked; events remain queued")
				}
			}()
			// Never carry request claims, scope, system authority or cancellation into a relay.
			ctx, cancel := context.WithTimeout(base, 30*time.Second)
			defer cancel()
			if err := api.DrainAlarmEventsLocked(ctx); err != nil {
				log.Warn().Err(err).Msg("Alarm events remain queued for the next relay pass")
			}
		}()
		api.alarmEventMu.Lock()
		if api.alarmEventPending == nil {
			api.alarmEventRunning = false
			api.alarmEventMu.Unlock()
			return
		}
		api.alarmEventMu.Unlock()
	}
}
