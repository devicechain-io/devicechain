// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/fatih/color"
	"golang.org/x/term"
	"k8s.io/client-go/kubernetes"
)

// The instance secret-store root key (ADR-059) wraps every per-secret DEK, and
// bootstrap mints it in memory and hands it to Helm. Its only durable copy is then
// the instance K8s Secret — which lives in etcd, and etcd is in NO backup this
// platform takes: CNPG archives Postgres WAL and the Timescale backup covers
// Timescale. Neither contains a byte of the key.
//
// The failure that follows is the reason this file exists, and it is invisible
// until the worst possible moment. Restoring a database backup IN PLACE works
// perfectly, because etcd still holds the key — which is exactly the drill an
// operator is most likely to have run. Restoring the same backup to a FRESH
// cluster rehydrates ciphertext that nothing alive can decrypt, reports no error
// while doing it, and only surfaces when a service tries to read a stored
// credential. There is no recovery from that point: the key is not derivable, and
// the wrapped DEKs are not brute-forceable.
//
// So escrow is ON BY DEFAULT and must be opted OUT of. An opt-IN escrow would
// reproduce the exact gap it exists to close — an operator who does not know about
// this failure mode is precisely the operator who would not pass the flag. The cost
// of that choice is that a non-interactive bootstrap with no passphrase now FAILS
// rather than quietly producing an unrecoverable instance; --no-escrow is the
// one-word answer for a throwaway, and --dev implies it.
const (
	// EscrowPassphraseEnv supplies the passphrase to an automated bootstrap that has
	// no terminal to prompt at.
	EscrowPassphraseEnv = "DCCTL_ESCROW_PASSPHRASE"

	// EscrowFileExt is the artifact suffix. destroy keys on it to decide what to
	// spare, so it is a constant rather than a literal in two places.
	EscrowFileExt = ".escrow"

	// escrowFileMode keeps the artifact readable only by its owner. It is not the
	// protection — the passphrase is — but a key file that is world-readable on a
	// shared box gives an attacker unlimited offline guesses at that passphrase.
	escrowFileMode = 0o600
	escrowDirMode  = 0o700
)

// EscrowFlags is the raw operator input, before any of it has been reconciled.
type EscrowFlags struct {
	// File overrides where the artifact is written (default: DefaultEscrowPath).
	File string
	// PassphraseFile reads the passphrase from a file instead of the environment
	// or a prompt.
	PassphraseFile string
	// NoEscrow opts out entirely. See the note above for why this is the flag
	// rather than its inverse.
	NoEscrow bool
	// RestoreFile seeds the instance's root key FROM an existing artifact instead
	// of minting a fresh one — the disaster-recovery direction.
	RestoreFile string
}

// EscrowPlan is the settled decision: where the root key comes from and where its
// second copy goes. Resolving it is separated from acting on it so the whole thing
// can be decided BEFORE a cluster exists — a missing passphrase must fail in the
// first second, not as a prompt behind ten minutes of cluster spin-up, and a
// restore artifact that cannot be opened must fail before anything has been built
// around a key it was supposed to supply.
type EscrowPlan struct {
	// Path is where the artifact will be written. EMPTY MEANS NOTHING IS WRITTEN,
	// and it is the only thing that means that — there is no second "skip" field to
	// disagree with it. That also makes the zero EscrowPlan inert, which matters
	// because State is constructed in a dozen tests that have no opinion here.
	Path string
	// Passphrase protects the artifact at Path.
	Passphrase string
	// RestoredRootKey is the base64 root key recovered from RestoreFile, in the
	// form the instance config consumes. Empty unless restoring.
	RestoredRootKey string
	// RestoredFrom is the artifact the key came from. It distinguishes the two
	// reasons Path can be empty: a restore (the artifact exists, it is the input)
	// from --no-escrow (there is no artifact at all, and the report says so loudly).
	RestoredFrom string
}

