// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/userclient"
)

const reversePreviewQuery = `query($request: BulkChangeReversalRequest!) { preview: previewBulkChangeReversal(request: $request) { reversibleCount changedSinceCount redactedCount alreadyReversedCount notPostedCount requestDigest planDigest contractDigest } }`
const reverseQuery = `mutation($request: BulkChangeReversalRequest!) { change: reverseBulkChange(request: $request) { ` + changeFields + ` } }`
const resolveQuery = `mutation($request: BulkChangeAmbiguityResolution!) { change: resolveAmbiguousBulkChangeLines(request: $request) { ` + changeFields + ` } }`

// The server's refusals for a call that needs a person's recent second factor.
const (
	codeStepUpRequired       = "STEP_UP_REQUIRED"
	codeSecondFactorRequired = "SECOND_FACTOR_REQUIRED"
)

// MaxNoteRunes is the server's bound on a resolution note.
const MaxNoteRunes = 1000

// ReversalRequest is BulkChangeReversalRequest. Omitted ordinals reverse every applied line.
type ReversalRequest struct {
	Original         string         `json:"original"`
	Token            string         `json:"token"`
	Name             string         `json:"name"`
	Ordinals         []int          `json:"ordinals,omitempty"`
	OverrideChanged  bool           `json:"overrideChanged,omitempty"`
	OverrideOrdinals []int          `json:"overrideOrdinals,omitempty"`
	Safety           map[string]any `json:"safety,omitempty"`
}

// WantsOverride reports whether creating the reversal needs a fresh second factor.
func (r ReversalRequest) WantsOverride() bool {
	return r.OverrideChanged || len(r.OverrideOrdinals) > 0
}

// Validate refuses locally what the server would refuse, before anyone signs in.
func (r ReversalRequest) Validate() error {
	if strings.TrimSpace(r.Original) == "" {
		return errors.New("the bulk change token to reverse is required")
	}
	if r.Token == "" || len(r.Token) > 128 {
		return errors.New("--token is required: the reversal's own token, 1 to 128 characters")
	}
	if r.Name == "" || len(r.Name) > 128 {
		return errors.New("--name must be 1 to 128 characters")
	}
	if r.Ordinals != nil && len(r.Ordinals) == 0 {
		return errors.New("--ordinals must name at least one line; omit it to reverse every applied line")
	}
	if r.OverrideChanged && len(r.OverrideOrdinals) > 0 {
		return errors.New("use --override-changed or --override-ordinals, not both")
	}
	if err := checkOrdinals("--ordinals", r.Ordinals); err != nil {
		return err
	}
	if err := checkOrdinals("--override-ordinals", r.OverrideOrdinals); err != nil {
		return err
	}
	if len(r.Ordinals) > 0 {
		selected := map[int]bool{}
		for _, o := range r.Ordinals {
			selected[o] = true
		}
		for _, o := range r.OverrideOrdinals {
			if !selected[o] {
				return fmt.Errorf("override ordinal %d is not in --ordinals", o)
			}
		}
	}
	return nil
}

func checkOrdinals(flag string, ordinals []int) error {
	if len(ordinals) > ledger.MaxLinesPerOperation {
		return fmt.Errorf("%s names at most %d lines", flag, ledger.MaxLinesPerOperation)
	}
	for _, o := range ordinals {
		if o < 1 {
			return fmt.Errorf("%s start at 1", flag)
		}
	}
	return nil
}

// ReversalPreview is BulkChangeReversalPreview: what a reversal would do, nothing written.
type ReversalPreview struct {
	Original             string `json:"original"`
	Token                string `json:"token"`
	ReversibleCount      int    `json:"reversibleCount"`
	ChangedSinceCount    int    `json:"changedSinceCount"`
	RedactedCount        int    `json:"redactedCount"`
	AlreadyReversedCount int    `json:"alreadyReversedCount"`
	NotPostedCount       int    `json:"notPostedCount"`
	RequestDigest        string `json:"requestDigest"`
	PlanDigest           string `json:"planDigest"`
	ContractDigest       string `json:"contractDigest"`
}

// Resolution is BulkChangeAmbiguityResolution. Exactly one of Ordinals or AllAmbiguous.
type Resolution struct {
	Token        string `json:"token"`
	Ordinals     []int  `json:"ordinals,omitempty"`
	AllAmbiguous bool   `json:"allAmbiguous,omitempty"`
	Verdict      string `json:"verdict"`
	Note         string `json:"note"`
}

