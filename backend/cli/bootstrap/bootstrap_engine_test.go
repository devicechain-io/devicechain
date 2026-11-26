// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// rawReporter records exactly what the engine hands its reporter — events, the summary,
// the secrets it registers and the log text — BEFORE any hub redacts anything. The
// producer-side rule is the defence: a canary found here is a leak, whatever a hub would
// have scrubbed afterwards.
type rawReporter struct {
	mu      sync.Mutex
	events  []installflow.Event
	summary *installflow.Summary
	secrets []string
	logs    bytes.Buffer
}

func (r *rawReporter) Emit(ev installflow.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *rawReporter) Finish(s installflow.Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summary != nil {
		return installflow.ErrFinished
	}
	r.summary = &s
	return nil
}

func (r *rawReporter) AddSecret(v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, v)
	return nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (r *rawReporter) AddRedaction(v string) error { return r.AddSecret(v) }

func (r *rawReporter) LogWriter(installflow.StepID, string, installflow.LogLevel) (io.WriteCloser, error) {
	return nopWriteCloser{&r.logs}, nil
}

func (r *rawReporter) steps() []installflow.StepID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []installflow.StepID
	for _, ev := range r.events {
		if _, ok := ev.Payload.(installflow.StepStarted); ok {
			ids = append(ids, ev.Step)
		}
	}
	return ids
}

// offers is a reveal sink that keeps what it is offered.
type offers struct{ got []installflow.RevealOffer }

func (o *offers) Offer(x installflow.RevealOffer) { o.got = append(o.got, x) }

// stubTerminal makes the escrow plan see a terminal on stdin.
func stubTerminal(t *testing.T) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = orig })
}

// isolateEscrow gives the run a home of its own, so the default escrow path resolves
// somewhere disposable, and an environment with no passphrase in it.
func isolateEscrow(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EscrowPassphraseEnv, "unset-below")
	os.Unsetenv(EscrowPassphraseEnv)
}

// canaryState is a settled bootstrap whose every secret is a canary.
func canaryState(t *testing.T) *State {
	st := goldenBootstrapState(nil)
	st.Values["secretsRootKey"] = canaryValue(t, "root-key")
	st.Credentials = &credentialSet{
		SuperuserPassword: canaryValue(t, "superuser-seed"),
		ObjectStoreSecret: canaryValue(t, "s3-secret-key"),
	}
	return st
}

// reportOnlyPipeline is NewDefaultPipeline's steps with stand-ins for all but the report.
func reportOnlyPipeline(t *testing.T) Pipeline { return goldenPipeline(t, "") }

type bootstrapRawRun struct {
	raw     *rawReporter
	con     bytes.Buffer
	sum     installflow.Summary
	err     error
	offers  *offers
	replies *testkit.ScriptedResponder
}

// runEngineRaw runs e against a raw reporter, a scripted responder and an offer-keeping
// reveal sink (nil reveals: none).
func runEngineRaw(t *testing.T, e *BootstrapEngine, req installflow.Request, reveals bool, replies ...testkit.Reply) *bootstrapRawRun {
	t.Helper()
	r := &bootstrapRawRun{raw: &rawReporter{}, replies: testkit.NewScriptedResponder(replies...)}
	rt := installflow.Runtime{
		Events: r.raw, Prompts: installflow.NewBroker(r.raw, r.replies, req.AssumeYes),
		Clock: installflow.SystemClock{}, Console: installflow.Console{Stdout: &r.con, Stderr: &r.con},
	}
	if reveals {
		r.offers = &offers{}
		rt.Reveals = r.offers
	}
	req.Operation = installflow.OperationBootstrap
	r.sum, r.err = e.Run(context.Background(), req, rt)
	return r
}

func secretReply(v string) testkit.Reply {
	return testkit.Reply{Response: installflow.Response{Secret: installflow.NewSecret([]byte(v))}}
}

