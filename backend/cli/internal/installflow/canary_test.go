// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

// leakEverywhere pushes every canary through every path into the event core
// that carries text: log lines in each encoding a tool might print, the line
// writer with the value split across writes, message parameters, remedy
// parameters, the target's context label, a next command, secret prompt
// answers, and wrapped values in a struct. It returns everything a presenter,
// a JSON consumer or a debugger could see afterwards.
func leakEverywhere(t *testing.T, register bool) []byte {
	t.Helper()
	f := newFixture(t, installflow.Options{})
	if register {
		must(t, testkit.RegisterCanaries(f.hub))
	}
	canaries := testkit.Canaries()
	text := func(i int) installflow.SafeText { return installflow.Text("value " + canaries[i%len(canaries)].Value) }

	must(t, f.hub.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target:   installflow.Target{Mode: "adopt-context", Context: text(0)},
		Manifest: manifest("a"),
	}}))
	params := make([]installflow.Param, len(canaries))
	for i := range canaries {
		params[i] = installflow.Param{Name: "p", Value: text(i)}
	}
	must(t, f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: installflow.StepStarted{Message: "m", Params: params}}))
	// A host tool's output can reach a preflight observation's text.
	for i := range canaries {
		must(t, f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: installflow.PreflightObservation{
			Observation: installflow.ObserveCheck, Verdict: installflow.CheckWarn,
			Text: text(i), Highlight: text(i + 1), Fix: text(i + 2)}}))
	}

	w, err := f.hub.LogWriter("a", "tool", installflow.LogInfo)
	must(t, err)
	for _, c := range canaries {
		v := c.Value
		j, _ := json.Marshal(map[string]string{"password": v})
		for _, form := range []string{
			v, string(j), url.QueryEscape(v), "https://u:" + url.PathEscape(v) + "@host/",
			base64.StdEncoding.EncodeToString([]byte(v)), "\x1b[1m" + v + "\x1b[0m",
		} {
			must(t, f.hub.Emit(installflow.Event{Step: "a", Payload: installflow.LogLine{
				Level: installflow.LogWarn, Source: "tofu", Text: installflow.Text("got " + form)}}))
		}
		// Split mid-value across writes, the way a pipe delivers it.
		half := len(v) / 2
		_, _ = io.WriteString(w, "leak: "+v[:half])
		_, _ = io.WriteString(w, v[half:]+"\n")
		for _, line := range strings.Split(v, "\n") {
			_, _ = io.WriteString(w, line+"\n")
		}
	}
	_ = w.Close()

	// A secret answer travels through the broker; only its disposition may.
	r := testkit.NewScriptedResponder(
		testkit.Reply{Response: installflow.Response{Secret: installflow.NewSecret([]byte(canaries[1].Value))}},
	)
	b := installflow.NewBroker(f.hub, r, false)
	ans, err := b.Ask(context.Background(), installflow.Prompt{Step: "a", Kind: installflow.PromptSecret, Question: "q.passphrase"})
	must(t, err)

	must(t, f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: installflow.StepFailed{
		Code: "tofu.apply", Retry: installflow.RetryUnknown, Effect: installflow.EffectMayHaveChanged,
		Remedy: "rerun", RemedyParams: params,
	}}))
	argv := []installflow.SafeText{installflow.Text("dcctl")}
	for i := range canaries {
		argv = append(argv, text(i))
	}
	_ = f.hub.Finish(installflow.Summary{
		Outcome: installflow.RunFailed, InstallRecord: installflow.RecordApplying, Readiness: installflow.ReadinessUnknown,
		Claim: installflow.ClaimHeld, FailedStep: "a", PartialState: installflow.EffectMayHaveChanged,
		Next:        []installflow.NextCommand{{Purpose: "retry", Argv: argv}},
		Credentials: []installflow.SecretRef{{Namespace: "devicechain", Name: "superuser-seed", Key: "password"}},
	})
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	final, _ := f.hub.Final()
	type wrapped struct {
		Answer   installflow.Answer
		Redacted installflow.Redacted
	}
	var all []any
	for _, e := range envs {
		all = append(all, e)
	}
	all = append(all, f.hub.Logs(), f.hub.Projection(), final, r.Questions(),
		wrapped{Answer: ans, Redacted: installflow.Redact(canaries[0].Value)})
	out := testkit.Dump(all...)
	for _, e := range envs {
		line, err := json.Marshal(e)
		must(t, err)
		out = append(out, line...) // the NDJSON line a JSON consumer would read
	}
	ans.Secret.Zero()
	return out
}

