// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNotTofu: the binary is not OpenTofu. There is deliberately no fallback to `terraform`.
	ErrNotTofu = errors.New("tofurun: the binary is not OpenTofu")
	// ErrVersionMismatch: the binary is OpenTofu at a version other than the pinned one.
	ErrVersionMismatch = errors.New("tofurun: OpenTofu version is not the pinned one")

	versionRe = regexp.MustCompile(`^OpenTofu v([0-9]+\.[0-9]+\.[0-9]+)\s*$`)
)

// ResolveTofu finds the OpenTofu binary. path, when given, must be an absolute path to a file named
// tofu (or tofu.exe); when empty it is looked up as `tofu` on PATH. A `terraform` binary is never
// looked for, and a file with any other name is refused even if it would answer like OpenTofu.
func ResolveTofu(path string) (string, error) {
	if path == "" {
		p, err := exec.LookPath("tofu")
		if err != nil {
			return "", fmt.Errorf("%w: no `tofu` on PATH (a `terraform` binary is not a substitute)", ErrNotTofu)
		}
		path = p
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: the path must be absolute", ErrNotTofu)
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	if base != "tofu" {
		return "", fmt.Errorf("%w: %s is not named tofu", ErrNotTofu, filepath.Base(path))
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not a file", ErrNotTofu, path)
	}
	return path, nil
}

// CheckVersion runs `tofu version` and requires an OpenTofu binary of exactly want. Terraform
// prints "Terraform v..." and is refused here as ErrNotTofu even under the name tofu.
func CheckVersion(ctx context.Context, bin, want string) error {
	if want == "" {
		return fmt.Errorf("%w: no expected version given", ErrVersionMismatch)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = minimalEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: `version` failed: %v", ErrNotTofu, err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	m := versionRe.FindStringSubmatch(strings.TrimSpace(first))
	if m == nil {
		return fmt.Errorf("%w: the binary reports %q", ErrNotTofu, truncate(first, 40))
	}
	if m[1] != want {
		return fmt.Errorf("%w: have %s, want %s", ErrVersionMismatch, m[1], want)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
