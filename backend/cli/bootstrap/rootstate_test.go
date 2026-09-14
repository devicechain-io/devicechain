// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-deploy"
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

// TestTheSupersededRootConfigIsRemoved covers the other half of the layout
// change, and it is the half a temporary directory could not have shown me.
//
// Found against a COPY of a real ~/.devicechain/<instance>/infra: after extract +
// relocate, five .tf files written by an older dcctl were still sitting at the top
// with the state gone from under them. extractFS only writes, so nothing
// overwrites a file the new tree no longer places there.
func TestTheSupersededRootConfigIsRemoved(t *testing.T) {
	workdir := t.TempDir()
	rootdir := filepath.Join(workdir, "instance")

	// What an older dcctl left: a whole root configuration at the top.
	for _, name := range []string{"main.tf", "variables.tf", "outputs.tf", "providers.tf", "versions.tf"} {
		writeState(t, workdir, name, "# from an older dcctl\n")
	}
	// What must survive: the new tree, and the local artifacts beside it.
	writeState(t, rootdir, "main.tf", "# current\n")
	writeState(t, rootdir, "terraform.tfstate", `{"serial":7}`)
	writeState(t, filepath.Join(workdir, "modules", "nats"), "main.tf", "# module\n")
	writeState(t, workdir, ".terraform.lock.hcl", "provider hashes\n")

	if err := removeSupersededRootConfig(workdir); err != nil {
		t.Fatalf("removing superseded config: %v", err)
	}

	for _, name := range []string{"main.tf", "variables.tf", "outputs.tf", "providers.tf", "versions.tf"} {
		if _, err := os.Stat(filepath.Join(workdir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived at the top of the working directory. With the state moved out from "+
				"under it, a tofu plan there reads an empty state against an intact configuration and "+
				"answers that it will CREATE an entire second infrastructure", name)
		}
	}
	// 🔴 The blast radius must stop at top-level .tf. Everything below is the
	// configuration actually in use, and the lock file is not ours to delete.
	for _, p := range []string{
		filepath.Join(rootdir, "main.tf"),
		filepath.Join(rootdir, "terraform.tfstate"),
		filepath.Join(workdir, "modules", "nats", "main.tf"),
		filepath.Join(workdir, ".terraform.lock.hcl"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed and must not have been: %v", p, err)
		}
	}
}

// TestAFreshInstanceHasNoSupersededConfig is the negative control for the above:
// without it, a cleanup that deleted nothing at all would still look correct on
// the survivor assertions.
func TestAFreshInstanceHasNoSupersededConfig(t *testing.T) {
	workdir := t.TempDir()
	writeState(t, filepath.Join(workdir, "instance"), "main.tf", "# current\n")

	if err := removeSupersededRootConfig(workdir); err != nil {
		t.Fatalf("a first bootstrap must not be an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "instance", "main.tf")); err != nil {
		t.Errorf("the current configuration was removed on a fresh install: %v", err)
	}
}

// TestTheEmbeddedTreePlacesNoConfigAtTheTop pins the invariant the cleanup rests
// on, which is otherwise only true by inspection.
//
// removeSupersededRootConfig deletes every top-level .tf on the reasoning that the
// embedded tree puts nothing there, so anything it finds is left over. That
// reasoning is a property of the go:embed directives in another module, and if one
// ever placed a .tf at the top of the tree the cleanup would delete a file the very
// same run had just written — after the apply had been configured to read it.
//
// Nothing else in either module would notice, which is why this asserts it here
// rather than trusting the pattern to stay as it is.
func TestTheEmbeddedTreePlacesNoConfigAtTheTop(t *testing.T) {
	entries, err := fs.ReadDir(assets.OpenTofu(), ".")
	if err != nil {
		t.Fatalf("reading the embedded tree: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the embedded tree is empty, so this assertion cannot fail")
	}
	roots := 0
	for _, e := range entries {
		if e.IsDir() {
			roots++
			continue
		}
		if strings.HasSuffix(e.Name(), ".tf") {
			t.Errorf("the embedded tree places %q at its TOP LEVEL. removeSupersededRootConfig "+
				"deletes every top-level .tf as superseded, so this file would be written by "+
				"extractFS and then deleted in the same run — either embed it inside a root "+
				"directory, or narrow the cleanup", e.Name())
		}
	}
	if roots == 0 {
		t.Error("the embedded tree contains no directories at all, so there is no root to apply")
	}
}
