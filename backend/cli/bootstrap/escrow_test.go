// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
)

// fakeHome points os.UserHomeDir (and therefore instanceRoot and
// DefaultEscrowPath) at a temp directory, so path decisions are exercised against
// a real layout without touching the developer's ~/.devicechain.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// noTerminal makes the passphrase resolver take the non-interactive branch and
// makes any attempt to prompt a loud test failure rather than a hang.
func noTerminal(t *testing.T) {
	t.Helper()
	origTerm, origPrompt := stdinIsTerminal, promptPassphrase
	t.Cleanup(func() { stdinIsTerminal, promptPassphrase = origTerm, origPrompt })
	stdinIsTerminal = func() bool { return false }
	promptPassphrase = func(string, bool) (string, error) {
		t.Error("the resolver reached the interactive prompt with no terminal available")
		return "", nil
	}
}

// withPrompt simulates a terminal that answers with the given passphrase.
func withPrompt(t *testing.T, answer string, err error) *int {
	t.Helper()
	calls := 0
	origTerm, origPrompt := stdinIsTerminal, promptPassphrase
	t.Cleanup(func() { stdinIsTerminal, promptPassphrase = origTerm, origPrompt })
	stdinIsTerminal = func() bool { return true }
	promptPassphrase = func(string, bool) (string, error) {
		calls++
		return answer, err
	}
	return &calls
}

// clearEscrowEnv removes any DCCTL_ESCROW_PASSPHRASE the developer's shell carries,
// so the environment branch is only ever taken when a test asks for it.
func clearEscrowEnv(t *testing.T) {
	t.Helper()
	if _, ok := os.LookupEnv(EscrowPassphraseEnv); ok {
		t.Setenv(EscrowPassphraseEnv, "")
		os.Unsetenv(EscrowPassphraseEnv)
	}
}

const testRootKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 32 bytes, 0x00..0x1f

// ---------------------------------------------------------------------------
// Where the artifact goes
// ---------------------------------------------------------------------------

// The default path must not be inside the directory `dcctl destroy` deletes.
//
// This is the one assertion the whole file exists for. An escrow written into
// ~/.devicechain/<instance>/ would be removed by the command whose entire premise
// is that the cluster is disposable — and the operator would find out on the day
// they restored a database backup they could no longer read.
func TestDefaultEscrowPathSurvivesDestroy(t *testing.T) {
	fakeHome(t)

	path, err := DefaultEscrowPath("prod")
	if err != nil {
		t.Fatalf("DefaultEscrowPath: %v", err)
	}
	root, err := instanceRoot("prod")
	if err != nil {
		t.Fatalf("instanceRoot: %v", err)
	}
	within, err := pathIsWithin(path, root)
	if err != nil {
		t.Fatalf("pathIsWithin: %v", err)
	}
	if within {
		t.Fatalf("the default escrow path %s is inside %s, which dcctl destroy removes", path, root)
	}
}

// An explicit --escrow-file inside the instance state directory is refused, and
// the refusal names both the reason and the default.
func TestEscrowPathInsideTheStateDirectoryIsRefused(t *testing.T) {
	fakeHome(t)
	root, err := instanceRoot("prod")
	if err != nil {
		t.Fatalf("instanceRoot: %v", err)
	}

	for _, name := range []string{
		filepath.Join(root, "rootkey.escrow"),
		filepath.Join(root, "tofu", "nested", "rootkey.escrow"),
		// Spelled to LOOK outside while landing inside — the check normalizes, so a
		// path is judged on where it lands rather than on how it was typed.
		filepath.Join(root, "..", "prod", "rootkey.escrow"),
	} {
		_, err := resolveEscrowPath("prod", name)
		if err == nil {
			t.Errorf("%s was accepted, but dcctl destroy deletes it", name)
			continue
		}
		if !strings.Contains(err.Error(), "destroy") {
			t.Errorf("error for %s does not say what deletes it: %v", name, err)
		}
	}
}