func TestCanariesNeverReachAnyOutput(t *testing.T) {
	testkit.AssertNoCanary(t, "the event stream, log ring, projection and summary", leakEverywhere(t, true))
}

// TestCanaryHarnessSeesALeak is the negative control: with redaction not told
// about the values, the same run must be caught leaking every one of them —
// otherwise the test above proves nothing.
func TestCanaryHarnessSeesALeak(t *testing.T) {
	leaks := testkit.FindCanaries(leakEverywhere(t, false))
	if len(leaks) != len(testkit.Canaries()) {
		t.Fatalf("the harness found %d of %d planted leaks: %v", len(leaks), len(testkit.Canaries()), leaks)
	}
}

// TestASecretIsRefusedAsAnIdentifier: a lower-case secret fits the
// identifier grammar, so a code that is a known secret is refused outright
// rather than redacted into a meaningless code.
func TestASecretIsRefusedAsAnIdentifier(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, testkit.RegisterCanaries(f.hub))
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	token := "dccanarykubetoken9w8x7v6u5t4s3r"
	for _, ev := range []installflow.Event{
		{Step: "a", Attempt: 1, Payload: installflow.StepFailed{Code: installflow.ErrorCode(token), Retry: installflow.RetryUnknown, Effect: installflow.EffectNoneObserved}},
		{Step: "a", Payload: installflow.LogLine{Level: installflow.LogInfo, Source: token}},
		{Step: "a", Attempt: 1, Payload: installflow.StepProgress{MetricID: token, Mode: installflow.ProgressIndeterminate, Unit: installflow.UnitActions, Source: "s"}},
		{Payload: installflow.PromptRequested{ID: installflow.PromptID(token), Prompt: installflow.PromptConfirm, Question: "q"}},
	} {
		if err := f.hub.Emit(ev); !errors.Is(err, installflow.ErrInvalidEvent) {
			t.Errorf("%s with a secret identifier: %v", ev.Payload.Kind(), err)
		}
	}
	err := f.hub.Finish(installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown, PartialState: installflow.EffectMayHaveChanged,
		FailedStep: installflow.StepID(token), Credentials: []installflow.SecretRef{{Namespace: "ns", Name: token}}})
	if err == nil {
		t.Fatal("a summary naming a secret was accepted")
	}
	out := testkit.Dump(f.wait(t), f.hub.Projection())
	out = append(out, []byte(fmt.Sprint(err))...)
	testkit.AssertNoCanary(t, "stream and error text", out)
}