// The stage map: the eleven pipeline steps are the eleven pipeline stages, one for one
// and in order, after the command's own four.
func TestEveryPipelineStepHasItsOwnStage(t *testing.T) {
	ids := installflow.BootstrapSteps()
	if len(ids) != 15 {
		t.Fatalf("%d bootstrap stages, want 4 + 11", len(ids))
	}
	steps := NewDefaultPipeline().Steps
	if len(steps) != 11 || len(bootstrapStageOf) != 11 {
		t.Fatalf("%d pipeline steps and %d mapped, want 11 of each", len(steps), len(bootstrapStageOf))
	}
	for i, s := range steps {
		if got, want := bootstrapStageOf[s.Name], ids[4+i]; got != want {
			t.Errorf("step %d %q reports as %q, want %q", i+1, s.Name, got, want)
		}
	}
}

// A step with no stage fails the run instead of running unreported.
func TestAPipelineStepWithNoStageFailsTheRun(t *testing.T) {
	ran := false
	e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, Escrow: EscrowFlags{NoEscrow: true},
		Target: func(context.Context, Console, EscrowPlan) (*State, error) { return goldenBootstrapState(nil), nil },
		Pipeline: Pipeline{Steps: []Step{{Name: "Something new", Run: func(context.Context, *State) error {
			ran = true
			return nil
		}}}},
	}
	r := runEngineRaw(t, e, installflow.Request{}, true)
	if r.err == nil || ran {
		t.Fatalf("err = %v, ran = %t: an unmapped step ran", r.err, ran)
	}
}

// The run moves through every stage in order, and the pipeline's run order — the cluster,
// then the steps, then Finish — is the engine's, not its caller's.
func TestTheBootstrapEngineRunsItsStagesInOrder(t *testing.T) {
	var calls []string
	e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, Escrow: EscrowFlags{NoEscrow: true},
		Target: func(context.Context, Console, EscrowPlan) (*State, error) {
			calls = append(calls, "target")
			return goldenBootstrapState(nil), nil
		},
		Pipeline: reportOnlyPipeline(t),
		Finish: func(_ context.Context, _ Console, _ *State, err error) error {
			calls = append(calls, "finish")
			return err
		},
	}
	r := runEngineRaw(t, e, installflow.Request{}, true)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := installflow.BootstrapSteps()
	got := r.raw.steps()
	// The skipped preflight starts and finishes not_applicable, so it is in the list too.
	if !slices.Equal(got, want) {
		t.Fatalf("stages %v, want %v", got, want)
	}
	if !slices.Equal(calls, []string{"target", "finish"}) {
		t.Fatalf("calls %v", calls)
	}
	if r.sum.Outcome != installflow.RunSucceeded {
		t.Fatalf("outcome %s", r.sum.Outcome)
	}
}

