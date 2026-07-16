// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package nldraft turns a natural-language description into a compiling DETECT rule
// (ADR-056 slice 1) — the "third front door" to the one CEL rule compiler. It orchestrates
// a bounded infer→compile→repair loop: it prompts the active inference provider (via the
// injected Inferer, an ai-inference service-token client) for a rules.Rule candidate, runs
// that candidate through the SAME rules.Compile firewall the form and canvas doors use, and
// on a compile rejection feeds the structured error back for a bounded number of repair
// attempts. The AI never sits in the replay-correct path: it only proposes a candidate STRING;
// the deterministic compiler disposes, and the human reviews the draft before saving through
// the normal authoring door. Persisting is out of scope here — this produces a draft only.
package nldraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/rs/zerolog/log"
)

// defaultMaxAttempts bounds the infer→compile→repair loop. Each attempt is one paid provider
// call, so the loop is short: a candidate that has not compiled after a couple of repair turns
// is reported back to the author with diagnostics rather than burning further budget.
const defaultMaxAttempts = 3

// unavailableReason is the FIXED, safe message surfaced to the author when the inference path
// cannot run. It deliberately does NOT echo the underlying error: svcclient wraps upstream
// failures with the peer URL and raw response bodies, and ai-inference already coarsens its
// author-facing errors (ADR-056 0c) so an unprivileged caller never learns internal topology —
// re-expanding err.Error() here would undo that. The real error is logged server-side instead.
const unavailableReason = "the inference provider is unavailable, or this tenant has not enabled external AI routing"

// rateLimitedReason is the safe message for a tenant over its inference rate ceiling
// (ADR-056 §6 / ADR-023). Distinct from unavailableReason because the two call for
// opposite actions: "unavailable" sends the author to an operator, while this resolves
// by waiting a moment. Telling an author they are over their own tenant's ceiling
// leaks nothing — it describes their own behaviour against a limit their own operator
// set, and reveals no topology.
const rateLimitedReason = "this tenant has reached its AI drafting rate limit; wait a moment and try again"

// repairTruncatedMessage is surfaced as an unanchored diagnostic when the repair loop
// is cut short by the rate limit AFTER a candidate was already produced. Without it a
// truncated loop is indistinguishable from a model that simply could not write a
// compiling rule, and the author would rewrite a description that was never the
// problem. Like rateLimitedReason it is a fixed constant carrying no upstream detail.
const repairTruncatedMessage = "the AI drafting rate limit was reached before this draft could be repaired; wait a moment and try again"

// ErrRateLimited marks an inference call rejected because the tenant is over its rate
// ceiling. The Inferer implementation classifies the transport error into it (see the
// processor's inference client), so the drafter can report the transient, retryable
// outcome without depending on the transport.
var ErrRateLimited = errors.New("inference rate limited")

// MetricHint is one entry of the target profile's metric vocabulary, supplied by the caller
// (the console already loads it) so the prompt can reference real metric keys. All fields but
// Key are advisory prompt context.
type MetricHint struct {
	Key         string
	DataType    string
	Unit        string
	Description string
}

// Request is a single NL-drafting request.
type Request struct {
	// Text is the author's plain-language description.
	Text string
	// ProfileToken is the target device profile (carried for context/provenance; the metric
	// vocabulary is supplied separately in Metrics).
	ProfileToken string
	// Metrics is the optional profile metric vocabulary for prompt context.
	Metrics []MetricHint
}

// Diagnostic is one compiler rejection reason surfaced to the author (field anchor + message).
type Diagnostic struct {
	// Field is the rules.ValidationError field anchor (e.g. "when", "threshold"), empty for a
	// decode-level or unanchored error.
	Field string
	// Message is the console-surfaceable reason.
	Message string
}

// Result is the outcome of a draft request.
type Result struct {
	// OK is true when a candidate compiled through the DETECT firewall.
	OK bool
	// Definition is the compiled, canonical rules.Rule JSON (id blanked — the token is assigned
	// at save). Set only when OK.
	Definition string
	// EstimatedCost is the compiled leaf predicate's worst-case cost, clamped to int32. Set only
	// when OK.
	EstimatedCost int32
	// Model / Provider carry the provenance of the answering provider (set whenever a candidate
	// was produced, OK or not).
	Model    string
	Provider string
	// Attempts is the number of inference rounds performed.
	Attempts int32
	// RawCandidate is the model's last (rejected) raw output — surfaced on !OK so the author can
	// see what the AI tried (Derek's steer: diagnostics + raw candidate).
	RawCandidate string
	// Diagnostics are the compiler rejection reasons from the final failed attempt (set on !OK).
	Diagnostics []Diagnostic
	// Unavailable is true when the inference path itself could not run — the ai-inference endpoint
	// is not configured, no provider is active, or the tenant has not opted in to external routing
	// (all fail-closed, ADR-056). UnavailableReason carries the (already-coarsened) message.
	Unavailable       bool
	UnavailableReason string
}

// InferOutput is one raw provider answer.
type InferOutput struct {
	Candidate string
	Model     string
	Provider  string
}

// Inferer is the seam to the ai-inference service: it carries a prompt to the active provider
// under the given tenant and returns the raw candidate. It is dependency-inverted so the drafter
// never depends on the transport (the concrete client mints an ai:infer service token and calls
// ai-inference's inferRuleCandidate; see processor). An error means the inference could not be
// produced — not configured, no active provider, consent denied, or a transport failure — all of
// which the drafter reports as Unavailable.
type Inferer interface {
	Infer(ctx context.Context, tenant, prompt, system string) (InferOutput, error)
}

