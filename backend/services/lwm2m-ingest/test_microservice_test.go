// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-microservice/core"
)

// newTestMicroservice installs a Microservice with its own mux and metrics registry,
// restoring the package globals afterwards. Each call gets a fresh one, so the probe
// registrations of one test cannot collide with another's.
//
// The HTTP surface tests that used to live beside this moved to core/service with the
// server itself: this service registers no route and builds no server of its own any more.
func newTestMicroservice(t *testing.T) {
	t.Helper()

	prevMs, prevSvc := Microservice, Svc
	t.Cleanup(func() { Microservice, Svc = prevMs, prevSvc })

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "lwm2m-ingest",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	// No validator, matching production: this service authenticates devices at DTLS-PSK and
	// verifies no JWT, so the gate is opened without an auth surface rather than left
	// closed (which would make every /readyz a 503 for the wrong reason).
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Svc = nil
}
