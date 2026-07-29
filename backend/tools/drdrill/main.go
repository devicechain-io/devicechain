// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command drdrill is the two halves of the ADR-028 root-key restore drill: it
// SEEDS a secret through the running platform's own API, and later PROVES that
// the same secret still decrypts after the instance's databases have been
// restored into a cluster that was built from an escrow artifact.
//
// # WHY IT EXISTS
//
// Everything the A5 escrow slice ships is a mechanism that CLAIMS a rebuilt
// cluster can read restored data. Nothing in it proves the claim: the escrow
// primitive's tests wrap and open in one process, and the dcctl tests never see
// a database. The failure this whole workstream exists to prevent —
// ciphertext rehydrated under a root key that no longer matches — is invisible
// at restore time and looks exactly like success.
//
// So the drill is built around one rule, borrowed from the A0 HA rig: a check is
// worth nothing until it has been shown to FAIL. Hence the exit-code taxonomy
// below. The negative control (same drill, escrow withheld) must fail at the
// DECRYPT — exit 3, and nothing else. A control that fails because the cluster
// did not come up, or because the dump did not restore, proves nothing at all,
// and would pass just as happily against a broken positive path.
//
// # WHAT IT PROVES, AND WHAT IT DOES NOT
//
// It proves: a secret written by the deployed platform, under the root key the
// deployed platform was given, is still readable after that cluster is gone —
// using only a database dump and the escrow artifact.
//
// It covers BOTH of an instance's databases. Its data sits on two separate
// servers: the relational one, which holds tenants, devices, rules, dashboards and
// every stored secret, and TimescaleDB, which holds event history.
// event-management is the only service that talks to the latter and it talks to
// nothing else, so there are no cross-writes between them — they are two backups
// and two restores. The root key gates the core half ONLY (event data carries no
// ciphertext), so restoring it is the operation the escrow exists to make possible
// and the one that had to be proved first; seed-events/verify-events are the other
// half, because "we can restore" is false as a claim if half of it has never been
// tried.
//
// The event half checks TimescaleDB's MACHINERY and not just its rows, because a
// hypertable can come back as a plain table holding every row, a continuous
// aggregate can come back as a definition with no materialization, the jobs that
// maintain both are themselves data, and an extension whose catalog rehydrated
// without its resource manager being loaded returns compressed chunks as nothing
// at all — silently, on a Cluster that reports healthy. See README.md.
//
// JetStream state and object storage remain undrilled.
//
// The seed half talks to the platform over GraphQL — the real write path,
// through the real ingress, into the service that holds the real KEK. The verify
// half reads the row back through core/secrets, the identical decrypt path the
// service uses, with the root key taken from the rebuilt cluster's own Secret.
// It deliberately does NOT go through the service for the read, because the
// platform has no read-back API for a secret by design (ADR-059: cleartext never
// crosses the API boundary) — and inventing one for a drill would be a worse
// trade than reading the store directly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Exit codes. These ARE the interface: the rig asserts on them, and the negative
// control is only meaningful because "could not decrypt" is distinguishable from
// "could not connect" and from "the row is not there".
const (
	exitOK = 0
	// exitSetup is every inconclusive outcome: bad flags, no database, no root
	// key, an API that would not answer. Never a verdict about the data.
	exitSetup = 1
	// exitDecryptFailed is THE negative control's expected result: the row was
	// found, and the root key in this cluster cannot open it.
	exitDecryptFailed = 3
	// exitNotFound is the row not being there at all — which usually means the
	// restore did not happen, so it is a broken drill rather than a finding.
	exitNotFound = 4
	// exitMismatch is CORRUPTION rather than a key problem, in either of its two
	// forms: the envelope was damaged and could not have opened under any key, or
	// it opened and the plaintext is not what was sealed. Both are worth shouting
	// about, and neither may be reported as a decrypt failure — a damaged envelope
	// masquerading as a wrong key would make the negative control hold for a
	// reason that has nothing to do with the escrow.
	exitMismatch = 5
	// exitTimescaleBroken is the event half's own verdict, and it is separate from
	// exitMismatch because it is a different KIND of answer. The rows came back
	// and are correct; what did not come back is TimescaleDB's own machinery —
	// the hypertable partitioning, a continuous aggregate's materialization, a
	// compressed chunk, or the background scheduler that maintains them.
	//
	// It gets its own code because the two demand opposite responses. Corrupt data
	// means restore again from an earlier point. This means the data is fine and
	// the cluster is not, which is a platform bug and is invisible to every
	// row-counting check — a plain table full of the right rows reads as a pass.
	exitTimescaleBroken = 6
)

