// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

var start = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// engineFunc adapts a function to installflow.Engine.
type engineFunc func(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error)

func (f engineFunc) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	return f(ctx, req, rt)
}

func manifest() []installflow.ManifestStep {
	return []installflow.ManifestStep{{ID: "install.inputs"}, {ID: "install.cluster"}, {ID: "install.cluster.kind", Parent: "install.cluster"}}
}

func succeeded() installflow.Summary {
	return installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordInstalled,
		Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased, PartialState: installflow.EffectApplied}
}

func emit(t *testing.T, rt installflow.Runtime, ev installflow.Event) {
	t.Helper()
	if err := rt.Events.Emit(ev); err != nil {
		t.Fatalf("emit %s: %v", ev.Payload.Kind(), err)
	}
}

// fullRun plays every kind of event, and writes to the console the way the engine and
// the tools it runs do: in fragments, with colour, with a carriage return.
func fullRun(t *testing.T) installflow.Engine {
	return engineFunc(func(ctx context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		emit(t, rt, installflow.Event{Payload: installflow.RunStarted{
			Target: installflow.Target{Mode: "managed-kind", Context: installflow.Text("kind-devicechain")}, Manifest: manifest()}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.StepStarted{Message: "install.inputs"}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.PreflightObservation{
			Observation: installflow.ObserveCheck, Verdict: installflow.CheckWarn, Text: installflow.Text("cgroup v1")}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepUnchanged}})
		emit(t, rt, installflow.Event{Step: "install.cluster", Attempt: 1, Payload: installflow.StepStarted{Message: "install.cluster", BudgetMS: 90000}})
		emit(t, rt, installflow.Event{Step: "install.cluster.kind", Parent: "install.cluster", Attempt: 1,
			Payload: installflow.StepStarted{Message: "install.cluster.kind"}})
		total := int64(4)
		emit(t, rt, installflow.Event{Step: "install.cluster.kind", Attempt: 1, Payload: installflow.StepProgress{
			MetricID: "nodes", Mode: installflow.ProgressMeasured, Current: 4, Total: &total,
			Unit: installflow.UnitReplicas, Source: "kubernetes"}})
		if _, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: "install.cluster", Kind: installflow.PromptConfirm,
			Question: installflow.QuestionCreateKindCluster, YesResolvable: true}); err != nil {
			t.Errorf("a --yes confirmation: %v", err)
		}
		io.WriteString(rt.Console.Stdout, "Creating kind cluster... ")
		io.WriteString(rt.Console.Stdout, "\x1b[32mdone.\x1b[0m\r\n")
		io.WriteString(rt.Console.Stderr, "\x1b]0;title\x07a tool's warning\n")
		emit(t, rt, installflow.Event{Step: "install.cluster.kind", Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}})
		emit(t, rt, installflow.Event{Step: "install.cluster", Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}})
		s := succeeded()
		s.Next = []installflow.NextCommand{{Purpose: "bootstrap-an-instance",
			Argv: []installflow.SafeText{installflow.Text("dcctl"), installflow.Text("bootstrap")}}}
		s.Credentials = []installflow.SecretRef{{Namespace: "dc-system", Name: "dc-rdb-superuser", Key: "password"}}
		return s, rt.Events.Finish(s)
	})
}

func run(t *testing.T, e installflow.Engine, req installflow.Request) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := Presenter{Stdout: &out, RunID: "run-1", Clock: testkit.NewManualClock(start)}.Run(context.Background(), e, req, nil)
	return out.Bytes(), err
}

func decodeAll(t *testing.T, out []byte) []installflow.Envelope {
	t.Helper()
	envs, err := DecodeStream(out)
	if err != nil {
		t.Fatalf("stdout is not strict NDJSON: %v\n%s", err, out)
	}
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatalf("stream breaks the protocol: %v\n%s", err, out)
	}
	return envs
}

