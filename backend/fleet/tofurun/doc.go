// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tofurun is the server-side OpenTofu runner: the library a fleet runner Job drives to
// run `tofu init / plan / apply / destroy` for ONE cluster, in an isolated work directory, against
// remote state, under exactly one cloud identity class, and only after the writer slot admitted it.
//
// It is a library and nothing else. It serves no API and makes no cloud call of its own; the
// manager-side halves it coordinates with (the writer slot, the approval, the Job) reach it through
// the interfaces named here. The checks below are the runner's share of the exclusion and gate
// design; the cloud's own IAM and the manager's database remain the boundaries that do not trust it.
//
// What it enforces, each with its own test and negative control:
//
//   - tofu only. The binary must identify itself as OpenTofu at the pinned version; there is no
//     fallback to a `terraform` binary, and a binary named tofu that is Terraform is refused.
//   - writer exclusion. No process starts before the Fence admitted the run, and the Fence's
//     adapter (package slotfence) is the writer slot, so a second writer is refused by the
//     database, not by this package.
//   - identity classes. A run's kind fixes its class (apply or destroy); a credential of the other
//     class is refused before any process exists, and the child environment is built from scratch
//     so the other class's credential cannot ride along from the parent process.
//   - the plan gate. Plan output is reduced to canonical facts (never values) and judged per kind;
//     a plan the gate refuses is never retained, so there is nothing to apply.
//   - saved-plan staleness. An apply is refused when the state's lineage or serial moved since the
//     plan, when the plan file changed, when a fresh plan's facts differ from the approved ones, and
//     when the plan was already applied once.
//   - secrets and state stay out of every error and log: the child sees a minimal environment,
//     output is bounded, and known secret values are scrubbed from the one diagnostic tail returned.
package tofurun

// PinnedVersion is the one OpenTofu version the runner image carries and the premise record binds
// to. A floor in the roots' required_version cannot say "never two minors on one state"; this does.
const PinnedVersion = "1.10.10"
