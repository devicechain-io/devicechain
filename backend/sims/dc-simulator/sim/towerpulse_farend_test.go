// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/tls"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-simulator/cmdreceiver"
)

// handlerSim is a farEndSim that answers its own commands, like towerpulse.
type handlerSim struct {
	farEndSim
	handler cmdreceiver.Handler
}

func (s *handlerSim) CommandHandler() cmdreceiver.Handler { return s.handler }

type noopHandler struct{}

func (noopHandler) Receive(string, cmdreceiver.Command, func(bool)) {}

// handlerFarEnd is a fake far end that can take a handler, and records WHEN it was given one
// relative to the first device attaching.
type handlerFarEnd struct {
	fakeFarEnd
	mu2           sync.Mutex
	handler       cmdreceiver.Handler
	attachedAtSet int
}

func (f *handlerFarEnd) SetHandler(h cmdreceiver.Handler) {
	f.mu2.Lock()
	defer f.mu2.Unlock()
	f.handler = h
	f.attachedAtSet = len(f.tokens())
}

// A scenario that answers its own commands has its handler set on the far end BEFORE any device
// subscribes: a frame answered by the default behaviour and then by another would split one cohort
// between two meanings of "answered".
func TestTheScenariosHandlerIsSetBeforeAnyDeviceSubscribes(t *testing.T) {
	fe := &handlerFarEnd{}
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(_, _, broker string, _ *tls.Config) CommandFarEnd { fe.broker = broker; return fe },
	}
	h := noopHandler{}
	driver := &handlerSim{farEndSim: farEndSim{mode: FarEndInternal, devices: []DeviceInstance{
		{Token: "dev-001", CredentialId: "c1"}, {Token: "dev-002", CredentialId: "c2"},
	}}, handler: h}
	lc := NewLifecycle(driver, rt)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fe.handler != h {
		t.Fatal("the scenario's handler never reached the far end")
	}
	if fe.attachedAtSet != 0 {
		t.Errorf("%d devices had subscribed when the handler was set; it must be set first", fe.attachedAtSet)
	}
	if len(fe.tokens()) != 2 {
		t.Errorf("%d devices attached, want 2", len(fe.tokens()))
	}
}

// A far end that cannot take a handler is refused, and closed: leaving it answering at once is the
// false success the handler exists to prevent, so the scenario must not come up on it.
func TestAFarEndThatCannotTakeAHandlerIsRefused(t *testing.T) {
	fe := &fakeFarEnd{}
	rt := &Runtime{
		Tenant: "acme", InstanceId: "dc", MqttBroker: "ssl://127.0.0.1:1883",
		NewFarEnd: func(_, _, _ string, _ *tls.Config) CommandFarEnd { return fe },
	}
	driver := &handlerSim{farEndSim: farEndSim{mode: FarEndInternal, devices: []DeviceInstance{{Token: "dev-001", CredentialId: "c1"}}},
		handler: noopHandler{}}
	err := NewLifecycle(driver, rt).Bootstrap(context.Background())
	if err == nil || !strings.Contains(err.Error(), "handler") {
		t.Fatalf("Bootstrap = %v, want a refusal naming the handler", err)
	}
	if len(fe.tokens()) != 0 {
		t.Error("devices were attached to a far end that was then refused")
	}
	if fe.closeCount() != 1 {
		t.Errorf("the refused far end was closed %d times, want 1", fe.closeCount())
	}
}

// A scenario with no handler of its own keeps the default behaviour: nothing is set, and a far end
// that cannot take one is fine.
func TestAScenarioWithoutAHandlerLeavesTheFarEndAlone(t *testing.T) {
	lc, fe := farEndFixture(t, FarEndInternal, nil)
	if err := lc.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.tokens()) != 2 {
		t.Errorf("%d devices attached, want 2", len(fe.tokens()))
	}
}

// Towerpulse itself IS such a scenario: it implements the hook the lifecycle looks for.
func TestTowerpulseAnswersItsOwnCommands(t *testing.T) {
	var s Sim = NewTowerpulse(1, Load{})
	if _, ok := s.(commandHandlerSource); !ok {
		t.Fatal("towerpulse does not implement commandHandlerSource, so the lifecycle would attach the answer-at-once receiver and report boosts SUCCESSFUL on arrival")
	}
}