// Every byte on stdout is part of an envelope line; the console arrives as sanitised
// log lines inside the stream, never raw; the summary is the last line.
func TestStdoutIsOnlyEnvelopes(t *testing.T) {
	out, err := run(t, fullRun(t), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
	if err != nil {
		t.Fatal(err)
	}
	envs := decodeAll(t, out)
	if bytes.ContainsAny(out, "\x1b\r\a") {
		t.Fatalf("a control byte reached stdout:\n%q", out)
	}
	var logs []string
	kinds := map[installflow.Kind]bool{}
	for _, e := range envs {
		kinds[e.Kind] = true
		if l, ok := e.Payload.(installflow.LogLine); ok {
			logs = append(logs, l.Source+": "+l.Text.String())
		}
		if e.RunID != "run-1" || !e.Time.Equal(start) || e.ElapsedMS != 0 {
			t.Errorf("event %d: run %q time %v elapsed %d", e.Seq, e.RunID, e.Time, e.ElapsedMS)
		}
	}
	want := []string{SourceConsole + ": Creating kind cluster... done.", SourceConsoleStderr + ": a tool's warning"}
	if strings.Join(logs, "|") != strings.Join(want, "|") {
		t.Errorf("console became %q, want %q", logs, want)
	}
	for _, k := range []installflow.Kind{installflow.KindRunStarted, installflow.KindStepStarted, installflow.KindStepProgress,
		installflow.KindStepFinished, installflow.KindLogLine, installflow.KindPromptRequested, installflow.KindPromptResolved,
		installflow.KindPreflightObservation, installflow.KindRunSummary} {
		if !kinds[k] {
			t.Errorf("no %s in the stream", k)
		}
	}
	if last := envs[len(envs)-1]; last.Kind != installflow.KindRunSummary {
		t.Errorf("the last line is %s", last.Kind)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Error("the stream does not end with a complete line")
	}
}

// Each line is exactly the encoding of its envelope: the decoder refuses anything a
// consumer could trip on.
func TestDecodeIsStrict(t *testing.T) {
	out, err := run(t, fullRun(t), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
	if err != nil {
		t.Fatal(err)
	}
	line := out[:bytes.IndexByte(out, '\n')]
	for name, bad := range map[string][]byte{
		"text before":   append([]byte("Creating... "), line...),
		"trailing":      append(append([]byte{}, line...), ' '),
		"two objects":   append(append(append([]byte{}, line...), ' '), line...),
		"unknown field": bytes.Replace(line, []byte(`{"schema"`), []byte(`{"extra":1,"schema"`), 1),
		"unknown kind":  bytes.Replace(line, []byte(`"run.started"`), []byte(`"run.unknown"`), 1),
		"escape":        bytes.Replace(line, []byte(`kind-devicechain`), []byte(`kind-\u001b[31m`), 1),
		"reordered":     bytes.Replace(line, []byte(`"schema":"dcctl.install.events/v1","run_id":"run-1"`), []byte(`"run_id":"run-1","schema":"dcctl.install.events/v1"`), 1),
		"empty":         {},
	} {
		if _, err := Decode(bad); !errors.Is(err, ErrNotAnEvent) {
			t.Errorf("%s: decoded %q", name, bad)
		}
	}
	if _, err := DecodeStream(out[:len(out)-1]); !errors.Is(err, ErrNotAnEvent) {
		t.Error("a last line without its newline decoded")
	}
	if _, err := DecodeStream(append([]byte("\n"), out...)); !errors.Is(err, ErrNotAnEvent) {
		t.Error("an empty line decoded")
	}
}

// withStdin replaces the process's stdin with a pipe holding text, and returns what is
// still unread there after f.
func withStdin(t *testing.T, text string, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	w.Close()
	prev := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = prev; r.Close() }()
	f()
	rest, _ := io.ReadAll(r)
	return string(rest)
}

// THE PROMPT POLICY. --yes answers the confirmations it answers; every other prompt
// is unavailable — a confirmation --yes does not settle, any confirmation without
// --yes, a choice, a secret even with --yes — and nothing is ever read from stdin.
func TestPromptPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		yes       bool
		prompt    installflow.Prompt
		want      installflow.PromptDisposition
		wantErr   error
		confirmed bool
	}{
		{"--yes answers its confirmation", true, installflow.Prompt{Kind: installflow.PromptConfirm, YesResolvable: true},
			installflow.PromptAutoConfirmed, nil, true},
		{"no --yes, nobody asked", false, installflow.Prompt{Kind: installflow.PromptConfirm, YesResolvable: true},
			installflow.PromptUnavailable, installflow.ErrPromptUnavailable, false},
		{"a confirmation --yes does not settle", true, installflow.Prompt{Kind: installflow.PromptConfirm},
			installflow.PromptUnavailable, installflow.ErrPromptUnavailable, false},
		{"a choice", true, installflow.Prompt{Kind: installflow.PromptChoice, Choices: []string{"a", "b"}},
			installflow.PromptUnavailable, installflow.ErrPromptUnavailable, false},
		{"a secret, even with --yes", true, installflow.Prompt{Kind: installflow.PromptSecret, YesResolvable: true},
			installflow.PromptUnavailable, installflow.ErrPromptUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.prompt.Step, tc.prompt.Question = "install.cluster", installflow.QuestionCreateKindCluster
			var ans installflow.Answer
			var askErr error
			var out []byte
			rest := withStdin(t, "y\nsecret-from-stdin\n", func() {
				var err error
				out, err = run(t, engineFunc(func(ctx context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
					emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, Manifest: manifest()}})
					emit(t, rt, installflow.Event{Step: "install.cluster", Attempt: 1, Payload: installflow.StepStarted{Message: "install.cluster"}})
					ans, askErr = rt.Prompts.Ask(ctx, tc.prompt)
					emit(t, rt, installflow.Event{Step: "install.cluster", Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepUnchanged}})
					return succeeded(), nil
				}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: tc.yes})
				if err != nil {
					t.Fatal(err)
				}
			})
			if rest != "y\nsecret-from-stdin\n" {
				t.Errorf("stdin was read: %q left of it", rest)
			}
			if ans.Disposition != tc.want || !errors.Is(askErr, tc.wantErr) || (tc.wantErr == nil && askErr != nil) ||
				ans.Confirmed != tc.confirmed || !ans.Secret.IsEmpty() {
				t.Errorf("answer %+v, %v; want %s, %v", ans, askErr, tc.want, tc.wantErr)
			}
			var resolved installflow.PromptDisposition
			for _, e := range decodeAll(t, out) {
				if p, ok := e.Payload.(installflow.PromptResolved); ok {
					resolved = p.Disposition
				}
			}
			if resolved != tc.want {
				t.Errorf("the stream says %q, want %q", resolved, tc.want)
			}
			if bytes.Contains(out, []byte("secret-from-stdin")) || bytes.Contains(out, []byte("[y/N]")) {
				t.Error("the stream shows stdin or a terminal question")
			}
		})
	}
}