// A path outside the state directory is accepted and returned absolute.
func TestEscrowPathOutsideTheStateDirectoryIsAccepted(t *testing.T) {
	home := fakeHome(t)
	want := filepath.Join(home, "backups", "prod.escrow")

	got, err := resolveEscrowPath("prod", want)
	if err != nil {
		t.Fatalf("resolveEscrowPath: %v", err)
	}
	if got != want {
		t.Fatalf("resolveEscrowPath = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("resolveEscrowPath returned a relative path %q", got)
	}
}

// pathIsWithin must not mistake a sibling with a shared prefix for a child.
// ~/.devicechain/prod-backups is NOT inside ~/.devicechain/prod, and a naive
// string-prefix check would say it is — refusing a path that is perfectly safe.
func TestSiblingWithASharedPrefixIsNotInside(t *testing.T) {
	fakeHome(t)
	root, err := instanceRoot("prod")
	if err != nil {
		t.Fatalf("instanceRoot: %v", err)
	}
	sibling := root + "-backups"

	within, err := pathIsWithin(filepath.Join(sibling, "k.escrow"), root)
	if err != nil {
		t.Fatalf("pathIsWithin: %v", err)
	}
	if within {
		t.Fatalf("%s was judged inside %s on a shared name prefix", sibling, root)
	}
	if _, err := resolveEscrowPath("prod", filepath.Join(sibling, "k.escrow")); err != nil {
		t.Fatalf("a sibling directory was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Acquiring the passphrase
// ---------------------------------------------------------------------------

func TestPassphraseFileStripsOnlyTheTrailingNewline(t *testing.T) {
	clearEscrowEnv(t)
	noTerminal(t)
	dir := t.TempDir()

	// `echo 'hunter2' > pass.txt` is how this file gets made, and a passphrase that
	// silently carried the "\n" would open nothing when typed by hand later.
	for _, tc := range []struct{ written, want string }{
		{"hunter2\n", "hunter2"},
		{"hunter2\r\n", "hunter2"},
		{"hunter2", "hunter2"},
		// Interior and leading whitespace may genuinely be part of the passphrase.
		{"  two words \n", "  two words "},
	} {
		p := filepath.Join(dir, "pass.txt")
		if err := os.WriteFile(p, []byte(tc.written), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveEscrowPassphrase(p, true, "test")
		if err != nil {
			t.Fatalf("resolveEscrowPassphrase(%q): %v", tc.written, err)
		}
		if got != tc.want {
			t.Errorf("passphrase from %q = %q, want %q", tc.written, got, tc.want)
		}
	}
}

func TestEmptyPassphraseFileIsRefusedAndNamed(t *testing.T) {
	clearEscrowEnv(t)
	noTerminal(t)
	p := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(p, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := resolveEscrowPassphrase(p, true, "test")
	if err == nil {
		t.Fatal("an empty passphrase file was accepted")
	}
	// The primitive also refuses an empty passphrase, but it cannot know WHICH of
	// three sources was empty. Naming the file is the only thing this layer adds,
	// so it is the thing worth asserting.
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q does not name the empty file", err)
	}
}

// An empty DCCTL_ESCROW_PASSPHRASE is a broken pipeline, not a choice.
//
// An unset CI secret expands to the empty string. Falling through to the next
// source would take a job with no terminal to the "no passphrase anywhere" error,
// which sends the operator looking for a flag they already set.
func TestEmptyPassphraseEnvIsRefusedRatherThanIgnored(t *testing.T) {
	noTerminal(t)
	t.Setenv(EscrowPassphraseEnv, "")

	_, err := resolveEscrowPassphrase("", true, "test")
	if err == nil {
		t.Fatal("an empty $" + EscrowPassphraseEnv + " was ignored instead of refused")
	}
	// Assert the DISTINGUISHING phrase, not the variable name.
	//
	// The fall-through this test forbids ends at the "no passphrase and no terminal"
	// error, whose text also names the variable — so an assertion on the name alone
	// was satisfied by the exact behaviour the test exists to prevent. Review proved
	// it: swapping LookupEnv for Getenv left this green.
	if !strings.Contains(err.Error(), "set but empty") {
		t.Errorf("error %q does not distinguish an empty variable from an absent one, so it would "+
			"also pass if the empty value silently fell through to the next source", err)
	}
}

func TestPassphraseSourcesArePreferredInOrder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(p, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a file beats the environment", func(t *testing.T) {
		noTerminal(t)
		t.Setenv(EscrowPassphraseEnv, "from-env")
		got, err := resolveEscrowPassphrase(p, true, "test")
		if err != nil {
			t.Fatal(err)
		}
		if got != "from-file" {
			t.Fatalf("got %q, want the file's value", got)
		}
	})

	t.Run("the environment beats a prompt", func(t *testing.T) {
		calls := withPrompt(t, "from-prompt", nil)
		t.Setenv(EscrowPassphraseEnv, "from-env")
		got, err := resolveEscrowPassphrase("", true, "test")
		if err != nil {
			t.Fatal(err)
		}
		if got != "from-env" {
			t.Fatalf("got %q, want the environment's value", got)
		}
		if *calls != 0 {
			t.Errorf("prompted %d time(s) with the environment already supplying a passphrase", *calls)
		}
	})

	t.Run("a prompt is the last resort", func(t *testing.T) {
		clearEscrowEnv(t)
		calls := withPrompt(t, "from-prompt", nil)
		got, err := resolveEscrowPassphrase("", true, "test")
		if err != nil {
			t.Fatal(err)
		}
		if got != "from-prompt" {
			t.Fatalf("got %q, want the prompt's value", got)
		}
		if *calls != 1 {
			t.Errorf("prompted %d time(s), want exactly 1", *calls)
		}
	})
}

// With no passphrase and no terminal, bootstrap must FAIL — and the error must
// name the way out, including --no-escrow.
//
// This is the branch that decides whether an automated bootstrap silently produces
// an unrecoverable instance. An error that did not name --no-escrow would leave a
// CI job with no legal move at all.
func TestNoPassphraseAndNoTerminalFailsWithEveryWayOut(t *testing.T) {
	clearEscrowEnv(t)
	noTerminal(t)

	_, err := resolveEscrowPassphrase("", true, "test")
	if err == nil {
		t.Fatal("a non-interactive run with no passphrase was allowed to proceed")
	}
	for _, want := range []string{"--escrow-passphrase-file", EscrowPassphraseEnv, "--no-escrow"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s, leaving a non-interactive caller with no route:\n%v", want, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Writing and reading the artifact
// ---------------------------------------------------------------------------

func TestWriteEscrowProducesAnArtifactThatOpensTheKey(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	plan := EscrowPlan{Path: path, Passphrase: "correct horse battery staple"}

	if err := WriteEscrow(plan, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatalf("WriteEscrow: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		t.Fatalf("the artifact dcctl just wrote does not parse: %v", err)
	}
	if art.Instance != "prod" {
		t.Errorf("artifact instance = %q, want prod", art.Instance)
	}
	key, err := escrow.Open(art, plan.Passphrase)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(key); got != testRootKey {
		t.Fatalf("recovered key = %q, want the key that was escrowed", got)
	}
}

// The file must not be readable by other users on the box. It is not the
// protection — the passphrase is — but a world-readable key file hands an attacker
// unlimited offline guesses at that passphrase.
func TestEscrowArtifactIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("escrow artifact mode is %04o, want no group/other access", perm)
	}
}

// Re-bootstrapping an instance of the same name must not silently overwrite the
// previous instance's escrow — that artifact is the only copy of the key that opens
// the database backups the operator still has.
func TestWriteEscrowRefusesToOverwriteAnExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	plan := EscrowPlan{Path: path, Passphrase: "pw"}
	if err := WriteEscrow(plan, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A different key, so a successful overwrite is detectable in the bytes.
	other := base64.StdEncoding.EncodeToString([]byte("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"))
	err = WriteEscrow(plan, other, "prod", time.Now().UTC())
	if err == nil {
		t.Fatal("WriteEscrow overwrote an existing escrow artifact")
	}
	// The refusal has to describe the file it is protecting ACCURATELY. The first
	// version asserted that any bytes at this path were "the only copy of ITS root
	// key" — which is false in the commonest way to get here, a bootstrap that failed
	// after step 1 and left an artifact for a cluster that was never built. It then
	// sent the operator hunting for an instance that never existed.
	//
	// So: name the key it actually holds, and name the flag that REUSES it, since a
	// retry after a failed bootstrap almost always wants exactly that key.
	for _, want := range []string{"--restore-root-key", "escrow show"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s, so it names no way forward:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "281a2419") {
		t.Errorf("error does not identify WHICH key the blocking artifact holds:\n%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("the existing artifact was modified by the refused write")
	}
}

func TestWriteEscrowCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatalf("WriteEscrow: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// The plan
// ---------------------------------------------------------------------------

func TestNoEscrowPlanWritesNothing(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)

	plan, err := ResolveEscrowPlan("prod", EscrowFlags{NoEscrow: true})
	if err != nil {
		t.Fatalf("--no-escrow was refused: %v", err)
	}
	if plan.Path != "" {
		t.Fatalf("--no-escrow still resolved a path: %q", plan.Path)
	}
}

// The zero EscrowPlan must be inert THROUGH THE PIPELINE. State is constructed in a
// dozen tests with no opinion about escrow, and a zero value that meant "write one"
// would send every one of them at an empty path.
//
// Asserted by running the step, not by reading the struct back: the first version of
// this test was `var plan EscrowPlan; if plan.Path != ""`, which restates Go's zero
// value for a string and cannot fail under any mutation of this package.
func TestZeroPlanWritesNothingThroughTheStep(t *testing.T) {
	home := fakeHome(t)
	st := &State{Instance: "prod", BuildImages: true, DryRun: true, Values: map[string]string{}}

	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if st.Values["secretsRootKey"] == "" {
		t.Fatal("no root key was established at all")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a zero escrow plan wrote %d entries into the home directory", len(entries))
	}
}

func TestPlanResolvesTheDefaultPathAndPassphrase(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	withPrompt(t, "typed", nil)

	plan, err := ResolveEscrowPlan("prod", EscrowFlags{})
	if err != nil {
		t.Fatalf("ResolveEscrowPlan: %v", err)
	}
	want, err := DefaultEscrowPath("prod")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != want {
		t.Errorf("plan path = %q, want %q", plan.Path, want)
	}
	if plan.Passphrase != "typed" {
		t.Errorf("plan passphrase = %q, want the prompted value", plan.Passphrase)
	}
}

// ResolveEscrowPlan must not touch the filesystem: it runs before the cluster
// exists and may be followed by a validation failure that aborts the whole run.
func TestPlanResolutionWritesNothing(t *testing.T) {
	home := fakeHome(t)
	clearEscrowEnv(t)
	withPrompt(t, "typed", nil)

	if _, err := ResolveEscrowPlan("prod", EscrowFlags{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain")); !os.IsNotExist(err) {
		t.Fatalf("resolving the plan created state under %s", home)
	}
}

// ---------------------------------------------------------------------------
// Restore — the direction the whole feature exists for
// ---------------------------------------------------------------------------

// Write an artifact, then plan a bootstrap that restores from it, and check the
// key that comes back out is the key that went in. This is the DR path end to end
// through the CLI surface.
func TestRestoreRoundTripsTheKeyThroughThePlan(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	passFile := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(passFile, []byte("recovery-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "recovery-pass"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	plan, err := ResolveEscrowPlan("prod", EscrowFlags{RestoreFile: path, PassphraseFile: passFile})
	if err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if plan.RestoredRootKey != testRootKey {
		t.Fatalf("restored key = %q, want the escrowed key", plan.RestoredRootKey)
	}
	if plan.RestoredFrom != path {
		t.Errorf("RestoredFrom = %q, want %q", plan.RestoredFrom, path)
	}
	// A restore writes no new artifact: the one it read IS the second copy, and
	// writing another would demand a passphrase decision in the middle of a
	// recovery.
	if plan.Path != "" {
		t.Errorf("a restore also resolved a write path %q", plan.Path)
	}
}

// The restored key must reach the pipeline instead of a freshly minted one.
// stepRenderConfig is where the two paths meet, and picking the wrong one there
// produces a perfectly healthy cluster that cannot read the restored database.
func TestRenderConfigUsesTheRestoredRootKey(t *testing.T) {
	st := &State{
		Instance: "prod",
		DryRun:   true,
		// BuildImages, only so the step's image-source resolution has something to
		// settle on: an untagged dev build names no published image and the step
		// refuses to go on. Nothing here is about images.
		BuildImages: true,
		Escrow:      EscrowPlan{RestoredRootKey: testRootKey, RestoredFrom: "prod.escrow"},
		Values:      map[string]string{},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if got := st.Values["secretsRootKey"]; got != testRootKey {
		t.Fatalf("the instance was configured with %q, not the restored key — a fresh cluster "+
			"built this way cannot decrypt anything restored from backup", got)
	}
}

// A bootstrap carrying a plan must actually write the artifact — and the key in
// the file must be the key the instance was configured with.
//
// The two halves are asserted together on purpose. An escrow that is written but
// holds a different key is indistinguishable from a correct one until a restore,
// which is the same class of silent failure as writing no file at all.
func TestBootstrapWritesTheEscrowArtifact(t *testing.T) {
	// Stub the deployed-instance lookup. Without it this test reaches the REAL
	// one, and then its result depends on the machine: a developer box with a
	// kubeconfig gets a NotFound and passes, while CI has no kubeconfig at all,
	// gets "no configuration has been provided", and fails closed — correctly,
	// since minting on a "cannot tell" is the destructive branch. A test about
	// writing an artifact should not be asking a cluster anything.
	withExistingInstance(t, "", nil)

	path := filepath.Join(t.TempDir(), "prod.escrow")
	st := &State{
		Instance:    "prod",
		BuildImages: true,
		Escrow:      EscrowPlan{Path: path, Passphrase: "bootstrap-pass"},
		Values:      map[string]string{},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("bootstrap reported success but wrote no escrow artifact: %v", err)
	}
	art, err := escrow.Decode(raw)
	if err != nil {
		t.Fatalf("decoding the artifact bootstrap wrote: %v", err)
	}
	key, err := escrow.Open(art, "bootstrap-pass")
	if err != nil {
		t.Fatalf("opening the artifact bootstrap wrote: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(key); got != st.Values["secretsRootKey"] {
		t.Fatal("the escrowed key is not the key the instance was configured with; " +
			"this artifact would open nothing")
	}
}

// The summary line must describe the PLAN, and it must be right in every branch.
//
// This exists because the first version keyed it on a value the write step left
// behind, so a --dry-run — which writes nothing — announced "none (--no-escrow)"
// for a run that had a passphrase, a path, and every intention of escrowing. Unit
// tests were green throughout: they checked the file, and the file was the one
// thing --dry-run legitimately does not produce. The line an operator actually
// reads was the only place the lie appeared, so that is what this reads.
func TestTheSummaryTellsTheTruthAboutTheEscrow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		st           *State
		want, unwant string
	}{
		{
			name:   "a planned escrow names the file",
			st:     &State{Escrow: EscrowPlan{Path: "/keys/prod.escrow"}},
			want:   "/keys/prod.escrow",
			unwant: "--no-escrow",
		},
		{
			name:   "a dry run says it wrote nothing without claiming there is no escrow",
			st:     &State{DryRun: true, Escrow: EscrowPlan{Path: "/keys/prod.escrow"}},
			want:   "dry run",
			unwant: "--no-escrow",
		},
		{
			name:   "a restore points at the artifact it came from",
			st:     &State{Escrow: EscrowPlan{RestoredFrom: "/keys/old.escrow"}},
			want:   "/keys/old.escrow",
			unwant: "--no-escrow",
		},
		{
			name:   "--no-escrow says so, and says what it costs",
			st:     &State{Escrow: EscrowPlan{}},
			want:   "permanently unreadable",
			unwant: "/keys/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.st.Values = map[string]string{}
			out := captureStdout(t, func() {
				if err := stepReport(t.Context(), tc.st); err != nil {
					t.Fatalf("stepReport: %v", err)
				}
			})
			if !strings.Contains(out, tc.want) {
				t.Errorf("summary does not contain %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.unwant) {
				t.Errorf("summary wrongly contains %q:\n%s", tc.unwant, out)
			}
		})
	}
}

// captureStdout collects what fn prints. The escrow summary is a print, so this is
// the only way to assert what it actually tells the operator.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	// Deferred, not restored inline: fn may call t.Fatalf, which is a runtime.Goexit,
	// and an inline restore is then never reached — leaving os.Stdout pointed at this
	// pipe for the rest of the test binary. One failing assertion would swallow every
	// later test's output and present as a confusing cascade.
	defer func() { os.Stdout = orig }()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		io.Copy(&sb, r)
		done <- sb.String()
	}()
	fn()
	w.Close()
	out := <-done
	r.Close()
	return out
}

// Under --dry-run nothing is written. A dry run that produced a real artifact
// would claim an instance's key exists on disk when no instance was ever built.
func TestDryRunWritesNoEscrowArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	st := &State{
		Instance:    "prod",
		DryRun:      true,
		BuildImages: true,
		Escrow:      EscrowPlan{Path: path, Passphrase: "pw"},
		Values:      map[string]string{},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote an escrow artifact at %s", path)
	}
}

// --dry-run must not demand a passphrase, because it writes nothing.
//
// The first version resolved the plan unconditionally, so a script running
// `bootstrap --dry-run` as a validation step hard-failed with "no escrow passphrase
// and no terminal", and an operator at a terminal was prompted — twice, ignoring
// --yes — for the real escrow passphrase by a run that then discarded it. Training
// someone to type that into a no-op is its own harm.
func TestDryRunDoesNotDemandAPassphrase(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t) // fails the test loudly if anything tries to prompt

	plan, err := ResolveEscrowPlan("prod", EscrowFlags{DryRun: true})
	if err != nil {
		t.Fatalf("a dry run demanded an escrow passphrase: %v", err)
	}
	if plan.Path == "" {
		t.Error("a dry run resolved no path, so it cannot report what a real run would write")
	}
	if plan.Passphrase != "" {
		t.Error("a dry run acquired a passphrase it has no use for")
	}
}

// A restore under --dry-run parses the artifact but does not open it — same rule.
func TestDryRunRestoreDoesNotDemandAPassphrase(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	plan, err := ResolveEscrowPlan("prod", EscrowFlags{RestoreFile: path, DryRun: true})
	if err != nil {
		t.Fatalf("a dry-run restore demanded a passphrase: %v", err)
	}
	if plan.RestoredFrom != path {
		t.Errorf("RestoredFrom = %q, want %q", plan.RestoredFrom, path)
	}
	if plan.RestoredRootKey != "" {
		t.Error("a dry run decrypted the artifact; it should only have parsed it")
	}
}

// A dry run must predict what the real run does. Reporting a clean plan for a
// bootstrap that dies at step 1 is the same class of lie as the summary bug: the
// operator's only preview of the run says it will work when it will not.
func TestDryRunNoticesAnArtifactThatWouldBlockTheRealRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	st := &State{
		Instance:    "prod",
		DryRun:      true,
		BuildImages: true,
		Escrow:      EscrowPlan{Path: path},
		Values:      map[string]string{},
	}

	out := captureStdout(t, func() {
		if err := stepRenderConfig(t.Context(), st); err != nil {
			t.Fatalf("stepRenderConfig: %v", err)
		}
	})
	if !strings.Contains(out, "already exists") {
		t.Fatalf("the dry run predicted a clean bootstrap that would in fact stop at step 1:\n%s", out)
	}
}

// Flag combinations that ask for two different things are refused rather than
// silently resolved.
//
// --dev sets --no-escrow implicitly, and --no-escrow used to discard --escrow-file
// and --escrow-passphrase-file without a word — including a passphrase file that did
// not exist, which was never even opened. That contradicts how every other --dev
// interaction works: the preset refuses a contradictory flag precisely so it cannot
// mask a mistake.
func TestContradictoryEscrowFlagsAreRefused(t *testing.T) {
	for name, f := range map[string]EscrowFlags{
		"--no-escrow with --escrow-file":            {NoEscrow: true, File: "/tmp/x.escrow"},
		"--no-escrow with --escrow-passphrase-file": {NoEscrow: true, PassphraseFile: "/tmp/pass"},
		"--no-escrow with --restore-root-key":       {NoEscrow: true, RestoreFile: "/tmp/x.escrow"},
		"--restore-root-key with --escrow-file":     {RestoreFile: "/tmp/in.escrow", File: "/tmp/out.escrow"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveEscrowPlan("prod", f); err == nil {
				t.Fatalf("%s was accepted; one of the two flags is being silently discarded", name)
			}
		})
	}
}

// The ordinary path still mints a fresh key, so the restore branch cannot have
// swallowed it.
func TestRenderConfigMintsAKeyWhenNotRestoring(t *testing.T) {
	st := &State{Instance: "prod", DryRun: true, BuildImages: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(st.Values["secretsRootKey"])
	if err != nil {
		t.Fatalf("the minted root key is not base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("the minted root key is %d bytes, want 32", len(raw))
	}
}

func TestRestoreWithTheWrongPassphraseFails(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	passFile := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(passFile, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "right"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveEscrowPlan("prod", EscrowFlags{RestoreFile: path, PassphraseFile: passFile})
	if err == nil {
		t.Fatal("a restore with the wrong passphrase produced a plan")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the artifact it failed on", err)
	}
}

func TestRestoreOfAMissingFileFails(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)

	_, err := ResolveEscrowPlan("prod", EscrowFlags{RestoreFile: filepath.Join(t.TempDir(), "nope.escrow")})
	if err == nil {
		t.Fatal("a restore from a nonexistent artifact produced a plan")
	}
	// The error must be about the FILE. With noTerminal active, "no passphrase and no
	// terminal" would also satisfy a bare err != nil — and would mean the artifact is
	// only read after a passphrase has been demanded, which is the wrong order: a
	// typo'd path should be reported before anyone is asked for a secret.
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error %q is not about the missing artifact; the file is being read after the "+
			"passphrase is demanded rather than before", err)
	}
}

// A relative --escrow-file must be resolved against the working directory, not
// stored as typed. Nothing else in the suite passes a relative path, so the
// filepath.Abs call had no coverage at all.
func TestARelativeEscrowPathIsMadeAbsolute(t *testing.T) {
	fakeHome(t)

	got, err := resolveEscrowPath("prod", filepath.Join("relative", "prod.escrow"))
	if err != nil {
		t.Fatalf("resolveEscrowPath: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("resolveEscrowPath returned %q, which is still relative; where the artifact lands "+
			"would then depend on the working directory at write time", got)
	}
}

// A symlinked path whose TARGET is inside the instance state directory must be
// refused too. The guard is described as a refusal, and a check that can be stepped
// around with a symlink is advisory.
func TestASymlinkIntoTheStateDirectoryIsAlsoRefused(t *testing.T) {
	fakeHome(t)
	root, err := instanceRoot("prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "innocent")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolveEscrowPath("prod", filepath.Join(link, "k.escrow")); err == nil {
		t.Fatal("a symlink whose target is the instance state directory was accepted; " +
			"dcctl destroy would delete the artifact anyway")
	}
}

// --restore-root-key and --escrow-file ask for opposite things; honouring one
// silently would leave the operator believing a fresh artifact had been written.
func TestRestoreCombinedWithEscrowFileIsRefused(t *testing.T) {
	fakeHome(t)
	clearEscrowEnv(t)
	noTerminal(t)

	_, err := ResolveEscrowPlan("prod", EscrowFlags{
		RestoreFile: filepath.Join(t.TempDir(), "in.escrow"),
		File:        filepath.Join(t.TempDir(), "out.escrow"),
	})
	if err == nil {
		t.Fatal("--restore-root-key with --escrow-file was accepted")
	}
	// Refused before the file is even read, so the error is about the combination
	// rather than about a missing input file.
	if strings.Contains(err.Error(), "no such file") {
		t.Errorf("the combination was not checked before the read: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A re-run must not rotate the key out from under a live instance
// ---------------------------------------------------------------------------

// withExistingInstance makes the deployed-instance lookup return a fixed answer.
func withExistingInstance(t *testing.T, key string, err error) {
	t.Helper()
	orig := lookupExistingRootKey
	t.Cleanup(func() { lookupExistingRootKey = orig })
	lookupExistingRootKey = func(context.Context, string, string) (string, error) {
		return key, err
	}
}

// THE test for the most destructive bug in this area, and it predates the escrow
// work entirely.
//
// stepRenderConfig minted a fresh root key on every non-restore run, and helmInstall
// UPGRADES an existing release with that value — so re-running `dcctl bootstrap`
// against a live instance rewrote the KEK and made every secret it had stored
// permanently undecryptable. Silently, on a run that reported success, of a pipeline
// that documents itself as idempotent and that the docs tell operators to re-run to
// change the host.
func TestRerunReusesTheDeployedRootKey(t *testing.T) {
	const deployed = "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0="
	withExistingInstance(t, deployed, nil)
	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}

	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	if got := st.Values["secretsRootKey"]; got != deployed {
		t.Fatalf("a re-run configured the instance with a NEW root key (%q, want the deployed %q).\n"+
			"helm upgrades the release with this value, so every secret the instance has stored "+
			"would become permanently undecryptable.", got, deployed)
	}
}

// A fresh install still mints, so the reuse branch cannot have swallowed the normal
// path.
func TestAFreshInstallStillMintsAKey(t *testing.T) {
	withExistingInstance(t, "", nil)
	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}

	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("stepRenderConfig: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(st.Values["secretsRootKey"])
	if err != nil || len(raw) != 32 {
		t.Fatalf("no fresh 256-bit key was minted: %q (%v)", st.Values["secretsRootKey"], err)
	}
}

// "I could not tell whether the instance exists" must STOP the run, not mint.
//
// Minting is the destructive branch, so it must never be the fallback for an
// unknown. EnsureCluster has already run by this point, so an unreachable API here
// is an anomaly worth halting for rather than guessing past.
func TestBootstrapStopsWhenItCannotTellIfTheInstanceExists(t *testing.T) {
	withExistingInstance(t, "", fmt.Errorf("connection refused"))
	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}

	err := stepRenderConfig(t.Context(), st)
	if err == nil {
		t.Fatal("bootstrap continued after failing to determine whether the instance already " +
			"exists; if it does, it just had its root key rotated out from under it")
	}
	if st.Values["secretsRootKey"] != "" {
		t.Error("a root key was established despite the lookup failing")
	}
}

// ---------------------------------------------------------------------------
// A RESTORE must not rotate the key out from under a live instance either
// ---------------------------------------------------------------------------

// restoreState builds a State on the recovery path, restoring the given key.
func restoreState(key string) *State {
	return &State{
		Instance:    "prod",
		BuildImages: true,
		Escrow:      EscrowPlan{RestoredRootKey: key, RestoredFrom: "prod.escrow"},
		Values:      map[string]string{},
	}
}

// The re-run guard above was applied to the branch that MINTS and left off the
// branch that RESTORES, which reaches the same helm upgrade with the same
// consequence. Point --restore-root-key at the wrong artifact, or at the right
// artifact under the wrong instance name, and a live instance's KEK is rewritten
// with something else — every secret it holds unreadable, no error, no undo.
func TestARestoreRefusesToOverwriteALiveInstancesDifferentKey(t *testing.T) {
	const deployed = "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0="
	withExistingInstance(t, deployed, nil)
	st := restoreState(testRootKey) // a DIFFERENT key from the one deployed

	err := stepRenderConfig(t.Context(), st)
	if err == nil {
		t.Fatal("a restore overwrote the root key of a live instance running a different one; " +
			"every secret that instance had stored is now permanently unreadable")
	}
	// The operator has to be able to tell this from an ordinary failure, because the
	// recovery action is specific: destroy first, or recover under another name.
	for _, want := range []string{"DIFFERENT", "dcctl destroy", "prod.escrow"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so it does not tell the operator what to do:\n%v", want, err)
		}
	}
	if st.Values["secretsRootKey"] != "" {
		t.Error("a root key was established despite the refusal")
	}
}

// Re-running a restore is as ordinary as re-running a bootstrap, so an artifact
// carrying the key the instance is ALREADY running must not be refused. Without
// this the guard would be indistinguishable from "restores are not idempotent".
func TestARestoreOfTheKeyAlreadyDeployedIsAllowed(t *testing.T) {
	withExistingInstance(t, testRootKey, nil)
	st := restoreState(testRootKey)

	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("re-running a restore against the instance it already recovered was refused: %v", err)
	}
	if got := st.Values["secretsRootKey"]; got != testRootKey {
		t.Fatalf("the instance was configured with %q, not the restored key", got)
	}
}

// The ordinary recovery — a cluster where the instance does not exist yet — must
// still work, or the guard has broken the only path it was meant to protect.
func TestARestoreIntoAFreshClusterIsUnaffected(t *testing.T) {
	withExistingInstance(t, "", nil)
	st := restoreState(testRootKey)

	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatalf("restoring into a cluster with no such instance was refused: %v", err)
	}
	if got := st.Values["secretsRootKey"]; got != testRootKey {
		t.Fatalf("the instance was configured with %q, not the restored key", got)
	}
}

