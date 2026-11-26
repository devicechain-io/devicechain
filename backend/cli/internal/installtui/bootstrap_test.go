// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

func canaryValue(label string) string {
	for _, c := range testkit.Canaries() {
		if c.Label == label {
			return c.Value
		}
	}
	panic("no canary " + label)
}

var seedRef = installflow.SecretRef{Namespace: "dci-acme", Name: "dci-acme-superuser", Key: "password"}

func okBootstrapSummary() installflow.Summary {
	return installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordUnchanged,
		Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased, PartialState: installflow.EffectApplied,
		Credentials: []installflow.SecretRef{seedRef}}
}

func acmeFacts() InstanceFacts {
	return InstanceFacts{ConsoleURL: "https://devicechain.local/admin/", Account: "admin@devicechain.io",
		KubeContext: "kind-devicechain", Backups: "nightly base backups, 7 days of WAL",
		EscrowPath: "/home/op/.devicechain/escrow/acme-rootkey.escrow", EscrowWritten: true}
}

func TestInstanceChoicesSettleIntoAPlan(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L4, 100, 30)))
	d.typeText("acme")
	d.keys("tab", "tab", "down") // Host, then Profile: full
	d.keys("tab")
	d.typeText("lwm2m-ingest")
	d.keys("tab", "space") // TLS off
	d.keys("tab", "down", "down", "tab", "tab", "enter")
	if d.f.phase != phaseEscrow {
		t.Fatalf("still on %v:\n%s", d.f.phase, d.render())
	}
	if got := d.f.esc.path.Value(); got != "/home/op/.devicechain/escrow/acme-rootkey.escrow" {
		t.Fatalf("default escrow path %q does not follow the instance name", got)
	}
	for d.f.esc.focus != escrowFieldContinue {
		d.keys("tab")
	}
	d.keys("enter")
	p := d.planned()
	in := p.Instance
	if in.Name != "acme" || in.Profile != "full" || strings.Join(in.Areas, ",") != "lwm2m-ingest" ||
		!in.NoTLS || in.ObjectStore != "filesystem" {
		t.Fatalf("choices %+v", in)
	}
	for _, f := range []string{"profile", "enable-area", "no-tls", "object-store"} {
		if !in.Given[f] {
			t.Errorf("changed %s is not passed as a flag", f)
		}
	}
	if in.Given["host"] {
		t.Error("an untouched host is passed as a flag")
	}
	if p.Escrow.Mode != EscrowWrite || p.Escrow.PathGiven {
		t.Fatalf("escrow %+v", p.Escrow)
	}
	cmd := BootstrapCommand(in, p.Escrow, []string{"--cluster=devicechain"})
	want := "dcctl bootstrap local acme --cluster=devicechain --profile=full --enable-area=lwm2m-ingest --no-tls=true " +
		"--object-store=filesystem --escrow-passphrase-file=<file holding the escrow passphrase> --yes"
	if cmd != want {
		t.Fatalf("command\n got %s\nwant %s", cmd, want)
	}
}

func TestInstanceChoicesRefuseWhatTheCommandRefuses(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("Acme")
	d.keys("tab", "tab", "tab")
	d.typeText("bogus-area")
	for d.f.inst.focus != fieldContinue {
		d.keys("tab")
	}
	d.keys("enter")
	if len(d.out) != 0 || d.f.phase != phaseInstance {
		t.Fatalf("an invalid instance continued: %v", d.out)
	}
	view := d.render()
	for _, want := range []string{"ERROR: instance names are lower-case", `ERROR: unknown functional area "bogus-area"`} {
		if !strings.Contains(view, want) {
			t.Errorf("screen does not say %q:\n%s", want, view)
		}
	}
	if d.f.inst.focus != fieldName {
		t.Errorf("focus %v, want the first refused field", d.f.inst.focus)
	}
}

