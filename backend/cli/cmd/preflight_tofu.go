// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/go-version"
)

// Match the simple floor the bundled roots declare. Refuse an unsupported or
// missing declaration rather than silently weakening a future constraint.
var tofuRequiredVersion = regexp.MustCompile(`(?m)^\s*required_version\s*=\s*"(>=\s*[0-9]+\.[0-9]+(?:\.[0-9]+)?)"\s*$`)

func checkTofuRequirements(raw string, roots ...fs.FS) error {
	var reported struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(raw), &reported); err != nil {
		return fmt.Errorf("cannot read OpenTofu/Terraform version: %w", err)
	}
	v, err := version.NewVersion(reported.Version)
	if err != nil {
		return fmt.Errorf("invalid OpenTofu/Terraform version %q: %w", reported.Version, err)
	}
	for i, root := range roots {
		data, err := fs.ReadFile(root, "versions.tf")
		if err != nil {
			return fmt.Errorf("read bundled root %d: %w", i, err)
		}
		matches := tofuRequiredVersion.FindAllSubmatch(data, -1)
		if len(matches) != 1 {
			return fmt.Errorf("bundled root %d has no supported required_version floor", i)
		}
		constraint, err := version.NewConstraint(string(matches[0][1]))
		if err != nil {
			return fmt.Errorf("bundled required_version: %w", err)
		}
		if !constraint.Check(v) {
			return fmt.Errorf("OpenTofu/Terraform %s does not satisfy bundled required_version %s", v, constraint)
		}
	}
	return nil
}

func (d *doctor) checkTofuVersion() {
	// Keep the same preference as bootstrap.findTofu: an old tofu must not be
	// hidden by a newer terraform that the apply would never use.
	for _, name := range []string{"tofu", "terraform"} {
		path, err := lookPath(name)
		if err != nil {
			continue
		}
		if err := checkTofuRequirements(d.run(path, "version", "-json"), assets.OpenTofuCluster(), assets.OpenTofuInstance()); err != nil {
			d.fail(name+": "+err.Error(), "install a stable OpenTofu or Terraform satisfying the bundled required_version, then re-run preflight")
			return
		}
		d.pass(name + " (" + path + ") satisfies bundled required_version")
		return
	}
	d.fail("tofu/terraform not found", "install OpenTofu (https://opentofu.org) or Terraform")
}