// "Could not tell" must stop a restore for the same reason it stops a re-run:
// overwriting on an unknown is the branch with no way back.
func TestARestoreStopsWhenItCannotTellIfTheInstanceExists(t *testing.T) {
	withExistingInstance(t, "", fmt.Errorf("connection refused"))
	st := restoreState(testRootKey)

	if err := stepRenderConfig(t.Context(), st); err == nil {
		t.Fatal("a restore continued after failing to determine whether the instance already exists")
	}
	if st.Values["secretsRootKey"] != "" {
		t.Error("a root key was established despite the lookup failing")
	}
}

// The same rule at the source: only a genuine NotFound may be read as "no instance".
func TestExistingRootKeyFailsClosedWhenItCannotTell(t *testing.T) {
	// A kubeconfig that names a server nothing is listening on: reachable config,
	// unreachable API — which is exactly the "cannot tell" case, as distinct from a
	// NotFound.
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	const cfg = `apiVersion: v1
kind: Config
clusters:
- name: dead
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: dead
  context: {cluster: dead, user: dead}
current-context: dead
users:
- name: dead
  user: {token: x}
`
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)

	// Bounded, because client-go's default retry/backoff against an unreachable
	// endpoint takes tens of seconds and this test only needs the first refusal.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	key, err := ExistingRootKey(ctx, "dead", "prod")
	if err == nil {
		t.Fatal("an unreachable API was reported as 'no such instance', which is the branch that " +
			"mints a new key over a live one")
	}
	if key != "" {
		t.Errorf("a key was returned alongside the error: %q", key)
	}
}

