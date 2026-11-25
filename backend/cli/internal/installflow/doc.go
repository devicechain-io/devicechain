// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package installflow is the event core shared by every presenter of a dcctl
// install: the plain output of today, the terminal UI and, later, an NDJSON
// stream. It holds no install logic. It defines the contract between the
// engine, which owns execution and the facts, and the presenters, which only
// render.
//
// The contract is the event stream, schema dcctl.install.events/v1:
//
//   - an Envelope around one sealed Payload. Payload has unexported methods, so
//     only the concrete types declared here can travel; nothing marshals an
//     options struct, an error, a values map or a provider reply.
//   - a Hub, the one serialisation point. It assigns seq at delivery, keeps a
//     safe projection and a single sanitised log ring, coalesces measured
//     progress, and never lets a slow presenter stall the engine beyond a
//     bounded grace: logs and progress are bounded and lossy, control events
//     are bounded and never silently lost, and a control event that cannot be
//     delivered requests a graceful cancel and turns the run non-zero.
//   - exactly one run.summary, always last. Execute guarantees it even when an
//     engine returns without one or panics.
//   - a PromptBroker, the single owner of interactive input while a run is
//     live. A resolution event carries its disposition only, never the answer
//     or its length.
//   - safe value types. SafeText is sanitised on construction (terminal
//     escapes and controls stripped, length capped); Secret and Redacted print
//     as placeholders; and every text field is passed through known-value
//     redaction at the hub as defence in depth behind producer-side
//     allowlisting.
//
// This package imports the standard library only. Package testkit holds a
// scripted engine, a manual clock, a capture sink and the secret-canary
// harness that every presenter's tests reuse.
package installflow
