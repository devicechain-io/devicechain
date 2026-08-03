// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

// fakeTeardown stands in for the admin surface, recording the order of calls. It
// replaces the NETWORK only — destroySim's ordering and its unconditional tenant
// delete are the real code under test.
type fakeTeardown struct {
	calls           []string
	gotEmail        string
	gotTenant       string
	identityRemoved bool
	tenantRemoved   bool
	identityErr     error
	tenantErr       error
}

func (f *fakeTeardown) DeleteIdentity(_ context.Context, email string) (bool, error) {
	f.calls = append(f.calls, "identity")
	f.gotEmail = email
	return f.identityRemoved, f.identityErr
}

func (f *fakeTeardown) DeleteTenant(_ context.Context, token string) (bool, error) {
	f.calls = append(f.calls, "tenant")
	f.gotTenant = token
	return f.tenantRemoved, f.tenantErr
}

// recordDeleter is the local-record half of the teardown, recording into the same
// call log so the ORDER across both halves is one assertion rather than two.
func (f *fakeTeardown) recordDeleter() func() error {
	return func() error {
		f.calls = append(f.calls, "record")
		return nil
	}
}

func testRecord() *sim.Record {
	return &sim.Record{
		Name:     "wl",
		Tenant:   sim.DeriveTenant("wl"),
		SimEmail: sim.DeriveEmail("wl"),
	}
}

// The tenant delete is not optional and not conditional.
//
// It was, until this: `--purge` made it opt-in, and the default path deleted the sim
// identity — the only member the tenant is ever given, since `sim create` grants a
// membership to nothing else — while its help said the tenant was being kept "for
// inspection". What survived was a tenant in nobody's tenant menu.
func TestDestroySimDeletesTheTenantWithoutBeingAsked(t *testing.T) {
	rec := testRecord()
	fake := &fakeTeardown{identityRemoved: true, tenantRemoved: true}
	var out strings.Builder

	if err := destroySim(context.Background(), fake, rec, fake.recordDeleter(), &out); err != nil {
		t.Fatalf("destroySim: %v", err)
	}

	// The ORDER is the load-bearing part: the server refuses a tenant delete while
	// any membership still references it, so an identity-last teardown would fail
	// against a real instance while passing any test that only checked both ran.
	if got := strings.Join(fake.calls, ","); got != "identity,tenant,record" {
		t.Errorf("teardown order = %q, want \"identity,tenant,record\" — the tenant delete is "+
			"rejected while the sim's membership still exists, and the local record is what "+
			"makes an unfinished teardown retryable", got)
	}
	if fake.gotEmail != rec.SimEmail {
		t.Errorf("deleted identity %q, want %q", fake.gotEmail, rec.SimEmail)
	}
	if fake.gotTenant != rec.Tenant {
		t.Errorf("deleted tenant %q, want %q", fake.gotTenant, rec.Tenant)
	}
	if !strings.Contains(out.String(), rec.Tenant) {
		t.Errorf("output does not name the tenant it removed:\n%s", out.String())
	}
}

// A re-run after a half-finished teardown still deletes the tenant. This is the
// sharpest form of "unconditional": the identity is already gone (removed=false),
// which is exactly the state a failed first attempt leaves behind, and the tenant
// must still go rather than the command reporting success over a survivor.
func TestDestroySimDeletesTheTenantEvenWhenTheIdentityWasAlreadyGone(t *testing.T) {
	rec := testRecord()
	fake := &fakeTeardown{identityRemoved: false, tenantRemoved: true}

	if err := destroySim(context.Background(), fake, rec, fake.recordDeleter(), io.Discard); err != nil {
		t.Fatalf("destroySim: %v", err)
	}
	if fake.gotTenant != rec.Tenant {
		t.Errorf("tenant %q was not deleted after an already-removed identity", rec.Tenant)
	}
}

// A failed identity delete stops the teardown there. Continuing would attempt a
// tenant delete the server is going to refuse anyway (the membership is still
// live), turning one clear error into two confusing ones.
func TestDestroySimStopsBeforeTheTenantWhenTheIdentityFails(t *testing.T) {
	boom := errors.New("delete identity: 503")
	fake := &fakeTeardown{identityErr: boom}

	err := destroySim(context.Background(), fake, testRecord(), fake.recordDeleter(), io.Discard)
	if !errors.Is(err, boom) {
		t.Fatalf("destroySim error = %v, want it to wrap %v", err, boom)
	}
	if got := strings.Join(fake.calls, ","); got != "identity" {
		t.Errorf("calls = %q, want only \"identity\" — the tenant delete must not be "+
			"attempted while the membership is still live", got)
	}
}