// ReconcileEscrow is what a re-run does instead of refusing. Each branch tells the
// operator something different, and one of them is a refusal.
func TestReconcileEscrowOnARerun(t *testing.T) {
	t.Run("an artifact holding the running key is verified, not rewritten", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "prod.escrow")
		plan := EscrowPlan{Path: path, Passphrase: "pw"}
		if err := WriteEscrow(plan, testRootKey, "prod", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		outcome, err := ReconcileEscrow(plan, testRootKey, "prod", time.Now().UTC())
		if err != nil {
			t.Fatalf("a re-run over a correct escrow failed: %v", err)
		}
		if outcome != "verified" {
			t.Errorf("outcome = %q, want verified", outcome)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Error("the artifact was rewritten; a re-run must leave a correct escrow alone")
		}
	})

	t.Run("an orphaned artifact is refused, not silently left in place", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "prod.escrow")
		plan := EscrowPlan{Path: path, Passphrase: "pw"}
		// Escrowed for one key; the instance is now running a different one.
		if err := WriteEscrow(plan, testRootKey, "prod", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

		_, err := ReconcileEscrow(plan, other, "prod", time.Now().UTC())
		if err == nil {
			t.Fatal("a re-run accepted an escrow artifact that does not hold the instance's key; " +
				"the operator would keep a file they believe is their recovery path and is not")
		}
		if !strings.Contains(err.Error(), "orphan") {
			t.Errorf("error %q does not say the artifact is orphaned", err)
		}
	})

	t.Run("a missing artifact is written, so an instance can gain an escrow later", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "prod.escrow")
		plan := EscrowPlan{Path: path, Passphrase: "pw"}

		outcome, err := ReconcileEscrow(plan, testRootKey, "prod", time.Now().UTC())
		if err != nil {
			t.Fatalf("ReconcileEscrow: %v", err)
		}
		if outcome != "written" {
			t.Errorf("outcome = %q, want written", outcome)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("no artifact was written: %v", err)
		}
	})
}

