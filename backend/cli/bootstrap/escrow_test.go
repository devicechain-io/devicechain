// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
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
	if !strings.Contains(err.Error(), EscrowPassphraseEnv) {
		t.Errorf("error %q does not name the variable that is set but empty", err)
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
	if !strings.Contains(err.Error(), "--escrow-file") {
		t.Errorf("error %q does not name the flag that resolves it", err)
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

// The zero EscrowPlan must be inert. State is constructed in many places that have
// no opinion about escrow, and a zero value that meant "write one" would send them
// all at an empty path.
func TestZeroPlanIsInert(t *testing.T) {
	var plan EscrowPlan
	if plan.Path != "" {
		t.Fatal("the zero EscrowPlan carries a path")
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
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		io.Copy(&sb, r)
		done <- sb.String()
	}()
	fn()
	os.Stdout = orig
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
