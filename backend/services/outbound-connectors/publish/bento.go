// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package publish is the ADR-060 Tier-2 outbound sink: it delivers a single rendered
// payload through an embedded Bento output generated from a versioned Connector. It is
// the ONLY place the Bento dependency is linked — kept in the outbound-connectors module
// so the replay-correct DETECT/event-processing binary never sees the Bento tree
// (dep-isolation red line). Components are registered SELECTIVELY (see the blank imports
// below), never public/components/all, to bound the dep tree + supply-chain surface to
// the shipped connector set.
package publish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/warpstreamlabs/bento/public/service"

	// Selective component registration (ADR-060 §2). `pure` supplies the core plumbing
	// (the default buffer/broker/processors the bounded single-message stream composes
	// around); the remaining imports are exactly the shipped connector output families —
	// C4b shipped mqtt, C4c adds kafka + aws (aws_sns / aws_sqs). NEVER import
	// public/components/all. (gcp is deferred — its output has no per-connector credential
	// field; see connectorspec.) Importing the `aws` family registers all AWS components in
	// Bento, but the tenant-facing surface is gated by connectorspec's generator registry +
	// the model type vocabulary, so only aws_sns / aws_sqs connectors are creatable.
	_ "github.com/warpstreamlabs/bento/public/components/aws"
	_ "github.com/warpstreamlabs/bento/public/components/kafka"
	_ "github.com/warpstreamlabs/bento/public/components/mqtt"
	_ "github.com/warpstreamlabs/bento/public/components/pure"
)

// stopGrace bounds the graceful teardown of the ephemeral stream. Teardown runs
// ASYNCHRONOUSLY (off the send/ack path — see Send), so this does not count against the
// consumer AckWait budget; it only bounds how long a reaper goroutine lingers before it
// force-abandons a stream whose output is slow to close. Where an output exposes a connect
// timeout the generator bounds it (e.g. mqtt connect_timeout); combined with the caller's
// per-send ctx deadline and this grace, a stuck output cannot pin a reaper indefinitely.
const stopGrace = 5 * time.Second

// ErrPublishConfig marks a TERMINAL publish failure that originates from the output
// configuration or the stream itself — an empty/invalid/unbuildable config, or a stream
// that dies at construction (e.g. a config that lints but fails to build). It is distinct
// from a transient DELIVERY failure (broker briefly down), which is a plain error. The
// executor classifies ErrPublishConfig as terminal (dead-letter, not retry): a redelivery
// cannot fix a config Bento cannot run.
var ErrPublishConfig = errors.New("publish: invalid output configuration")

// Send performs a BOUNDED single-message send of payload through the Bento output
// described by outputYAML (generated from a Connector — the tenant never writes Bento
// YAML), blocking until the message is delivered and acknowledged by the output,
// rejected/undeliverable, ctx is cancelled, or the stream dies. It builds an ephemeral
// stream (one in-memory producer input → the connector output), NOT a resident pipeline,
// so SD-2's back-pressure is "don't send" and Bento's config surface stays minimal.
//
// metadata is attached to the message (e.g. an idempotency key) for outputs that can use
// it; outputs that can't ignore it. A nil/empty metadata map is fine.
//
// SECURITY: outputYAML and payload may carry a credential / PII — this function never logs
// either. Bento's own logger is routed to a discarding logger so a component cannot leak
// the rendered config to stdout, and config-level ${VAR} env interpolation is DISABLED so
// a tenant-authored value can never be substituted from the process environment.
func Send(ctx context.Context, outputYAML string, payload []byte, metadata map[string]string) error {
	if outputYAML == "" {
		return fmt.Errorf("%w: empty output configuration", ErrPublishConfig)
	}

	builder := service.NewStreamBuilder()
	// Disable Bento's ${VAR} config env-var interpolation. AddOutputYAML runs env substitution
	// (default os.LookupEnv) over the raw config BEFORE parsing, and json.Marshal does not escape
	// $/{/} — so without this a tenant-authored config value like "${DATABASE_PASSWORD}" would be
	// replaced with a POD-ENVIRONMENT secret and delivered to the tenant's own broker (host-secret
	// exfiltration). The generated config never legitimately contains a Bento env ref, so a lookup
	// that always reports "unset" neutralizes the vector with no loss of function.
	builder.SetEnvVarLookupFunc(func(string) (string, bool) { return "", false })
	// Silence Bento's logger: components may otherwise print connection details (which can include
	// a host/credential) to stdout. A leveled logger discarding everything keeps the embedded engine
	// quiet; our own metric/log around the call is the operator signal.
	builder.SetLeveledLogger(discardLogger{})

	// Build failures are deterministic config errors → terminal (ErrPublishConfig).
	handler, err := builder.AddProducerFunc()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPublishConfig, err)
	}
	if err := builder.AddOutputYAML(outputYAML); err != nil {
		return fmt.Errorf("%w: %v", ErrPublishConfig, err)
	}
	stream, err := builder.Build()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPublishConfig, err)
	}

	// Run the stream on a background context so a caller-context cancellation cancels the SEND (via
	// the handler's ctx below) while teardown stays under our own control. Run blocks until the
	// stream stops.
	runCtx, cancelRun := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- stream.Run(runCtx) }()

	msg := service.NewMessage(payload)
	for k, v := range metadata {
		msg.MetaSetMut(k, v)
	}

	// The producer handler blocks until the message is delivered+acked, rejected, or ctx is
	// cancelled — the definitive send outcome. Run it in a goroutine so we can also react to the
	// stream dying at construction (a config that lints but fails to build): otherwise the handler
	// would block until the ctx deadline while nothing consumes the producer channel. Both channels
	// are buffered, so a goroutine whose result we no longer wait for still completes and exits.
	sendErr := make(chan error, 1)
	go func() { sendErr <- handler(ctx, msg) }()

	select {
	case result := <-sendErr:
		// Teardown OFF the ack path: cancel the run and reap the stream asynchronously (bounded by
		// stopGrace + the output's connect_timeout), so the already-decided send outcome is returned
		// immediately and teardown never counts against the consumer AckWait budget.
		cancelRun()
		go reap(stream, runErr)
		return result
	case rerr := <-runErr:
		// The stream died before the message was consumed — fail fast with the real cause, classified
		// terminal (a redelivery cannot fix a config Bento cannot run). The lingering handler goroutine
		// returns on its own when ctx expires (buffered channel; no leak). runErr is already drained.
		cancelRun()
		go reap(stream, nil)
		return fmt.Errorf("%w: stream failed: %v", ErrPublishConfig, rerr)
	}
}

// reap tears down an ephemeral stream off the caller's path. runErr, when non-nil, is the
// channel carrying Run's return (drained here so the Run goroutine exits); pass nil when it
// has already been received. Bounded by stopGrace and the output's connect_timeout.
func reap(stream *service.Stream, runErr <-chan error) {
	_ = stream.StopWithin(stopGrace)
	if runErr != nil {
		<-runErr
	}
}

// discardLogger implements service.LeveledLogger and drops every message, so the embedded
// Bento engine never writes a component's connection/config details to stdout.
type discardLogger struct{}

func (discardLogger) Error(string, ...any) {}
func (discardLogger) Warn(string, ...any)  {}
func (discardLogger) Info(string, ...any)  {}
func (discardLogger) Debug(string, ...any) {}
func (discardLogger) Trace(string, ...any) {}