func TestFlagValuesAreLabelledAndKept(t *testing.T) {
	s := bsSetup(bsCaps(terminal.L1, 100, 30))
	s.Instance.Name, s.Instance.Profile, s.Instance.Host = "acme", "telemetry", "localhost"
	s.Instance.FromFlag = map[string]bool{"name": true, "profile": true, "host": true}
	d := newBSDriver(t, s)
	view := d.render()
	for _, want := range []string{"from --profile", "from --host", "< telemetry >"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	p := d.throughPlanAsIs()
	if !p.Instance.Given["profile"] || !p.Instance.Given["host"] || p.Instance.Profile != "telemetry" {
		t.Fatalf("flag values not kept: %+v", p.Instance)
	}
}

// throughPlanAsIs continues from both pre-run screens without typing.
func (d *bsDriver) throughPlanAsIs() BootstrapPlanned {
	d.t.Helper()
	for i := 0; i < 50 && len(d.out) == 0; i++ {
		switch {
		case d.f.phase == phaseInstance && d.f.inst.focus == fieldContinue,
			d.f.phase == phaseEscrow && d.f.esc.focus == escrowFieldContinue:
			d.keys("enter")
		default:
			d.keys("tab")
		}
	}
	return d.planned()
}

// --no-escrow is never the default, is chosen only by selecting it, and is
// shown with the permanent-loss warning from the moment it is selected.
func TestNoEscrowIsAnExplicitWarnedChoice(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	d.throughPlanAsIs()
	if p := d.planned(); p.Escrow.Mode != EscrowWrite {
		t.Fatalf("default escrow mode %q", p.Escrow.Mode)
	}

	d = newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	for d.f.phase == phaseInstance {
		if d.f.inst.focus == fieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
	if strings.Contains(d.render(), "PERMANENT LOSS") {
		t.Fatal("the warning is shown before --no-escrow is chosen")
	}
	d.keys("tab", "space") // focus "Do not escrow", select it
	if !strings.Contains(d.render(), "WARNING: PERMANENT LOSS") {
		t.Fatalf("no warning with --no-escrow selected:\n%s", d.render())
	}
	p := d.throughPlanAsIs()
	if p.Escrow.Mode != EscrowNone || p.Escrow.Path != "" {
		t.Fatalf("escrow %+v", p.Escrow)
	}
	if cmd := BootstrapCommand(p.Instance, p.Escrow, nil); !strings.HasSuffix(cmd, " --no-escrow --yes") ||
		strings.Contains(cmd, "passphrase") {
		t.Fatalf("command %s", cmd)
	}
}

func TestEscrowLocationUsesTheEnginesPathRule(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	for d.f.phase == phaseInstance {
		if d.f.inst.focus == fieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
	d.keys("tab", "tab") // Location
	d.clearField()
	d.typeText("/srv/keys/exists.escrow")
	d.keys("tab", "tab", "enter")
	if len(d.out) != 0 || !strings.Contains(d.render(), "ERROR: refusing to overwrite the existing file") {
		t.Fatalf("an existing file was accepted:\n%s", d.render())
	}
	if d.f.esc.focus != escrowFieldPath {
		t.Fatal("the refused path is not focused")
	}
	d.clearField()
	d.typeText("/srv/keys/acme.escrow")
	p := d.throughPlanAsIs()
	if p.Escrow.Path != "/srv/keys/acme.escrow" || !p.Escrow.PathGiven {
		t.Fatalf("escrow %+v", p.Escrow)
	}
	if !strings.Contains(BootstrapCommand(p.Instance, p.Escrow, nil), "--escrow-file=/srv/keys/acme.escrow") {
		t.Fatal("a custom path is not passed as --escrow-file")
	}
}

func TestASuppliedPassphraseAsksNothing(t *testing.T) {
	for src, want := range map[PassphraseSource]string{
		PassphraseFromFile: "Passphrase supplied by protected file", PassphraseFromEnv: "Passphrase supplied by the environment",
	} {
		s := bsSetup(bsCaps(terminal.L1, 100, 30))
		s.Escrow.Passphrase = src
		d := newBSDriver(t, s)
		d.typeText("acme")
		d.keys("tab", "tab", "tab", "tab", "tab", "tab", "tab", "enter")
		if v := d.render(); !strings.Contains(v, want) || strings.Contains(v, "asked for its passphrase") {
			t.Errorf("%s:\n%s", src, v)
		}
	}
}

// bsEngine is a bootstrap stand-in: it asks the escrow passphrase twice through
// the run's broker, as the real escrow stage does, and offers the superuser seed
// as a one-time reveal, as the real report stage does.
type bsEngine struct {
	seed  string
	got   chan string
	asked chan struct{}
}

func (e *bsEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	ev := rt.Events
	steps := []installflow.StepID{installflow.StepBootstrapEscrow, installflow.StepBootstrapReport}
	var m []installflow.ManifestStep
	for _, s := range steps {
		m = append(m, installflow.ManifestStep{ID: s})
	}
	if err := ev.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target: installflow.Target{Mode: "managed-kind", Context: installflow.Text("kind-devicechain")}, Manifest: m}}); err != nil {
		return installflow.Summary{}, err
	}
	start := func(id installflow.StepID) error {
		return ev.Emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepStarted{Message: installflow.MessageCode(id)}})
	}
	finish := func(id installflow.StepID) error {
		return ev.Emit(installflow.Event{Step: id, Attempt: 1, Payload: installflow.StepFinished{Outcome: installflow.StepSucceeded}})
	}
	if err := start(installflow.StepBootstrapEscrow); err != nil {
		return installflow.Summary{}, err
	}
	var answers []string
	for _, q := range []installflow.MessageCode{installflow.QuestionEscrowPassphrase, installflow.QuestionEscrowPassphraseConfirm} {
		ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepBootstrapEscrow, Kind: installflow.PromptSecret,
			Question: q, Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}})
		if err != nil {
			return installflow.Summary{}, err
		}
		answers = append(answers, string(ans.Secret.Expose()))
		ans.Secret.Zero()
	}
	if answers[0] != answers[1] {
		return installflow.Summary{}, errors.New("the two passphrases do not match")
	}
	e.got <- answers[0]
	_ = finish(installflow.StepBootstrapEscrow)
	_ = start(installflow.StepBootstrapReport)
	rt.Reveals.Offer(installflow.RevealOffer{Step: installflow.StepBootstrapReport, Subject: installflow.RevealSuperuserSeed,
		Where: seedRef, Reveal: installflow.NewReveal([]byte(e.seed))})
	_ = finish(installflow.StepBootstrapReport)
	return okBootstrapSummary(), nil
}

