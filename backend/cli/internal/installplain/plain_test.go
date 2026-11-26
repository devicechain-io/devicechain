// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installplain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/fatih/color"
)

func init() {
	// The bytes below are the non-terminal ones the goldens pin; force them whatever
	// the test binary's stdout is.
	color.NoColor = true
}

func check(v installflow.CheckVerdict, text, fix string) installflow.PreflightObservation {
	return installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: v,
		Text: installflow.Text(text), Fix: installflow.Text(fix)}
}

// Each observation renders exactly as the doctor printed it before it reported
// through observations.
func TestRenderObservationIsTheDoctorsOutput(t *testing.T) {
	for _, tc := range []struct {
		o    installflow.PreflightObservation
		want string
	}{
		{installflow.PreflightObservation{Observation: installflow.ObserveSection, Section: SectionTooling}, "\n🧰 Tooling\n"},
		{check(installflow.CheckPass, "docker (/fake/docker)", ""), "  ✅ docker (/fake/docker)\n"},
		{check(installflow.CheckInfo, "no current-context set yet", ""), "  • no current-context set yet\n"},
		{installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: installflow.CheckInfo,
			Text: installflow.Text("current-context: "), Highlight: installflow.Text("kind-lab")}, "  • current-context: kind-lab\n"},
		{check(installflow.CheckWarn, "cgroup v1 detected", "kind prefers cgroup v2"),
			"  ⚠️ cgroup v1 detected\n     ↳ kind prefers cgroup v2\n"},
		{check(installflow.CheckFail, "engine CPUs: 2", "give Docker >= 4 CPUs"),
			"  ❌ engine CPUs: 2\n     ↳ give Docker >= 4 CPUs\n"},
		{installflow.PreflightObservation{Observation: installflow.ObserveSummary, Fails: 2},
			"\nPreflight: 2 failure(s), 0 warning(s) — fix failures before bootstrap.\n"},
		{installflow.PreflightObservation{Observation: installflow.ObserveSummary, Warns: 1},
			"\nPreflight: ready, with 1 warning(s) — review above.\n"},
		{installflow.PreflightObservation{Observation: installflow.ObserveSummary},
			"\nPreflight: all checks passed. ✨\n"},
	} {
		var b bytes.Buffer
		RenderObservation(&b, tc.o)
		if b.String() != tc.want {
			t.Errorf("%+v rendered %q, want %q", tc.o, b.String(), tc.want)
		}
	}
}

// Every section the doctor names has a heading, and no two share one.
func TestEverySectionHasItsOwnHeading(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range []installflow.MessageCode{SectionTooling, SectionDockerEngine, SectionPorts,
		SectionKubeContexts, SectionKubeServerVersion, SectionKernel, SectionDisk, SectionWSL, SectionSystem} {
		h, ok := sectionHeadings[code]
		if !ok {
			t.Errorf("section %s has no heading", code)
		}
		if seen[h.title] {
			t.Errorf("heading %q used twice", h.title)
		}
		seen[h.title] = true
	}
}

// A verdict or section with no wording is shown, not dropped.
func TestUnknownObservationsAreShown(t *testing.T) {
	var b bytes.Buffer
	RenderObservation(&b, check(installflow.CheckUnknown, "docker CPUs", "could not be read"))
	RenderObservation(&b, installflow.PreflightObservation{Observation: installflow.ObserveSection, Section: "preflight.new"})
	if !strings.Contains(b.String(), "UNKNOWN docker CPUs") || !strings.Contains(b.String(), "preflight.new") {
		t.Errorf("rendered %q", b.String())
	}
}

func TestResponderAnswersFromALine(t *testing.T) {
	q := installflow.Question{Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
		Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}}
	for _, tc := range []struct {
		in      string
		yes     bool
		wantErr error
	}{
		{"y\n", true, nil}, {"YES\n", true, nil}, {" yes \n", true, nil},
		{"n\n", false, nil}, {"\n", false, nil}, {"yes please\n", false, nil},
		{"", false, installflow.ErrPromptUnavailable}, {"y", false, installflow.ErrPromptUnavailable},
	} {
		var out bytes.Buffer
		resp, err := Responder{Out: &out, In: strings.NewReader(tc.in)}.Respond(context.Background(), q)
		if !errors.Is(err, tc.wantErr) || (err == nil && resp.Confirmed != tc.yes) {
			t.Errorf("%q: confirmed %v, err %v", tc.in, resp.Confirmed, err)
		}
		if want := `No kind cluster "devicechain" found. Create it now? [y/N]: `; out.String() != want {
			t.Errorf("%q: asked %q, want %q", tc.in, out.String(), want)
		}
	}
}

