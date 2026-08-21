// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package presence implements `dcctl presence` — the operator door onto the
// device-state presence projection (ADR-067 demotion).
//
// Its one operation today is DEMOTION: returning an event source's ASSERTED device
// states to INFERRED, so a fleet whose presence tap has stopped running is handed
// back to the ordinary inference machinery instead of staying frozen at whatever it
// last held.
//
// 🔴 THE WALK LIVES HERE, AND THAT IS THE POINT OF THE COMMAND. The server mutation
// answers for ONE PAGE and returns a cursor; something has to drive it to the end of
// the source. Leaving that to a human clicking "next" is how most of a fleet stays
// frozen while the operator believes the repair ran — so the loop, its termination
// rule and its cursor discipline are code, in one place, with tests.
package presence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Client is the GraphQL seam this package drives: one operation, decoded into out.
// *userclient.TenantSession satisfies it, which is what carries the caller's own
// tenant access token — dcctl holds no service credential and mints no token of its
// own, so a demotion is authorized as the operator who ran it and nobody else.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// assertedPresenceSource is the value DeviceState.presenceSource reports for a row a
// transport holds custody of. It is a literal rather than an import: dcctl talks to
// device-state over the wire and does not compile that service's model. The dry run
// below is the only thing that compares against it, and a rename would show up there
// as "nothing matches", which is a signal this command already prints loudly.
const assertedPresenceSource = "ASSERTED"

// MaxPageSize mirrors the server's own bound, which is a refusal rather than a clamp:
// a caller that stops on a short page would read a silently reduced one as the end of
// the set, and this command IS that caller.
const MaxPageSize = 1000

// DefaultPageSize is the walk's page size unless --page overrides it.
const DefaultPageSize = 200

// demoteMutation is one page of the walk.
//
// deviceTokens is DECLARED here but is supplied only when the caller narrowed the
// run, and that asymmetry is load-bearing: the server reads an OMITTED list as "the
// whole source" and an EMPTY list as "nothing at all". Sending [] because a caller
// passed no --device would demote nothing while reporting a clean walk; sending the
// whole source because a caller's list came out empty would be the same slip in the
// direction that cannot be undone. buildVariables below is the only place that
// decides, so there is one answer rather than one per call site.
const demoteMutation = `mutation($source:String!,$deviceTokens:[String!],$limit:Int!,$afterId:ID,$reason:String!){` +
	`demoteAssertedPresence(source:$source,deviceTokens:$deviceTokens,limit:$limit,afterId:$afterId,reason:$reason)` +
	`{scanned demoted skipped lastId}}`

// dryRunQuery is the PREVIEW read, and it is deliberately deviceStates(criteria:)
// rather than assertedDeviceStates.
//
// 🔴 A DRY RUN IS AN INDEPENDENT SECOND OPINION, NOT A REHEARSAL OF THE MECHANISM.
// assertedDeviceStates takes the same `source` argument and applies the same
// server-side predicate (presence_source = ASSERTED AND source = ?) that the mutation
// itself selects rows with — it is, to within an argument, the mutation's own reader.
// Previewing through it asks the write path's own code the write path's own question,
// so it can only ever agree with the run it is supposed to be checking: the instrument
// reporting on itself.
//
// deviceStates(criteria:) is the generic search behind the console's device-state
// list. It applies no source filter and no presence filter, so the narrowing happens
// HERE, in the client, against the two fields the operator can see rendered — which
// makes the answer reproducible without dcctl. An operator who does not trust this
// count can open the console, or run this same query with any GraphQL client, and
// arrive at the blast radius independently. That is worth an unfiltered read: a
// preview whose only corroboration is the thing it is previewing is not a check.
//
// Two consequences, stated rather than hidden. It reads the tenant's whole projection
// a page at a time, so it costs more than the mutation's own selection does; and its
// paging is by offset over a newest-first order, so a row created mid-preview can
// shift the window. Both are acceptable in a preview and neither is acceptable in the
// walk, which is why the walk uses the mutation's keyset cursor instead.
const dryRunQuery = `query($pageNumber:Int!,$pageSize:Int!){` +
	`deviceStates(criteria:{pageNumber:$pageNumber,pageSize:$pageSize})` +
	`{results{deviceToken source presenceSource}}}`

