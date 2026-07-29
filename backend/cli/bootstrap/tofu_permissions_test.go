// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// ~/.devicechain/<instance>/infra holds terraform.tfstate, and tfstate carries the
// database superuser password and the NATS server's TLS private key in cleartext.
// These tests pin the permissions on the directory that holds it.
//
// The interesting half of each is the PRE-EXISTING case. Both os.MkdirAll and
// os.WriteFile apply their mode only when they create the thing; handed a path an
// older dcctl already made at 0755/0644 they succeed, change nothing, and report
// no error. A test that only ever starts from an empty temp dir passes against
// code that leaves every existing install world-readable — which is most of them,
// since this is a fix for something already shipped.

func TestInstanceStateDirIsOwnerOnly(t *testing.T) {
	home := fakeHome(t)

	dir, err := instanceStateDir("prod", "infra")
	if err != nil {
		t.Fatalf("instanceStateDir: %v", err)
	}
	want := filepath.Join(home, ".devicechain", "prod", "infra")
	if dir != want {
		t.Fatalf("instanceStateDir = %q, want %q", dir, want)
	}

	// Every level, not just the leaf: a 0700 leaf under a 0755 parent is still
	// private, but the parent holds the escrow artifacts and every other
	// instance, and this is the only code that creates it.
	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", "prod"),
		dir,
	} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != stateDirMode {
			t.Errorf("%s has mode %04o, want %04o", p, got, stateDirMode)
		}
	}
}

func TestInstanceStateDirTightensAnExistingLooseDirectory(t *testing.T) {
	home := fakeHome(t)

	// The layout an older dcctl left behind: every level world-readable and
	// world-traversable, with a state file already in it.
	loose := filepath.Join(home, ".devicechain", "prod", "infra")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatalf("seeding the old layout: %v", err)
	}
	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", "prod"),
		loose,
	} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
	state := filepath.Join(loose, "terraform.tfstate")
	if err := os.WriteFile(state, []byte(`{"secret":"hunter2"}`), 0o644); err != nil {
		t.Fatalf("seeding tfstate: %v", err)
	}

	if _, err := instanceStateDir("prod", "infra"); err != nil {
		t.Fatalf("instanceStateDir: %v", err)
	}

	for _, p := range []string{
		filepath.Join(home, ".devicechain"),
		filepath.Join(home, ".devicechain", "prod"),
		loose,
	} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != stateDirMode {
			t.Errorf("pre-existing %s left at %04o, want %04o", p, got, stateDirMode)
		}
	}

	// The directory mode is the protection, so the file is checked separately by
	// the hardenStateFiles tests below — but assert here that the existing state
	// file survived, because a "fix" that tightened permissions by deleting the
	// state would pass every mode assertion and destroy the instance.
	if b, err := os.ReadFile(state); err != nil || string(b) != `{"secret":"hunter2"}` {
		t.Fatalf("tfstate not preserved: content=%q err=%v", b, err)
	}
}

func TestHardenStateFilesTightensBothStateFiles(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "terraform.tfstate")
	backup := filepath.Join(dir, "terraform.tfstate.backup")
	for _, p := range []string{state, backup} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", p, err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}

	if err := hardenStateFiles(dir); err != nil {
		t.Fatalf("hardenStateFiles: %v", err)
	}

	for _, p := range []string{state, backup} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != stateFileMode {
			t.Errorf("%s has mode %04o, want %04o", filepath.Base(p), got, stateFileMode)
		}
	}
}

func TestHardenStateFilesIgnoresAbsentFiles(t *testing.T) {
	// A dry run never produces a state file, and there is no .backup until the
	// second apply. The deferred call in applyInfra runs on every exit path, so
	// treating "not there" as a failure would turn every first apply into an
	// error after the work had already succeeded.
	if err := hardenStateFiles(t.TempDir()); err != nil {
		t.Fatalf("hardenStateFiles on an empty directory: %v", err)
	}
}

func TestExtractFSWritesOwnerOnlyAndTightensExistingFiles(t *testing.T) {
	dir := t.TempDir()

	src := fstest.MapFS{
		"main.tf":            {Data: []byte("# root\n")},
		"modules/nats/x.tf":  {Data: []byte("# nats\n")},
		"modules/cnpg/y.tf":  {Data: []byte("# cnpg\n")},
		"modules/cnpg/z.tfv": {Data: []byte("# vars\n")},
	}

	// One file AND one directory already present at the old loose modes. Both
	// halves are needed and the first version of this test only had the file:
	// os.MkdirAll ignores its mode on an existing directory exactly as WriteFile
	// ignores its mode on an existing file, so a test that seeds only a file
	// passes against code that leaves every pre-existing modules/ subdirectory
	// at 0755. That is not hypothetical — it is what shipped in the first draft
	// of this fix, and a `find -perm /0077` over a real ~/.devicechain found it.
	if err := os.MkdirAll(filepath.Join(dir, "modules", "cnpg"), 0o755); err != nil {
		t.Fatalf("seeding modules/cnpg: %v", err)
	}
	for _, p := range []string{
		filepath.Join(dir, "modules"),
		filepath.Join(dir, "modules", "cnpg"),
	} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seeding main.tf: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "main.tf"), 0o644); err != nil {
		t.Fatalf("chmod main.tf: %v", err)
	}

	if err := extractFS(src, dir); err != nil {
		t.Fatalf("extractFS: %v", err)
	}

	// Counted, not spot-checked. Walking and asserting per file is only a check
	// if something also confirms the walk SAW every file — otherwise an extract
	// that silently wrote nothing passes with zero assertions.
	var files, dirs int
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if fi.IsDir() {
			dirs++
			if got := fi.Mode().Perm(); got != stateDirMode {
				t.Errorf("dir %s has mode %04o, want %04o", p, got, stateDirMode)
			}
			return nil
		}
		files++
		if got := fi.Mode().Perm(); got != stateFileMode {
			t.Errorf("file %s has mode %04o, want %04o", p, got, stateFileMode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	if files != len(src) {
		t.Fatalf("walked %d files, want %d — the extract did not write what the FS holds", files, len(src))
	}
	if dirs != 3 { // modules, modules/nats, modules/cnpg
		t.Fatalf("walked %d directories, want 3", dirs)
	}

	// And the pre-existing file was overwritten with the embedded content, not
	// merely chmodded.
	if b, err := os.ReadFile(filepath.Join(dir, "main.tf")); err != nil || string(b) != "# root\n" {
		t.Fatalf("main.tf not refreshed: content=%q err=%v", b, err)
	}
}
