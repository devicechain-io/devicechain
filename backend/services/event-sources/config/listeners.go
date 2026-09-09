// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strconv"
	"time"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

const (
	// SourceTypeHttp is the EventSource.Type of a device-facing HTTP listener. It is
	// declared here, rather than only in the processor package that builds the source,
	// because validation has to know which sources LISTEN and the processor package
	// imports this one — so the two cannot share a constant the other way round.
	// processor.TYPE_HTTP is defined from this, so there is one spelling.
	SourceTypeHttp = "http"

	// DefaultHttpIngestPort is the listen port for HTTP ingest when a source
	// configures none. It is distinct from the service's GraphQL/management server
	// (gqlcore.GRAPHQL_PORT) so device telemetry and the management API do not share
	// a listener.
	DefaultHttpIngestPort = 8081

	// DefaultHttpHeaderTimeoutSeconds bounds how long a device may take to send its
	// request headers to an ingest listener. It is the value the shared HTTP server
	// has always applied; it is named here so the device-facing listener can be moved
	// off it without moving the management surfaces too.
	DefaultHttpHeaderTimeoutSeconds = 5

	// DefaultHttpRequestTimeoutSeconds bounds the read of a WHOLE ingest request,
	// headers and body. The shared server sets no such bound, so a client that
	// dribbles a body holds a connection and a goroutine indefinitely — the body is
	// capped in BYTES (1 MiB) but was not capped in TIME.
	//
	// It is deliberately far more generous than the header budget rather than close to
	// it: a constrained device on a slow link sends a small body slowly, and the point
	// of the bound is to end an unbounded hold, not to police throughput.
	DefaultHttpRequestTimeoutSeconds = 60
)

// HttpIngest configures the DEVICE-FACING HTTP listeners — every event source of type
// "http" — and nothing else. The service's GraphQL/management server keeps the shared
// platform defaults.
//
// 🔑 THE TWO LISTENERS HAVE DIFFERENT CALLERS, WHICH IS THE WHOLE REASON THIS EXISTS.
// A management API is reached by a console over a datacentre network; an ingest
// listener is reached by a device that may be on NB-IoT, 2G or satellite, where a
// multi-second round trip before the request is complete is ordinary rather than
// pathological. A device that exceeds a bound has its connection closed, and the
// symptom is that events from the SLOWEST devices only stop arriving with nothing in
// the event pipeline to attribute it to — the request never became an event.
type HttpIngest struct {
	// HeaderTimeoutSeconds bounds the wait for a request's headers. Defaults to
	// DefaultHttpHeaderTimeoutSeconds; a non-positive value falls back to it, so this
	// bound can be raised or lowered but never removed.
	HeaderTimeoutSeconds int
	// RequestTimeoutSeconds bounds the read of the whole request, headers and body.
	// Defaults to DefaultHttpRequestTimeoutSeconds; a non-positive value falls back to
	// it, for the same reason.
	RequestTimeoutSeconds int
}

// HeaderTimeout and RequestTimeout render the configured seconds as durations, applying
// the same fail-safe defaulting the rest of this package uses: a non-positive value
// falls back to the platform default rather than to zero, which for these two would
// mean "no bound at all" and is the state they exist to end.
func (h HttpIngest) HeaderTimeout() time.Duration {
	return secondsOr(h.HeaderTimeoutSeconds, DefaultHttpHeaderTimeoutSeconds)
}

func (h HttpIngest) RequestTimeout() time.Duration {
	return secondsOr(h.RequestTimeoutSeconds, DefaultHttpRequestTimeoutSeconds)
}

// HttpSourcePort resolves the listen port an HTTP event source's configuration names.
//
// 🔴 IT IS THE ONE PARSER, READ BY BOTH THE VALIDATION AND THE CONSTRUCTION. Config
// validation refuses a port the process cannot serve on, and the source then binds
// whatever the same function returns — so the value that is checked and the value that
// is bound cannot drift apart. A second parser here would let a configuration pass
// validation and bind something else.
//
// An absent or empty key yields DefaultHttpIngestPort, NOT zero.
func HttpSourcePort(cfg map[string]string) (int, error) {
	raw, ok := cfg["port"]
	if !ok || raw == "" {
		return DefaultHttpIngestPort, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid http event source port %q: %w", raw, err)
	}
	// Range-checked here rather than left to the bind: a configured port outside the
	// TCP range is a configuration error the operator can fix, and saying so before the
	// listener is built keeps the value that reaches it a real port number.
	if parsed < 0 || parsed > 65535 {
		return 0, fmt.Errorf("invalid http event source port %q: out of range 0-65535", raw)
	}
	// 🔴 ZERO IS IN RANGE AND IS NEVER CORRECT FOR A DEVICE-FACING LISTENER. net.Listen
	// reads 0 as "any free port", so a source configured with it binds an ephemeral
	// port, starts successfully, logs a healthy address and ingests nothing a device
	// can reach — a silent success, in the one value an operator is most likely to
	// arrive at by accident. Tests that want an OS-chosen port set the port on the
	// source directly; a CONFIGURATION never asks for one.
	if parsed == 0 {
		return 0, fmt.Errorf("invalid http event source port %q: 0 asks the operating system for any free port, "+
			"so the source would bind an ephemeral port no device can be told to reach — name the port it should listen on", raw)
	}
	return parsed, nil
}

// validateListenerPorts refuses a configuration in which two of this process's listeners
// would ask for the same port, and does it before anything binds.
//
// 🔴 event-sources IS THE ONE SERVICE THAT RUNS TWO KINDS OF LISTENER IN ONE PROCESS:
// the GraphQL/management server, and one server per HTTP event source. Nothing used to
// compare their ports. That was survivable only while every listener bound inside a
// goroutine, where a collision killed one ingest transport silently and the pod still
// came up healthy; now that a bind failure is fatal, the same configuration stops the
// whole deployment — including the GraphQL surface and the MQTT sources that were
// working.
//
// 🔑 THE BIND ERROR REMAINS THE BACKSTOP AND MUST STAY DISTINCT FROM THIS ONE. This
// check sees only what configuration can see; something outside the process can still
// hold the port. A collision found here is an operator error with a specific remedy, so
// it names both listeners and the port; a bind failure at runtime is an environment
// problem and says so in its own words.
func (c *EventSourcesConfiguration) validateListenerPorts() error {
	// The management server is seeded FIRST so a source colliding with it is reported
	// against it, rather than the two sources being compared and the fixed listener
	// nobody configured going unmentioned.
	// Each entry names the listener holding the port, the way an operator would
	// recognise it in their own configuration document.
	claimed := map[int]string{
		gqlcore.GRAPHQL_PORT: "this service's GraphQL/management server",
	}
	for i, src := range c.EventSources {
		// Only an HTTP source LISTENS. An MQTT source's port is the port it dials out
		// to on a broker, so including it here would refuse a correct configuration —
		// a source reading from a broker on 8081 collides with nothing.
		if src.Type != SourceTypeHttp {
			continue
		}
		port, err := HttpSourcePort(src.Configuration)
		if err != nil {
			return fmt.Errorf("eventSources[%d].id %q: %w", i, src.Id, err)
		}
		if held, taken := claimed[port]; taken {
			return fmt.Errorf(
				"eventSources[%d].id %q listens on port %d, which is already taken by %s: "+
					"two listeners in one process cannot share a port, and this one would fail to bind and stop the service. "+
					"Give one of them a different port",
				i, src.Id, port, held)
		}
		claimed[port] = fmt.Sprintf("eventSources[%d].id %q", i, src.Id)
	}
	return nil
}