// Options is one invocation of the demotion walk.
type Options struct {
	// Endpoint is the device-state GraphQL URL.
	Endpoint string
	// Source is the event source whose custody is being released, exactly as
	// DeviceState.source reports it. Required, and never inferred.
	Source string
	// Devices narrows the run WITHIN the source. Empty means the whole source.
	Devices []string
	// Reason is stamped on every emitted event and logged with the call.
	Reason string
	// PageSize is the mutation's per-page limit and the preview's page size.
	PageSize int
	// DryRun previews the affected rows and writes nothing.
	DryRun bool
	// Out receives the human-readable report. Required.
	Out io.Writer
	// Approve is asked once, before the first page is written, and a false answer
	// abandons the run with ErrDeclined. It is REQUIRED for a non-dry run.
	//
	// 🔴 THE APPROVAL LIVES WITH THE WRITE, NOT WITH THE CALLER THAT PROMPTS. A gate
	// held one level up is a gate any second caller silently does not have, and the
	// resulting hole looks like nothing at all: a missing call site compiles, tests,
	// and demotes a fleet without asking. Run refuses a non-dry run whose Approve is
	// nil rather than reading an absent approver as approval — so the failure mode of
	// forgetting it is a loud refusal on the first run, not a quiet fleet-wide write.
	Approve func() (bool, error)
}

// ErrDeclined is returned when Approve answers no. It is a distinct error rather than
// a silent success because a caller has to be able to tell "you said no" from "it
// ran": the two print differently, and only one of them changed anything.
var ErrDeclined = errors.New("the demotion was not confirmed")

// Summary is what a walk did, across every page.
type Summary struct {
	// Pages is how many server round-trips the walk made.
	Pages int
	// Scanned is how many ASSERTED rows the walk examined.
	Scanned int
	// Demoted is how many demotion events were published (always 0 for a dry run).
	Demoted int
	// Skipped is how many rows the server could not release.
	Skipped int
	// Matched is how many rows a DRY RUN found IN SCOPE. It is separate from Scanned
	// because a preview and a run count different things: a preview reads the whole
	// tenant projection and narrows client-side, so its Scanned is every row it read
	// and Matched is the blast radius. Folding the two would let a preview report
	// itself in the vocabulary of a run that never happened.
	Matched int
	// DryRun records which of the two this summary describes, so a caller printing it
	// cannot label a preview as a completed repair.
	DryRun bool
}

// page is one decoded demoteAssertedPresence result.
type page struct {
	Scanned int32   `json:"scanned"`
	Demoted int32   `json:"demoted"`
	Skipped int32   `json:"skipped"`
	LastId  *string `json:"lastId"`
}

// Run executes a demotion (or its preview) to completion.
func Run(ctx context.Context, c Client, opts Options) (Summary, error) {
	if err := opts.validate(); err != nil {
		return Summary{DryRun: opts.DryRun}, err
	}
	if opts.DryRun {
		return dryRun(ctx, c, opts)
	}
	// After validate, deliberately. Asking an operator to approve a run that a missing
	// --reason was always going to refuse trains them to approve without reading,
	// which is the one habit this gate depends on not having.
	if opts.Approve == nil {
		return Summary{}, errors.New("presence: a demotion needs an approval step and was given none")
	}
	ok, err := opts.Approve()
	if err != nil {
		return Summary{}, err
	}
	if !ok {
		return Summary{}, ErrDeclined
	}
	return walk(ctx, c, opts)
}

// validate refuses locally what the server would refuse anyway, so an operator who
// mistyped an argument finds out before a token is minted and before any page is
// written. It does not restate the server's rules more strictly: the page bound is
// the server's, and the message says so in the server's own terms.
func (o Options) validate() error {
	if o.Out == nil {
		return errors.New("presence: no output writer")
	}
	if strings.TrimSpace(o.Endpoint) == "" {
		return errors.New("no device-state endpoint was resolved")
	}
	if strings.TrimSpace(o.Source) == "" {
		return errors.New("--source is required: it names the event source whose devices are being released, and it is never guessed")
	}
	if strings.TrimSpace(o.Reason) == "" {
		return errors.New("--reason is required: it is stamped on every event this writes and is the only record of the change")
	}
	if o.PageSize < 1 || o.PageSize > MaxPageSize {
		return fmt.Errorf("--page must be between 1 and %d", MaxPageSize)
	}
	for _, d := range o.Devices {
		if strings.TrimSpace(d) == "" {
			return errors.New("--device was given an empty token")
		}
	}
	return nil
}