// THE CANARY HARNESS over every event and every line: secret values reach the stream
// through each path a value can travel — the console in one write and split across
// writes, a target, a step's parameters, a remedy's parameters, a next command, a
// preflight observation — and none survives.
func TestNoCanaryInTheStream(t *testing.T) {
	cans := testkit.Canaries()
	out, err := run(t, engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		if err := testkit.RegisterCanaries(rt.Events); err != nil {
			t.Fatal(err)
		}
		text := func(i int) installflow.SafeText { return installflow.Text("value " + cans[i%len(cans)].Value + " end") }
		emit(t, rt, installflow.Event{Payload: installflow.RunStarted{
			Target: installflow.Target{Mode: "existing-context", Context: text(0)}, Manifest: manifest()}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.StepStarted{Message: "install.inputs",
			Params: []installflow.Param{{Name: "a", Value: text(1)}, {Name: "b", Value: text(2)}}}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.PreflightObservation{
			Observation: installflow.ObserveCheck, Verdict: installflow.CheckFail, Text: text(3), Highlight: text(4), Fix: text(5)}})
		for _, c := range cans {
			io.WriteString(rt.Console.Stdout, "echo "+c.Value+"\n")
			half := len(c.Value) / 2
			io.WriteString(rt.Console.Stderr, "split "+c.Value[:half])
			io.WriteString(rt.Console.Stderr, c.Value[half:]+"\n")
		}
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.LogLine{
			Level: installflow.LogError, Source: "tofu", Text: text(6)}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.StepFailed{
			Code: "install.inputs.failed", Retry: installflow.RetryPermanent, Remedy: installflow.RemedyRerunWithYes,
			RemedyParams: []installflow.Param{{Name: "c", Value: text(0)}}, Effect: installflow.EffectNoneObserved}})
		s := installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordNotRead,
			Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimNotClaimed, FailedStep: "install.inputs",
			PartialState: installflow.EffectNoneObserved,
			Next:         []installflow.NextCommand{{Purpose: "retry", Argv: []installflow.SafeText{text(1), text(5)}}}}
		return s, rt.Events.Finish(s)
	}), installflow.Request{Operation: installflow.OperationInstall})
	if err == nil {
		t.Fatal("a failed run returned no error")
	}
	testkit.AssertNoCanary(t, "the stream", out)
	envs := decodeAll(t, out)
	for _, e := range envs {
		testkit.AssertNoCanary(t, "event "+string(e.Kind), testkit.Dump(e, e.Payload))
	}
	if !bytes.Contains(out, []byte("[redacted]")) {
		t.Error("nothing was redacted: the canaries never reached the stream, so this proves nothing")
	}
}

