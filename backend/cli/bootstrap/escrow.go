// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/fatih/color"
	"golang.org/x/term"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// DryRun suppresses passphrase acquisition. A dry run writes nothing, so it must
	// not demand a secret — the first version did, which meant any script running
	// `bootstrap --dry-run` as a validation step failed, and an operator at a
	// terminal was prompted (twice) for the real escrow passphrase by a run that
	// discarded it. Training someone to type that into a no-op is its own harm.
	DryRun bool
}

// Validate rejects flag combinations that ask for two different things, BEFORE any
// of them is acted on.
//
// The escrow flags used to be silently discarded whenever escrow was off — including
// by --dev, which sets --no-escrow implicitly. So `--dev --escrow-file ~/keep.escrow`
// wrote nothing and said nothing, and even a --escrow-passphrase-file pointing at a
// nonexistent path was never opened. That directly contradicts how every other --dev
// interaction works: the preset REFUSES a contradictory flag rather than masking it,
// precisely so it can never hide a mistake.
func (f EscrowFlags) Validate() error {
	if f.NoEscrow && f.RestoreFile != "" {
		return fmt.Errorf("--no-escrow and --restore-root-key contradict each other: one says this " +
			"instance's key needs no second copy, the other recovers it from one. Drop whichever you did not mean")
	}
	if f.NoEscrow && f.File != "" {
		return fmt.Errorf("--escrow-file names where to write an escrow artifact and --no-escrow says not to " +
			"write one. Drop one of them (note --dev implies --no-escrow, so pass --no-escrow=false to keep the escrow)")
	}
	if f.NoEscrow && f.PassphraseFile != "" {
		return fmt.Errorf("--escrow-passphrase-file supplies a passphrase for an artifact that --no-escrow " +
			"prevents being written. Drop one of them (note --dev implies --no-escrow)")
	}
	if f.RestoreFile != "" && f.File != "" {
		return fmt.Errorf("--restore-root-key reads an existing escrow artifact and --escrow-file names a new one " +
			"to write; pick one (a restored instance keeps the artifact it was restored from)")
	}
	return nil
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

	// lookupDeployedInstance is the seam stepRenderConfig uses to ask what the
	// instance is ALREADY running. Indirected because the branch behind it is the one
	// that decides between "upgrade this instance" and "rotate every credential out
	// from under it", and that decision must be testable without standing up a cluster.
	lookupDeployedInstance = DeployedInstanceConfig

	// encodeArtifact is the seam that makes the post-write verification testable.
	//
	// That check exists for a case nothing here can produce on purpose: an artifact
	// that encodes without error and does not open. It guards against a defect in the
	// format itself — and the format has had exactly that defect, where a header's
	// bytes did not survive the round trip. A guard whose failure case cannot be
	// reached is a guard no test can hold, so the test reaches it through here.
	encodeArtifact = func(a *escrow.Artifact) ([]byte, error) { return a.Encode() }
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
	if err := f.Validate(); err != nil {
		return EscrowPlan{}, err
	}
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
	if f.DryRun {
		// The path is resolved so the plan can be reported; the passphrase is not,
		// because nothing will be written with it. See EscrowFlags.DryRun.
		return EscrowPlan{Path: path}, nil
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
	if f.DryRun {
		// Reported, not opened: a dry run writes nothing and must not ask for a
		// secret. The artifact parsed, which is what a dry run can honestly check.
		return EscrowPlan{RestoredFrom: f.RestoreFile}, nil
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
	// Symlinks are resolved on both sides, so the check is about where the file
	// LANDS rather than how the path is spelled. Without this, --escrow-file
	// /some/link/key.escrow whose target directory is the instance state root was
	// accepted, and the guard described as a refusal was only advisory.
	//
	// Resolved leniently: a path that does not exist yet cannot be resolved, and the
	// escrow file by definition does not exist yet, so each side falls back to its
	// lexical form. Resolving the PARENT is what does the real work here.
	absPath = filepath.Join(resolveSymlinks(filepath.Dir(absPath)), filepath.Base(absPath))
	absDir = resolveSymlinks(absDir)
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
		// Unset immediately. Bootstrap goes on to exec docker, kind, ko and tofu, all
		// of which inherit the environment, and tofu dumps its whole environment under
		// TF_LOG=trace. The passphrase has been read; there is no reason for it to stay
		// reachable by every child process for the next ten minutes.
		os.Unsetenv(EscrowPassphraseEnv)
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
	encoded, err := encodeArtifact(art)
	if err != nil {
		return err
	}
	dir := filepath.Dir(plan.Path)
	if err := os.MkdirAll(dir, escrowDirMode); err != nil {
		return fmt.Errorf("creating the escrow directory: %w", err)
	}

	// Refuse an existing artifact — but say something TRUE about it first.
	//
	// The refusal used to assert that any bytes at this path were "the only copy of
	// ITS root key", which is false for the most common way to get here: a bootstrap
	// that failed after step 1 leaves an artifact for a cluster that was never built,
	// and the corrected retry then hits a message sending the operator to hunt for an
	// instance that never existed. A zero-length stub from a failed write triggered
	// the same speech while dcctl's own reader called the file unreadable.
	if existing, err := os.ReadFile(plan.Path); err == nil {
		return fmt.Errorf("%s", describeBlockingArtifact(plan.Path, existing))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking for an existing escrow artifact: %w", err)
	}

	// Written to a temporary file and renamed, so the artifact is either absent or
	// complete and never a truncated stub that blocks its own replacement forever. It
	// is fsynced because the whole point of the file is to survive the machine, and
	// bootstrap goes on to spend ten minutes building a cluster around a key whose
	// only other copy would otherwise be an unflushed page.
	tmp, err := os.CreateTemp(dir, ".escrow-*.tmp")
	if err != nil {
		return fmt.Errorf("creating the escrow artifact: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if err := tmp.Chmod(escrowFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("securing the escrow artifact: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("writing the escrow artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flushing the escrow artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing the escrow artifact: %w", err)
	}

	// Prove the file opens BEFORE publishing it, so a failed check leaves nothing
	// behind. Verifying after the rename would put an artifact that does not work at
	// the destination, where the overwrite guard would then refuse every retry.
	//
	// This runs at all because everything else about this artifact is checked on the
	// day it is needed, which is the day nothing can be done — and the one defect
	// that mattered most (a header whose bytes did not survive a round trip) was
	// invisible to an in-memory check and visible to exactly this one.
	if err := verifyWrittenArtifact(tmpName, plan.Passphrase, rootKeyBase64); err != nil {
		return err
	}

	if err := os.Rename(tmpName, plan.Path); err != nil {
		return fmt.Errorf("publishing the escrow artifact: %w", err)
	}
	syncDir(dir)
	return nil
}

// describeBlockingArtifact explains what is already at path, distinguishing a real
// escrow from a stub or a stray file, and names the action that fits each.
func describeBlockingArtifact(path string, existing []byte) string {
	art, err := escrow.Decode(existing)
	if err != nil {
		return fmt.Sprintf("%s already exists but is not a readable escrow artifact (%v). "+
			"It is most likely left over from an interrupted bootstrap. Move it aside and re-run",
			path, err)
	}
	return fmt.Sprintf("an escrow artifact for instance %q already exists at %s (key id %s), and this "+
		"will not overwrite it.\n"+
		"  If that is the key you want this instance to use — for example this is a retry after a failed "+
		"bootstrap — re-run with --restore-root-key %s.\n"+
		"  If it belongs to an instance that is truly gone, move it aside. `dcctl secrets escrow show %s` "+
		"will tell you what it is before you decide",
		art.Instance, path, art.Fingerprint, path, path)
}

// verifyWrittenArtifact re-reads the artifact from disk and opens it.
func verifyWrittenArtifact(path, passphrase, rootKeyBase64 string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-reading the escrow artifact to verify it: %w", err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		return fmt.Errorf("the escrow artifact just written does not parse (%w); refusing to build an "+
			"instance whose key has no working second copy", err)
	}
	key, err := escrow.Open(art, passphrase)
	if err != nil {
		return fmt.Errorf("the escrow artifact just written does not open with the passphrase it was "+
			"written with (%w); refusing to build an instance whose key has no working second copy", err)
	}
	defer zeroBytes(key)
	if base64.StdEncoding.EncodeToString(key) != rootKeyBase64 {
		return fmt.Errorf("the escrow artifact just written returns a different key than the one being " +
			"deployed; refusing to build an instance whose escrow would open nothing")
	}
	return nil
}

// syncDir flushes a directory entry so a freshly renamed file survives a power loss.
// Best-effort: a filesystem that refuses this (or a platform without it) is not a
// reason to fail a bootstrap that has otherwise succeeded.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// DeployedInstanceConfig returns the configuration the named instance is ALREADY
// running, or nil if no such instance exists in this cluster.
//
// This is what stops `dcctl bootstrap` wrecking an instance it is only meant to
// update. stepRenderConfig used to mint every generated credential unconditionally,
// and helmInstall upgrades an existing release with them, so a re-run replaced the
// live instance's secrets with fresh ones — on a pipeline that documents itself as
// idempotent and that the docs tell operators to re-run (e.g. to change --host).
// Two flavours of damage came out of that, and the second is why this returns the
// whole document rather than one field:
//
//   - the secret-store root key was the KEK wrapping every stored secret, so
//     rewriting it made all of them permanently undecryptable — silently, on a run
//     that reported success.
//   - the broker-auth material and the service-auth secret live on BOTH sides of a
//     boundary that updates at different times (the NATS ConfigMap reloads when the
//     kubelet gets round to projecting it; the services roll when Helm rolls them),
//     so rotating them opens a window where one side rejects the other. That window
//     is what fails a bootstrap re-run with `Authorization Violation`.
//
// It FAILS CLOSED. Only a NotFound — the instance genuinely is not there — is read
// as "fresh install, mint everything". Any other error means we could not tell, and
// minting on a "could not tell" is exactly the destructive branch. EnsureCluster has
// already run by this point, so the API being unreachable here is an anomaly worth
// stopping for rather than guessing past.
func DeployedInstanceConfig(ctx context.Context, kubeContext, instanceId string) (*config.InstanceConfiguration, error) {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("building kube config to check for an existing instance: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("dci-%s-config", instanceId)
	sec, err := typed.CoreV1().Secrets(instanceId).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checking whether instance %q already exists (Secret %s/%s): %w. "+
			"Refusing to continue: if the instance IS there, minting fresh credentials would rotate them "+
			"out from under it — making every stored secret unreadable and breaking broker auth for every "+
			"pod that starts afterwards", instanceId, instanceId, name, err)
	}
	return parseDeployedConfig(sec.Data["instance"], instanceId, name)
}

