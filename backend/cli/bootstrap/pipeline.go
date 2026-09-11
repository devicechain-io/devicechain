// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/fatih/color"
)

// DefaultImageRegistry is where published images live.
const DefaultImageRegistry = "ghcr.io/devicechain-io"

// LocalRegistry is the default registry for the developer build-from-source path
// (a registry:2 container reachable from host and the kind network as
// localhost:5000). Mirrors deploy/local.
const LocalRegistry = "localhost:5000"

// DefaultIngressHost is the host the instance ingress is exposed on. It matches
// the chart's ingress.host default; the pipeline sets it explicitly so the
// access report can print a real URL instead of a placeholder. (A future --host
// flag / gcp provider can override this through State.)
const DefaultIngressHost = "devicechain.local"

// DefaultImageVersion is the published image tag deployed by default, injected
// via ldflags by GORELEASER — and only by goreleaser, from the git tag it is
// building. "dev" is the fallback for every other build.
//
// 🔑 The fallback is the normal case for anything built locally, and that is
// deliberate. This must name a tag that RESOLVES IN A REGISTRY, and a working
// tree corresponds to no such tag: publishing is what a release does. So a
// locally built dcctl has no default, refuses the published path outright, and
// says to pass --version <tag> or --build. The makefile used to stamp the
// repo-root VERSION file here, which reads 0.0.1 and named an image that has
// never been pushed — an ImagePullBackOff on every workload, minutes into a
// bootstrap that looked healthy, from a value shaped exactly like a real release
// so IsUnpublishedImageVersion could not catch it.
//
// This is also why it is not simply cmd.Version. A local build's cmd.Version
// carries a build stamp ("0.0.1-dev.20260716T155833Z") so two binaries months
// apart can be told apart; that value names no image either. The two differ by
// design and `dcctl version` prints both, so the pairing stays visible rather
// than implied by their being equal.
var DefaultImageVersion = "dev"

// IsUnpublishedImageVersion reports whether tag names an image that the
// published registry cannot have: the unstamped "dev" fallback, or a dev build
// stamp that only ever named a locally-built image.
//
// This guards the seam between two values that must not be conflated — see
// DefaultImageVersion. It is enforced at the point of consumption because the
// mistake it catches is made in the build tooling (the Makefile ldflags), which
// no Go test executes.
func IsUnpublishedImageVersion(tag string) bool {
	return tag == "dev" || strings.Contains(tag, "-dev.")
}

