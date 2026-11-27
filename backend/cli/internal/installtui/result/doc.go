// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package result draws how a run ended: the failure screen, the cluster success
// screen, their help overlay, and the short exit summary left on stdout once the
// terminal is restored. It also settles what the command returns afterwards.
//
// Every screen is built from the run's Summary and nothing else that could have
// changed: no screen says success unless the summary does, and leaving a screen
// never changes dcctl's exit status. The engine's own error is returned to the
// command unchanged, so it prints and exits with what it always has; on screen it
// appears only after the hub's redaction, and in the exit summary never.
//
// A screen never quits the program. It reports what the operator chose with a
// Done message, and the program's router decides what happens next.
package result
