// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// call is one GraphQL round-trip the fake client saw.
type call struct {
	query string
	vars  map[string]any
}

// fakeClient answers each call with the next canned response, so a test can describe
// a server as a sequence of pages. A response is the JSON the real client would decode
// out of the envelope's "data" object, which keeps these tests honest about the shape
// the walk actually parses rather than about a Go struct someone hand-built.
type fakeClient struct {
	responses []string
	errs      []error
	calls     []call
}

func (f *fakeClient) Query(_ context.Context, _ string, query string, vars map[string]any, out any) error {
	i := len(f.calls)
	f.calls = append(f.calls, call{query: query, vars: vars})
	if i < len(f.errs) && f.errs[i] != nil {
		return f.errs[i]
	}
	if i >= len(f.responses) {
		return fmt.Errorf("the walk asked for call %d and the fake has %d canned responses; "+
			"an unbounded loop looks exactly like this", i+1, len(f.responses))
	}
	return json.Unmarshal([]byte(f.responses[i]), out)
}

// demotePage renders one demoteAssertedPresence result. lastId of "" is null.
func demotePage(scanned, demoted, skipped int, lastId string) string {
	id := "null"
	if lastId != "" {
		id = fmt.Sprintf("%q", lastId)
	}
	return fmt.Sprintf(`{"demoteAssertedPresence":{"scanned":%d,"demoted":%d,"skipped":%d,"lastId":%s}}`,
		scanned, demoted, skipped, id)
}

// statesPage renders one deviceStates result from (token, source, presenceSource)
// triples.
func statesPage(rows ...[3]string) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		src := "null"
		if r[1] != "" {
			src = fmt.Sprintf("%q", r[1])
		}
		parts = append(parts, fmt.Sprintf(`{"deviceToken":%q,"source":%s,"presenceSource":%q}`, r[0], src, r[2]))
	}
	return fmt.Sprintf(`{"deviceStates":{"results":[%s]}}`, strings.Join(parts, ","))
}

func baseOptions(out *bytes.Buffer) Options {
	return Options{
		Endpoint: "http://device-state/graphql",
		Source:   "mqtt1",
		Reason:   "the mqtt tap was decommissioned",
		PageSize: 2,
		Out:      out,
		Approve:  func() (bool, error) { return true, nil },
	}
}

// 🔴 AN ABSENT APPROVER IS NOT APPROVAL. A caller that forgot to wire one gets a
// refusal on its first run, not a fleet-wide write — which is the whole reason the
// gate sits in the package that writes rather than in the command that prompts.
//
// Killing mutation: delete the nil check in Run — a forgotten approval seam becomes a
// silent demotion, and nothing in the tree would say so.
func TestANonDryRunWithNoApproverIsRefused(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.Approve = nil
	c := &fakeClient{responses: []string{demotePage(0, 0, 0, "")}}
	if _, err := Run(context.Background(), c, opts); err == nil {
		t.Fatal("a demotion with no approval step ran anyway")
	}
	if len(c.calls) != 0 {
		t.Errorf("%d call(s) were made without approval", len(c.calls))
	}
}

// Declining writes nothing at all, and says so in a way a caller can distinguish from
// a completed run.
//
// Killing mutation: ignore the approver's answer, or return a nil error alongside it —
// the command then prints a successful demotion of zero devices.
func TestADeclinedRunWritesNothing(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.Approve = func() (bool, error) { return false, nil }
	c := &fakeClient{responses: []string{demotePage(2, 2, 0, "7")}}
	sum, err := Run(context.Background(), c, opts)
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("error = %v, want ErrDeclined", err)
	}
	if len(c.calls) != 0 {
		t.Errorf("%d call(s) were made after the run was declined", len(c.calls))
	}
	if sum.Demoted != 0 {
		t.Errorf("demoted = %d after a declined run", sum.Demoted)
	}
}

