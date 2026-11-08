// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// AI-off (the Substrate Constitution draft, CON-7): with every AI component disabled,
// detection and REACT still decide, because no admit-or-refuse decision on the live path
// calls a model. The structural half of that is checkable here: the packages that DECIDE
// (the DETECT core, the rule compiler, REACT, the runtime, geofencing, replay preview —
// every package under internal/ except the natural-language door itself) must not link
// the inference client, the ai-inference wire contract, the service's own
// GraphQL/processor layers that host the client, or any LLM SDK.
//
// It covers internal/... BY ENUMERATION, so a new package added under internal/ is on the
// decision path by default and has to be excluded here, by name, to escape the check.
//
// Not covered, and stated so the check is not read as more than it is: the processor
// package, which runs the live pipeline, also HOSTS the inference client
// (nl_inference_client.go, used only by the authoring resolver). The live path does not
// call it, but no import listing can prove that while both live in one package.

const epModule = "github.com/devicechain-io/dc-event-processing"

// notOnTheDecisionPath are the internal/ packages that are allowed to reach a model,
// because they are the authoring door and never decide anything live.
var notOnTheDecisionPath = []string{
	epModule + "/internal/nldraft",
}

// inferenceReach are import paths (exact, or as a path prefix) a decision-path package
// must never link.
var inferenceReach = []string{
	epModule + "/internal/nldraft",
	epModule + "/processor",
	epModule + "/graphql",
	"github.com/devicechain-io/dc-microservice/aiwire",
	"github.com/devicechain-io/dc-ai-inference",
	// LLM SDKs, none linked anywhere today; listed so the first one is caught here.
	"github.com/anthropics",
	"github.com/openai",
	"github.com/sashabaranov/go-openai",
	"github.com/google/generative-ai-go",
	"google.golang.org/genai",
	"cloud.google.com/go/vertexai",
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime",
	"github.com/tmc/langchaingo",
	"github.com/ollama/ollama",
}

func reachesInference(p string) bool {
	if p == epModule {
		return true // package main wires the inference client
	}
	for _, f := range inferenceReach {
		if p == f || strings.HasPrefix(p, f+"/") {
			return true
		}
	}
	return false
}

// inferenceIn is the verdict: the packages of deps that reach a model, nil when none do.
func inferenceIn(deps []string) ([]string, error) {
	var reached []string
	for _, d := range deps {
		if reachesInference(d) {
			reached = append(reached, d)
		}
	}
	if len(reached) > 0 {
		return reached, fmt.Errorf("%d inference-reaching package(s): %v", len(reached), reached)
	}
	return nil, nil
}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list", "-f", "{{.ImportPath}}"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return strings.Fields(string(out))
}

func TestTheDecisionPathImportsNoInferenceClient(t *testing.T) {
	t.Run("the verdict finds a single planted inference package", func(t *testing.T) {
		for _, bad := range []string{
			epModule + "/processor", // the draft's planted defect: detect/core importing processor
			epModule + "/internal/nldraft",
			epModule,
			"github.com/devicechain-io/dc-microservice/aiwire",
			"github.com/anthropics/anthropic-sdk-go",
			"github.com/sashabaranov/go-openai",
		} {
			listing := []string{"fmt", epModule + "/internal/detect/core", bad, "github.com/google/cel-go/cel"}
			got, err := inferenceIn(listing)
			if err == nil || !slices.Equal(got, []string{bad}) {
				t.Errorf("inferenceIn(%v) = %v, %v; want [%s] and an error", listing, got, err, bad)
			}
		}
		// Lookalikes by prefix are not the thing itself.
		clean := []string{epModule + "/processorx", epModule + "/internal/nldraftx", "github.com/openaix/y", "github.com/devicechain-io/dc-microservice/aiwirex"}
		if got, err := inferenceIn(clean); err != nil {
			t.Errorf("inferenceIn(%v) = %v, %v; want none", clean, got, err)
		}
	})

	// The listing really follows imports to the inference client: processor hosts it, so the
	// verdict over processor's dependencies names the door and the wire contract.
	t.Run("the verdict follows the processor to the inference client", func(t *testing.T) {
		reached, err := inferenceIn(goList(t, "-deps", "./processor"))
		if err == nil {
			t.Fatal("processor's dependencies are clean; processor hosts the inference client")
		}
		for _, want := range []string{epModule + "/internal/nldraft", "github.com/devicechain-io/dc-microservice/aiwire"} {
			if !slices.Contains(reached, want) {
				t.Fatalf("processor reaches %v, which does not include %s", reached, want)
			}
		}
	})

	t.Run("the decision-path packages reach none of it", func(t *testing.T) {
		var decision []string
		for _, p := range goList(t, "./internal/...") {
			if !slices.Contains(notOnTheDecisionPath, p) {
				decision = append(decision, p)
			}
		}
		// Anchors: the DETECT core and the rule compiler must be in the set, or it is not the
		// decision path, and the absence below means nothing.
		for _, anchor := range []string{epModule + "/internal/detect/core", epModule + "/internal/rules", epModule + "/internal/react"} {
			if !slices.Contains(decision, anchor) {
				t.Fatalf("the decision-path set does not contain %s: %v", anchor, decision)
			}
		}
		deps := goList(t, append([]string{"-deps"}, decision...)...)
		if _, err := inferenceIn(deps); err != nil {
			t.Fatalf("the decision path links %v", err)
		}
	})
}