// WriteEscrow must leave nothing behind that blocks its own retry.
//
// A partial write used to leave a zero-length stub at the destination, and the
// overwrite guard then refused every subsequent attempt while calling the stub "the
// only copy of ITS root key" — a message that was false, about a file dcctl's own
// reader could not parse. A single full disk bricked that instance name permanently.
func TestAPartialArtifactDoesNotBlockItsOwnReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prod.escrow")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC())
	if err == nil {
		t.Skip("an empty stub is now overwritten outright, which also resolves this")
	}
	// It still refuses — but it must describe what is actually there, and tell the
	// operator how to get out of it.
	if strings.Contains(err.Error(), "only copy") {
		t.Errorf("the refusal calls an unreadable stub the only copy of a root key:\n%v", err)
	}
	if !strings.Contains(err.Error(), "Move it aside") {
		t.Errorf("the refusal names no way forward:\n%v", err)
	}
}

// A successful write leaves the artifact and nothing else.
func TestAWrittenArtifactLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC()); err != nil {
		t.Fatalf("WriteEscrow: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file survived the write: %s", e.Name())
		}
	}
}

// An artifact that does not open must never be published, and bootstrap must not
// continue past it.
//
// The condition is unreachable by ordinary means — the artifact is encoded and
// immediately re-read by the same code — which is exactly why the check is here and
// why the test needs a seam to reach it. It guards against a defect in the FORMAT,
// and the format has had precisely that defect: a header whose bytes did not survive
// the round trip produced a file that verified in memory and opened from disk never.
//
// (Found by mutation: the first version of this test passed with the whole
// verification deleted, because it only ever exercised artifacts that were fine.)
func TestAnArtifactThatDoesNotOpenIsNeverPublished(t *testing.T) {
	orig := encodeArtifact
	t.Cleanup(func() { encodeArtifact = orig })
	encodeArtifact = func(a *escrow.Artifact) ([]byte, error) {
		raw, err := a.Encode()
		if err != nil {
			return nil, err
		}
		// Corrupt the sealed body while leaving a perfectly well-formed file: this is
		// what a format-level round-trip defect looks like from here.
		return bytes.Replace(raw, []byte("eyJoZHIi"), []byte("eyJoZHJk"), 1), nil
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "prod.escrow")
	err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "pw"}, testRootKey, "prod", time.Now().UTC())
	if err == nil {
		t.Fatal("WriteEscrow accepted an artifact that cannot be opened; bootstrap would go on to " +
			"build an instance whose only escrow opens nothing")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the unopenable artifact was published to %s anyway; the overwrite guard would then "+
			"refuse every retry", path)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("the failed write left a temporary file behind: %s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// destroy must not eat the artifact
// ---------------------------------------------------------------------------

func TestDestroyRemovesStateButSparesEscrowArtifacts(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tfstate := mustWrite("tofu/terraform.tfstate")
	mustWrite("tofu/modules/main.tf")
	topLevel := mustWrite("rootkey" + EscrowFileExt)
	nested := mustWrite("keys/deep/prod" + EscrowFileExt)

	kept, err := removeStatePreservingEscrow(dir)
	if err != nil {
		t.Fatalf("removeStatePreservingEscrow: %v", err)
	}

	for _, p := range []string{topLevel, nested} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("escrow artifact %s was deleted with the state: %v", p, err)
		}
		if !containsPath(kept, p) {
			t.Errorf("%s survived but was not reported, so nothing tells the operator it is there", p)
		}
	}
	if _, err := os.Stat(tfstate); !os.IsNotExist(err) {
		t.Errorf("ordinary state %s survived: %v", tfstate, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tofu")); !os.IsNotExist(err) {
		t.Error("a subdirectory holding no escrow artifact was left behind")
	}
}

