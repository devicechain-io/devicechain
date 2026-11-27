// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installjson"
	"github.com/spf13/pflag"
)

func isJSONScenario(sc installScenario) bool {
	for _, a := range sc.args {
		if a == "--help" || a == "-h" {
			return false // help is not a run: it writes no stream
		}
	}
	for i, a := range sc.args {
		if a == "--output=json" || a == "-ojson" ||
			((a == "-o" || a == "--output") && i+1 < len(sc.args) && sc.args[i+1] == "json") {
			return true
		}
	}
	return false
}

// TestInstallJSONStreamsAreStrict runs every --output json scenario and holds its stdout
// to the stream's contract, whatever the golden says: every byte is part of a line,
// every line decodes as exactly one event envelope (no escape sequence, no stray text,
// no partial line), the stream passes the protocol's own invariants, it ends with its
// one run.summary, the exit status is the summary's, and no secret canary is anywhere.
func TestInstallJSONStreamsAreStrict(t *testing.T) {
	n := 0
	for _, sc := range append(append([]installScenario{}, installScenarios...), installCheckScenarios...) {
		if !isJSONScenario(sc) {
			continue
		}
		n++
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			r := runInstallChildRaw(t, sc)
			if len(r.stdout) == 0 {
				t.Fatal("stdout is empty: a stream with no summary is an unknown outcome")
			}
			envs, err := installjson.DecodeStream(r.stdout)
			if err != nil {
				t.Fatalf("stdout is not strict NDJSON: %v\nstdout:\n%s", err, r.stdout)
			}
			if err := testkit.VerifyStream(envs); err != nil {
				t.Fatalf("stream breaks the protocol: %v", err)
			}
			last := envs[len(envs)-1]
			sum, ok := last.Payload.(installflow.Summary)
			if !ok {
				t.Fatalf("the last line is %s, not run.summary", last.Kind)
			}
			if r.exit != sum.ExitCode() {
				t.Errorf("exit %d, but the summary says %s (exit %d)", r.exit, sum.Outcome, sum.ExitCode())
			}
			for _, e := range envs {
				if e.RunID != installGoldenRunID || e.Operation != installflow.OperationInstall {
					t.Errorf("event %d: run %q operation %q", e.Seq, e.RunID, e.Operation)
				}
			}
			// Nothing a consumer parses may arrive on stderr, and nothing human on stdout.
			if bytes.Contains(r.stderr, []byte(`"schema":`)) {
				t.Errorf("an event reached stderr:\n%s", r.stderr)
			}
			testkit.AssertNoCanary(t, "stdout", r.stdout)
			testkit.AssertNoCanary(t, "stderr", r.stderr)
		})
	}
	if n < 8 {
		t.Fatalf("only %d --output json scenarios: the strict check is checking too little", n)
	}
}

// The stream is non-interactive: a "y" waiting on stdin is never read. The run is
// refused at the kind prompt with the documented code, before it created anything —
// where text output reads that "y" and goes on to look for kind.
func TestInstallJSONNeverReadsStdin(t *testing.T) {
	var sc installScenario
	for _, s := range installScenarios {
		if s.name == "json-kind-refused" {
			sc = s
		}
	}
	if sc.stdin != "y\n" {
		t.Fatalf("scenario json-kind-refused must offer a \"y\" on stdin, has %q", sc.stdin)
	}
	r := runInstallChildRaw(t, sc)
	envs, err := installjson.DecodeStream(r.stdout)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []installflow.Kind
	var failed *installflow.StepFailed
	var disposition installflow.PromptDisposition
	for _, e := range envs {
		kinds = append(kinds, e.Kind)
		switch p := e.Payload.(type) {
		case installflow.StepFailed:
			failed = &p
			if e.StepID != installflow.StepInstallCluster {
				t.Errorf("failed at %s, want %s", e.StepID, installflow.StepInstallCluster)
			}
		case installflow.PromptResolved:
			disposition = p.Disposition
		}
	}
	if disposition != installflow.PromptUnavailable {
		t.Errorf("the prompt resolved %q, want %q (kinds %v)", disposition, installflow.PromptUnavailable, kinds)
	}
	if failed == nil {
		t.Fatalf("no step.failed in %v", kinds)
	}
	if failed.Code != installflow.ErrorPromptUnavailable || failed.Remedy != installflow.RemedyRerunWithYes ||
		failed.Effect != installflow.EffectNoneObserved || failed.Retry != installflow.RetryPermanent {
		t.Errorf("step.failed %+v, want code %s, remedy %s, effect none_observed, retry permanent",
			*failed, installflow.ErrorPromptUnavailable, installflow.RemedyRerunWithYes)
	}
	sum := envs[len(envs)-1].Payload.(installflow.Summary)
	if sum.PartialState != installflow.EffectNoneObserved || sum.Outcome != installflow.RunFailed {
		t.Errorf("summary %+v, want failed with nothing changed", sum)
	}
	if strings.Contains(string(r.stderr), "kind not found") || r.engine != "not called\n" {
		t.Errorf("the run went past the prompt:\nstderr: %s\nengine: %s", r.stderr, r.engine)
	}
	if strings.Contains(string(r.stdout)+string(r.stderr), "[y/N]") {
		t.Error("the stream asked the question on a terminal")
	}
}