// walk drives demoteAssertedPresence from the first page to the last.
//
// 🔴 THE TERMINATION RULE IS `scanned`, NEVER `demoted`. A page whose every row was
// skippable — no presence time, or a presence time the server's clock has not passed
// yet — demotes nothing and is emphatically NOT the end of the source. A loop keyed on
// `demoted` stops at the first such page and leaves the rest of the fleet frozen,
// reporting a clean finish. `scanned` counts rows EXAMINED, so a short page is the one
// and only signal that the source ran out.
func walk(ctx context.Context, c Client, opts Options) (Summary, error) {
	sum := Summary{}
	afterId := ""
	for {
		var out struct {
			Result page `json:"demoteAssertedPresence"`
		}
		if err := c.Query(ctx, opts.Endpoint, demoteMutation, opts.buildVariables(afterId), &out); err != nil {
			// The page that failed wrote nothing (the server abandons a page rather
			// than partially reporting it), and the cursor is still where it was — so
			// re-running the identical command resumes from the last completed page.
			return sum, fmt.Errorf("demoting %q after page %d (%d demoted so far): %w",
				opts.Source, sum.Pages, sum.Demoted, err)
		}
		p := out.Result
		sum.Pages++
		sum.Scanned += int(p.Scanned)
		sum.Demoted += int(p.Demoted)
		sum.Skipped += int(p.Skipped)

		if sum.Pages == 1 && p.Scanned == 0 {
			warnNothingMatched(opts.Out, opts.Source, len(opts.Devices) > 0)
		}

		// TERMINATION. Keep walking while the page came back full.
		if int(p.Scanned) < opts.PageSize {
			return sum, nil
		}

		next, err := advance(afterId, p.LastId)
		if err != nil {
			return sum, fmt.Errorf("demoting %q after page %d: %w", opts.Source, sum.Pages, err)
		}
		afterId = next
	}
}

// buildVariables assembles one page's variables.
//
// afterId is OMITTED on the first page rather than sent as an empty id, and
// deviceTokens is omitted whenever the caller narrowed nothing — see demoteMutation
// for why the second of those is not a stylistic choice.
func (o Options) buildVariables(afterId string) map[string]any {
	vars := map[string]any{
		"source": o.Source,
		"limit":  o.PageSize,
		"reason": o.Reason,
	}
	if len(o.Devices) > 0 {
		vars["deviceTokens"] = o.Devices
	}
	if afterId != "" {
		vars["afterId"] = afterId
	}
	return vars
}

// advance returns the cursor for the next page, refusing one that does not move.
//
// A full page with no cursor, or with a cursor that repeats or goes backwards, is a
// walk that never ends: the same page is demanded forever, and because the server
// abandons nothing it would keep answering. Ids ascend, so "strictly greater" is the
// invariant, and breaking it is a failure to report rather than a loop to spin in.
func advance(current string, lastId *string) (string, error) {
	if lastId == nil {
		return "", errors.New("the server returned a full page with no cursor, so the walk cannot continue")
	}
	next, err := strconv.ParseUint(*lastId, 10, 64)
	if err != nil {
		return "", fmt.Errorf("the server returned a cursor that is not an id (%q)", *lastId)
	}
	if current != "" {
		prev, perr := strconv.ParseUint(current, 10, 64)
		if perr != nil {
			return "", fmt.Errorf("the walk is holding a cursor that is not an id (%q)", current)
		}
		if next <= prev {
			return "", fmt.Errorf("the cursor did not advance (%d after %d), so the walk would repeat this page forever", next, prev)
		}
	}
	return *lastId, nil
}

