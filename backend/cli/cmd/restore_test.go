// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The "create the directory" advice is given only when the target volume has
// no instance directory, never for some other missing file.
func TestInstanceDirectoryHintOnlyForTheInstanceDirectory(t *testing.T) {
	missing := fmt.Errorf("target object store: %w", fs.ErrNotExist)
	bare := t.TempDir()
	if err := withInstanceDirectoryHint(missing, bare, "acme"); err == nil || !strings.Contains(err.Error(), `create the directory "acme"`) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing instance directory not explained: %v", err)
	}
	present := t.TempDir()
	if err := os.Mkdir(filepath.Join(present, "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	vanished := fmt.Errorf("open streams/x.snap: %w", fs.ErrNotExist)
	if err := withInstanceDirectoryHint(vanished, present, "acme"); err != vanished {
		t.Fatalf("hint attached to an unrelated missing file: %v", err)
	}
	other := errors.New("permission denied")
	if err := withInstanceDirectoryHint(other, bare, "acme"); err != other {
		t.Fatalf("hint attached to a non-missing error: %v", err)
	}
	if err := withInstanceDirectoryHint(nil, bare, "acme"); err != nil {
		t.Fatalf("success turned into %v", err)
	}
}

func TestRestoreValidationBeforeCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sample", "--from", "../backup/testdata/good"}, "--plan or --apply is required"},
		{[]string{"sample", "--plan=false"}, "--plan or --apply is required"},
		{[]string{"sample", "--plan", "--apply", "--only", "object-store"}, "mutually exclusive"},
		// --apply never means "everything": the resource must be explicit.
		{[]string{"sample", "--apply", "--from", "../backup/testdata/good"}, "--only object-store"},
		{[]string{"sample", "--apply", "--only", "stream"}, "--from is required"},
		{[]string{"sample", "--apply", "--only", "stream", "--from", "../backup/testdata/tampered-hash"}, "SHA-256 mismatch"},
		{[]string{"sample", "--plan", "--only", "object-store"}, "apply only to --apply"},
		{[]string{"sample", "--plan", "--overwrite-target"}, "apply only to --apply"},
		{[]string{"sample", "--apply", "--only", "object-store"}, "--from is required"},
		// The set is verified before the cluster is contacted, for an apply as for a plan.
		{[]string{"sample", "--apply", "--only", "object-store", "--from", "../backup/testdata/tampered-hash"}, "SHA-256 mismatch"},
		{[]string{"sample", "--apply", "--only", "object-store", "--overwrite-target", "--from", "../backup/testdata/good"}, "identity are required"},
		{[]string{"sample", "--plan"}, "--from is required"},
		{[]string{"sample", "--plan", "--from", "../backup/testdata/tampered-hash"}, "SHA-256 mismatch"},
		{[]string{"sample", "--plan", "--from", "../backup/testdata/good"}, "identity are required"},
		{[]string{"sample", "--plan", "--from", "../backup/testdata/good", "--force-foreign"}, "identity are required"},
	} {
		cmd := newRestoreCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(tc.args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) || exitCode(err) == 0 || strings.Contains(out.String(), ": create") {
			t.Fatalf("args %v output %q error %v; want %s", tc.args, out.String(), err, tc.want)
		}
	}
	if cmd, _, err := rootCmd.Find([]string{"restore"}); err != nil || cmd.Name() != "restore" {
		t.Fatalf("restore not registered: %v", err)
	}
}