// A failed tenant delete leaves the LOCAL RECORD ALONE, and that is the assertion
// that matters: the record is the only thing naming the tenant that just survived,
// so deleting it would strand a live tenant with nothing to retry against. The wrap
// must also preserve the cause — the server's reason is the actionable part.
func TestDestroySimKeepsTheLocalRecordWhenTheTenantDeleteFails(t *testing.T) {
	boom := errors.New("tenant still has memberships")
	fake := &fakeTeardown{identityRemoved: true, tenantErr: boom}

	err := destroySim(context.Background(), fake, testRecord(), fake.recordDeleter(), io.Discard)
	if !errors.Is(err, boom) {
		t.Fatalf("destroySim error = %v, want it to wrap %v", err, boom)
	}
	if strings.Contains(strings.Join(fake.calls, ","), "record") {
		t.Errorf("the local sim record was deleted after a FAILED tenant delete (calls = %v), "+
			"which strands a live tenant with nothing naming it and makes destroy unretryable",
			fake.calls)
	}
}

// ---- Is this sim's actor still running? ----------------------------------------

// The probe must answer about THIS sim, not about whatever answers on the port.
//
// 🔴 --control-addr defaults to http://localhost:8090 for EVERY sim, so a second
// sim's actor is the ordinary case, not a contrived one. A probe that only checked
// "did something answer" would warn about a process that has nothing to do with the
// sim being destroyed — and the warning it prints tells the operator to go stop it.
func TestSimActorRunningIdentifiesTheSimByTenant(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	body = `{"state":"running","tenant":"sim-wl"}`
	if !simActorRunning(context.Background(), srv.URL, "sim-wl") {
		t.Error("a running actor reporting this sim's tenant was not detected")
	}

	body = `{"state":"running","tenant":"sim-other"}`
	if simActorRunning(context.Background(), srv.URL, "sim-wl") {
		t.Error("another sim's actor on the shared default control port was reported as this one")
	}

	// Every uncertainty resolves to "not running", so the warning never fires on a
	// host dcctl simply cannot reach.
	body = `not json`
	if simActorRunning(context.Background(), srv.URL, "sim-wl") {
		t.Error("an unparseable status body was treated as a running actor")
	}
	srv.Close()
	if simActorRunning(context.Background(), srv.URL, "sim-wl") {
		t.Error("an unreachable control address was treated as a running actor")
	}
}

// ---- The commands `sim create` tells an operator to run next -------------------

// checkSuggestedCommands resolves every `dcctl …` invocation in lines against the
// REAL cobra tree, returning the problems it found and the command paths it reached.
//
// Shared by the gate below and its negative control, so the control exercises the
// same checker rather than a re-implementation of it.
func checkSuggestedCommands(lines []string) (problems, reached []string) {
	for _, line := range lines {
		invocations := dcctlInvocations(line)
		if len(invocations) == 0 && strings.TrimSpace(line) != "" {
			// Otherwise a line whose binary token drifted ("dcttl sim destroy wl") is
			// checked not at all, and only the coverage guard below would notice.
			problems = append(problems, "line names no dcctl invocation: "+line)
			continue
		}
		for _, argv := range invocations {
			// Resolve through cobra's own Find, flags and all: it strips them using the
			// real per-command arity, which a hand-rolled split cannot do (a flag with a
			// space-separated value would otherwise land in the command path).
			cmd, rest, err := rootCmd.Find(argv)
			if err != nil {
				problems = append(problems, "dcctl "+strings.Join(argv, " ")+": "+err.Error())
				continue
			}
			// Runnable is the exact test for "an operator can type this": cobra resolves
			// as deep as it can and reports an unknown SUBCOMMAND as leftover args on a
			// parent, not as an error, so `dcctl sim vaporize wl` lands on `dcctl sim` —
			// which has no Run and is not something anyone can execute.
			if !cmd.Runnable() {
				problems = append(problems, "dcctl "+strings.Join(argv, " ")+": "+
					cmd.CommandPath()+" is not runnable"+firstUnknown(rest))
				continue
			}
			reached = append(reached, cmd.CommandPath())

			var flags, positional []string
			for _, tok := range rest {
				if strings.HasPrefix(tok, "-") && len(tok) > 1 {
					flags = append(flags, tok)
					continue
				}
				positional = append(positional, tok)
			}
			for _, flag := range flags {
				name, _, _ := strings.Cut(strings.TrimLeft(flag, "-"), "=")
				if !flagExists(cmd, name, strings.HasPrefix(flag, "--")) {
					problems = append(problems, cmd.CommandPath()+": no flag "+flag)
				}
			}
			// The argument count is only checkable on a flag-free invocation: with a
			// flag present, a space-separated VALUE is indistinguishable from a
			// positional here, and guessing would reject a legitimate suggestion.
			if len(flags) == 0 && cmd.Args != nil {
				if err := cmd.Args(cmd, positional); err != nil {
					problems = append(problems, cmd.CommandPath()+": "+err.Error())
				}
			}
		}
	}
	return problems, reached
}

// firstUnknown names the leftover token that most likely caused a non-runnable
// resolution, for a message that points at the typo rather than at its symptom.
func firstUnknown(rest []string) string {
	for _, tok := range rest {
		if !strings.HasPrefix(tok, "-") {
			return " (no subcommand " + tok + ")"
		}
	}
	return ""
}