// Drafter runs the bounded infer→compile→repair loop.
type Drafter struct {
	inferer     Inferer
	limits      rules.Limits
	maxAttempts int
}

// NewDrafter builds a drafter over an Inferer and the compile limits (the platform-default
// ceilings, shared with every other compile door so a drafted rule compiles identically
// everywhere). maxAttempts <= 0 uses the default.
func NewDrafter(inferer Inferer, limits rules.Limits, maxAttempts int) *Drafter {
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	return &Drafter{inferer: inferer, limits: limits, maxAttempts: maxAttempts}
}

// Draft produces a compiling rules.Rule draft from the request's NL text, or a diagnostic
// result the author can act on. It never returns an error for an EXPECTED outcome (a
// non-compiling model, or an unavailable inference path both come back in the Result); it
// returns an error only on an internal invariant break.
func (d *Drafter) Draft(ctx context.Context, tenant string, req Request) (Result, error) {
	system := buildSystemPrompt(req.Metrics)
	prompt := buildInitialPrompt(req.Text)

	var last InferOutput
	var lastDiags []Diagnostic
	rounds := 0 // candidates actually produced (a call that errored produced none).
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		out, err := d.inferer.Infer(ctx, tenant, prompt, system)
		if err != nil {
			// The inference call failed (unconfigured / no active provider / consent denied /
			// transport). Fail closed and NEVER surface err.Error() — log the detail server-side,
			// return the fixed safe reason (see unavailableReason).
			log.Warn().Err(err).Str("tenant", tenant).Int("attempt", attempt).Msg("nldraft: inference call failed")
			if rounds > 0 {
				// A prior attempt already produced a rejected candidate + diagnostics; return them
				// (actionable) rather than discarding that spent work as a bare "unavailable".
				if errors.Is(err, ErrRateLimited) {
					// Say so, rather than let a truncated loop read as "the AI could not write a
					// compiling rule". The repair turn that would likely have fixed it never ran,
					// and the author's next move is to wait — not to rewrite their description.
					lastDiags = append(lastDiags, Diagnostic{Message: repairTruncatedMessage})
				}
				break
			}
			// Rate-limited is reported as its own reason: it is transient and the author
			// fixes it by waiting, whereas the generic reason points at configuration and
			// would send them to an operator for nothing.
			reason := unavailableReason
			if errors.Is(err, ErrRateLimited) {
				reason = rateLimitedReason
			}
			return Result{
				Unavailable:       true,
				UnavailableReason: reason,
				Attempts:          int32(rounds),
			}, nil
		}
		rounds++
		last = out

		def, cost, cerr := compileCandidate(out.Candidate, d.limits)
		if cerr == nil {
			return Result{
				OK:            true,
				Definition:    def,
				EstimatedCost: cost,
				Model:         out.Model,
				Provider:      out.Provider,
				Attempts:      int32(rounds),
			}, nil
		}
		lastDiags = diagnosticsFrom(cerr)
		prompt = buildRepairPrompt(req.Text, out.Candidate, cerr)
	}

	// Exhausted the repair budget (or bailed on a mid-loop error) without a compiling candidate;
	// return the last candidate + diagnostics so the author sees what the AI tried and why.
	return Result{
		OK:           false,
		Model:        last.Model,
		Provider:     last.Provider,
		Attempts:     int32(rounds),
		RawCandidate: last.Candidate,
		Diagnostics:  lastDiags,
	}, nil
}

// compileCandidate decodes a raw model candidate and runs it through the DETECT firewall,
// returning the canonical (id-blanked) definition and the leaf cost on success. The candidate's
// id is forced to a placeholder before Compile (Compile requires a non-empty id + name; the real
// token is assigned at save) and blanked from the returned definition.
func compileCandidate(candidate string, limits rules.Limits) (definition string, cost int32, err error) {
	rule, err := rules.Decode([]byte(extractJSON(candidate)))
	if err != nil {
		return "", 0, err
	}
	rule.ID = "draft" // Compile requires a non-empty id; the authoritative token is assigned at save.
	compiled, err := rules.Compile(rule, limits)
	if err != nil {
		return "", 0, err
	}
	rule.ID = "" // omit the placeholder from the draft the author saves.
	out, err := json.Marshal(rule)
	if err != nil {
		return "", 0, fmt.Errorf("nldraft: marshal compiled rule: %w", err)
	}
	return string(out), clampCost(compiled.Predicate.CostMax()), nil
}

// diagnosticsFrom maps a compile/decode error to author-facing diagnostics. A structured
// rules.ValidationError yields a field-anchored entry; any other error (a decode/unknown-field
// rejection, which is not field-anchored) yields a single unanchored message.
func diagnosticsFrom(err error) []Diagnostic {
	var ve *rules.ValidationError
	if errors.As(err, &ve) {
		return []Diagnostic{{Field: ve.Field, Message: ve.Msg}}
	}
	return []Diagnostic{{Message: err.Error()}}
}

// extractJSON strips a Markdown code fence the model may wrap its output in, even though the
// prompt forbids one — rules.Decode rejects trailing bytes and unknown fields, so a stray fence
// would otherwise fail an otherwise-valid rule. It removes a leading ``` (optionally ```json)
// and a trailing ```; a bare JSON object passes through unchanged.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimSpace(rest)
		rest = strings.TrimSuffix(rest, "```")
		s = strings.TrimSpace(rest)
	}
	return s
}

// clampCost narrows a predicate cost (uint64) to the GraphQL Int range, saturating rather than
// wrapping a pathological value.
func clampCost(v uint64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}
