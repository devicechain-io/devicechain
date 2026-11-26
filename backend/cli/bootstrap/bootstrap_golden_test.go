// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// 🔴 THESE GOLDENS ARE A CONTRACT, NOT A RECORDING. They characterise what a bootstrap's
// pipeline prints on stdout — the numbered step banners, what each step says, and the
// closing access report with its superuser block, including the one line that shows the
// generated seed password — and the error a failed step ends the run with. They were
// captured BEFORE the pipeline ran inside an engine, from Pipeline.Run writing to the
// process's stdout, so a change to one in a PR that set out to change no output is that
// PR's bug. Rewrite (-update) only when the output is MEANT to change.
//
// The steps are stand-ins that each print one line through the same helpers the real
// steps print through, under the real step names; the report is the real stepReport.
// The command-layer half (argv to the cluster) is cmd/testdata/bootstrap_golden.
var updateBootstrapGoldens = flag.Bool("update", false, "rewrite bootstrap/testdata/bootstrap_golden")

const bootstrapPipelineGoldenDir = "testdata/bootstrap_golden"

// goldenSeed is the generated superuser password the report shows once. It is not a
// canary: showing it is the report's job, and the canary harness asserts the reveal
// channel is the ONLY place it appears.
const goldenSeed = "golden-seed-pw-4Kq9"

func goldenBootstrapState(mutate func(*State)) *State {
	st := &State{
		Instance:    "acme",
		KubeContext: "kind-devicechain",
		Profile:     "default",
		Values: map[string]string{
			"instance": "acme", "namespace": "dci-acme", "profile": "default",
			"imageSource": "published ghcr.io/devicechain-io @ v1.9.0",
			"ingressHost": "devicechain.local", "scheme": "https",
		},
		Credentials:   &credentialSet{SuperuserPassword: goldenSeed},
		SuperuserSeed: superuserSeedMinted,
		Escrow:        EscrowPlan{Path: "/home/op/.devicechain/escrow/acme-rootkey.escrow"},
	}
	if mutate != nil {
		mutate(st)
	}
	return st
}

// goldenPipeline is NewDefaultPipeline's steps, by name and in order, with stand-ins for
// everything but the report. failAt names a step that fails instead.
func goldenPipeline(t *testing.T, failAt string) Pipeline {
	t.Helper()
	var p Pipeline
	for _, s := range NewDefaultPipeline().Steps {
		name := s.Name
		if name == "Report access info" {
			p.Steps = append(p.Steps, Step{Name: name, Run: stepReport})
			continue
		}
		p.Steps = append(p.Steps, Step{Name: name, Run: func(ctx context.Context, st *State) error {
			return goldenStandIn(ctx, st, name, name == failAt)
		}})
	}
	return p
}

type bootstrapPipelineScenario struct {
	name   string
	mutate func(*State)
	failAt string
}

var bootstrapPipelineScenarios = []bootstrapPipelineScenario{
	{name: "pipeline-seed-minted"},
	{name: "pipeline-seed-recovered", mutate: func(st *State) { st.SuperuserSeed = superuserSeedRecovered }},
	{name: "pipeline-over-live-instance", mutate: func(st *State) {
		st.SuperuserSeed, st.OverLiveInstance = superuserSeedRecovered, true
	}},
	{name: "pipeline-seed-absent", mutate: func(st *State) {
		st.SuperuserSeed, st.OverLiveInstance, st.Credentials = superuserSeedAbsent, true, nil
	}},
	{name: "pipeline-restored-from-escrow", mutate: func(st *State) {
		st.Escrow = EscrowPlan{RestoredFrom: "/home/op/acme-rootkey.escrow"}
	}},
	{name: "pipeline-no-escrow-plain-http", mutate: func(st *State) {
		st.Escrow = EscrowPlan{}
		st.Values["scheme"], st.Values["ingressHost"] = "http", "localhost"
	}},
	{name: "pipeline-dry-run", mutate: func(st *State) {
		st.DryRun, st.Credentials, st.SuperuserSeed = true, nil, superuserSeedUnsettled
	}},
	{name: "pipeline-step-fails", failAt: "Apply infrastructure"},
}

func TestBootstrapPipelineGoldens(t *testing.T) {
	noColor := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = noColor }()
	for _, sc := range bootstrapPipelineScenarios {
		t.Run(sc.name, func(t *testing.T) {
			stdout, err := runPipelineForGolden(t, goldenBootstrapState(sc.mutate), goldenPipeline(t, sc.failAt))
			var b bytes.Buffer
			writePipelineGoldenSection(&b, "stdout", []byte(stdout))
			errText := "<nil>"
			if err != nil {
				errText = err.Error()
			}
			writePipelineGoldenSection(&b, "error", []byte(errText))
			comparePipelineGolden(t, filepath.Join(bootstrapPipelineGoldenDir, sc.name+".txt"), b.Bytes())
		})
	}
}

// errGoldenStep is what a failing stand-in returns.
var errGoldenStep = errors.New("the stand-in step failed")

func writePipelineGoldenSection(out *bytes.Buffer, name string, b []byte) {
	fmt.Fprintf(out, "-- %s (%d bytes) --\n", name, len(b))
	out.Write(b)
	out.WriteString("\n")
}

func comparePipelineGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateBootstrapGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it, and review what it captured)", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	gl, wl := strings.SplitAfter(string(got), "\n"), strings.SplitAfter(string(want), "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("%s differs from the golden at line %d:\n got: %q\nwant: %q\n\nfull output:\n%s", path, i+1, g, w, got)
		}
	}
	t.Fatalf("%s differs from the golden", path)
}