// State threads data between pipeline steps. Values is populated as the
// pipeline runs (generated passwords, endpoints, admin cred, etc.).
type State struct {
	Instance    string
	KubeContext string
	Profile     string
	DryRun      bool
	AssumeYes   bool
	// Image source. By default the pipeline pulls published images
	// (ImageRegistry/<area>:ImageVersion); BuildImages flips to the developer
	// path of building from source into a local registry.
	ImageRegistry string
	ImageVersion  string
	BuildImages   bool
	// IngressHost is the host the instance ingress is exposed on; NoTLS serves
	// plain HTTP instead of a self-signed cert. See Options for the UX rationale.
	IngressHost string
	NoTLS       bool
	// NoMonitoring skips the kube-prometheus-stack install in the infra apply
	// (default-on, like Postgres/Timescale). See Options for the rationale.
	NoMonitoring bool
	// NoCNPG skips the CloudNativePG operator + backup plugin in the infra apply
	// (default-on, ADR-020 A2). See Options for the rationale.
	NoCNPG bool
	// AllowLegacyDbRemoval passes the cutover-guard escape hatch through to
	// OpenTofu (ADR-020 A2.3/A2.4).
	//
	// 🔴 It exists because without it the hatch is UNREACHABLE through the
	// supported path. dcctl re-extracts the OpenTofu root into the instance's
	// working directory on every run and passes a fixed set of -var flags, so an
	// operator told by the guard to "set allow_legacy_tsdb_removal = true" had
	// nowhere to set it. On a local cluster the other branch works (destroy and
	// rebuild); on a real one there was no route past the guard at all.
	AllowLegacyDbRemoval bool
	// GrafanaSSO wires Grafana login to DeviceChain SSO (ADR-047). See Options.
	GrafanaSSO bool
	// Compact applies the small-footprint preset (compactSizing). See Options.
	Compact bool
	// HA applies the ADR-020 messaging topology (haTopology). See Options.
	HA bool
	// EnableAreas is the raw extra areas requested via --enable-area (the delta over
	// the profile), kept for an honest access-report label. EnabledAreas is the
	// resolved+validated explicit set (profile ∪ extras), or nil when no extra area
	// was requested; when set, helmValues emits it as enabledFunctionalAreas IN PLACE
	// OF profile (the chart treats the two as mutually exclusive). See
	// ResolveEnabledAreas.
	EnableAreas  []string
	EnabledAreas []string
	// Lwm2mIdentities are the provisioned LwM2M DTLS-PSK credentials (--lwm2m-identities):
	// helmValues renders them into a chart-owned Secret + lwm2m-ingest config. Setting
	// this implies lwm2m-ingest ∈ EnabledAreas. Empty when the flag is unused.
	Lwm2mIdentities []Lwm2mIdentity
	// Escrow is the settled root-key plan (ADR-059 / ADR-028): where the key comes
	// from and where its second copy goes. Resolved in the command layer, BEFORE any
	// cluster exists, so a missing passphrase or an unopenable restore artifact
	// fails in the first second rather than behind ten minutes of spin-up. The zero
	// value writes nothing, which is what every test and every internal pipeline
	// construction wants.
	Escrow EscrowPlan
	// Restore is the settled database-restore intent (ADR-028 / ADR-020 A2.5):
	// which archive each store recovers FROM, and how far it replays. Resolved in
	// the command layer for the same reason as Escrow — every way it can be wrong is
	// knowable from argv, and an incident is the wrong time to learn it. The zero
	// value is an ordinary install.
	Restore RestorePlan
	// DcctlVersion is the build that is running, recorded on the declaration as
	// provenance. Set in the command layer from cmd.Version; empty in tests and in
	// internal pipeline constructions, which record nothing rather than guessing.
	DcctlVersion string
	// Binding is the cluster this run resolved to, and Provider is what resolved
	// it. Both are settled by EnsureCluster in the command layer, and both are
	// recorded on the declaration — so the pipeline carries them rather than
	// re-deriving them, which is the derivation that was wrong for every instance
	// bootstrapped with --kube-context.
	Binding  ClusterBinding
	Provider string
	// OperatorNamespace is where the operator overlay puts itself, read from the
	// rendered manifests rather than assumed. The cluster lock lives here too, so
	// this is set before the claim step runs.
	OperatorNamespace string
	// Claim is the lock this run holds on the cluster, taken by the claim step and
	// released by the command layer. Nil on a dry run and in tests, which take no
	// lock because they write nothing.
	Claim  *Claim
	Values map[string]string
}

// Step is a single named unit of bootstrap work.
type Step struct {
	Name string
	Run  func(ctx context.Context, st *State) error
}

// Pipeline is an ordered list of steps executed sequentially.
type Pipeline struct {
	Steps []Step
}

