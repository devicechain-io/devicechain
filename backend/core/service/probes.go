// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
)

// ProbesPort is the port a Service with no GraphQL plane serves its probes on.
//
// 🔴 IT IS A VARIABLE ONLY SO A TEST CAN ASK FOR AN EPHEMERAL PORT, and a test that
// changes it must restore it. The services that take this path are exercised end to end
// by driving their real start callbacks, and those callbacks reach this server through
// Service.Start — so without a seam here, each of those tests would bind 8080 in CI and
// race whatever else holds it. The GraphQL manager solves the same problem with a field
// and an unexported sentinel, because its zero value is a value a dropped assignment can
// produce; a package variable initialized where it is declared has no such failure mode.
var ProbesPort int32 = core.HttpPort

// probeServer is the HTTP surface of a Service that has no GraphQL plane: /healthz,
// /readyz and /metrics, plus any route the service registered on Microservice.Mux()
// before Initialize.
//
// Every DeviceChain pod needs these. The chart points its liveness, readiness and startup
// probes at them by name and its ServiceMonitor scrapes /metrics, so a Service is never
// without them: through the GraphQL manager when there is one, through this otherwise.
//
// 🔑 IT STARTS FIRST AND STOPS LAST, THE OPPOSITE END OF THE SEQUENCE FROM THE GRAPHQL
// MANAGER, AND BOTH POSITIONS FOLLOW FROM ONE RULE: A PROBE SURFACE STAYS UP AS LONG AS
// ITS DEPENDENCIES ALLOW. The GraphQL server's resolvers read wiring the broker builds,
// so it cannot accept traffic before NATS has started and must stop taking it before
// NATS drains. Nothing served here depends on any manager — /metrics gathers the
// microservice's own registry, /healthz reads the microservice's liveness latch, and
// /readyz reads the gate and that latch — so it can answer through the whole unwind, and
// it does: an operator watching a shutdown keeps the metrics of the part that is actually
// slow. The ingest services already stopped
// their servers last before they joined this package, though the reason recorded for it
// was a different one, and wrong: it conflated the HTTP stop with the leadership unwind.
type probeServer struct {
	ms     *core.Microservice
	server *core.HttpServer

	lifecycle core.LifecycleManager
}

func newProbeServer(ms *core.Microservice) *probeServer {
	p := &probeServer{ms: ms}
	p.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-%s", ms.FunctionalArea, "probes"),
		p, core.NewNoOpLifecycleCallbacks())
	return p
}

func (p *probeServer) Initialize(ctx context.Context) error { return p.lifecycle.Initialize(ctx) }

// ExecuteInitialize registers the probes, once. ServeMux panics on a duplicate pattern
// and a retried start re-enters ExecuteStart, so the routes belong here and not there.
func (p *probeServer) ExecuteInitialize(context.Context) error {
	p.ms.RegisterProbes(p.ms.Readiness)
	return nil
}

func (p *probeServer) Start(ctx context.Context) error { return p.lifecycle.Start(ctx) }

// ExecuteStart binds a fresh server. An http.Server cannot be restarted — Shutdown
// latches it — so a retried start needs a new one, and the bind error is returned so a
// port collision refuses startup instead of leaving a pod with no probes.
func (p *probeServer) ExecuteStart(context.Context) error {
	p.server = p.ms.NewHttpServer(ProbesPort)
	return p.server.Start()
}

func (p *probeServer) Stop(ctx context.Context) error { return p.lifecycle.Stop(ctx) }

// ExecuteStop lets in-flight requests finish until ctx expires. The server is nil only
// if Stop is driven by hand without a Start, which gets a no-op rather than a panic.
func (p *probeServer) ExecuteStop(ctx context.Context) error {
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown(ctx)
}

func (p *probeServer) Terminate(ctx context.Context) error { return p.lifecycle.Terminate(ctx) }

func (p *probeServer) ExecuteTerminate(context.Context) error { return nil }