// An approver that FAILS is not an approver that said yes. The refusal a terminal-less
// run produces arrives this way, so dropping the error check would make every scripted
// run without --yes demote the fleet it was refusing to.
//
// Killing mutation: `ok, _ := opts.Approve()`.
func TestAnApprovalFailureStopsTheRun(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	boom := errors.New("no terminal to confirm on")
	opts.Approve = func() (bool, error) { return true, boom }
	c := &fakeClient{responses: []string{demotePage(2, 2, 0, "7")}}
	if _, err := Run(context.Background(), c, opts); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the approver's own failure", err)
	}
	if len(c.calls) != 0 {
		t.Errorf("%d call(s) were made after the approval failed", len(c.calls))
	}
}

// A dry run writes nothing, so it is never asked. Prompting on a read is how an
// operator learns to answer without reading.
//
// Killing mutation: move the approval ahead of the DryRun branch.
func TestADryRunIsNeverAskedForApproval(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	opts.PageSize = 10
	asked := 0
	opts.Approve = func() (bool, error) { asked++; return true, nil }
	c := &fakeClient{responses: []string{statesPage([3]string{"dev-a", "mqtt1", "ASSERTED"})}}
	if _, err := Run(context.Background(), c, opts); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if asked != 0 {
		t.Errorf("a dry run asked for approval %d time(s)", asked)
	}
}

// Approval is asked ONCE for the whole walk, not once per page. A ten-page repair
// that prompts ten times is a repair nobody finishes.
//
// Killing mutation: move the approval inside walk's loop.
func TestApprovalIsAskedOnceForTheWholeWalk(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	asked := 0
	opts.Approve = func() (bool, error) { asked++; return true, nil }
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(2, 2, 0, "9"),
		demotePage(1, 1, 0, "10"),
	}}
	if _, err := Run(context.Background(), c, opts); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if asked != 1 {
		t.Errorf("approval was asked %d time(s) across a 3-page walk, want 1", asked)
	}
}

// 🔴 THE TERMINATION RULE. A page that scanned a full limit but demoted NOTHING (every
// row skippable) is not the end of the source. A loop keyed on `demoted` stops here
// with two thirds of the fleet untouched and reports a clean finish.
//
// Killing mutation: `if int(p.Demoted) < opts.PageSize` — one page, three devices left
// frozen.
func TestTheWalkTerminatesOnScannedNotDemoted(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{
		demotePage(2, 0, 2, "7"),
		demotePage(2, 2, 0, "9"),
		demotePage(1, 1, 0, "10"),
	}}
	sum, err := Run(context.Background(), c, baseOptions(out))
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if sum.Pages != 3 {
		t.Errorf("pages = %d, want 3 — a page that demoted nothing is not the end of the source", sum.Pages)
	}
	if sum.Scanned != 5 || sum.Demoted != 3 || sum.Skipped != 2 {
		t.Errorf("summary = %+v, want scanned 5 / demoted 3 / skipped 2", sum)
	}
}

// The other half of the rule: a SHORT page ends the walk, and nothing after it is
// requested. The fake refuses a fourth call, so an over-running loop fails loudly
// rather than hanging.
//
// Killing mutation: `<=` for `<` in the termination test — a fourth call the fake
// cannot answer.
func TestAShortPageEndsTheWalk(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{demotePage(1, 1, 0, "4")}}
	sum, err := Run(context.Background(), c, baseOptions(out))
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(c.calls))
	}
	if sum.Demoted != 1 {
		t.Errorf("demoted = %d, want 1", sum.Demoted)
	}
}

// The cursor is the LAST ID SCANNED from the previous page, and the first page carries
// none at all.
//
// Killing mutation: never assign afterId, or send the same one twice — the walk then
// re-demotes page one forever.
func TestTheCursorAdvancesFromEachPagesLastId(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(2, 2, 0, "9"),
		demotePage(0, 0, 0, ""),
	}}
	if _, err := Run(context.Background(), c, baseOptions(out)); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(c.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(c.calls))
	}
	if _, ok := c.calls[0].vars["afterId"]; ok {
		t.Errorf("the first page sent afterId = %v; it must be omitted so the walk starts at the beginning",
			c.calls[0].vars["afterId"])
	}
	for i, want := range []string{"7", "9"} {
		got := c.calls[i+1].vars["afterId"]
		if got != want {
			t.Errorf("page %d sent afterId = %v, want %q", i+2, got, want)
		}
	}
}