// With nothing to spare, the directory goes entirely — the escrow rule must not
// turn every teardown into a half-teardown that leaves empty directories behind.
func TestDestroyRemovesEverythingWhenThereIsNoEscrow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prod")
	if err := os.MkdirAll(filepath.Join(dir, "tofu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tofu", "terraform.tfstate"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	kept, err := removeStatePreservingEscrow(dir)
	if err != nil {
		t.Fatalf("removeStatePreservingEscrow: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("kept %v, want nothing", kept)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("%s survived a teardown with no escrow artifact in it", dir)
	}
}

// The spare rule matches the spellings an operator actually produces, not just the
// exact suffix dcctl writes.
//
// A directory called `backups.escrow` used to be descended into and emptied, because
// the switch asked "is it a directory?" before "is it escrow material?". And the
// realistic hand-made names — an encrypted copy, a dated backup, a plainly-named
// key file — were all deleted, while the function's own doc claimed to exist for
// exactly those.
func TestDestroySparesEscrowMaterialWhateverItIsCalled(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, isDir bool) string {
		p := filepath.Join(dir, rel)
		if isDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "inner.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(p, "inner.txt")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	survive := []string{
		mk("backups.escrow", true),           // a DIRECTORY of escrow material
		mk("prod-rootkey.escrow.gpg", false), // encrypted copy
		mk("prod.escrow.bak", false),         // dated backup
		mk("rootkey.txt", false),             // plainly named
		mk("ROOT-KEY-COPY", false),           // shouted
	}
	doomed := mk("tofu/terraform.tfstate", false)

	kept, err := removeStatePreservingEscrow(dir)
	if err != nil {
		t.Fatalf("removeStatePreservingEscrow: %v", err)
	}
	for _, p := range survive {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
	if _, err := os.Stat(doomed); !os.IsNotExist(err) {
		t.Errorf("ordinary state survived: %v", err)
	}
	if len(kept) == 0 {
		t.Error("nothing was reported as kept, so the operator is told none of this survived")
	}
}

// A failure part-way through must still name what it spared. A half-removed tree is
// exactly when an operator needs to know what is left in it, and reporting only on
// the success path meant the failure case said nothing at all.
func TestDestroyReportsWhatItSparedEvenWhenItFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable directory cannot be simulated")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prod-rootkey"+EscrowFileExt), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "zz-locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	kept, err := removeStatePreservingEscrow(dir)
	if err == nil {
		t.Skip("the walk succeeded despite the unreadable directory; nothing to assert")
	}
	if len(kept) == 0 {
		t.Fatal("the walk failed and reported nothing spared, so the surviving escrow artifact " +
			"is invisible to the operator staring at a half-destroyed instance")
	}
}

