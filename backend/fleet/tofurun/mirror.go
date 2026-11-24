// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckMirror proves, offline, that the providers baked into mirror satisfy the root at rootPath
// against its COMMITTED lock file: it copies the tree to a scratch directory and runs
// `init -backend=false -lockfile=readonly` there with direct provider installation excluded, so the
// only place a provider can come from is the mirror and a hash the lock file does not hold fails.
// It is what makes "the providers are baked" a measured statement and not a comment.
func CheckMirror(ctx context.Context, bin, tree, rootPath, mirror string) error {
	if fi, err := os.Stat(mirror); err != nil || !fi.IsDir() {
		return fmt.Errorf("the provider mirror %q is not a directory", mirror)
	}
	digest, lock, err := inspectTree(tree, rootPath)
	if err != nil {
		return err
	}
	if lock == "" {
		return fmt.Errorf("%w: %s has no %s", ErrModuleRefused, rootPath, lockFileName)
	}
	rp, err := cleanRootPath(rootPath)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "mirror-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	copyTo := filepath.Join(scratch, "tree")
	for _, d := range []string{copyTo, filepath.Join(scratch, "home"), filepath.Join(scratch, "data"), filepath.Join(scratch, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := copyModule(tree, copyTo, digest); err != nil {
		return err
	}
	cli := filepath.Join(scratch, "tofurc")
	if err := os.WriteFile(cli, []byte(cliConfig(mirror)), 0o600); err != nil {
		return err
	}
	var env []string
	for _, kv := range minimalEnv() {
		if k, _, _ := strings.Cut(kv, "="); k != "TMPDIR" && k != "TEMP" && k != "TMP" {
			env = append(env, kv)
		}
	}
	tmp := filepath.Join(scratch, "tmp")
	env = append(env, "TMPDIR="+tmp, "TEMP="+tmp, "TMP="+tmp, "HOME="+filepath.Join(scratch, "home"),
		"TF_DATA_DIR="+filepath.Join(scratch, "data"), "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "TF_INPUT=0")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "init", "-backend=false", "-input=false", "-no-color", "-lockfile=readonly")
	cmd.Dir = filepath.Join(copyTo, filepath.FromSlash(rp))
	cmd.Env = env
	var tail tailBuffer
	cmd.Stderr = &tail
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("offline init of %s against the mirror failed: %w: %s", rootPath, err, diagnosticTail(tail.b, nil))
	}
	return nil
}