// --yes answers the one confirmation an install asks: no prompt is raised at all, and
// the run goes on to the cluster work (here, to the missing kind binary).
func TestInstallJSONAssumeYesAnswersTheConfirmation(t *testing.T) {
	var sc installScenario
	for _, s := range installScenarios {
		if s.name == "json-kind-assume-yes" {
			sc = s
		}
	}
	r := runInstallChildRaw(t, sc)
	envs, err := installjson.DecodeStream(r.stdout)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range envs {
		if e.Kind == installflow.KindPromptRequested {
			t.Errorf("--yes still raised a prompt: %+v", e.Payload)
		}
		if f, ok := e.Payload.(installflow.StepFailed); ok && f.Code == installflow.ErrorPromptUnavailable {
			t.Errorf("--yes was refused as unanswered: %+v", f)
		}
	}
}

// A command refused before its run starts still ends its stream with one failed
// summary, naming the inputs as the step that failed, and changing nothing.
func TestInstallJSONRefusalBeforeTheRun(t *testing.T) {
	var sc installScenario
	for _, s := range installScenarios {
		if s.name == "json-refuse-no-tls-without-compact" {
			sc = s
		}
	}
	r := runInstallChildRaw(t, sc)
	envs, err := installjson.DecodeStream(r.stdout)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Fatalf("%d events, want the one summary", len(envs))
	}
	sum, ok := envs[0].Payload.(installflow.Summary)
	if !ok || sum.Outcome != installflow.RunFailed || sum.FailedStep != installflow.StepInstallInputs ||
		sum.PartialState != installflow.EffectNoneObserved {
		t.Errorf("got %+v", envs[0].Payload)
	}
	if r.exit != 1 || !strings.Contains(string(r.stderr), "--no-tls") {
		t.Errorf("exit %d, stderr %q: want 1 and the refusal's own words", r.exit, r.stderr)
	}
}

// An --output that is neither text nor json is refused before anything runs, with no
// stream: there is no stream format to write one in.
func TestInstallOutputRejectsUnknownFormat(t *testing.T) {
	prev := installOutput
	t.Cleanup(func() { installOutput = prev })
	installOutput = "yaml"
	s, err := beginInstallOutput(installCmd.Flags())
	if err == nil || s != nil || activeInstallStream != nil {
		t.Fatalf("got %v, %v", s, err)
	}
}

// --output json with the terminal UI is refused before anything runs; any other --ui
// value, and no --ui at all, is not.
func TestInstallJSONRefusesTheTerminalUI(t *testing.T) {
	for _, tc := range []struct {
		ui      *string
		refused bool
	}{{nil, false}, {ptr("plain"), false}, {ptr("auto"), false}, {ptr("tui"), true}} {
		fs := pflag.NewFlagSet("install", pflag.ContinueOnError)
		if tc.ui != nil {
			fs.String("ui", "plain", "")
			if err := fs.Parse([]string{"--ui=" + *tc.ui}); err != nil {
				t.Fatal(err)
			}
		}
		if err := refuseTerminalUIWithStream(fs); (err != nil) != tc.refused {
			t.Errorf("--ui %v: got %v, refused want %t", tc.ui, err, tc.refused)
		}
	}
}

func ptr(s string) *string { return &s }
