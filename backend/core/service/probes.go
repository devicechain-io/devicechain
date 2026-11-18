// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
)

// ProbesPort is the public HTTP port of a Service with no GraphQL plane.
//
// 🔴 IT IS A VARIABLE ONLY SO A TEST CAN ASK FOR AN EPHEMERAL PORT, and a test that
// changes it must restore it. The services that take this path are exercised end to end
// by driving their real start callbacks, and those callbacks reach this server through
// Service.Start — so without a seam here, each of those tests would bind 8080 in CI and
// race whatever else holds it. The GraphQL manager solves the same problem with a field
// and an unexported sentinel, because its zero value is a value a dropped assignment can
// produce; a package variable initialized where it is declared has no such failure mode.
var ProbesPort int32 = core.HttpPort

// OpsProbesPort is the ops HTTP port of a Service with no GraphQL plane. Tests may
// set it to zero for an ephemeral port and must restore it afterwards. When only
// ProbesPort is overridden, the default ops port also becomes ephemeral so existing
// real-service test helpers keep both listeners off the fixed production ports.
var OpsProbesPort int32 = core.OpsPort

// probeServer is the HTTP surface of a Service that has no GraphQL plane: /healthz,
// /readyz and /metrics on Microservice.OpsMux(), and public routes on Microservice.Mux().
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
	ms        *core.Microservice
	server    *core.HttpServer
	opsServer *core.HttpServer

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

// ExecuteStart binds fresh servers, ops first and public second. An http.Server cannot be restarted — Shutdown
// latches it — so a retried start needs a new one, and the bind error is returned so a
// port collision refuses startup instead of leaving a pod with no probes.
func (p *probeServer) ExecuteStart(ctx context.Context) error {
	opsPort := OpsProbesPort
	if ProbesPort != core.HttpPort && opsPort == core.OpsPort {
		opsPort = 0
	}
	p.opsServer = core.NewHttpServerForHandler(opsPort, p.ms.OpsMux())
	if err := p.opsServer.Start(); err != nil {
		return err
	}
	p.server = p.ms.NewHttpServer(ProbesPort)
	if err := p.server.Start(); err != nil {
		return errors.Join(err, p.opsServer.Shutdown(ctx))
	}
	return nil
}

func (p *probeServer) Stop(ctx context.Context) error { return p.lifecycle.Stop(ctx) }

// ExecuteStop drains public requests before stopping ops, so probes answer through
// the whole unwind. Both shutdowns are attempted even when the context expires.
// Servers are nil before Start, which gets a no-op rather than a panic.
func (p *probeServer) ExecuteStop(ctx context.Context) error {
	var publicErr, opsErr error
	if p.server != nil {
		publicErr = p.server.Shutdown(ctx)
	}
	if p.opsServer != nil {
		opsErr = p.opsServer.Shutdown(ctx)
	}
	return errors.Join(publicErr, opsErr)
}

func (p *probeServer) Terminate(ctx context.Context) error { return p.lifecycle.Terminate(ctx) }

func (p *probeServer) ExecuteTerminate(context.Context) error { return nil }