// ONE VERDICT, LAST, even when the engine returns without sending it or panics: the
// hub closes the stream, and Run returns only once the summary line is written.
func TestSummaryIsAlwaysTheLastLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		e       installflow.Engine
		outcome installflow.RunOutcome
	}{
		{"engine never finishes, succeeds", engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
			emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, Manifest: manifest()}})
			return succeeded(), nil
		}), installflow.RunSucceeded},
		{"engine never finishes, fails", engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
			emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, Manifest: manifest()}})
			return succeeded(), errors.New("boom")
		}), installflow.RunFailed},
		{"planned", engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
			emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, DryRun: true, Manifest: manifest()}})
			s := succeeded()
			s.Outcome = installflow.RunPlanned
			return s, rt.Events.Finish(s)
		}), installflow.RunPlanned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.e, installflow.Request{Operation: installflow.OperationInstall})
			envs := decodeAll(t, out)
			sum, ok := envs[len(envs)-1].Payload.(installflow.Summary)
			if !ok || sum.Outcome != tc.outcome {
				t.Fatalf("last line %+v, want a %s summary", envs[len(envs)-1].Payload, tc.outcome)
			}
			if (err == nil) != (sum.ExitCode() == 0) {
				t.Errorf("returned %v for a %s summary: the exit status must be the summary's", err, sum.Outcome)
			}
		})
	}
}

// A command refused before its run starts: one line, a failed summary that changed
// nothing.
func TestRefuse(t *testing.T) {
	var out bytes.Buffer
	if err := (Presenter{Stdout: &out, RunID: "run-1", Clock: testkit.NewManualClock(start)}).Refuse(
		context.Background(), installflow.OperationInstall, installflow.StepInstallInputs); err != nil {
		t.Fatal(err)
	}
	envs := decodeAll(t, out.Bytes())
	if len(envs) != 1 {
		t.Fatalf("%d lines, want 1", len(envs))
	}
	sum, ok := envs[0].Payload.(installflow.Summary)
	if !ok || sum.Outcome != installflow.RunFailed || sum.FailedStep != installflow.StepInstallInputs ||
		sum.PartialState != installflow.EffectNoneObserved || sum.Claim != installflow.ClaimNotClaimed {
		t.Errorf("got %+v", envs[0].Payload)
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, errors.New("broken pipe")
	}
	w.after--
	return len(p), nil
}

// A stream that could not be written in full never exits 0, even when the run
// succeeded: the consumer did not see the verdict.
func TestWriteFailureIsNotSuccess(t *testing.T) {
	w := &failingWriter{after: 2}
	err := Presenter{Stdout: w, RunID: "run-1", Clock: testkit.NewManualClock(start)}.Run(context.Background(),
		fullRun(t), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("got %v, want the write failure", err)
	}
	if err := (Presenter{Stdout: &failingWriter{}}).Refuse(context.Background(), installflow.OperationInstall,
		installflow.StepInstallInputs); err == nil {
		t.Fatal("a refusal that could not be written reported nothing")
	}
}

// slowWriter takes its time over every line, so a caller that returned before the
// stream was written would find it short.
type slowWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *slowWriter) Write(b []byte) (int, error) {
	time.Sleep(20 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(b)
}

// 🔴 AN ENGINE PANIC ENDS THE STREAM WITH ITS SUMMARY, AND RUN RETURNS ONLY AFTER IT IS
// WRITTEN, AS AN ERROR: the command then exits 1, the failed summary's verdict, where an
// unrecovered panic would exit 2 and might cut the stream before its last line.
func TestEnginePanicEndsWithTheSummary(t *testing.T) {
	e := engineFunc(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
		emit(t, rt, installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"}, Manifest: manifest()}})
		emit(t, rt, installflow.Event{Step: "install.inputs", Attempt: 1, Payload: installflow.StepStarted{Message: "install.inputs"}})
		panic("an engine bug")
	})
	w := &slowWriter{}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Run panicked: %v", p)
			}
		}()
		err = Presenter{Stdout: w, RunID: "run-1", Clock: testkit.NewManualClock(start)}.Run(context.Background(), e,
			installflow.Request{Operation: installflow.OperationInstall}, nil)
	}()
	var pe *PanicError
	if !errors.As(err, &pe) || pe.Value != "an engine bug" || len(pe.Stack) == 0 {
		t.Fatalf("Run returned %v, want the panic as a *PanicError with its stack", err)
	}
	w.mu.Lock()
	out := append([]byte(nil), w.buf.Bytes()...)
	w.mu.Unlock()
	envs := decodeAll(t, out)
	sum, ok := envs[len(envs)-1].Payload.(installflow.Summary)
	if !ok || sum.Outcome != installflow.RunFailed || sum.ExitCode() != 1 {
		t.Fatalf("the last line is %+v, want a failed summary", envs[len(envs)-1].Payload)
	}
}