// 🔴 THE WHOLE INSTANCE JOURNEY, THROUGH A REAL HUB AND BROKER. The passphrase is
// typed into the form and reaches the engine exactly; the seed is shown once on
// an explicit action and cleared on the acknowledgement. Neither ever appears in
// the log ring, an event, a screen other than the one reveal frame, or the exit
// summary.
func TestTheBootstrapJourneyKeepsItsSecrets(t *testing.T) {
	pass, seed := canaryValue("escrow-passphrase"), canaryValue("superuser-seed")
	sink := &testkit.CaptureSink{}
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationBootstrap, Sink: sink, RunID: "run-s17"})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := hub.LogWriter(installflow.StepBootstrapEscrow, "tui", installflow.LogInfo)
	if err != nil {
		t.Fatal(err)
	}
	s := bsSetup(bsCaps(terminal.L4, 100, 30))
	s.Notes = notes
	d := newBSDriver(t, s)
	var screens []string
	look := func() string { v := d.render(); screens = append(screens, v); return v }
	d.throughPlan()
	look()

	inbox := make(chan tea.Msg, 8)
	broker := installflow.NewBroker(hub, SecretResponder{Send: func(m tea.Msg) { inbox <- m }}, false)
	eng := &bsEngine{seed: seed, got: make(chan string, 1)}
	type result struct {
		sum installflow.Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := hub.Run(context.Background(), eng, installflow.Request{}, installflow.Runtime{Prompts: broker, Reveals: s.Reveals})
		done <- result{sum, err}
	}()
	next := func() tea.Msg {
		select {
		case m := <-inbox:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("the run asked nothing")
		}
		return nil
	}
	// The first answer typed key by key, the confirmation pasted.
	d.send(next())
	if v := look(); !strings.Contains(v, "Escrow passphrase") || !strings.Contains(v, "instance acme") {
		t.Fatalf("passphrase form:\n%s", v)
	}
	d.typeText(pass)
	if v := look(); strings.Contains(v, strings.Repeat("*", 4)) || strings.Contains(v, "•") {
		t.Fatalf("the form draws a mask:\n%s", v)
	}
	d.keys("enter")
	d.send(next())
	look()
	d.send(tea.PasteMsg{Content: pass})
	look()
	d.keys("enter")
	if got := <-eng.got; got != pass {
		t.Fatalf("the engine got a different passphrase (%d bytes)", len(got))
	}
	r := <-done
	if r.err != nil || !r.sum.Succeeded() {
		t.Fatalf("run %+v %v", r.sum, r.err)
	}
	_ = notes.Close()
	hub.Flush()

	d.send(BootstrapRunFinished{Summary: r.sum, Facts: acmeFacts()})
	v := look()
	if !strings.Contains(v, "Root-key escrow written and checked") || !strings.Contains(v, "[ ] I have stored the escrow file") {
		t.Fatalf("receipt:\n%s", v)
	}
	d.keys("space", "enter")

	v = look()
	if d.f.phase != phaseFirstLogin || strings.Contains(v, seed) {
		t.Fatalf("first login before the reveal:\n%s", v)
	}
	d.keys("s")
	revealFrame := d.render() // never added to screens: the one place the value may be
	if !strings.Contains(revealFrame, seed) {
		t.Fatalf("the reveal did not show the value:\n%s", revealFrame)
	}
	held := d.f.login.shown
	d.keys("space", "enter")
	if !bytes.Equal(held, make([]byte, len(held))) {
		t.Fatal("the shown value was not cleared on the acknowledgement")
	}
	v = look()
	if d.f.phase != phaseSuccess || !strings.Contains(v, "Instance acme is ready.") ||
		!strings.Contains(v, "off-site custody confirmed") || !strings.Contains(v, "shown once on the previous screen") {
		t.Fatalf("success:\n%s", v)
	}
	d.keys("s", "enter")
	if !d.doneSent() {
		t.Fatal("Enter on the success screen did not finish")
	}
	var summary bytes.Buffer
	d.f.ExitSummary(&summary)
	d.f.Close()

	testkit.AssertNoCanary(t, "the screens", []byte(strings.Join(screens, "\n")))
	testkit.AssertNoCanary(t, "the exit summary", summary.Bytes())
	testkit.AssertNoCanary(t, "the log ring", testkit.Dump(hub.Logs()))
	testkit.AssertNoCanary(t, "the events", testkit.Dump(sink.Envelopes()))
	testkit.AssertNoCanary(t, "the flow", testkit.Dump(d.f.choice, d.f.sum))
	if !strings.Contains(summary.String(), "Password: kept in Secret dci-acme/dci-acme-superuser, key password") ||
		!strings.Contains(summary.String(), "--escrow-passphrase-file=<file holding the escrow passphrase>") {
		t.Fatalf("exit summary:\n%s", summary.String())
	}
	var noted bool
	for _, l := range hub.Logs().Lines {
		noted = noted || strings.Contains(l.Text.String(), "escrow passphrase entered")
	}
	if !noted {
		t.Fatal("the passphrase entry left no note in the log ring")
	}
}