// A full page whose cursor does not move would make the walk demand the same page
// forever, and the server would keep answering it. It is reported, not spun in.
//
// Killing mutation: drop the `next <= prev` refusal in advance().
func TestANonAdvancingCursorIsRefusedRatherThanLooped(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(2, 2, 0, "7"),
	}}
	sum, err := Run(context.Background(), c, baseOptions(out))
	if err == nil {
		t.Fatal("a repeated cursor was accepted; the walk would repeat that page forever")
	}
	if !strings.Contains(err.Error(), "did not advance") {
		t.Errorf("error = %v, want it to name the cursor that did not advance", err)
	}
	if sum.Demoted != 4 {
		t.Errorf("demoted = %d, want 4 — the pages that DID complete must still be reported", sum.Demoted)
	}
}

// A full page with a null cursor cannot be continued from, and guessing one would
// restart the walk at the beginning.
//
// Killing mutation: drop the nil check in advance().
func TestAFullPageWithNoCursorIsRefused(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{demotePage(2, 2, 0, "")}}
	if _, err := Run(context.Background(), c, baseOptions(out)); err == nil {
		t.Fatal("a full page with no cursor was accepted")
	}
}

// 🔴 THE TYPO SIGNAL. A misspelled --source matches nothing and returns a perfectly
// successful empty page, which is byte-identical to a source that is already fully
// demoted. Without the warning the likelier of the two reads as success.
//
// Killing mutation: delete the warnNothingMatched call in walk().
func TestAnEmptyFirstPageWarnsThatTheSourceMayBeMisspelled(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{demotePage(0, 0, 0, "")}}
	sum, err := Run(context.Background(), c, baseOptions(out))
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if sum.Pages != 1 || sum.Demoted != 0 {
		t.Errorf("summary = %+v, want one page and nothing demoted", sum)
	}
	if !strings.Contains(out.String(), `Nothing matched source "mqtt1"`) {
		t.Errorf("an empty first page printed no warning naming the source:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "spelling") {
		t.Errorf("the warning does not point at the spelling, which is the whole reason it exists:\n%s", out.String())
	}
}

// …and an empty page LATER in a walk is an ordinary finish. Warning there would cry
// wolf on every successful run of a source whose row count divides the page size.
//
// Killing mutation: drop the `sum.Pages == 1` conjunct.
func TestAnEmptyLaterPageIsNotAWarning(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(0, 0, 0, ""),
	}}
	if _, err := Run(context.Background(), c, baseOptions(out)); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if strings.Contains(out.String(), "Nothing matched") {
		t.Errorf("a finished walk warned about its own last page:\n%s", out.String())
	}
}

// 🔴 THREE-STATE deviceTokens. Omitted means the whole source; an EMPTY list means
// nothing at all. A run the operator did not narrow must send no list, never [].
//
// Killing mutation: set vars["deviceTokens"] unconditionally — every unnarrowed run
// silently demotes nothing while reporting a clean walk.
func TestAnUnnarrowedRunSendsNoDeviceTokens(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{demotePage(0, 0, 0, "")}}
	if _, err := Run(context.Background(), c, baseOptions(out)); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if v, ok := c.calls[0].vars["deviceTokens"]; ok {
		t.Errorf("an unnarrowed run sent deviceTokens = %#v; omitted is what means 'the whole source'", v)
	}
}

// …and a narrowed run sends exactly the tokens it was given, on every page.
//
// Killing mutation: drop the deviceTokens variable — the narrowing is lost and the
// WHOLE source is demoted, which is the failure this command exists to bound.
func TestANarrowedRunSendsItsTokensOnEveryPage(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.Devices = []string{"dev-a", "dev-b"}
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(1, 1, 0, "8"),
	}}
	if _, err := Run(context.Background(), c, opts); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(c.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(c.calls))
	}
	for i, got := range c.calls {
		tokens, ok := got.vars["deviceTokens"].([]string)
		if !ok {
			t.Fatalf("page %d sent deviceTokens = %#v, want a []string", i+1, got.vars["deviceTokens"])
		}
		if strings.Join(tokens, ",") != "dev-a,dev-b" {
			t.Errorf("page %d sent deviceTokens = %v, want [dev-a dev-b]", i+1, tokens)
		}
	}
}