// flagExists reports whether a command carries a flag, on itself or inherited, by
// long name or shorthand.
func flagExists(cmd *cobra.Command, name string, long bool) bool {
	if !long {
		// pflag PANICS on a multi-character shorthand lookup, so a combined form like
		// `-abc` must be reported rather than looked up.
		if len(name) != 1 {
			return false
		}
		return cmd.Flags().ShorthandLookup(name) != nil ||
			cmd.InheritedFlags().ShorthandLookup(name) != nil
	}
	return cmd.Flags().Lookup(name) != nil || cmd.InheritedFlags().Lookup(name) != nil
}

// dcctlInvocations splits a printed line into the dcctl invocations it contains. A
// line can hold more than one (`dcctl sim stop x   |   dcctl sim start x`), and a
// token may be bracketed as optional, which is stripped so an optional flag is still
// checked rather than silently skipped for not starting with a dash.
func dcctlInvocations(line string) [][]string {
	var out [][]string
	for _, tok := range strings.Fields(line) {
		tok = strings.Trim(tok, "[]")
		switch {
		case tok == "dcctl":
			out = append(out, nil)
		case tok == "|" || tok == "":
			// A separator between two invocations on one line.
		case len(out) > 0:
			out[len(out)-1] = append(out[len(out)-1], tok)
		}
	}
	return out
}

// Every command `sim create` prints as a next step must exist, be runnable, take the
// arguments it is shown with, and carry every flag the line names.
//
// WHY. These lines are copy-paste bait, and nothing else ties them to the tree they
// describe. This block printed `dcctl sim destroy <name> [--purge]` for as long as
// that flag existed; when the flag was removed, the only thing standing between the
// operator and a command that errors on an unknown flag was remembering to edit a
// Printf three files away.
//
// SCOPE, stated exactly. This covers dcctl invocations in the lines
// simCreateNextSteps returns. It does NOT cover the `dc-simulator --handshake …`
// line printed just above them (a different binary, with its own stdlib flag set),
// the suggestions embedded in error strings elsewhere in the CLI, or the ones in
// deploy/local/up.sh.
func TestSimCreateNextStepsNameRealCommandsAndFlags(t *testing.T) {
	lines := simCreateNextSteps("wl")
	problems, reached := checkSuggestedCommands(lines)
	for _, p := range problems {
		t.Errorf("`sim create` suggests a command an operator cannot run — %s", p)
	}

	// THE COVERAGE GUARD. An empty or restructured next-steps block would make every
	// assertion above pass while asserting nothing, so name the commands that must be
	// reachable — `destroy` above all, since it is the line that actually rotted.
	got := strings.Join(reached, " ")
	for _, want := range []string{
		"dcctl sim status", "dcctl sim start", "dcctl sim stop", "dcctl sim destroy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the next-steps block no longer suggests %q, so this test stopped "+
				"covering it silently.\n  reached: %v\n  lines: %v", want, reached, lines)
		}
	}
}

// The negative control, and it is not optional here: the lines above currently carry
// NO flags at all, so the flag half of the checker runs over nothing. Without this it
// would be a check that cannot fail — passing just as happily if flagExists always
// returned true — and it would stay that way until the day a flag came back.
func TestSuggestedCommandCheckerRejectsWhatItShould(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"an unknown flag", "dcctl sim destroy wl --purge", "no flag --purge"},
		{"an unknown flag in brackets", "dcctl sim destroy wl [--purge]", "no flag --purge"},
		{"an unknown shorthand", "dcctl sim status wl -Z", "no flag -Z"},
		{"a combined shorthand pflag would panic on", "dcctl sim status wl -abc", "no flag -abc"},
		{"an unknown subcommand", "dcctl sim vaporize wl", "no subcommand vaporize"},
		{"a command that is not runnable", "dcctl sim", "not runnable"},
		{"a lost argument", "dcctl sim destroy", "accepts 1 arg"},
		{"a stray extra argument", "dcctl sim destroy wl oops", "accepts 1 arg"},
		{"a typo in the binary itself", "dcttl sim destroy wl", "names no dcctl invocation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, _ := checkSuggestedCommands([]string{tc.line})
			if len(problems) == 0 {
				t.Fatalf("the checker accepted %q, so it cannot fail on a real defect", tc.line)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Errorf("problems for %q = %v, want one mentioning %q", tc.line, problems, tc.want)
			}
		})
	}

	// And it must still accept the real thing — a checker that rejected everything
	// would pass every case above and fail the gate for the wrong reason. The second
	// line puts a flag VALUE before the subcommand, which is exactly what a
	// hand-rolled path/flag split gets wrong.
	for _, ok := range []string{
		"dcctl sim create wl --manifest widgetlab -s localhost",
		"dcctl sim --server localhost status wl",
	} {
		if problems, _ := checkSuggestedCommands([]string{ok}); len(problems) > 0 {
			t.Errorf("the checker rejected a valid invocation %q: %v", ok, problems)
		}
	}
}