// 🔴 ESCROW PRECEDENCE, THROUGH THE ENGINE: --escrow-passphrase-file, then
// DCCTL_ESCROW_PASSPHRASE, then a person — and a person only at a terminal. Routing the
// last source through the prompt broker changed who asks, never the order.
func TestEscrowPassphrasePrecedenceIsUnchangedThroughTheEngine(t *testing.T) {
	fromFile, fromEnv, typed := "from-the-file-pw", "from-the-env-pw", "typed-at-the-terminal"
	for _, tc := range []struct {
		name     string
		file     bool
		env      bool
		terminal bool
		want     string
		asked    int
		envAfter bool // the variable is still set afterwards
		wantErr  string
	}{
		{name: "file over env and prompt", file: true, env: true, terminal: true, want: fromFile, envAfter: true},
		{name: "env over prompt", env: true, terminal: true, want: fromEnv},
		{name: "prompt last", terminal: true, want: typed, asked: 2},
		{name: "no terminal, no prompt", wantErr: "no terminal to ask at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEscrow(t)
			if tc.terminal {
				stubTerminal(t)
			} else {
				orig := stdinIsTerminal
				stdinIsTerminal = func() bool { return false }
				t.Cleanup(func() { stdinIsTerminal = orig })
			}
			flags := EscrowFlags{}
			if tc.file {
				p := filepath.Join(t.TempDir(), "pass.txt")
				if err := os.WriteFile(p, []byte(fromFile+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				flags.PassphraseFile = p
			}
			if tc.env {
				t.Setenv(EscrowPassphraseEnv, fromEnv)
			}
			var plan EscrowPlan
			e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, Escrow: flags,
				Target: func(_ context.Context, _ Console, p EscrowPlan) (*State, error) {
					plan = p
					return nil, errors.New("stop here")
				},
			}
			r := runEngineRaw(t, e, installflow.Request{}, true, secretReply(typed), secretReply(typed))
			if tc.wantErr != "" {
				if r.err == nil || !strings.Contains(r.err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", r.err, tc.wantErr)
				}
			} else if plan.Passphrase != tc.want {
				t.Fatalf("passphrase from the wrong source: got %q, want %q", plan.Passphrase, tc.want)
			}
			if n := len(r.replies.Questions()); n != tc.asked {
				t.Fatalf("asked %d times, want %d", n, tc.asked)
			}
			if _, set := os.LookupEnv(EscrowPassphraseEnv); set != tc.envAfter {
				t.Fatalf("%s set afterwards = %t, want %t", EscrowPassphraseEnv, set, tc.envAfter)
			}
		})
	}
}

// The person is asked through the broker, as secret questions, and judged in the
// terminal prompt's own words.
func TestTheEscrowPromptIsASecretQuestionJudgedAsTheTerminalJudgedIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []testkit.Reply
		want    error
	}{
		{"empty", []testkit.Reply{secretReply("")}, errEmptyPassphrase},
		{"mismatch", []testkit.Reply{secretReply("one-passphrase"), secretReply("another-one")}, errPassphraseMismatch},
		{"empty confirmation", []testkit.Reply{secretReply("one-passphrase"), secretReply("")}, errPassphraseMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateEscrow(t)
			stubTerminal(t)
			e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, AssumeYes: true,
				Target: func(context.Context, Console, EscrowPlan) (*State, error) { return nil, errors.New("unreached") }}
			r := runEngineRaw(t, e, installflow.Request{AssumeYes: true}, true, tc.replies...)
			if !errors.Is(r.err, tc.want) {
				t.Fatalf("err = %v, want %v", r.err, tc.want)
			}
			for _, q := range r.replies.Questions() {
				if q.Kind != installflow.PromptSecret {
					t.Fatalf("question %s is a %s prompt; --yes must not be able to answer it", q.Question, q.Kind)
				}
			}
		})
	}
}

