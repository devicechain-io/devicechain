// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
)

// runDecoy mints a well-formed escrow artifact holding a root key that is NOT the
// instance's — the SYSTEM-level negative control for the restore drill.
//
// # WHY THIS EXISTS AT ALL
//
// The drill's original control was "rebuild with --no-escrow and restore the same
// backup", and it can no longer be expressed: dcctl refuses --restore-rdb-from
// without --restore-root-key, precisely because that combination is how an
// operator permanently loses every secret while being told the restore succeeded.
// That guard is right, and it takes the old control with it.
//
// So the control becomes: restore the SAME archive under a DIFFERENT key. The
// rows come back, the envelope is intact, and the key does not fit — drdrill's
// exit 3, distinguishable from "nothing restored" (4) and from "could not run" (1).
//
// Producing that artifact through the shipped CLI would cost a whole extra
// bootstrap: there is no `dcctl secrets escrow create`, only `show` and `verify`,
// so the only supported way to obtain an artifact is to bootstrap an instance and
// let it escrow the key it minted. And because Wrap binds the instance name into
// the authenticated header, and bootstrap refuses an artifact whose instance does
// not match, that throwaway instance would have to carry the drill's own name —
// which means a second cluster, torn down again before the real control could run.
// Three bootstraps for one control is enough friction that the control does not
// get run, and a control that does not get run is not a control.
//
// # WHAT THIS PROVES, AND WHAT IT DOES NOT
//
// The artifact is produced by escrow.Wrap — the same function dcctl calls, with
// the same KDF parameters and the same passphrase — so it is not a stub or a
// fixture. It is consumed by the same --restore-root-key path a real artifact
// takes, and bootstrap has no way to tell the two apart, which is the point.
//
// What it does NOT stand in for: it says nothing about whether two real
// bootstraps mint different keys from each other. That is a separate claim about
// the key generator, tested where the key generator lives. The claim under test
// here is narrower and is the one that matters in an incident — given the right
// archive and the wrong key, does the platform hand over the plaintext?
//
// The rig asserts the premise both ways before reading the verdict: the control
// instance must verify AGAINST the decoy and must FAIL to verify against the real
// artifact. Without both, a decoy that was silently ignored would look exactly
// like a control that held.
func runDecoy(args []string) error {
	fs := flagSetFor("decoy")
	instance := fs.String("instance", "", "instance name to bind into the artifact; must match the instance being restored")
	out := fs.String("out", "", "path to write the artifact to; refused if it already exists")
	if err := fs.Parse(args); err != nil {
		return failWith(exitSetup, "parse flags: %w", err)
	}
	if *instance == "" {
		return failWith(exitSetup, "--instance is required; escrow.Wrap authenticates the instance name and bootstrap refuses an artifact that names a different one")
	}
	if *out == "" {
		return failWith(exitSetup, "--out is required")
	}

	// Same variable dcctl reads, so the rig sets it once. LookupEnv rather than
	// Getenv: an empty passphrase would produce a file that is encrypted in form
	// and public in substance, and escrow.Wrap refuses it — but saying so here
	// names the missing variable instead of reporting a crypto precondition.
	passphrase, ok := os.LookupEnv("DCCTL_ESCROW_PASSPHRASE")
	if !ok || passphrase == "" {
		return failWith(exitSetup, "DCCTL_ESCROW_PASSPHRASE is unset or empty; the decoy has to be openable by the same bootstrap that opens the real artifact")
	}

	key := make([]byte, escrow.RootKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return failWith(exitSetup, "generate a decoy root key: %w", err)
	}

	art, err := escrow.Wrap(key, passphrase, *instance, escrow.DefaultKDFParams(), time.Now())
	if err != nil {
		return failWith(exitSetup, "wrap the decoy root key: %w", err)
	}
	raw, err := art.Encode()
	if err != nil {
		return failWith(exitSetup, "encode the decoy artifact: %w", err)
	}

	// O_EXCL, and 0600. Overwriting is the one mistake with no recovery: point
	// --out at the drill's real escrow file and the key that opens the archive is
	// gone, taking the positive result with it. Refuse rather than clobber.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return failWith(exitSetup, "open %s for writing: %w", *out, err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return failWith(exitSetup, "write %s: %w", *out, err)
	}
	if err := f.Close(); err != nil {
		return failWith(exitSetup, "close %s: %w", *out, err)
	}

	fmt.Printf("ok   decoy escrow artifact for instance %q written to %s\n", *instance, *out)
	fmt.Printf("     key id: %s\n", art.Fingerprint)
	fmt.Printf("     This is NOT the instance's key. Restoring with it MUST fail at the decrypt.\n")
	return nil
}
