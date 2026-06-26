// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fatih/color"
)

// DefaultImageRegistry is where published images live.
const DefaultImageRegistry = "ghcr.io/devicechain-io"

// LocalRegistry is the default registry for the developer build-from-source path
// (a registry:2 container reachable from host and the kind network as
// localhost:5000). Mirrors deploy/local.
const LocalRegistry = "localhost:5000"

// DefaultImageVersion is the published image tag deployed by default. It is the
// single source of truth from the repo-root VERSION file, injected via ldflags
// at build time (the Makefile stamps the same value into cmd.Version, so
// `dcctl version` and the default image version always match). "dev" is the
// unstamped fallback for plain `go build`.
var DefaultImageVersion = "dev"

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
	Values        map[string]string
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
