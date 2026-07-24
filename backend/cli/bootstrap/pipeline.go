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

// DefaultImageVersion is the published image tag deployed by default: the
// repo-root VERSION file value, injected via ldflags at build time. "dev" is the
// unstamped fallback for a plain `go build`.
//
// This must remain a tag that RESOLVES IN A REGISTRY, which is why it is not
// simply cmd.Version. A dev build's cmd.Version carries a build stamp
// ("0.0.1-dev.20260716T155833Z") to distinguish it from every other build of the
// same VERSION; that value names no image that has ever been pushed. The two
// therefore differ by design for dev builds and coincide only for releases —
// `dcctl version` prints both. Stamping cmd.Version's value here would send
// bootstrap after a nonexistent tag; IsUnpublishedImageVersion is the backstop.
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
	// GrafanaSSO wires Grafana login to DeviceChain SSO (ADR-047). See Options.
	GrafanaSSO bool
	// Compact applies the small-footprint preset (compactSizing). See Options.
	Compact bool
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
	Values          map[string]string
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
		fmt.Println(GreenUnderline(fmt.Sprintf("\n[%d/%d] %s", i+1, total, step.Name)))
		if err := step.Run(ctx, st); err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}
	return nil
}

// GreenUnderline mirrors the cmd package house style for section headers.
var GreenUnderline = color.New(color.Underline, color.FgHiGreen).SprintFunc()

// NewDefaultPipeline returns the MVP Phase-B steps in execution order.
func NewDefaultPipeline() Pipeline {
	return Pipeline{Steps: []Step{
		{Name: "Render configuration", Run: stepRenderConfig},
		{Name: "Ensure local registry", Run: stepLocalRegistry},
		{Name: "Apply infrastructure", Run: stepInfraApply},
		{Name: "Install core components", Run: stepInstallCore},
		{Name: "Install instance (Helm)", Run: stepHelmInstall},
		{Name: "Seed admin credential", Run: stepSeedAdmin},
		{Name: "Wait for readiness", Run: stepWaitReady},
		{Name: "Report access info", Run: stepReport},
	}}
}
