// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command fleet-runner is the entry point of the fleet runner image.
//
// In this build it does ONE thing: `fleet-runner check`, which proves the image is what it claims
// (the pinned OpenTofu and no other binary, the baked roots acceptable and unchanged, the provider
// mirror present). The wrapper that claims a run from the manager, heartbeats and posts facts needs
// the manager's runner API, which does not exist yet; running anything else exits 78 with that said,
// rather than returning a success that did nothing.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/devicechain-io/dc-fleet/tofurun"
)

const (
	exitUsage          = 2
	exitNotImplemented = 78
	exitCheckFailed    = 1
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 1 && args[0] == "check" {
		if err := check(context.Background(), os.Getenv); err != nil {
			fmt.Fprintf(stderr, "fleet-runner: check failed: %v\n", err)
			return exitCheckFailed
		}
		fmt.Fprintln(stdout, "fleet-runner: image check passed")
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fleet-runner check")
		return exitUsage
	}
	fmt.Fprintln(stderr, "fleet-runner: NOT IMPLEMENTED: this build has no runner API to claim a run from; only `check` runs")
	return exitNotImplemented
}

// check verifies the image layout named by the environment:
//
//	FLEET_RUNNER_TOFU      absolute path of the tofu binary (default /usr/local/bin/tofu)
//	FLEET_RUNNER_TREE      the baked tree (default /opt/fleet/tree); every directory under its fleet/
//	                       is one root, and the roots' relative module sources resolve inside the tree
//	FLEET_RUNNER_MIRROR    the baked provider mirror (default /opt/fleet/providers); each root must
//	                       initialise offline from it under its committed lock file
//	FLEET_RUNNER_EXPECTED  the expected OpenTofu version (default the pinned one)
func check(ctx context.Context, getenv func(string) string) error {
	def := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	bin, err := tofurun.ResolveTofu(def("FLEET_RUNNER_TOFU", "/usr/local/bin/tofu"))
	if err != nil {
		return err
	}
	if err := tofurun.CheckVersion(ctx, bin, def("FLEET_RUNNER_EXPECTED", tofurun.PinnedVersion)); err != nil {
		return err
	}
	// There is no Terraform in the image, on PATH or anywhere a fallback could look.
	if p, err := exec.LookPath("terraform"); err == nil {
		return fmt.Errorf("a terraform binary is present at %s", p)
	}
	tree := def("FLEET_RUNNER_TREE", "/opt/fleet/tree")
	mirror := def("FLEET_RUNNER_MIRROR", "/opt/fleet/providers")
	roots := filepath.Join(tree, "fleet")
	entries, err := os.ReadDir(roots)
	if err != nil {
		return fmt.Errorf("the baked roots: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		if err := tofurun.CheckRoot(tree, "fleet/"+e.Name()); err != nil {
			return fmt.Errorf("root %s: %w", e.Name(), err)
		}
		if err := tofurun.CheckMirror(ctx, bin, tree, "fleet/"+e.Name(), mirror); err != nil {
			return fmt.Errorf("root %s: %w", e.Name(), err)
		}
	}
	if n == 0 {
		return fmt.Errorf("no baked roots under %s", roots)
	}
	return nil
}