// 🔴 THE SEED IS SHOWN ONCE. After the acknowledgement nothing shows it again: not
// the success screen, not a second "show once", not a re-render, not help.
func TestTheSeedIsNeverShownAgainAfterTheAcknowledgement(t *testing.T) {
	seed := canaryValue("superuser-seed")
	d := postRun(t, bsCaps(terminal.L4, 100, 30), seed)
	d.keys("enter") // past the receipt, unacknowledged
	d.keys("s")
	if !strings.Contains(d.render(), seed) {
		t.Fatal("the reveal showed nothing")
	}
	d.keys("space", "enter")
	for i, k := range []string{"s", "f1", "esc", "?", "esc", "enter"} {
		if strings.Contains(d.render(), seed) {
			t.Fatalf("shown again (before key %d):\n%s", i, d.render())
		}
		d.keys(k)
	}
	if d.f.login != nil {
		t.Fatal("the first-login screen outlived its acknowledgement")
	}
}

// 🔴 THE ACKNOWLEDGEMENT GATE. Once the value is on screen, only the person's own
// tick lets the screen be left: Enter alone, however often, keeps it.
func TestTheRevealIsLeftOnlyThroughTheAcknowledgement(t *testing.T) {
	seed := canaryValue("superuser-seed")
	d := postRun(t, bsCaps(terminal.L1, 100, 30), seed)
	d.keys("enter", "s")
	for i := 0; i < 3; i++ {
		d.keys("enter")
		if d.f.phase != phaseFirstLogin || !strings.Contains(d.render(), seed) {
			t.Fatalf("Enter #%d left the reveal without the acknowledgement:\n%s", i+1, d.render())
		}
	}
	if !strings.Contains(d.render(), `Tick "I have stored this password"`) {
		t.Fatal("no reason given for staying")
	}
	d.keys("space", "enter")
	if d.f.phase != phaseSuccess {
		t.Fatal("the acknowledged screen did not leave")
	}
}