// 🔴 THE CANARY HARNESS OVER EVERY PATH a bootstrap's secrets take: the passphrase typed
// at the escrow prompt, the root key and credentials a step settles, the seed the report
// shows once, and an error whose text carries every class of secret (SMTP, S3, a
// webhook, a kubeconfig) — held, producer side, against everything the engine hands its
// reporter and writes to its console. Only the reveal capability may hold the seed.
func TestNoBootstrapSecretReachesAnEventALogLineOrTheSummary(t *testing.T) {
	isolateEscrow(t)
	stubTerminal(t)
	pass := canaryValue(t, "escrow-passphrase")
	var everything strings.Builder
	for _, c := range testkit.Canaries() {
		everything.WriteString(c.Value + " ")
	}
	for _, tc := range []struct {
		name   string
		failAt string
	}{{name: "success"}, {name: "a step fails with every secret in its error", failAt: "Apply infrastructure"}} {
		t.Run(tc.name, func(t *testing.T) {
			p := reportOnlyPipeline(t)
			if tc.failAt != "" {
				for i := range p.Steps {
					if p.Steps[i].Name == tc.failAt {
						p.Steps[i].Run = func(context.Context, *State) error { return errors.New(everything.String()) }
					}
				}
			}
			e := &BootstrapEngine{Instance: "acme", SkipPreflight: true,
				Target:   func(context.Context, Console, EscrowPlan) (*State, error) { return canaryState(t), nil },
				Pipeline: p,
			}
			r := runEngineRaw(t, e, installflow.Request{}, true, secretReply(pass), secretReply(pass))
			if (r.err != nil) != (tc.failAt != "") {
				t.Fatalf("err = %v", r.err)
			}
			testkit.AssertNoCanary(t, "the events", testkit.Dump(anySlice(r.raw.events)...))
			testkit.AssertNoCanary(t, "the summary", testkit.Dump(r.raw.summary, r.sum))
			testkit.AssertNoCanary(t, "the console", r.con.Bytes())
			testkit.AssertNoCanary(t, "the log writers", r.raw.logs.Bytes())
			for _, o := range r.offers.got {
				testkit.AssertNoCanary(t, "a reveal offer's printable forms", testkit.Dump(o))
			}
			// And the hub is told every value, for what a careless tool might echo.
			for _, label := range []string{"escrow-passphrase", "root-key", "superuser-seed", "s3-secret-key"} {
				if !slices.Contains(r.raw.secrets, canaryValue(t, label)) {
					t.Errorf("the %s is not registered for redaction", label)
				}
			}
		})
	}
}

// 🔴 DEFENCE IN DEPTH: what a careless tool echoes into the console of a presenter that
// takes it as log lines is redacted at the hub, for every secret the bootstrap settled.
func TestABootstrapToolEchoingSecretsIsRedactedFromTheStream(t *testing.T) {
	isolateEscrow(t)
	stubTerminal(t)
	pass := canaryValue(t, "escrow-passphrase")
	p := reportOnlyPipeline(t)
	var echoed int
	for i := range p.Steps {
		if p.Steps[i].Name == "Install instance (Helm)" {
			p.Steps[i].Run = func(ctx context.Context, st *State) error {
				for _, v := range []string{st.Values["secretsRootKey"], st.Escrow.Passphrase,
					st.Credentials.SuperuserPassword, st.Credentials.ObjectStoreSecret} {
					out(ctx).println("tool says: " + v)
					echoed++
				}
				return nil
			}
		}
	}
	e := &BootstrapEngine{Instance: "acme", SkipPreflight: true,
		Target: func(context.Context, Console, EscrowPlan) (*State, error) { return canaryState(t), nil }, Pipeline: p}

	sink := &testkit.CaptureSink{}
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationBootstrap, Sink: sink,
		Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	so, _ := hub.LogWriter("", "dcctl", installflow.LogInfo)
	se, _ := hub.LogWriter("", "dcctl.stderr", installflow.LogInfo)
	sum, err := hub.Run(context.Background(), e, installflow.Request{Operation: installflow.OperationBootstrap},
		installflow.Runtime{Prompts: installflow.NewBroker(hub, testkit.NewScriptedResponder(secretReply(pass), secretReply(pass)), false),
			Console: installflow.Console{Stdout: so, Stderr: se, Flush: hub.Flush}})
	<-hub.Done()
	if err != nil || !sum.Succeeded() {
		t.Fatalf("%v %s", err, sum.Outcome)
	}
	if echoed != 4 {
		t.Fatalf("the tool echoed %d values: the test proves nothing", echoed)
	}
	envs := sink.Envelopes()
	if verr := testkit.VerifyStream(envs); verr != nil {
		t.Fatal(verr)
	}
	testkit.AssertNoCanary(t, "the stream", testkit.Dump(anySlice(envs)...))
	testkit.AssertNoCanary(t, "the projection", testkit.Dump(hub.Projection()))
	// No reveal sink: the seed was never offered, and the summary points at its Secret.
	if len(sum.Credentials) != 1 || sum.Credentials[0].Name != "dci-acme-superuser" {
		t.Fatalf("summary credentials %+v, want the superuser Secret", sum.Credentials)
	}
}