// dryRun previews the rows a real run would scan, and writes nothing.
//
// It reports the rows IN SCOPE — asserted, filed under this source, and inside any
// --device narrowing. It deliberately does not re-derive the server's per-row skip
// verdict (a row whose presence time is absent, or not yet in the server's past).
// That judgement is about whether the ordering guard would accept an emitted event,
// and a second implementation of it here could only disagree with the one that
// decides. The question a fleet-wide write needs answered first is "which devices are
// in range", and this answers exactly that.
func dryRun(ctx context.Context, c Client, opts Options) (Summary, error) {
	sum := Summary{DryRun: true}
	narrow := map[string]bool{}
	for _, d := range opts.Devices {
		narrow[d] = true
	}

	matched := make([]string, 0)
	for pageNumber := 1; ; pageNumber++ {
		var out struct {
			DeviceStates struct {
				Results []struct {
					DeviceToken    string  `json:"deviceToken"`
					Source         *string `json:"source"`
					PresenceSource string  `json:"presenceSource"`
				} `json:"results"`
			} `json:"deviceStates"`
		}
		vars := map[string]any{"pageNumber": pageNumber, "pageSize": opts.PageSize}
		if err := c.Query(ctx, opts.Endpoint, dryRunQuery, vars, &out); err != nil {
			return sum, fmt.Errorf("previewing %q at page %d: %w", opts.Source, pageNumber, err)
		}
		sum.Pages++
		sum.Scanned += len(out.DeviceStates.Results)
		for _, row := range out.DeviceStates.Results {
			if row.PresenceSource != assertedPresenceSource {
				continue
			}
			if row.Source == nil || *row.Source != opts.Source {
				continue
			}
			if len(narrow) > 0 && !narrow[row.DeviceToken] {
				continue
			}
			matched = append(matched, row.DeviceToken)
		}
		if len(out.DeviceStates.Results) < opts.PageSize {
			break
		}
	}

	sum.Matched = len(matched)
	if sum.Matched == 0 {
		warnNothingMatched(opts.Out, opts.Source, len(opts.Devices) > 0)
		return sum, nil
	}
	report(opts.Out, matched, opts.Source)
	return sum, nil
}

// listedDevices bounds how many device tokens a preview prints before summarizing the
// rest. A preview of a whole fleet is still a preview: the count is the decision, the
// names are the sanity check on it.
const listedDevices = 20

// report prints the preview's finding.
func report(w io.Writer, matched []string, source string) {
	fmt.Fprintf(w, "Dry run: %d asserted device(s) are filed under source %q and would be demoted.\n",
		len(matched), source)
	shown := matched
	if len(shown) > listedDevices {
		shown = shown[:listedDevices]
	}
	for _, token := range shown {
		fmt.Fprintf(w, "    %s\n", token)
	}
	if len(matched) > len(shown) {
		fmt.Fprintf(w, "    ... and %d more\n", len(matched)-len(shown))
	}
	fmt.Fprintln(w, "\nNothing was written. Re-run without --dry-run to demote them.")
}

// warnNothingMatched is the typo detector, and it exists because a source nobody uses
// is NOT an error.
//
// The server matches rows by an exact string. A misspelled --source therefore matches
// nothing and returns a perfectly successful empty first page — which is byte-for-byte
// what a source with nothing left to demote returns. Without this line the two are
// indistinguishable, and the likelier of them (a typo, on an argument nobody can
// autocomplete) is the one that reads as success.
func warnNothingMatched(w io.Writer, source string, narrowed bool) {
	fmt.Fprintf(w, "⚠️  Nothing matched source %q.\n", source)
	fmt.Fprintln(w, "    A source that nobody uses is not an error — it simply matches no rows — so this")
	fmt.Fprintln(w, "    looks identical to a source that has already been fully demoted.")
	fmt.Fprintln(w, "    Check the spelling: it must match a device's reported source EXACTLY (an event")
	fmt.Fprintln(w, "    source's own configured id, \"sparkplug:<hostId>\", or \"lwm2m\").")
	if narrowed {
		fmt.Fprintln(w, "    --device narrows WITHIN the source, so a token belonging to another source")
		fmt.Fprintln(w, "    matches nothing here either.")
	}
}

// Print renders a finished summary.
func Print(w io.Writer, source string, sum Summary) {
	if sum.DryRun {
		fmt.Fprintf(w, "\nPreviewed %d device state(s) over %d page(s); %d in scope for source %q.\n",
			sum.Scanned, sum.Pages, sum.Matched, source)
		return
	}
	fmt.Fprintf(w, "\nDemoted %d device(s) of %d scanned for source %q, over %d page(s); %d skipped.\n",
		sum.Demoted, sum.Scanned, source, sum.Pages, sum.Skipped)
	if sum.Skipped > 0 {
		fmt.Fprintln(w, "A skipped row is one the platform could not release: it holds no presence time,")
		fmt.Fprintln(w, "or its presence time is not yet in the past. The service log names each one.")
	}
}