// Every page carries the source, the limit and the reason. The reason is the only
// record of a fleet-wide write, so a page that dropped it would leave part of the run
// unattributed.
func TestEveryPageCarriesSourceLimitAndReason(t *testing.T) {
	out := &bytes.Buffer{}
	c := &fakeClient{responses: []string{
		demotePage(2, 2, 0, "7"),
		demotePage(0, 0, 0, ""),
	}}
	if _, err := Run(context.Background(), c, baseOptions(out)); err != nil {
		t.Fatalf("walk: %v", err)
	}
	for i, got := range c.calls {
		if got.vars["source"] != "mqtt1" {
			t.Errorf("page %d source = %v", i+1, got.vars["source"])
		}
		if got.vars["limit"] != 2 {
			t.Errorf("page %d limit = %v, want the page size the termination rule is compared against", i+1, got.vars["limit"])
		}
		if got.vars["reason"] != "the mqtt tap was decommissioned" {
			t.Errorf("page %d reason = %v", i+1, got.vars["reason"])
		}
	}
}

// A page that failed wrote nothing, but the pages BEFORE it did. The summary comes
// back with the error so an operator is not told "nothing happened".
//
// Killing mutation: return Summary{} on the error path.
func TestAFailedPageStillReportsWhatTheWalkAlreadyWrote(t *testing.T) {
	out := &bytes.Buffer{}
	boom := errors.New("connection reset")
	c := &fakeClient{
		responses: []string{demotePage(2, 2, 0, "7"), ""},
		errs:      []error{nil, boom},
	}
	sum, err := Run(context.Background(), c, baseOptions(out))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want it to wrap the transport failure", err)
	}
	if sum.Demoted != 2 || sum.Pages != 1 {
		t.Errorf("summary = %+v, want the one completed page reported", sum)
	}
}