// 🔴 THE SEED, ONCE: offered under printSuperuserReport's eligibility and nowhere else,
// as a capability the first Show consumes.
func TestTheSuperuserSeedIsOfferedOnceUnderTheReportsEligibility(t *testing.T) {
	seed := canaryValue(t, "superuser-seed")
	for _, tc := range []struct {
		name    string
		mutate  func(*State)
		req     installflow.Request
		reveals bool
		offered bool
	}{
		{name: "minted by this run", reveals: true, offered: true},
		{name: "recovered from an earlier run that died", mutate: func(st *State) { st.SuperuserSeed = superuserSeedRecovered },
			reveals: true, offered: true},
		{name: "over a live instance", mutate: func(st *State) { st.OverLiveInstance = true }, reveals: true},
		{name: "restored from escrow", mutate: func(st *State) {
			st.Escrow = EscrowPlan{RestoredFrom: "/x.escrow"}
		}, reveals: true},
		{name: "relational store restored", mutate: func(st *State) { st.Restore = RestorePlan{RdbFrom: "dc-rdb"} }, reveals: true},
		{name: "dry run", mutate: func(st *State) { st.DryRun = true }, req: installflow.Request{DryRun: true}, reveals: true},
		{name: "progress only (--yes in a screen of its own)", req: installflow.Request{AssumeYes: true, ProgressOnly: true},
			reveals: true},
		{name: "a presenter that takes no reveals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := goldenBootstrapState(tc.mutate)
			st.Credentials.SuperuserPassword = seed
			e := &BootstrapEngine{Instance: "acme", DryRun: tc.req.DryRun, AssumeYes: tc.req.AssumeYes, SkipPreflight: true,
				Escrow:   EscrowFlags{NoEscrow: true},
				Target:   func(context.Context, Console, EscrowPlan) (*State, error) { return st, nil },
				Pipeline: reportOnlyPipeline(t)}
			r := runEngineRaw(t, e, tc.req, tc.reveals)
			if r.err != nil {
				t.Fatal(r.err)
			}
			var got []installflow.RevealOffer
			if r.offers != nil {
				got = r.offers.got
			}
			if (len(got) == 1) != tc.offered || len(got) > 1 {
				t.Fatalf("%d offers, want offered = %t", len(got), tc.offered)
			}
			testkit.AssertNoCanary(t, "the console", r.con.Bytes())
			testkit.AssertNoCanary(t, "the events and summary", testkit.Dump(append(anySlice(r.raw.events), r.raw.summary)...))
			if !tc.offered {
				for _, o := range got {
					o.Reveal.Discard()
				}
				return
			}
			o := got[0]
			if o.Subject != installflow.RevealSuperuserSeed || o.Where.Name != "dci-acme-superuser" ||
				o.Step != installflow.StepBootstrapReport {
				t.Fatalf("offer %+v", o)
			}
			var shown []string
			if err := o.Reveal.Show(func(v []byte) { shown = append(shown, string(v)) }); err != nil {
				t.Fatal(err)
			}
			if err := o.Reveal.Show(func(v []byte) { shown = append(shown, string(v)) }); !errors.Is(err, installflow.ErrRevealConsumed) {
				t.Fatalf("a second Show returned %v", err)
			}
			if len(shown) != 1 || shown[0] != seed {
				t.Fatalf("shown %d times", len(shown))
			}
			if !strings.Contains(r.con.String(), "shown only this once") {
				t.Fatalf("the report does not say the seed is shown once:\n%s", r.con.String())
			}
		})
	}
}