// Help hides the value first, and the value is never revealed without the
// explicit action.
func TestHelpHidesTheRevealAndNothingRevealsByItself(t *testing.T) {
	seed := canaryValue("superuser-seed")
	d := postRun(t, bsCaps(terminal.L4, 100, 30), seed)
	d.keys("enter")
	for _, k := range []string{"tab", "tab", "space", "f1", "esc"} {
		d.keys(k)
		if strings.Contains(d.render(), seed) || d.f.login.spent {
			t.Fatalf("revealed without the action (after %s)", k)
		}
	}
	d.keys("s", "f1")
	if v := d.render(); strings.Contains(v, seed) || !strings.Contains(v, "Help") {
		t.Fatalf("help shows the value:\n%s", v)
	}
	d.keys("esc")
	if !strings.Contains(d.render(), seed) {
		t.Fatal("closing help lost the one reveal")
	}
}

// Skipping discards the reveal unshown; the success screen points at the Secret.
func TestSkippingTheRevealDiscardsIt(t *testing.T) {
	seed := canaryValue("superuser-seed")
	d := postRun(t, bsCaps(terminal.L1, 100, 30), seed)
	offer := d.f.login.offer
	d.keys("enter", "tab", "enter")
	if d.f.phase != phaseSuccess || !offer.Reveal.Consumed() {
		t.Fatal("skip did not discard the reveal")
	}
	v := d.render()
	if strings.Contains(v, seed) || !strings.Contains(v, "kubectl -n dci-acme get secret dci-acme-superuser") {
		t.Fatalf("success after a skip:\n%s", v)
	}
}

// The custody acknowledgement starts unchecked, is the person's alone, and
// changes neither the outcome nor the exit status.
func TestCustodyAcknowledgementIsThePersonsAndChangesNothing(t *testing.T) {
	d := postRun(t, bsCaps(terminal.L1, 100, 30), canaryValue("superuser-seed"))
	if d.f.rcpt.acked || !strings.Contains(d.render(), "Off-site custody not confirmed.") {
		t.Fatal("the acknowledgement does not start unchecked")
	}
	if c := d.f.rcpt.verifyCommand(); c != "dcctl secrets escrow verify /home/op/.devicechain/escrow/acme-rootkey.escrow "+
		"--instance acme --kube-context kind-devicechain" || !strings.Contains(d.render(), "dcctl secrets escrow verify") {
		t.Fatalf("verify command %q:\n%s", c, d.render())
	}
	d.keys("enter", "tab", "enter") // continue unacknowledged, skip the reveal
	if d.f.phase != phaseSuccess || !strings.HasSuffix(d.f.succ.escrowLine(), "(off-site custody not confirmed)") {
		t.Fatalf("success:\n%s", d.render())
	}
	d.keys("enter")
	for _, m := range d.out {
		if done, ok := m.(BootstrapDone); ok && done.Summary.ExitCode() != 0 {
			t.Fatal("declining custody changed the exit status")
		}
	}
}

