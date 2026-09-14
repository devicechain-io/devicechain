// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package assets embeds the DeviceChain deploy artifacts — the OpenTofu
// infrastructure config, the per-instance Helm chart, and the local kind cluster
// topology — directly into any binary that imports it. This is what lets
// `dcctl bootstrap` create and provision a cluster with no source checkout, no
// git, and no kubectl/kustomize/helm binaries on the user's machine: the
// manifests travel inside dcctl.
//
// The embed globs are deliberately precise: they pull in the .tf source and the
// modules tree but never the local terraform state/tfvars (which hold secrets
// and per-machine values), nor the gitignored .terraform provider cache.
package assets

import (
	"embed"
	"io/fs"
)

// opentofu holds every OpenTofu ROOT plus the modules tree they share. Roots are
// peers: each is a directory of .tf files tofu is meant to init and apply
// directly, and each reaches the shared modules as `source = "../modules/<x>"`.
//
// 🔴 A ROOT IS EMBEDDED BY ITS *.tf GLOB, NEVER BY `all:`. MEASURED, not reasoned:
// `all:opentofu/instance` was written here first and the build failed on
//
//	embed opentofu/instance/terraform.tfstate: no such file or directory
//
// because a root directory is precisely where tofu RUNS — `tofu init`, the CI
// validate step, hack/check-tofu-validations.sh — and every one of those leaves
// terraform.tfstate, .terraform.lock.hcl and a .terraform cache beside the .tf
// files. go:embed captures what is ON DISK at build time, not what git tracks, so
// `all:` over a root ships whichever of those happen to exist. tfstate is not a
// summary of the infrastructure but its values in cleartext, including the database
// superuser password and the broker's TLS private key.
//
// 🔑 The old single-root layout was protected by ACCIDENT: the root sat at the top
// of the tree and `opentofu/*.tf` picked up its .tf files while leaving the state
// beside them. Moving the root into its own directory and reaching for `all:` looks
// like a tidy-up and quietly removes that protection. TestNoSecretsEmbedded is the
// other half of the net, and it is deliberately blunt enough to catch this.
//
// `modules/` keeps `all:` because it holds non-.tf assets a module needs — the
// cnpg-cluster chart — and nothing ever runs tofu inside a module directory.
//
// 🔑 WHAT MAKES A PER-ROOT GLOB SAFE NOW, when the same shape used to be the
// hazard: `*` does not cross a `/`, so a NEW root directory matches no pattern here
// and go:embed says nothing — it errors only when a pattern matches NOTHING, and
// these still match. That silence used to be unbounded. It is now bounded by
// TestEveryOpenTofuFileOnDiskEitherShipsOrIsNamed, which walks the real tree and
// fails on any file that did not survive the embed. Adding a root means adding a
// line here, and forgetting is a test failure rather than a binary shipped without
// a root it needs.
//
//go:embed opentofu/instance/*.tf all:opentofu/modules
var opentofu embed.FS

// helmChart holds the per-instance chart (Chart.yaml, values, templates). The
// all: prefix is essential: go:embed otherwise skips _-prefixed files, dropping
// templates/_helpers.tpl (the chart's named-template library).
//
//go:embed all:helm/devicechain
var helmChart embed.FS

// kindClusterConfig is the kind cluster topology the local provider creates: a
// control-plane node labelled ingress-ready with host-port mappings (80/443 for
// ingress, 1883 for MQTT) plus the localhost:5000 registry mirror. Shared with
// dcctl, which is the only bring-up path -- deploy/local/up.sh applied this tree
// directly and was withdrawn once the credentials moved into dcctl.
//
//go:embed local/kind-cluster.yaml
var kindClusterConfig []byte

// KindClusterConfig returns the embedded kind cluster configuration.
func KindClusterConfig() []byte { return kindClusterConfig }

// InstanceRootDir is the subdirectory holding the per-instance root — the one
// `dcctl bootstrap` applies. Exported because the working directory dcctl runs
// tofu in is this path under the extracted tree, and a literal repeated at the
// extraction site and the exec site is a literal that can disagree with itself.
const InstanceRootDir = "instance"

// OpenTofu returns the whole embedded tree — every root plus the shared modules,
// with the directory structure intact.
//
// 🔴 EXTRACT THIS, NOT A SINGLE ROOT. Roots reach modules as "../modules/<x>", so
// a root extracted on its own cannot resolve them. Callers write this to a working
// directory and then run tofu INSIDE the root subdirectory they want.
func OpenTofu() fs.FS {
	sub, err := fs.Sub(opentofu, "opentofu")
	if err != nil {
		panic(err) // embed paths are compile-time constant; this cannot fail
	}
	return sub
}

// OpenTofuInstance returns the per-instance root alone, rooted so main.tf is at
// the top level. For READING the root's own files — variables.tf for a default,
// main.tf for a wiring assertion — never for extraction.
func OpenTofuInstance() fs.FS {
	sub, err := fs.Sub(opentofu, "opentofu/"+InstanceRootDir)
	if err != nil {
		panic(err)
	}
	return sub
}

// HelmChart returns the embedded chart root, rooted so Chart.yaml is at the top
// level (suitable for the Helm loader).
func HelmChart() fs.FS {
	sub, err := fs.Sub(helmChart, "helm/devicechain")
	if err != nil {
		panic(err)
	}
	return sub
}