// Run executes each step in order, printing a numbered banner per step, and
// returns on the first error wrapping it with the failing step's name.
func (p Pipeline) Run(ctx context.Context, st *State) error {
	total := len(p.Steps)
	for i, step := range p.Steps {
		// 🔴 THE FENCE LIVES IN THE LOOP, NOT IN THE STEPS, and that placement is
		// the point. Once the claim is held, every step boundary is checked by
		// construction — including the boundary before a step somebody adds later,
		// who would otherwise have to know to write the check. A guard that each
		// new call site must remember is a guard with a hole in it the first time
		// somebody forgets.
		//
		// 🔴 WHAT THIS DOES NOT DO, STATED IN FULL BECAUSE THE HONEST NUMBER IS
		// UNCOMFORTABLE: it cannot stop a step already running. A reclaim that
		// lands one second into "Apply infrastructure" is not acted on until that
		// step returns, and that step is a single tofu apply whose helm_release
		// resources carry timeouts of 600–900 seconds EACH, run sequentially,
		// followed by a second apply. The exposure is therefore the remainder of
		// the current step — tens of minutes in the worst case — and NOT the
		// renewal interval. An earlier draft of this design claimed a ten-second
		// bound; it was reading the detection latency and calling it the window.
		//
		// Closing it means cancelling the run's context mid-step. That is safe for
		// tofu (tfexec sets cmd.Cancel to SIGINT, so tofu stops gracefully and
		// writes its state) but not yet for the in-process Helm install, which on
		// cancellation can leave the release in pending-upgrade with no resume path
		// in this tree. Until that path exists, the window is bounded by the
		// reclaim ceremony instead: a reclaim cannot happen until the lock has gone
		// a full lease duration untouched, and a human has typed the holder back.
		if st.Claim != nil {
			if err := st.Claim.CheckHeld(ctx); err != nil {
				return fmt.Errorf("stopping before %q: %w", step.Name, err)
			}
		}
		fmt.Println(GreenUnderline(fmt.Sprintf("\n[%d/%d] %s", i+1, total, step.Name)))
		if err := step.Run(ctx, st); err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}
	return nil
}

// GreenUnderline mirrors the cmd package house style for section headers.
var GreenUnderline = color.New(color.Underline, color.FgHiGreen).SprintFunc()

// NewDefaultPipeline returns the bootstrap steps in execution order.
//
// 🔴 THE ORDER IS THE DESIGN, AND THREE OF ITS EDGES ARE LOAD-BEARING (ADR-080).
// EnsureCluster runs before all of this, in the command layer, so every step
// below may assume a reachable cluster.
//
//  1. The CRDs and the operator go in FIRST, ahead of the infrastructure apply
//     they used to follow. An instance is declared in the cluster now, so the
//     Instance CRD has to exist before anything can write the declaration — and
//     the thing that writes it is the next step. Nothing blocks the move: the
//     operator overlay creates its own namespace, its webhook/cert-manager
//     patches are not enabled, and it consumes nothing OpenTofu produces.
//
//  2. The local registry goes in AHEAD of that, which is the one place this
//     order departs from the sequence ADR-080 wrote down. Installing the
//     operator means applying a Deployment that names an image, and on the
//     --build path stepLocalRegistry is what builds and pushes that image. Put
//     the operator first and a developer bootstrap installs a Deployment
//     pointing at a registry that does not exist yet: it recovers on its own
//     once the push lands, but only after a pull-backoff long enough to look
//     like a broken install. Registry-first costs nothing — it needs only the
//     cluster — and the CRD still lands before the claim.
//
//  3. Render still precedes the infrastructure apply, because the broker
//     credentials it mints must be recorded before OpenTofu configures the
//     broker with them (see broker_record.go, and the test that pins it).
//
// The image source is settled before any of this, in the command layer — see
// ResolveImageSource. Step 2 always consumes it and step 1 does on --build, and
// neither can wait for the render step to fill it in.
func NewDefaultPipeline() Pipeline {
	return Pipeline{Steps: []Step{
		{Name: "Ensure local registry", Run: stepLocalRegistry},
		{Name: "Install core components", Run: stepInstallCore},
		{Name: "Claim and declare the instance", Run: stepClaimAndDeclare},
		{Name: "Render configuration", Run: stepRenderConfig},
		{Name: "Apply infrastructure", Run: stepInfraApply},
		{Name: "Install instance (Helm)", Run: stepHelmInstall},
		{Name: "Seed admin credential", Run: stepSeedAdmin},
		{Name: "Wait for readiness", Run: stepWaitReady},
		{Name: "Report access info", Run: stepReport},
	}}
}