// A failed or progress-only run reveals nothing: every offer is discarded.
func TestNoRevealAfterAFailedOrProgressOnlyRun(t *testing.T) {
	for name, tc := range map[string]struct {
		caps terminal.Capabilities
		sum  installflow.Summary
	}{
		"failed":        {bsCaps(terminal.L4, 100, 30), installflow.Summary{Outcome: installflow.RunFailed}},
		"progress-only": {progressOnlyCaps(), okBootstrapSummary()},
	} {
		s := bsSetup(tc.caps)
		d := newBSDriver(t, s)
		if !tc.caps.ProgressOnly {
			d.typeText("acme")
			d.throughPlanAsIs()
		}
		r := installflow.NewReveal([]byte("dcCanarySeedPw-8Kp3Wm7Rt2Vn"))
		s.Reveals.Offer(installflow.RevealOffer{Subject: installflow.RevealSuperuserSeed, Where: seedRef, Reveal: r})
		d.send(BootstrapRunFinished{Summary: tc.sum, Facts: acmeFacts()})
		if !r.Consumed() || !d.doneSent() || d.f.login != nil {
			t.Errorf("%s: the reveal survived (consumed %v, done %v)", name, r.Consumed(), d.doneSent())
		}
	}
}

func progressOnlyCaps() terminal.Capabilities {
	c := bsCaps(terminal.L4, 100, 30)
	c.ProgressOnly = true
	return c
}

// A withdrawn question clears the form, and Close clears whatever is left.
func TestWithdrawnQuestionsAndCloseClearTheScreens(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	d.throughPlanAsIs()
	reply := &secretReply{ch: make(chan installflow.Response, 1)}
	d.send(SecretQuestion{Question: installflow.Question{ID: "prompt-1", Kind: installflow.PromptSecret,
		Question: installflow.QuestionEscrowPassphrase}, reply: reply})
	d.typeText("half-typed")
	form := d.f.pass
	d.send(SecretWithdrawn{ID: "prompt-1"})
	if d.f.pass != nil || len(form.field.buf) != 0 {
		t.Fatal("a withdrawn question left its input")
	}
	// An empty Enter answers nothing.
	d.send(SecretQuestion{Question: installflow.Question{ID: "prompt-2", Kind: installflow.PromptSecret,
		Question: installflow.QuestionEscrowPassphrase}, reply: reply})
	d.keys("enter")
	if len(reply.ch) != 0 || !strings.Contains(d.render(), "cannot be empty") {
		t.Fatal("an empty passphrase was answered")
	}
	d.typeText("x")
	form = d.f.pass
	d.f.Close()
	if len(form.field.buf) != 0 {
		t.Fatal("Close left the input")
	}
	late := installflow.NewReveal([]byte("late"))
	d.f.setup.Reveals.Offer(installflow.RevealOffer{Reveal: late})
	if !late.Consumed() {
		t.Fatal("an offer after Close was kept")
	}
}

// A late answer, after the run stopped waiting, is cleared rather than kept.
func TestALateAnswerIsCleared(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	msgs := make(chan tea.Msg, 2)
	errc := make(chan error, 1)
	go func() {
		_, err := SecretResponder{Send: func(m tea.Msg) { msgs <- m }}.Respond(ctx,
			installflow.Question{ID: "prompt-1", Kind: installflow.PromptSecret})
		errc <- err
	}()
	q := (<-msgs).(SecretQuestion)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Respond: %v", err)
	}
	if _, ok := (<-msgs).(SecretWithdrawn); !ok {
		t.Fatal("no withdrawal sent")
	}
	late := installflow.NewSecret([]byte("late answer"))
	q.reply.answer(installflow.Response{Secret: late})
	if !late.IsEmpty() {
		t.Fatal("a late answer was kept")
	}
	if _, err := (SecretResponder{Send: func(tea.Msg) {}}).Respond(ctx, installflow.Question{Kind: installflow.PromptConfirm}); !errors.Is(err, installflow.ErrPromptUnavailable) {
		t.Fatal("a confirmation was taken as a secret question")
	}
}

