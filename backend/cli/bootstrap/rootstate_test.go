// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The relocation these tests cover has no other coverage anywhere, and that is
// not an oversight to be fixed elsewhere — it is a property of the verb.
//
// `dcctl upgrade` deliberately runs no infrastructure apply, and the recreate
// drill refuses before reaching one, so the upgrade gate never executes this code
// however green it goes. Nothing in CI installs with the branch's own dcctl
// either. The apply path is reached by `dcctl bootstrap` alone, on a real cluster,
// which means these tests and a live run are the entire evidence base.
//
// What makes that tolerable is that the failure is catastrophic and quiet: state
// the apply cannot find is not an error to OpenTofu, it is a fresh install, and
// the apply proceeds to CREATE an instance's infrastructure on top of the
// infrastructure already running.

func writeState(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStateWrittenBesideTheOldRootMovesIntoIt(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")

	// An instance built before the roots had their own directories: tofu ran in the
	// working directory, so the local backend put its state there.
	writeState(t, workdir, "terraform.tfstate", `{"serial":7}`)
	writeState(t, workdir, "terraform.tfstate.backup", `{"serial":6}`)

	if err := relocateRootState(workdir, rootdir); err != nil {
		t.Fatalf("relocating: %v", err)
	}

	for name, want := range map[string]string{
		"terraform.tfstate":        `{"serial":7}`,
		"terraform.tfstate.backup": `{"serial":6}`,
	} {
		got, err := os.ReadFile(filepath.Join(rootdir, name))
		if err != nil {
			t.Fatalf("%s did not arrive in the root directory: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q — the CONTENT must survive the move, not just the name",
				name, got, want)
		}
		if _, err := os.Stat(filepath.Join(workdir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the old location; a COPY leaves two states that drift apart", name)
		}
	}
}

// TestAFreshInstanceHasNothingToMove is the negative control. Without it, a
// relocation that silently did nothing at all would pass every other test here.
func TestAFreshInstanceHasNothingToMove(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")

	if err := relocateRootState(workdir, rootdir); err != nil {
		t.Fatalf("a first bootstrap must not be an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootdir, "terraform.tfstate")); !os.IsNotExist(err) {
		t.Error("a state file was invented for an instance that has none")
	}
}

// TestTheSecondRunFindsNothingLeftToMove pins that this is a one-time repair and
// not something that fires forever. A relocation that ran on every apply would be
// a second chance to get it wrong on every apply.
func TestTheSecondRunFindsNothingLeftToMove(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")
	writeState(t, workdir, "terraform.tfstate", `{"serial":7}`)

	if err := relocateRootState(workdir, rootdir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := relocateRootState(workdir, rootdir); err != nil {
		t.Fatalf("second run must be a no-op, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(rootdir, "terraform.tfstate"))
	if err != nil || string(got) != `{"serial":7}` {
		t.Errorf("the second run disturbed the relocated state: %q, %v", got, err)
	}
}

// TestTwoStateFilesForOneRootAreRefusedRatherThanChosen is the case that must not
// be resolved by preference. Two states mean an interrupted move or two binaries
// disagreeing about where state lives, and applying against the wrong one rebuilds
// infrastructure that already exists.
func TestTwoStateFilesForOneRootAreRefusedRatherThanChosen(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")
	writeState(t, workdir, "terraform.tfstate", `{"serial":7}`)
	writeState(t, rootdir, "terraform.tfstate", `{"serial":99}`)

	err := relocateRootState(workdir, rootdir)
	if err == nil {
		t.Fatal("two state files for one root were accepted; one of them was silently preferred")
	}
	for _, want := range []string{"two", "terraform.tfstate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q, so it does not tell the operator what to look at: %v",
				want, err)
		}
	}

	// 🔴 AND IT MUST HAVE CHANGED NOTHING. A refusal that already moved one of the
	// two files has destroyed the evidence the operator needs to choose between them.
	got, err := os.ReadFile(filepath.Join(workdir, "terraform.tfstate"))
	if err != nil || string(got) != `{"serial":7}` {
		t.Errorf("the old state was disturbed by a run that refused: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(rootdir, "terraform.tfstate"))
	if err != nil || string(got) != `{"serial":99}` {
		t.Errorf("the new state was disturbed by a run that refused: %q, %v", got, err)
	}
}

// TestABackupAloneStillMoves covers the ordering inside the loop. The backup only
// appears from the second apply onward, so an instance can hold one file or two,
// and a loop that stopped at the first missing name would strand the rest.
func TestABackupAloneStillMoves(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")
	writeState(t, workdir, "terraform.tfstate.backup", `{"serial":6}`)

	if err := relocateRootState(workdir, rootdir); err != nil {
		t.Fatalf("relocating: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootdir, "terraform.tfstate.backup")); err != nil {
		t.Errorf("the backup was left behind when the primary state was absent: %v", err)
	}
}
