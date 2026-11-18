// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupInspect(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"good", []string{"inspect", "../backup/testdata/good", "--verify"}, ""},
		{"hash", []string{"inspect", "../backup/testdata/tampered-hash", "--verify"}, "SHA-256 mismatch"},
		{"missing", []string{"inspect", "../backup/testdata/missing-file", "--verify"}, "bucket.snap"},
		{"version", []string{"inspect", "../backup/testdata/unknown-version", "--verify"}, "unsupported manifest version"},
		{"class", []string{"inspect", "../backup/testdata/wrong-class", "--verify"}, "not checkpoint-preserving"},
		{"no verify", []string{"inspect", "../backup/testdata/good"}, "--verify is required"},
		{"false verify", []string{"inspect", "../backup/testdata/good", "--verify=false"}, "--verify is required"},
		{"no directory", []string{"inspect", "--verify"}, "accepts 1 arg"},
		{"extra directory", []string{"inspect", "a", "b", "--verify"}, "accepts 1 arg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately unusable kubeconfig: inspection must remain offline.
			t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
			cmd := newBackupCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if tc.want == "" {
				if err != nil || !strings.Contains(out.String(), "Verified manifest v1: 3 snapshots") {
					t.Fatalf("output %q, error %v", out.String(), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || exitCode(err) == 0 || strings.Contains(out.String(), "Verified manifest") {
				t.Fatalf("output %q, error %v; want nonzero error %q", out.String(), err, tc.want)
			}
		})
	}
	if cmd, _, err := rootCmd.Find([]string{"backup", "inspect"}); err != nil || cmd.Name() != "inspect" {
		t.Fatalf("backup inspect not registered: %v, %v", cmd, err)
	}
}

func TestBackupExportValidatesBeforeClusterAccess(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "absent"))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sample"}, "--destination is required"},
		{[]string{"--destination", "backup"}, "accepts 1 arg"},
		{[]string{"sample", "--destination", t.TempDir()}, "destination already exists"},
	} {
		cmd := newBackupCmd()
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args %v: got %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestBareBackupKeepsItsHelpBehavior(t *testing.T) {
	cmd := newBackupCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "inspect") {
		t.Fatalf("bare backup should show help: %q, %v", out.String(), err)
	}
}