// promptPassphrase and stdinIsTerminal are the two terminal seams, indirected so
// tests can drive either branch.
//
// stdinIsTerminal is indirected for a sharper reason than testability. `go test`
// hands the test binary the developer's own stdin, so a test asserting "there is no
// terminal, therefore fail" would find a real terminal when run from a shell and
// BLOCK on a password prompt — passing in CI and hanging on the desk of the person
// who wrote it. A check that can silently become a prompt is not a check.
var (
	promptPassphrase = terminalPassphrase
	stdinIsTerminal  = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// DefaultEscrowPath returns where an instance's artifact is written by default:
// ~/.devicechain/escrow/<instance>-rootkey.escrow.
//
// Deliberately NOT ~/.devicechain/<instance>/, which is where every other piece of
// per-instance state lives, because `dcctl destroy` removes that directory whole.
// An escrow file there would be deleted by the command whose entire premise is that
// the cluster is expendable — taking with it the only thing that could still make
// sense of the database backup the operator kept.
func DefaultEscrowPath(instance string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devicechain", "escrow", instance+"-rootkey"+EscrowFileExt), nil
}

// ResolveEscrowPlan reconciles the flags into a plan, reading and opening a restore
// artifact or acquiring a passphrase as required. It touches nothing on disk that
// it does not have to.
func ResolveEscrowPlan(instance string, f EscrowFlags) (EscrowPlan, error) {
	if f.RestoreFile != "" {
		return resolveRestore(instance, f)
	}
	if f.NoEscrow {
		return EscrowPlan{}, nil
	}

	path, err := resolveEscrowPath(instance, f.File)
	if err != nil {
		return EscrowPlan{}, err
	}
	pass, err := resolveEscrowPassphrase(f.PassphraseFile, true, fmt.Sprintf(
		"Passphrase to protect the root-key escrow for instance %q", instance))
	if err != nil {
		return EscrowPlan{}, err
	}
	return EscrowPlan{Path: path, Passphrase: pass}, nil
}

// resolveRestore opens the supplied artifact and returns its key as the plan's
// input. It rejects flag combinations that ask for two different things at once
// rather than silently honouring one of them.
func resolveRestore(instance string, f EscrowFlags) (EscrowPlan, error) {
	if f.File != "" {
		return EscrowPlan{}, fmt.Errorf(
			"--restore-root-key reads an existing escrow artifact and --escrow-file names a new one to write; " +
				"pick one (a restored instance keeps the artifact it was restored from)")
	}
	raw, err := os.ReadFile(f.RestoreFile)
	if err != nil {
		return EscrowPlan{}, fmt.Errorf("reading the escrow artifact: %w", err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		return EscrowPlan{}, fmt.Errorf("%s: %w", f.RestoreFile, err)
	}
	// A mismatch is not fatal — an operator may deliberately restore an instance
	// under a new name — but it is very often the mistake of reaching for the wrong
	// file, and it is the only chance to say so before the key is in use.
	if art.Instance != instance {
		fmt.Println(color.YellowString(
			"  note: %s was escrowed for instance %q and this bootstrap is %q; continuing, but check this is the artifact you meant",
			f.RestoreFile, art.Instance, instance))
	}
	pass, err := resolveEscrowPassphrase(f.PassphraseFile, false, fmt.Sprintf(
		"Passphrase for the root-key escrow %s", f.RestoreFile))
	if err != nil {
		return EscrowPlan{}, err
	}
	key, err := escrow.Open(art, pass)
	if err != nil {
		return EscrowPlan{}, fmt.Errorf("%s: %w", f.RestoreFile, err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	zeroBytes(key)
	return EscrowPlan{
		RestoredRootKey: encoded,
		RestoredFrom:    f.RestoreFile,
	}, nil
}

// resolveEscrowPath settles the destination and refuses one that `dcctl destroy`
// would delete.
//
// Refused rather than warned: an artifact inside the instance's own state
// directory is never what anyone intends, the fix is one path, and a warning
// printed during a long bootstrap is a warning nobody reads until they need the
// file that is no longer there.
func resolveEscrowPath(instance, explicit string) (string, error) {
	var path string
	if explicit != "" {
		path = explicit
	} else {
		def, err := DefaultEscrowPath(instance)
		if err != nil {
			return "", fmt.Errorf("resolving the default escrow path: %w", err)
		}
		path = def
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	root, err := instanceRoot(instance)
	if err != nil {
		// No home directory is a real problem for an explicit path too, but not this
		// check's problem: an explicit absolute path is still perfectly usable.
		return abs, nil
	}
	within, err := pathIsWithin(abs, root)
	if err != nil {
		return "", err
	}
	if within {
		return "", fmt.Errorf(
			"the escrow path %s is inside %s, which `dcctl destroy` deletes along with the cluster — "+
				"the artifact would be removed by the one command that assumes the cluster is expendable. "+
				"Write it somewhere else (the default is %s%c)",
			abs, root, filepath.Dir(mustDefaultEscrowPath(instance)), filepath.Separator)
	}
	return abs, nil
}

// mustDefaultEscrowPath is only used to name the default in an error message, so a
// missing home directory degrades to a less specific message rather than replacing
// the real error with a different one.
func mustDefaultEscrowPath(instance string) string {
	if p, err := DefaultEscrowPath(instance); err == nil {
		return p
	}
	return filepath.Join(".devicechain", "escrow", instance+"-rootkey"+EscrowFileExt)
}

// pathIsWithin reports whether path is dir or lives beneath it. Both are cleaned
// and made absolute first, so "~/.devicechain/inst/../escrow" is judged on where it
// actually lands rather than on how it was spelled.
func pathIsWithin(path, dir string) (bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		// Different volumes on Windows; not beneath by definition.
		return false, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// resolveEscrowPassphrase acquires the passphrase from the first source that is
// present: a file, the environment, then an interactive prompt. confirm asks twice
// (a mistyped passphrase on the WRITE side is discovered years later, by an
// operator who no longer has any other copy of the key).
func resolveEscrowPassphrase(file string, confirm bool, purpose string) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the escrow passphrase file: %w", err)
		}
		// Trailing newlines only: `echo secret > pass.txt` is the normal way this file
		// gets made, and a passphrase that silently carries a "\n" would open nothing
		// when typed by hand later. Interior and leading whitespace is left alone —
		// it may well be part of the passphrase.
		pass := strings.TrimRight(string(raw), "\r\n")
		if pass == "" {
			return "", fmt.Errorf("the escrow passphrase file %s is empty", file)
		}
		return pass, nil
	}
	// LookupEnv, not Getenv: an empty DCCTL_ESCROW_PASSPHRASE is a broken pipeline
	// (an unset CI secret expands to ""), not a request for an empty passphrase, and
	// falling through to a prompt would hang a job instead of failing it.
	if pass, ok := os.LookupEnv(EscrowPassphraseEnv); ok {
		if pass == "" {
			return "", fmt.Errorf("%s is set but empty; unset it to use another source, or give it a value", EscrowPassphraseEnv)
		}
		return pass, nil
	}
	if !stdinIsTerminal() {
		return "", fmt.Errorf(
			"no escrow passphrase and no terminal to ask at.\n"+
				"  Supply one with --escrow-passphrase-file <path> or %s,\n"+
				"  or pass --no-escrow to bootstrap an instance whose root key has no second copy\n"+
				"  (a throwaway; losing the cluster then loses every stored secret permanently).",
			EscrowPassphraseEnv)
	}
	return promptPassphrase(purpose, confirm)
}

// terminalPassphrase reads a passphrase from the terminal without echoing it.
func terminalPassphrase(purpose string, confirm bool) (string, error) {
	fmt.Printf("%s\n", color.WhiteString(purpose))
	first, err := readHidden("  Passphrase: ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", fmt.Errorf("the escrow passphrase is empty; the artifact would be readable by anyone holding the file")
	}
	if !confirm {
		return first, nil
	}
	second, err := readHidden("  Confirm:    ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("the two passphrases do not match")
	}
	return first, nil
}

// readHidden prompts and reads one line with echo off.
func readHidden(prompt string) (string, error) {
	fmt.Print(prompt)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("reading the passphrase: %w", err)
	}
	return string(raw), nil
}

// WriteEscrow wraps the instance root key under the plan's passphrase and writes
// the artifact.
//
// O_EXCL, so an existing file is an error rather than an overwrite. The path is
// derived from the instance name, so re-bootstrapping an instance of the same name
// lands on the same file — and silently replacing it would destroy the escrow of
// the instance whose database backups the operator still has.
func WriteEscrow(plan EscrowPlan, rootKeyBase64, instance string, now time.Time) error {
	key, err := base64.StdEncoding.DecodeString(rootKeyBase64)
	if err != nil {
		return fmt.Errorf("decoding the instance root key: %w", err)
	}
	defer zeroBytes(key)

	art, err := escrow.Wrap(key, plan.Passphrase, instance, escrow.DefaultKDFParams(), now)
	if err != nil {
		return err
	}
	encoded, err := art.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plan.Path), escrowDirMode); err != nil {
		return fmt.Errorf("creating the escrow directory: %w", err)
	}
	f, err := os.OpenFile(plan.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, escrowFileMode)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf(
				"an escrow artifact already exists at %s. It belongs to an earlier instance of this name and "+
					"is the only copy of ITS root key, so this will not overwrite it. Move it aside if that "+
					"instance is truly gone, or pass --escrow-file to write elsewhere", plan.Path)
		}
		return fmt.Errorf("creating the escrow artifact: %w", err)
	}
	if _, err := f.Write(encoded); err != nil {
		f.Close()
		return fmt.Errorf("writing the escrow artifact: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing the escrow artifact: %w", err)
	}
	return nil
}

