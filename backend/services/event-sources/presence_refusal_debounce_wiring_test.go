// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-event-sources/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

type countingPresenceEmitter struct {
	mu   sync.Mutex
	gone []string
}

func (e *countingPresenceEmitter) EmitPresence(_ context.Context, _, _, device string, ev adapter.PresenceEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !ev.Connected {
		e.gone = append(e.gone, device)
	}
	return nil
}

func (e *countingPresenceEmitter) EmitPresenceDemotion(context.Context, string, string, string, adapter.DemotionEvent) error {
	return nil
}

func (e *countingPresenceEmitter) disconnects() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.gone)
}

// 🔑 OFF, A REFUSAL-CLOSE IS REPORTED AT ONCE. The debounce is for the fail-closed store's
// refusal-closes; without infrastructure.nats.mqttFailClosed the same broker reason names a client
// dropped for a malformed packet, and an install that never turned the setting on must see it go
// offline exactly when it did before this existed. On, the disconnect is held back.
//
// GUARD: kills a tap that holds refusal-closes back with the setting off, and (the control) one
// that never holds them with it on.
func TestTheRefusalCloseDebounceFollowsTheFailClosedSetting(t *testing.T) {
	prev := Microservice
	t.Cleanup(func() { Microservice = prev })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}

	advisory := func(device string) []byte {
		start, stop := time.Now().Add(-time.Minute), time.Now().Add(-time.Second)
		return []byte(fmt.Sprintf(`{"client":{"client_id":"inst-1:acme:%s","client_type":"mqtt","start":%q,"stop":%q},"reason":%q}`,
			device, start.UTC().Format(time.RFC3339Nano), stop.UTC().Format(time.RFC3339Nano), presence.RefusalCloseReason))
	}

	off := &countingPresenceEmitter{}
	tapOff := newPresenceTap("mqtt1", off, nil, presence.Metrics{}, false)
	t.Cleanup(tapOff.StopDeferred)
	tapOff.Handle(context.Background(), false, advisory("off"))
	require.Equal(t, 1, off.disconnects(), "with the setting off a refusal-close disconnect was held back")
	require.Zero(t, tapOff.DeferredDeaths())

	on := &countingPresenceEmitter{}
	tapOn := newPresenceTap("mqtt1", on, nil, presence.Metrics{}, true)
	t.Cleanup(tapOn.StopDeferred)
	tapOn.Handle(context.Background(), false, advisory("on"))
	require.Zero(t, on.disconnects(), "control: with the setting on a refusal-close disconnect is held back")
	require.Equal(t, 1, tapOn.DeferredDeaths())
}