// postRun is a flow past a successful run that wrote its escrow and offered the
// seed: at the receipt.
func postRun(t *testing.T, caps terminal.Capabilities, seed string) *bsDriver {
	t.Helper()
	s := bsSetup(caps)
	d := newBSDriver(t, s)
	d.typeText("acme")
	d.throughPlanAsIs()
	s.Reveals.Offer(installflow.RevealOffer{Step: installflow.StepBootstrapReport, Subject: installflow.RevealSuperuserSeed,
		Where: seedRef, Reveal: installflow.NewReveal([]byte(seed))})
	d.send(BootstrapRunFinished{Summary: okBootstrapSummary(), Facts: acmeFacts()})
	if d.f.phase != phaseReceipt {
		t.Fatalf("phase %v after the run", d.f.phase)
	}
	return d
}

// The equivalent command quotes what a shell would split.
func TestBootstrapCommandQuotes(t *testing.T) {
	in := InstanceChoices{Name: "acme", Host: "it's.local", Given: map[string]bool{"host": true}}
	got := BootstrapCommand(in, EscrowChoice{Mode: EscrowWrite, Path: "/a b/x.escrow", PathGiven: true}, []string{"--kube-context=my ctx"})
	want := `dcctl bootstrap local acme '--kube-context=my ctx' '--host=it'"'"'s.local' '--escrow-file=/a b/x.escrow' ` +
		`--escrow-passphrase-file=<file holding the escrow passphrase> --yes`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	r := BootstrapCommand(in, EscrowChoice{Mode: EscrowRestore, Path: "/k/acme.escrow"}, nil)
	if !strings.Contains(r, "--restore-root-key=/k/acme.escrow --escrow-passphrase-file=<file holding") {
		t.Fatalf("restore: %s", r)
	}
}