// exitError carries an exit code alongside the message, so each command returns a
// verdict rather than calling os.Exit from wherever it happened to notice.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func failWith(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// codeOf maps an error to the process exit code. Anything not carrying an
// explicit verdict is a setup failure — the conservative reading, since an
// unclassified error is by definition not evidence about the data.
func codeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitSetup
}

const usage = `drdrill — the ADR-028 root-key restore drill

  drdrill seed          [flags]   write a secret through the platform API, record a receipt
  drdrill verify        [flags]   prove that secret still decrypts, from a receipt
  drdrill seed-events   [flags]   write telemetry into the event store, record it on the receipt
  drdrill verify-events [flags]   prove that telemetry, and TimescaleDB's own machinery, survived
  drdrill decoy         [flags]   mint an escrow artifact holding the WRONG key (the control)
  drdrill codes                   print the exit-code taxonomy as shell assignments

Exit codes: 0 ok · 1 setup/inconclusive · 3 DECRYPT FAILED · 4 not found · 5 corrupt
                                          6 RESTORED BUT TIMESCALE MACHINERY BROKEN
`

// printExitCodes emits the taxonomy as shell assignments for `eval`.
//
// The codes are declared to be the interface between this tool and the rig, and
// an interface duplicated as bare literals in a shell script is not one: renaming
// exitDecryptFailed to 6 would leave every Go test passing, every build clean,
// and the negative control reporting INCONCLUSIVE forever — silently, since a
// control that never holds looks like an environment problem. Sourcing them makes
// the coupling real.
func printExitCodes() {
	fmt.Printf("DRDRILL_EXIT_OK=%d\n", exitOK)
	fmt.Printf("DRDRILL_EXIT_SETUP=%d\n", exitSetup)
	fmt.Printf("DRDRILL_EXIT_DECRYPT_FAILED=%d\n", exitDecryptFailed)
	fmt.Printf("DRDRILL_EXIT_NOT_FOUND=%d\n", exitNotFound)
	fmt.Printf("DRDRILL_EXIT_CORRUPT=%d\n", exitMismatch)
	fmt.Printf("DRDRILL_EXIT_TIMESCALE_BROKEN=%d\n", exitTimescaleBroken)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitSetup)
	}

	var err error
	switch os.Args[1] {
	case "seed":
		err = runSeed(ctx, os.Args[2:])
	case "seed-events":
		err = runSeedEvents(ctx, os.Args[2:])
	case "verify-events":
		err = runVerifyEvents(ctx, os.Args[2:])
	case "verify":
		err = runVerify(ctx, os.Args[2:])
	case "decoy":
		err = runDecoy(os.Args[2:])
	case "codes":
		printExitCodes()
		return
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(exitSetup)
	}

	if err != nil {
		code := codeOf(err)
		fmt.Fprintf(os.Stderr, "\ndrdrill %s: %v\n", os.Args[1], err)
		os.Exit(code)
	}
}

// flagSetFor builds a FlagSet that prints the subcommand's usage on a parse
// error rather than the package-level one.
func flagSetFor(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("drdrill "+name, flag.ContinueOnError)
	return fs
}