func TestDestroyOnAMissingStateDirectoryIsNotAnError(t *testing.T) {
	kept, err := removeStatePreservingEscrow(filepath.Join(t.TempDir(), "never-existed"))
	if err != nil {
		t.Fatalf("removeStatePreservingEscrow on a missing directory: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("kept %v from a directory that does not exist", kept)
	}
}

// An instance literally named "escrow" collides with the default escrow directory:
// ~/.devicechain/escrow IS its state root. Both guards must hold — the write is
// refused rather than landing somewhere destroy would eat, and destroying that
// instance still spares every OTHER instance's artifact living there.
func TestAnInstanceNamedEscrowDoesNotDisarmEitherGuard(t *testing.T) {
	fakeHome(t)
	root, err := instanceRoot("escrow")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := resolveEscrowPath("escrow", ""); err == nil {
		t.Error("the default path for an instance named \"escrow\" was accepted, but it " +
			"is inside that instance's own state directory")
	}

	// Another instance's artifact, sitting in the shared escrow directory that this
	// instance's teardown would otherwise remove wholesale.
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "prod-rootkey"+EscrowFileExt)
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := removeStatePreservingEscrow(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("destroying the instance named \"escrow\" deleted another instance's artifact: %v", err)
	}
}

func containsPath(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}