// ImageSource is the settled answer to "which images does this run deploy" —
// where they are pulled from, at what tag, and how to say so in a report.
type ImageSource struct {
	Registry string
	Version  string
	// Label is the human-readable form the access report prints.
	Label string
}

// ResolveImageSource settles the image source from argv alone: published images
// at a pinned tag, or a local registry fed by --build.
//
// 🔴 IT RUNS IN THE COMMAND LAYER, BEFORE ANY CLUSTER EXISTS, for the same
// reason ResolveEscrowPlan and ResolveRestorePlan do — every way it can be wrong
// is knowable from argv, and the way that matters (a dcctl build carrying no
// pinned image version) used to surface only after EnsureCluster had spun a kind
// cluster up to hold the run that was never going to work.
//
// It also has to be settled before the first step that CONSUMES it, and after
// the ADR-080 reorder that is no longer the render step: the operator Deployment
// installed by stepInstallCore always names an image, and stepLocalRegistry
// builds and pushes that image on the --build path (it returns early otherwise).
// Both now run ahead of stepRenderConfig, which is where this used to live.
//
// Idempotent, and deliberately so: re-resolving an already-settled pair returns
// it unchanged, which is what lets stepRenderConfig keep calling it for the
// label without needing to know whether the command layer got there first.
func ResolveImageSource(registry, version string, build bool) (ImageSource, error) {
	if registry == "" {
		if build {
			registry = LocalRegistry
		} else {
			registry = DefaultImageRegistry
		}
	}
	if version == "" {
		if build {
			version = "dev"
		} else {
			version = DefaultImageVersion
		}
	}
	// Deploying an image tag that was never published fails as an
	// ImagePullBackOff on every workload, several minutes into a run that looked
	// healthy — so reject it here, where we can say why.
	//
	// 🔴 THE EMPTY CHECK IS NOT DEFENSIVE PADDING, and leaving it out was a real
	// hole rather than a tidiness point. `version` reaches here empty only when
	// DefaultImageVersion is itself empty, which is what a broken ldflags stamp
	// produces — and IsUnpublishedImageVersion does NOT catch it, since "" is
	// neither "dev" nor a dev stamp. Without it a published-path bootstrap sails
	// through this function, creates a cluster, and then dies inside
	// stepInstallCore saying ResolveImageSource must run before the pipeline —
	// which is both false (it did run) and late (the cluster now exists), the
	// exact failure this function was moved forward to remove. Only the published
	// path can reach it — --build defaults to the literal "dev" — but the check is
	// written unconditionally because a reference with no tag at all is read by
	// Kubernetes as :latest, whatever produced it.
	//
	// resolveOperatorImageSource carried this check and this reasoning first;
	// `dcctl upgrade` now calls through here so there is one resolver rather than
	// two that must be kept in agreement.
	if version == "" || (!build && IsUnpublishedImageVersion(version)) {
		return ImageSource{}, fmt.Errorf(
			"this dcctl build has no pinned image version (%q is not a published tag); deploy a tagged release with --version <tag>, or build from source with --build",
			version)
	}

	label := fmt.Sprintf("%s/<area>:%s (published)", registry, version)
	if build {
		label = fmt.Sprintf("built from source → %s/<area>:%s", registry, version)
	}
	return ImageSource{Registry: registry, Version: version, Label: label}, nil
}

// requireResolvedImages is the fail-loud half of the rule above. A step that
// deploys an image reference must never build one out of an empty registry or
// tag: "/operator:" is a syntactically valid reference that pulls nothing, so
// the run would proceed and die minutes later as an ImagePullBackOff naming an
// image nobody asked for. The steps that need this all run before the render
// step now, so "the render step will have filled it in" is no longer true.
func requireResolvedImages(st *State, what string) error {
	if st.ImageRegistry == "" || st.ImageVersion == "" {
		return fmt.Errorf(
			"%s needs a settled image source and has none (registry %q, version %q); "+
				"ResolveImageSource must run before the pipeline", what, st.ImageRegistry, st.ImageVersion)
	}
	return nil
}