// The plain presenter shows the seed where the report always did, exactly once, on its
// own stdout — and the capability is spent by the time the run returns.
func TestThePlainPresenterShowsTheSeedOnceAndSpendsTheCapability(t *testing.T) {
	seed := canaryValue(t, "superuser-seed")
	st := goldenBootstrapState(nil)
	st.Credentials.SuperuserPassword = seed
	var keep *offers
	e := &BootstrapEngine{Instance: "acme", SkipPreflight: true, Escrow: EscrowFlags{NoEscrow: true},
		Target: func(context.Context, Console, EscrowPlan) (*State, error) { return st, nil }, Pipeline: reportOnlyPipeline(t)}
	var stdout, stderr bytes.Buffer
	// Wrap the plain sink to keep the offer it was handed.
	err := runPlainWithSpy(t, e, &stdout, &stderr, &keep)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stdout.String(), seed); n != 1 {
		t.Fatalf("the seed appears %d times on stdout, want 1", n)
	}
	if len(keep.got) != 1 || !keep.got[0].Reveal.Consumed() {
		t.Fatalf("the offer was not spent: %+v", keep.got)
	}
	if err := keep.got[0].Reveal.Show(func([]byte) { t.Fatal("shown twice") }); !errors.Is(err, installflow.ErrRevealConsumed) {
		t.Fatalf("second Show: %v", err)
	}
	testkit.AssertNoCanary(t, "stderr", stderr.Bytes())
}

// spySink forwards to the plain presenter's own reveal sink and keeps the offer.
type spySink struct {
	next installflow.RevealSink
	keep *offers
}

func (s spySink) Offer(o installflow.RevealOffer) {
	s.keep.Offer(o)
	s.next.Offer(o)
}

// runPlainWithSpy runs e as the plain presenter does, with a spy between the engine and
// the presenter's reveal sink.
func runPlainWithSpy(t *testing.T, e *BootstrapEngine, stdout, stderr *bytes.Buffer, keep **offers) error {
	t.Helper()
	*keep = &offers{}
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationBootstrap,
		Sink: installplain.Sink{W: stdout}})
	if err != nil {
		return err
	}
	con := installflow.Console{Stdout: stdout, Stderr: stderr, Flush: hub.Flush}
	_, err = hub.Run(context.Background(), e, installflow.Request{Operation: installflow.OperationBootstrap},
		installflow.Runtime{Prompts: installflow.NewBroker(hub, installplain.Responder{Out: stdout}, false), Console: con,
			Reveals: spySink{next: installplain.Reveals{W: stdout}, keep: *keep}})
	<-hub.Done()
	return err
}

// The plain presenter asks for the escrow passphrase exactly as the terminal prompt did:
// the purpose, "  Passphrase: ", a newline, "  Confirm:    ", a newline — and the answer
// is never on its output.
func TestThePlainPresenterAsksForThePassphraseAsTheTerminalDid(t *testing.T) {
	isolateEscrow(t)
	stubTerminal(t)
	pass := canaryValue(t, "escrow-passphrase")
	var plan EscrowPlan
	e := &BootstrapEngine{Instance: "acme", SkipPreflight: true,
		Target: func(_ context.Context, _ Console, p EscrowPlan) (*State, error) {
			plan = p
			return nil, errors.New("stop here")
		}}
	var stdout, stderr bytes.Buffer
	reads := 0
	err := installplain.Presenter{Stdout: &stdout, Stderr: &stderr, ReadHidden: func() ([]byte, error) {
		reads++
		return []byte(pass), nil
	}}.Run(context.Background(), e, installflow.Request{Operation: installflow.OperationBootstrap}, nil)
	if err == nil || err.Error() != "stop here" {
		t.Fatalf("err = %v", err)
	}
	if plan.Passphrase != pass || reads != 2 {
		t.Fatalf("passphrase %t after %d reads", plan.Passphrase == pass, reads)
	}
	want := "Passphrase to protect the root-key escrow for instance \"acme\"\n  Passphrase: \n  Confirm:    \n"
	if stdout.String() != want {
		t.Fatalf("stdout %q, want %q", stdout.String(), want)
	}
	testkit.AssertNoCanary(t, "stdout", stdout.Bytes())
	testkit.AssertNoCanary(t, "stderr", stderr.Bytes())
}

func anySlice[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