// TestARedactOnlyValueIsRedactedButNeverRefusesAnIdentifier: a value registered
// with AddRedaction (a short password, an exec-plugin setting that is also a common
// word) is removed from text, down to MinRedactionBytes, but a code that happens to
// spell it is accepted — refusing it would refuse ordinary runs. Below
// MinRedactionBytes it is refused, so the caller can say it is not covered.
func TestARedactOnlyValueIsRedactedButNeverRefusesAnIdentifier(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.AddRedaction("default"))
	must(t, f.hub.AddRedaction("pw4x"))
	if err := f.hub.AddRedaction("pw3"); !errors.Is(err, installflow.ErrSecretTooShort) {
		t.Errorf("a 3-byte value: %v, want ErrSecretTooShort", err)
	}
	if err := f.hub.AddSecret("pw4x"); !errors.Is(err, installflow.ErrSecretTooShort) {
		t.Errorf("AddSecret of a 4-byte value: %v, want ErrSecretTooShort", err)
	}
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	must(t, f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: installflow.StepFailed{
		Code: "install.default-profile", Retry: installflow.RetryUnknown, Effect: installflow.EffectNoneObserved}}))
	must(t, f.hub.Emit(installflow.Event{Step: "a", Payload: installflow.LogLine{Level: installflow.LogInfo,
		Source: "tool", Text: installflow.Text("profile default, password pw4x")}}))
	must(t, f.hub.Finish(installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown, PartialState: installflow.EffectMayHaveChanged}))
	var text string
	for _, e := range f.wait(t) {
		if l, ok := e.Payload.(installflow.LogLine); ok {
			text = l.Text.String()
		}
	}
	if strings.Contains(text, "default") || strings.Contains(text, "pw4x") {
		t.Errorf("the log line kept a redact-only value: %q", text)
	}
	if got := f.hub.Redact("pw4x and default"); strings.Contains(got, "pw4x") || strings.Contains(got, "default") {
		t.Errorf("Redact kept a value: %q", got)
	}
}

// TestATokenValueIsRedactedOnlyAsAWholeToken: a value registered with AddRedaction is
// replaced where it stands as a token and nowhere inside a longer word, so a profile
// named "prod" leaves "production" alone. A strict value is still replaced as a
// substring.
func TestATokenValueIsRedactedOnlyAsAWholeToken(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.AddRedaction("prod"))
	must(t, f.hub.AddSecret("s3cr3tXYZ"))
	for in, want := range map[string]string{
		"current-context: production":       "current-context: production",
		"--profile prod":                    "--profile [redacted]",
		"AWS_PROFILE=prod; region=x":        "AWS_PROFILE=[redacted]; region=x",
		"prod-eu and eu_prod and prod2":     "prod-eu and eu_prod and prod2",
		"\"prod\"":                          "\"[redacted]\"",
		"keyAs3cr3tXYZB is strict":          "keyA[redacted]B is strict",
		"prod prod":                         "[redacted] [redacted]",
		"reproduce the prod issue, prodigy": "reproduce the [redacted] issue, prodigy",
	} {
		if got := f.hub.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAURLPasswordIsRedactedInItsUserinfoForm: a password in a server or proxy URL is
// printed userinfo-escaped ("foo+bar:baz" as "foo+bar%3Abaz"), and that form is
// redacted too.
func TestAURLPasswordIsRedactedInItsUserinfoForm(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.AddRedaction("foo+bar:baz"))
	in := "proxyconnect tcp: http://user:foo+bar%3Abaz@proxy:3128 refused"
	if got := f.hub.Redact(in); strings.Contains(got, "foo+bar") || !strings.Contains(got, "user:[redacted]@proxy") {
		t.Errorf("Redact(%q) = %q", in, got)
	}
}

// TestTokensOnlyInErrorsLeavesTheEventsAlone: with TokensOnlyInErrors (text
// output), an AddRedaction value is redacted from Redact and RedactError and from
// nothing the events carry; a strict value is redacted from both.
func TestTokensOnlyInErrorsLeavesTheEventsAlone(t *testing.T) {
	f := newFixture(t, installflow.Options{TokensOnlyInErrors: true})
	must(t, f.hub.AddRedaction("prod"))
	must(t, f.hub.AddSecret("s3cr3tXYZ"))
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(installflow.Event{Step: "a", Payload: installflow.LogLine{Level: installflow.LogInfo,
		Source: "tool", Text: installflow.Text("context prod, key s3cr3tXYZ")}}))
	must(t, f.hub.Finish(installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown, PartialState: installflow.EffectMayHaveChanged}))
	var text string
	for _, e := range f.wait(t) {
		if l, ok := e.Payload.(installflow.LogLine); ok {
			text = l.Text.String()
		}
	}
	if text != "context prod, key [redacted]" {
		t.Errorf("the event carries %q", text)
	}
	if got := f.hub.RedactError(errors.New("context prod")).Error(); got != "context [redacted]" {
		t.Errorf("the error is %q", got)
	}
}