// Validate refuses locally what the server would refuse, before anyone signs in.
func (r *Resolution) Validate() error {
	if strings.TrimSpace(r.Token) == "" {
		return errors.New("bulk change token is required")
	}
	if r.AllAmbiguous == (len(r.Ordinals) > 0) {
		return errors.New("use exactly one of --ordinals or --all-ambiguous")
	}
	if err := checkOrdinals("--ordinals", r.Ordinals); err != nil {
		return err
	}
	switch v := strings.ToUpper(strings.TrimSpace(r.Verdict)); v {
	case "CONFIRMED", "FAILED":
		r.Verdict = v
	default:
		return errors.New("--verdict must be confirmed or failed")
	}
	if strings.TrimSpace(r.Note) == "" || !utf8.ValidString(r.Note) || utf8.RuneCountInString(r.Note) > MaxNoteRunes {
		return fmt.Errorf("--note is required: 1 to %d characters saying why", MaxNoteRunes)
	}
	return nil
}

// StepUpClient is a Client that can sign its person in again, answering a fresh second factor, so
// a refusal for a stale one can be retried once.
type StepUpClient interface {
	Client
	StepUp(context.Context) error
}

// PreviewReversal asks the server what reversing would do. It writes nothing and needs no step-up.
func (a API) PreviewReversal(ctx context.Context, request ReversalRequest) (*ReversalPreview, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var out struct {
		Preview *ReversalPreview `json:"preview"`
	}
	if err := a.query(ctx, reversePreviewQuery, map[string]any{"request": request}, &out); err != nil {
		return nil, err
	}
	if out.Preview == nil || out.Preview.ContractDigest == "" {
		return nil, errors.New("bulk change reversal preview is missing its contract digest")
	}
	out.Preview.Original, out.Preview.Token = request.Original, request.Token
	return out.Preview, nil
}

// Reverse creates the reversal in READY. An override needs a second factor verified within five
// minutes; see stepUp.
func (a API) Reverse(ctx context.Context, request ReversalRequest) (*Change, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return a.stepUp(ctx, reverseQuery, map[string]any{"request": request}, "overriding a changed value in a reversal")
}

// Resolve records a person's verdict on ambiguous lines. It always needs a recent second factor.
func (a API) Resolve(ctx context.Context, request Resolution) (*Change, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return a.stepUp(ctx, resolveQuery, map[string]any{"request": request}, "resolving ambiguous lines")
}

// stepUp sends a mutation that needs a person's second factor verified within the last five
// minutes. The server refuses SECOND_FACTOR_REQUIRED when the sign-in had none (no retry can fix
// that) and STEP_UP_REQUIRED when it is too old or was a recovery code. Only the second is retried,
// once, after signing in again with a fresh one-time code. Both refusals come before the server
// writes anything, so the retry cannot apply the call twice; any other answer is never retried.
func (a API) stepUp(ctx context.Context, document string, vars map[string]any, doing string) (*Change, error) {
	change, err := a.change(ctx, document, vars)
	if err == nil {
		return change, nil
	}
	if userclient.AllHaveCode(err, codeSecondFactorRequired) {
		return nil, fmt.Errorf("%s needs an account that signs in with a second factor; set up an authenticator app for this identity: %w", doing, err)
	}
	if !userclient.AllHaveCode(err, codeStepUpRequired) {
		return nil, err
	}
	stepper, ok := a.Client.(StepUpClient)
	if !ok {
		return nil, fmt.Errorf("%s needs a second factor verified within the last five minutes: %w", doing, err)
	}
	if serr := stepper.StepUp(ctx); serr != nil {
		return nil, fmt.Errorf("%s needs a fresh authenticator code (a recovery code does not count): %w: %w", doing, serr, err)
	}
	change, err = a.change(ctx, document, vars)
	if err != nil && userclient.AllHaveCode(err, codeStepUpRequired) {
		return nil, fmt.Errorf("%s needs an authenticator code verified within the last five minutes; a recovery code does not count: %w", doing, err)
	}
	return change, err
}

// PrintReversalPreview writes a preview, which wrote nothing on the server.
func PrintReversalPreview(w io.Writer, p *ReversalPreview, asJSON bool) error {
	if asJSON {
		return PrintJSON(w, p)
	}
	_, err := fmt.Fprintf(w, "original: %s\ntoken: %s\nreversible: %d\nchangedSince: %d\nredacted: %d\nalreadyReversed: %d\nnotPosted: %d\nrequestDigest: %s\nplanDigest: %s\ncontractDigest: %s\n",
		p.Original, p.Token, p.ReversibleCount, p.ChangedSinceCount, p.RedactedCount, p.AlreadyReversedCount, p.NotPostedCount, p.RequestDigest, p.PlanDigest, p.ContractDigest)
	return err
}
