// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/fatih/color"
)

// escrowOutcome is what an upgrade found where this instance's second copy of its
// root key should be.
type escrowOutcome string

const (
	escrowVerified              escrowOutcome = "verified"
	escrowWritten               escrowOutcome = "written"
	escrowAbsent                escrowOutcome = "absent"
	escrowProtectsADifferentKey escrowOutcome = "protects a different key"
	escrowUnreadable            escrowOutcome = "unreadable"
	escrowNotApplicable         escrowOutcome = "not applicable"
)

// reconcileUpgradeEscrow checks that the instance's root-key escrow still protects
// the key the instance is running on, and writes one when there is none and a
// passphrase to protect it with.
//
// 🔴 THIS JOB LOST ITS HOME WHEN BOOTSTRAP BECAME A CREATE VERB, and it is not a
// small one. Two published behaviours depended on being able to re-run a bootstrap
// over a live instance: an instance first built with --no-escrow could gain an
// escrow later, and an existing artifact was checked against the running key on every
// run. Both are documented, in both locales. Withdrawing the re-run without moving
// them would have left an instance whose only copy of its root key is inside the
// cluster, with no supported way to make a second one — and losing that key makes
// every stored secret permanently unreadable, including from a database backup,
// because no DeviceChain backup contains etcd.
//
// 🔑 VERIFICATION IS FREE, AND THAT MAKES IT BETTER HERE THAN IT WAS. An artifact
// records a FINGERPRINT of the key it protects, so checking that it still matches
// needs no passphrase at all — only writing one does. So an upgrade verifies every
// time, without asking for anything, which is more often than the old re-run managed
// and with less ceremony.
//
// 🔴 IT WARNS AND DOES NOT FAIL. An escrow problem is about a future disaster; the
// upgrade in front of it is about the running instance. Blocking a security patch
// because a backup artifact is stale would be trading a certain problem for a
// hypothetical one — and an operator who cannot upgrade will find a way around this
// check rather than fix it.
// The outcome is RETURNED as well as printed, so a test can tell which of these
// states was reached. Asserting only on the file left behind cannot: WriteEscrow
// refuses to overwrite an artifact that is already there, so "the mismatched file was
// not replaced" stays true even if this branch stopped distinguishing a mismatch from
// an absence. Two enforcers of one property is good; a test that cannot say which one
// is holding is not.
func reconcileUpgradeEscrow(st *State, rootKeyBase64 string, opts UpgradeOptions) escrowOutcome {
	if rootKeyBase64 == "" {
		// An instance that uses no secret store has no root key to protect, and
		// saying "no escrow" about it would be reporting a gap that does not exist.
		return escrowNotApplicable
	}

	path, err := resolveEscrowPath(st.Instance, opts.EscrowFile)
	if err != nil {
		fmt.Println(color.YellowString("  Could not work out where this instance's escrow lives (%v)", err))
		return escrowUnreadable
	}

	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return writeMissingEscrow(st, rootKeyBase64, path, opts)
	case err != nil:
		fmt.Println(color.YellowString(
			"  Could not read the root-key escrow at %s (%v), so it has not been checked against\n"+
				"  the key this instance is running on.", path, err))
		return escrowUnreadable
	}

	art, err := escrow.Decode(raw)
	if err != nil {
		fmt.Println(color.YellowString(
			"  ⚠️  %s exists but is not a readable escrow artifact (%v). This instance's root key\n"+
				"     may have no usable second copy. Move the file aside and re-run with\n"+
				"     --escrow-passphrase-file to write a fresh one.", path, err))
		return escrowUnreadable
	}

	key, err := base64.StdEncoding.DecodeString(rootKeyBase64)
	if err != nil {
		fmt.Println(color.YellowString("  Could not read this instance's root key to check its escrow (%v)", err))
		return escrowUnreadable
	}
	defer zeroBytes(key)

	if !art.Matches(key) {
		// 🔴 THE WORST STATE THERE IS, AND IT IS SILENT WITHOUT THIS. An artifact that
		// looks like an escrow, is stored like an escrow, and protects a DIFFERENT key
		// — most often one belonging to an instance of the same name that was
		// destroyed and rebuilt. It is discovered during a restore, which is the one
		// moment it cannot be fixed.
		fmt.Println(color.YellowString(
			"  ⚠️  The escrow artifact at %s does NOT protect the root key this instance is\n"+
				"     running on. It probably belongs to an earlier instance of the same name.\n"+
				"     Restoring from it would recover a cluster that cannot read its own secrets.\n"+
				"     Move it aside and re-run with --escrow-passphrase-file to write the right one.",
			path))
		return escrowProtectsADifferentKey
	}
	fmt.Printf("  %s %s\n", color.WhiteString("Root-key escrow:"),
		color.GreenString("verified against the running key"))
	return escrowVerified
}

// writeMissingEscrow writes the second copy an instance does not have, when there is
// a passphrase to protect it with.
func writeMissingEscrow(st *State, rootKeyBase64, path string, opts UpgradeOptions) escrowOutcome {
	// 🔴 ONLY FROM A FILE OR THE ENVIRONMENT, NEVER A PROMPT. An upgrade is the verb
	// most likely to be run unattended, and a command that stops halfway through
	// moving a live instance to ask for a passphrase is a command that hangs a
	// pipeline mid-rollout. Bootstrap may prompt because it has not touched anything
	// yet; this one has.
	pass := escrowPassphraseWithoutAsking(opts.EscrowPassphraseFile)
	if pass == "" {
		fmt.Println(color.YellowString(
			"  ⚠️  This instance's root key has no escrow at %s, so the only copy of it is inside\n"+
				"     the cluster: losing the cluster would make every stored secret permanently\n"+
				"     unreadable, even from a database backup. Re-run with\n"+
				"     --escrow-passphrase-file <path> (or $%s) to write one.",
			path, EscrowPassphraseEnv))
		return escrowAbsent
	}

	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: pass}, rootKeyBase64,
		st.Instance, time.Now().UTC()); err != nil {
		fmt.Println(color.YellowString("  ⚠️  Could not write the root-key escrow: %v", err))
		return escrowAbsent
	}
	fmt.Printf("  %s %s\n", color.WhiteString("Root-key escrow:"),
		color.GreenString("written to "+path+" (this instance had none)"))
	return escrowWritten
}

// escrowPassphraseWithoutAsking reads the passphrase from a file or the environment,
// and returns "" when neither has one.
//
// 🔴 IT DOES NOT FALL BACK TO A PROMPT, AND THAT IS THE DIFFERENCE FROM THE BOOTSTRAP
// PATH. This runs at the END of an upgrade, after the operator, the configuration
// document and the release have all moved. Bootstrap may stop and ask because it has
// not touched anything yet; stopping here to ask for a secret would block a finished
// rollout on somebody noticing a prompt — and the thing being asked for is optional.
// An absent passphrase is reported and the upgrade completes.
func escrowPassphraseWithoutAsking(file string) string {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Println(color.YellowString("  Could not read the escrow passphrase file: %v", err))
			return ""
		}
		// Trailing newlines only, matching the bootstrap path: `echo secret > pass.txt`
		// is how this file gets made, and a passphrase carrying a "\n" would open
		// nothing when typed by hand later.
		return strings.TrimRight(string(raw), "\r\n")
	}
	if pass, ok := os.LookupEnv(EscrowPassphraseEnv); ok {
		// Unset immediately, for the reason the bootstrap path does: what follows
		// inherits this environment.
		os.Unsetenv(EscrowPassphraseEnv)
		return pass
	}
	return ""
}