// parseDeployedConfig turns the config Secret's payload into the instance's
// configuration. Split out so the two ways it can refuse are testable without a
// cluster — they are the ways this whole lookup can be wrong about a LIVE instance,
// which is the only direction that costs anything.
//
// The Secret being there at all means the instance is there, so an empty or
// undecodable payload is a MALFORMED instance, not an absent one. Reading it as
// absent would take the mint branch against something live. (It used to do exactly
// that for an absent key; the blast radius was one key and is now every credential.)
func parseDeployedConfig(raw []byte, instanceId, secretName string) (*config.InstanceConfiguration, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("instance %q has a configuration Secret (%s/%s) with no \"instance\" payload, "+
			"so what it is running cannot be determined. Refusing to continue rather than treat it as a "+
			"fresh install, which would rotate every credential out from under it. Inspect the Secret, or "+
			"`dcctl destroy` the instance if it is not wanted", instanceId, instanceId, secretName)
	}
	cfg := &config.InstanceConfiguration{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("decoding the configuration of the existing instance %q: %w", instanceId, err)
	}
	return cfg, nil
}

// ReconcileEscrow settles the artifact for an instance whose root key already
// existed — a re-run, where WriteEscrow's refusal to overwrite would otherwise stop
// a perfectly ordinary upgrade.
//
// Three cases, and each says something different:
//   - an artifact that matches the running key: nothing to do, and saying so is the
//     "is my escrow current?" check happening for free on every re-run.
//   - an artifact that does NOT match: refuse. It is an ORPHAN — it protects a key
//     this instance no longer uses — and quietly leaving it would mean the operator
//     holds a file they believe is their recovery path and is not.
//   - no artifact: write one. An instance first bootstrapped with --no-escrow gains
//     an escrow the next time someone runs bootstrap properly.
func ReconcileEscrow(plan EscrowPlan, rootKeyBase64, instance string, now time.Time) (string, error) {
	if plan.Path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(plan.Path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := WriteEscrow(plan, rootKeyBase64, instance, now); err != nil {
				return "", err
			}
			return "written", nil
		}
		return "", fmt.Errorf("reading the existing escrow artifact: %w", err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		return "", fmt.Errorf("%s exists but is not a readable escrow artifact (%w). "+
			"Move it aside so a fresh one can be written, or restore the file if it matters", plan.Path, err)
	}
	key, err := base64.StdEncoding.DecodeString(rootKeyBase64)
	if err != nil {
		return "", fmt.Errorf("decoding the instance root key: %w", err)
	}
	defer zeroBytes(key)
	if !art.Matches(key) {
		return "", fmt.Errorf("the escrow artifact at %s does NOT hold the root key instance %q is running.\n"+
			"  It is an orphan: it would not open this instance's stored secrets, so this instance currently has\n"+
			"  no usable escrow. Move it aside to have a correct one written, after checking with\n"+
			"  `dcctl secrets escrow show %s` that it is not the only copy of a key you still need",
			plan.Path, instance, plan.Path)
	}
	return "verified", nil
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
		// The suffix is checked BEFORE IsDir, so a directory called `backups.escrow`
		// is spared rather than descended into and emptied. It reads as a container of
		// escrow material to everyone except a switch that asks "is it a directory?"
		// first, which is what the previous ordering did.
		case looksLikeEscrow(e.Name()):
			kept = append(kept, p)
		case e.IsDir():
			sub, err := removeStatePreservingEscrow(p)
			// Return what was spared even on failure. Dropping it meant that a
			// permission error mid-walk left a half-removed tree AND said nothing about
			// the artifacts still in it — the moment the operator most needs to know.
			kept = append(kept, sub...)
			if err != nil {
				return kept, err
			}
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

// resolveSymlinks returns the real path, or the input unchanged when it cannot be
// resolved (most often because it does not exist yet).
func resolveSymlinks(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// looksLikeEscrow reports whether a name is plausibly root-key escrow material that
// destroy must not delete.
//
// Deliberately GENEROUS, and the asymmetry is the whole argument: sparing a file
// that turns out to be nothing costs a stray file on disk, while deleting a real one
// costs every secret in a database backup the operator still has, silently, with no
// error at the time and no way back. So it matches decoration too — `.escrow.gpg`,
// `.escrow.bak`, `rootkey.txt` — because those are exactly the spellings a careful
// operator produces when they encrypt or copy the artifact, and the earlier
// exact-suffix rule deleted all of them.
func looksLikeEscrow(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, EscrowFileExt) || strings.Contains(lower, "rootkey") || strings.Contains(lower, "root-key")
}

// zeroBytes clears a key buffer once it is no longer needed.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