// DeployedRootKey reads the instance root key out of the config Secret the pods
// actually mount — the same document deployedInstanceConfig reads for the HA check,
// and for the same reason: what matters is the key the instance is RUNNING, not the
// one dcctl believes it installed.
func DeployedRootKey(ctx context.Context, kubeContext, instanceId string) ([]byte, error) {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("building kube config: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	cfg, err := deployedInstanceConfig(ctx, typed, instanceId)
	if err != nil {
		return nil, err
	}
	key, err := cfg.Infrastructure.Secrets.DecodedRootKey()
	if err != nil {
		return nil, fmt.Errorf("instance %q: %w", instanceId, err)
	}
	return key, nil
}

// removeStatePreservingEscrow removes dir the way destroy wants it removed —
// completely — EXCEPT for any *.escrow artifact beneath it, which it leaves in
// place along with the directories needed to hold them. It returns what it kept.
//
// resolveEscrowPath refuses to WRITE an artifact here, so nothing dcctl produces
// should land under this directory in the first place. This is the backstop for the
// two cases that check cannot see: a file an operator put here by hand because it
// seemed like the natural place, and one written by an earlier dcctl that had no
// opinion. Both are exactly the cases where the operator does not know the file is
// about to be deleted.
//
// The asymmetry is deliberate. Sparing a stale escrow file costs a stray file on
// disk. Deleting a live one costs every secret in a database backup that is still
// sitting in object storage, silently, with no error at the time and no way back.
func removeStatePreservingEscrow(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var kept []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			sub, err := removeStatePreservingEscrow(p)
			if err != nil {
				return kept, err
			}
			kept = append(kept, sub...)
		case strings.HasSuffix(e.Name(), EscrowFileExt):
			kept = append(kept, p)
		default:
			if err := os.Remove(p); err != nil {
				return kept, err
			}
		}
	}
	// Only collapse the directory itself once nothing under it survived — including
	// anything a nested call spared, which is why this reads the accumulated list
	// rather than this level's entries.
	if len(kept) == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

// zeroBytes clears a key buffer once it is no longer needed.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