func TestResponderRefusesAQuestionItCannotWord(t *testing.T) {
	var out bytes.Buffer
	r := Responder{Out: &out, In: strings.NewReader("y\n")}
	for _, q := range []installflow.Question{
		{Kind: installflow.PromptConfirm, Question: "install.something-new"},
		{Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster},
		{Kind: installflow.PromptSecret, Question: installflow.QuestionCreateKindCluster},
	} {
		if resp, err := r.Respond(context.Background(), q); err == nil || resp.Confirmed {
			t.Errorf("%+v answered %+v", q, resp)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a question with no wording printed %q", out.String())
	}
}

// slowSink delays the writes the hub's sink makes, which is what lets a console write
// that does not wait overtake them.
type slowOut struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *slowOut) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("✅")) {
		time.Sleep(50 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

type funcEngine func(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error)

func (f funcEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	return f(ctx, req, rt)
}

var okSummary = installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordInstalled,
	Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased, PartialState: installflow.EffectApplied}

// 🔴 WHAT THE ENGINE PRINTS NEVER OVERTAKES WHAT IT REPORTED FIRST. The preflight's
// lines are rendered from events on the hub's goroutine; the engine's own words go
// straight to the console. A slow first render must still come out first.
func TestPresenterKeepsRenderedEventsAheadOfLaterConsoleOutput(t *testing.T) {
	out := &slowOut{}
	eng := funcEngine(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		ev := func(e installflow.Event) {
			if err := rt.Events.Emit(e); err != nil {
				t.Error(err)
			}
		}
		ev(installflow.Event{Payload: installflow.RunStarted{Manifest: []installflow.ManifestStep{
			{ID: installflow.StepInstallPreflight}}, Target: installflow.Target{Mode: "managed-kind"}}})
		ev(installflow.Event{Step: installflow.StepInstallPreflight, Attempt: 1,
			Payload: installflow.StepStarted{Message: "install.preflight"}})
		ev(installflow.Event{Step: installflow.StepInstallPreflight, Attempt: 1,
			Payload: check(installflow.CheckPass, "first", "")})
		_, _ = rt.Console.Stdout.Write([]byte("second\n"))
		ev(installflow.Event{Step: installflow.StepInstallPreflight, Attempt: 1,
			Payload: installflow.StepFinished{Outcome: installflow.StepUnchanged}})
		return okSummary, rt.Events.Finish(okSummary)
	})
	err := Presenter{Stdout: out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}.Run(
		context.Background(), eng, installflow.Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.buf.String(), "  ✅ first\nsecond\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// The engine's error comes back with its own words, for the command to print as it
// always has, and unwraps to it.
func TestPresenterReturnsTheEnginesOwnError(t *testing.T) {
	cause := errors.New("kind not found on PATH")
	eng := funcEngine(func(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error) {
		return installflow.Summary{Outcome: installflow.RunFailed}, cause
	})
	err := Presenter{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}.Run(
		context.Background(), eng, installflow.Request{}, nil)
	if !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Errorf("err = %v, want the engine's own", err)
	}
}

// 🔴 A REGISTERED VALUE IS REDACTED FROM TEXT OUTPUT TOO: the console the engine
// writes to, and the error the command prints (a tool's diagnostics appended to it).
func TestPresenterRedactsRegisteredValues(t *testing.T) {
	const secret = "dcCanaryPlainSecret9Xk2"
	eng := funcEngine(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.AddSecret(secret); err != nil {
			t.Fatal(err)
		}
		io.WriteString(rt.Console.Stdout, "tool says "+secret+"\n")
		io.WriteString(rt.Console.Stderr, "tool warns "+secret+"\n")
		return installflow.Summary{Outcome: installflow.RunFailed}, errors.New("apply failed\nError: " + secret)
	})
	var stdout, stderr bytes.Buffer
	err := Presenter{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}.Run(
		context.Background(), eng, installflow.Request{}, nil)
	for where, text := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "error": fmt.Sprint(err)} {
		if strings.Contains(text, secret) || !strings.Contains(text, "[redacted]") {
			t.Errorf("%s: %q", where, text)
		}
	}
}

// An engine that says nothing went wrong but did not close as a success is not let
// through as one.
func TestPresenterRefusesASilentNonSuccess(t *testing.T) {
	eng := funcEngine(func(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error) {
		return installflow.Summary{Outcome: installflow.RunCancelled}, nil
	})
	err := Presenter{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}.Run(
		context.Background(), eng, installflow.Request{}, nil)
	if err == nil {
		t.Error("a cancelled run returned no error")
	}
}

// The prompt goes through the broker to the plain responder, on the console, and the
// broker's events carry only the disposition.
func TestPresenterAsksOnTheConsoleAndReadsStdin(t *testing.T) {
	var out bytes.Buffer
	var answer installflow.Answer
	eng := funcEngine(func(ctx context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.Emit(installflow.Event{Payload: installflow.RunStarted{Manifest: []installflow.ManifestStep{
			{ID: installflow.StepInstallCluster}}, Target: installflow.Target{Mode: "managed-kind"}}}); err != nil {
			return installflow.Summary{}, err
		}
		var err error
		answer, err = rt.Prompts.Ask(ctx, installflow.Prompt{Kind: installflow.PromptConfirm,
			Question:      installflow.QuestionCreateKindCluster,
			Params:        []installflow.Param{{Name: "cluster", Value: installflow.Text("lab")}},
			YesResolvable: true})
		if err != nil {
			return installflow.Summary{}, err
		}
		return okSummary, rt.Events.Finish(okSummary)
	})
	err := Presenter{Stdout: &out, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("y\n")}.Run(
		context.Background(), eng, installflow.Request{}, nil)
	if err != nil || !answer.Confirmed {
		t.Fatalf("err %v, answer %+v", err, answer)
	}
	if want := `No kind cluster "lab" found. Create it now? [y/N]: `; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}
