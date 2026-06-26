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

// opentofu holds the infrastructure root: top-level *.tf plus the modules tree.
// terraform.tfstate / terraform.tfvars are intentionally excluded.
//
//go:embed opentofu/*.tf all:opentofu/modules
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
// deploy/local/up.sh so the two bring-up paths never diverge.
//
//go:embed local/kind-cluster.yaml
var kindClusterConfig []byte

// KindClusterConfig returns the embedded kind cluster configuration.
func KindClusterConfig() []byte { return kindClusterConfig }

// OpenTofu returns the embedded OpenTofu root, rooted so main.tf is at the top
// level (callers extract this to a working directory for terraform-exec).
func OpenTofu() fs.FS {
	sub, err := fs.Sub(opentofu, "opentofu")
	if err != nil {
		panic(err) // embed paths are compile-time constant; this cannot fail
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