// 🔴 A DRY RUN WRITES NOTHING. It must never reach demoteAssertedPresence, whatever
// else it does.
//
// Killing mutation: route DryRun to walk().
func TestADryRunNeverIssuesTheMutation(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	c := &fakeClient{responses: []string{statesPage([3]string{"dev-a", "mqtt1", "ASSERTED"})}}
	sum, err := Run(context.Background(), c, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for i, got := range c.calls {
		if strings.Contains(got.query, "demoteAssertedPresence") {
			t.Fatalf("call %d issued the demotion mutation during a dry run:\n%s", i+1, got.query)
		}
		if strings.Contains(got.query, "assertedDeviceStates") {
			t.Fatalf("call %d previewed through assertedDeviceStates, which is the write path's own "+
				"reader; the preview must be an independent query an operator can also run:\n%s", i+1, got.query)
		}
		if !strings.Contains(got.query, "deviceStates(criteria:") {
			t.Fatalf("call %d is not the deviceStates search:\n%s", i+1, got.query)
		}
	}
	if sum.Demoted != 0 || !sum.DryRun {
		t.Errorf("summary = %+v, want a dry run that demoted nothing", sum)
	}
	if sum.Matched != 1 {
		t.Errorf("matched = %d, want 1", sum.Matched)
	}
	if !strings.Contains(out.String(), "Nothing was written") {
		t.Errorf("a dry run did not say it wrote nothing:\n%s", out.String())
	}
}

// The preview's narrowing is client-side, because the query it uses filters neither
// by source nor by presence. Every one of those three conjuncts has to hold.
//
// Killing mutation: drop the presenceSource test (inferred rows join the count), or
// drop the source test (a sibling source's devices are reported as in scope).
func TestADryRunCountsOnlyAssertedRowsOfThisSource(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	opts.PageSize = 10
	c := &fakeClient{responses: []string{statesPage(
		[3]string{"in-scope-1", "mqtt1", "ASSERTED"},
		[3]string{"inferred", "mqtt1", "INFERRED"},
		[3]string{"other-source", "mqtt2", "ASSERTED"},
		[3]string{"no-source", "", "ASSERTED"},
		[3]string{"in-scope-2", "mqtt1", "ASSERTED"},
	)}}
	sum, err := Run(context.Background(), c, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if sum.Matched != 2 {
		t.Errorf("matched = %d, want 2 (the two asserted mqtt1 rows)", sum.Matched)
	}
	got := out.String()
	for _, want := range []string{"in-scope-1", "in-scope-2"} {
		if !strings.Contains(got, want) {
			t.Errorf("the preview did not name %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"inferred", "other-source", "no-source"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the preview named %q, which is not in scope:\n%s", unwanted, got)
		}
	}
}

// --device narrows the preview the same way it narrows the run.
//
// Killing mutation: drop the narrow lookup — the preview reports the whole source and
// an operator approves a blast radius they were shown wrongly.
func TestADryRunNarrowsToTheNamedDevices(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	opts.PageSize = 10
	opts.Devices = []string{"in-scope-1"}
	c := &fakeClient{responses: []string{statesPage(
		[3]string{"in-scope-1", "mqtt1", "ASSERTED"},
		[3]string{"in-scope-2", "mqtt1", "ASSERTED"},
	)}}
	sum, err := Run(context.Background(), c, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if sum.Matched != 1 {
		t.Errorf("matched = %d, want 1", sum.Matched)
	}
	if strings.Contains(out.String(), "in-scope-2") {
		t.Errorf("the preview named a device outside the --device narrowing:\n%s", out.String())
	}
}

// The preview pages the projection to the end, and stops on the first short page.
//
// Killing mutation: read only page one — a fleet larger than one page is previewed as
// a fraction of itself.
func TestADryRunWalksEveryPageOfTheProjection(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	c := &fakeClient{responses: []string{
		statesPage([3]string{"dev-a", "mqtt1", "ASSERTED"}, [3]string{"dev-b", "mqtt1", "ASSERTED"}),
		statesPage([3]string{"dev-c", "mqtt1", "ASSERTED"}),
	}}
	sum, err := Run(context.Background(), c, opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(c.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(c.calls))
	}
	if c.calls[0].vars["pageNumber"] != 1 || c.calls[1].vars["pageNumber"] != 2 {
		t.Errorf("page numbers = %v, %v; want 1 then 2 (the search is 1-based)",
			c.calls[0].vars["pageNumber"], c.calls[1].vars["pageNumber"])
	}
	if sum.Matched != 3 || sum.Scanned != 3 {
		t.Errorf("summary = %+v, want three rows read and three in scope", sum)
	}
}

// A preview that matches nothing gets the same typo warning the run does — it is the
// one an operator is meant to hit FIRST.
//
// Killing mutation: warn only in walk().
func TestADryRunThatMatchesNothingWarns(t *testing.T) {
	out := &bytes.Buffer{}
	opts := baseOptions(out)
	opts.DryRun = true
	opts.PageSize = 10
	c := &fakeClient{responses: []string{statesPage([3]string{"dev-a", "mqtt2", "ASSERTED"})}}
	if _, err := Run(context.Background(), c, opts); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out.String(), `Nothing matched source "mqtt1"`) {
		t.Errorf("a preview that matched nothing printed no warning:\n%s", out.String())
	}
}

// Arguments are refused before a page is written, and the page bound is the server's.
func TestValidateRefusesUnusableArguments(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"blank source":   func(o *Options) { o.Source = "  " },
		"blank reason":   func(o *Options) { o.Reason = "" },
		"page zero":      func(o *Options) { o.PageSize = 0 },
		"page too big":   func(o *Options) { o.PageSize = MaxPageSize + 1 },
		"no endpoint":    func(o *Options) { o.Endpoint = "" },
		"empty --device": func(o *Options) { o.Devices = []string{"dev-a", " "} },
	} {
		t.Run(name, func(t *testing.T) {
			out := &bytes.Buffer{}
			opts := baseOptions(out)
			mutate(&opts)
			c := &fakeClient{responses: []string{demotePage(0, 0, 0, "")}}
			if _, err := Run(context.Background(), c, opts); err == nil {
				t.Fatal("the argument was accepted")
			}
			if len(c.calls) != 0 {
				t.Errorf("%d call(s) were made despite an unusable argument", len(c.calls))
			}
		})
	}
	// The control: the unmutated options must pass, or every case above would be
	// green for the wrong reason.
	if err := baseOptions(&bytes.Buffer{}).validate(); err != nil {
		t.Fatalf("the baseline options were refused: %v", err)
	}
}