// zeroed reports whether every byte of a backing array is zero.
func zeroed(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// passphraseForm opens the entry form on a fresh flow and returns its reply.
func passphraseForm(t *testing.T) (*bsDriver, *secretReply) {
	t.Helper()
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	d.throughPlanAsIs()
	reply := &secretReply{ch: make(chan installflow.Response, 1)}
	d.send(SecretQuestion{Question: installflow.Question{ID: "prompt-1", Kind: installflow.PromptSecret,
		Question: installflow.QuestionEscrowPassphrase}, reply: reply})
	return d, reply
}

// 🔴 THE PASSPHRASE'S BYTES ARE WIPED, NOT ABANDONED. Every backing array the form
// ever held — including the ones it outgrew while the passphrase was typed — is
// zero after submit, Esc, withdrawal and Close.
func TestThePassphraseBuffersAreWiped(t *testing.T) {
	long := strings.Repeat("Zx9?Qv=7/Wb2+Lm", 12) // long enough to outgrow the first array
	for name, end := range map[string]func(d *bsDriver){
		"submit":   func(d *bsDriver) { d.keys("enter") },
		"esc":      func(d *bsDriver) { d.keys("esc") },
		"withdraw": func(d *bsDriver) { d.send(SecretWithdrawn{ID: "prompt-1"}) },
		"close":    func(d *bsDriver) { d.f.Close() },
	} {
		d, reply := passphraseForm(t)
		d.typeText(long[:5])
		first := d.f.pass.field.buf[:cap(d.f.pass.field.buf)]
		d.typeText(long[5:])
		d.send(tea.PasteMsg{Content: "pasted"})
		last := d.f.pass.field.buf[:cap(d.f.pass.field.buf)]
		if &first[0] == &last[0] {
			t.Fatalf("%s: the field never outgrew its first array; the test proves nothing", name)
		}
		if !zeroed(first) {
			t.Errorf("%s: an outgrown array still holds the passphrase", name)
		}
		end(d)
		if !zeroed(last) {
			t.Errorf("%s: the field's array still holds the passphrase", name)
		}
		if name == "submit" {
			resp := <-reply.ch
			if got := string(resp.Secret.Expose()); got != long+"pasted" {
				t.Errorf("submit answered %d bytes, want %d", len(got), len(long)+6)
			}
		}
	}
	// Backspace wipes the byte it removes, too.
	d, _ := passphraseForm(t)
	d.typeText("ab")
	arr := d.f.pass.field.buf[:cap(d.f.pass.field.buf)]
	d.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if arr[1] != 0 || string(d.f.pass.field.buf) != "a" {
		t.Fatalf("backspace left %q in the array", arr[:2])
	}
}

// 🔴 A SCREEN THAT IS NOT ON SCREEN TAKES NO INPUT. In a terminal too small to show
// it, the escrow screen shows a resize message instead — and the keys that would
// select --no-escrow and continue do nothing, so the warning cannot be skipped
// unseen. The same holds for every screen of the flow.
func TestATooSmallTerminalTakesNoChoice(t *testing.T) {
	d := newBSDriver(t, bsSetup(bsCaps(terminal.L1, 100, 30)))
	d.typeText("acme")
	toEscrow(d)
	if d.f.phase != phaseEscrow {
		t.Fatal("not at the escrow screen")
	}
	d.send(tea.WindowSizeMsg{Width: 50, Height: 14})
	d.keys("tab", "space", "tab", "tab", "enter")
	if len(d.out) != 0 || d.f.esc.mode != EscrowWrite {
		t.Fatalf("an unseen escrow screen took a choice: mode %q, sent %v", d.f.esc.mode, d.out)
	}
	if v := d.render(); !strings.Contains(v, "too small") {
		t.Fatalf("no resize message:\n%s", v)
	}
	d.keys("f1")
	if !d.f.help {
		t.Fatal("help is refused while too small")
	}
	d.keys("esc")
	d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.keys("tab", "space")
	if d.f.esc.mode != EscrowNone || !strings.Contains(d.render(), "PERMANENT LOSS") {
		t.Fatal("the screen did not come back once there was room")
	}

	// The first-login acknowledgement and the reveal take no key unseen either.
	seed := canaryValue("superuser-seed")
	d = postRun(t, bsCaps(terminal.L1, 100, 30), seed)
	d.keys("enter")
	d.send(tea.WindowSizeMsg{Width: 50, Height: 14})
	d.keys("s", "space", "enter")
	if d.f.phase != phaseFirstLogin || d.f.login.spent {
		t.Fatal("an unseen first-login screen revealed or was left")
	}
	d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.keys("s")
	d.send(tea.WindowSizeMsg{Width: 50, Height: 14})
	d.keys("space", "enter")
	if d.f.phase != phaseFirstLogin || d.f.login.stored {
		t.Fatal("an unseen reveal was acknowledged")
	}
	if strings.Contains(d.render(), seed) {
		t.Fatal("the resize message shows the seed")
	}
}

// The reveal, its acknowledgement and the nudge fit every supported size, so a
// shown password is never cut off or left on a screen that takes no keys.
func TestTheRevealFitsEverySupportedSize(t *testing.T) {
	for _, size := range bsSizes {
		for _, level := range []terminal.Level{terminal.L1, terminal.L2} {
			d := postRun(t, bsCaps(level, size[0], size[1]), canaryValue("superuser-seed"))
			d.keys("enter", "s", "enter")
			if !d.f.shown() || !strings.Contains(d.render(), canaryValue("superuser-seed")) {
				t.Errorf("L%d %dx%d: the reveal does not fit:\n%s", level, size[0], size[1], d.render())
			}
		}
	}
}

// A paste into a form the terminal cannot show is dropped, as a key is.
func TestATooSmallTerminalTakesNoPaste(t *testing.T) {
	d, _ := passphraseForm(t)
	d.send(tea.WindowSizeMsg{Width: 50, Height: 14})
	d.send(tea.PasteMsg{Content: "unseen"})
	d.keys("x")
	if len(d.f.pass.field.buf) != 0 {
		t.Fatal("an unseen form took input")
	}
}
