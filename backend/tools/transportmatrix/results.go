// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package transportmatrix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// ResultsSchema is the only results format this version reads.
const ResultsSchema = 1

// Results is hack/conformance/results.json, written by hack/conformance-rig.sh.
type Results struct {
	Schema int `json:"schema"`
	// Commit is the full SHA of the tree the rig ran against; Dirty says it had uncommitted changes, in
	// which case no commit names what was tested and the file is refused.
	Commit      string `json:"commit"`
	Dirty       bool   `json:"dirty"`
	GeneratedAt string `json:"generatedAt"`
	// SpecDigest is Spec.Digest() at the time of the run.
	SpecDigest string          `json:"specDigest"`
	Checks     []CheckResult   `json:"checks"`
	Controls   []ControlResult `json:"controls"`
}

// CheckResult is one check's verdict. Evidence is a one-line account of what was read (the store, the
// counters), kept for the human who has to believe it.
type CheckResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // pass | fail
	Evidence string `json:"evidence"`
}

// ControlResult is one negative control's outcome: "red" if the planted defect or fabricated record
// turned the named check red, which is what it is for. "green" means the check could not tell.
type ControlResult struct {
	ID      string `json:"id"`
	Check   string `json:"check"`
	Outcome string `json:"outcome"` // red | green
	Detail  string `json:"detail"`
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// LoadResults reads a results file strictly.
func LoadResults(path string) (*Results, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the results: %w", err)
	}
	return ParseResults(raw)
}

// ParseResults decodes results strictly: an unknown key is an error, so a field the renderer does not
// know about cannot be silently ignored.
func ParseResults(raw []byte) (*Results, error) {
	var r Results
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parsing the results: %w", err)
	}
	return &r, nil
}

// Options says how strict to be about when and where the results were made.
type Options struct {
	// Now is the clock. Zero means time.Now().
	Now time.Time
	// MaxAge refuses results older than this. Zero disables the age check (CI's drift check does this
	// on purpose: a docs change must not fail because the last rig run is a month old. The release
	// check does not).
	MaxAge time.Duration
	// IsAncestor, when set, is asked whether the results' commit is an ancestor of the tree being
	// checked. A nil value skips the question (a shallow clone cannot answer it).
	IsAncestor func(commit string) (bool, error)
}

// maxClockSkew is how far into the future a timestamp may be before it is not a clock difference but a
// wrong file.
const maxClockSkew = 10 * time.Minute

// Verify refuses results that cannot back the spec: wrong schema, a dirty or unnamed tree, a stale or
// future timestamp, a different set of checks or controls than the spec declares, or a negative control
// that did not go red. It returns the set of checks that passed; whether a given cell may rely on them
// is the renderer's question.
func (r *Results) Verify(s *Spec, o Options) (map[string]bool, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if r.Schema != ResultsSchema {
		return nil, fmt.Errorf("results: schema %d, this renderer reads %d", r.Schema, ResultsSchema)
	}
	if !shaRe.MatchString(r.Commit) {
		return nil, fmt.Errorf("results: commit %q is not a full SHA", r.Commit)
	}
	if r.Dirty {
		return nil, fmt.Errorf("results: made on a tree with uncommitted changes, so commit %s does not name what was tested; re-run the rig on a clean checkout", r.Commit[:12])
	}
	at, err := time.Parse(time.RFC3339, r.GeneratedAt)
	if err != nil {
		return nil, fmt.Errorf("results: generatedAt %q is not an RFC 3339 time", r.GeneratedAt)
	}
	if at.After(now.Add(maxClockSkew)) {
		return nil, fmt.Errorf("results: generatedAt %s is in the future", r.GeneratedAt)
	}
	if o.MaxAge > 0 && now.Sub(at) > o.MaxAge {
		return nil, fmt.Errorf("results are stale: made %s (%s ago), the limit is %s; re-run hack/conformance-rig.sh",
			r.GeneratedAt, now.Sub(at).Round(time.Hour), o.MaxAge)
	}
	if want := s.Digest(); r.SpecDigest != want {
		return nil, fmt.Errorf("results are stale: they were made for a different set of checks and controls (digest %.12s, the spec's is %.12s); re-run hack/conformance-rig.sh", r.SpecDigest, want)
	}
	if o.IsAncestor != nil {
		ok, err := o.IsAncestor(r.Commit)
		if err != nil {
			return nil, fmt.Errorf("results: cannot tell whether commit %s is in this tree's history: %w", r.Commit[:12], err)
		}
		if !ok {
			return nil, fmt.Errorf("results: commit %s is not an ancestor of this tree; they describe a different line of history", r.Commit[:12])
		}
	}

	declared := map[string]bool{}
	for _, c := range s.Checks {
		declared[c.ID] = true
	}
	passed := map[string]bool{}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if !declared[c.ID] {
			return nil, fmt.Errorf("results: check %q is not in the spec", c.ID)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("results: check %q appears twice", c.ID)
		}
		seen[c.ID] = true
		switch c.Status {
		case "pass":
			passed[c.ID] = true
		case "fail":
		default:
			return nil, fmt.Errorf("results: check %q has status %q; want pass or fail", c.ID, c.Status)
		}
	}
	for _, c := range s.Checks {
		if !seen[c.ID] {
			return nil, fmt.Errorf("results: check %q was not run", c.ID)
		}
	}
	// A control that did not go red means its check cannot tell good from bad, so nothing that check
	// says counts. Every one is reported, not just the first.
	wantControls := map[string]ControlDef{}
	for _, c := range s.Controls {
		wantControls[c.ID] = c
	}
	gotControls := map[string]bool{}
	var blind []string
	for _, c := range r.Controls {
		def, ok := wantControls[c.ID]
		if !ok {
			return nil, fmt.Errorf("results: control %q is not in the spec", c.ID)
		}
		if gotControls[c.ID] {
			return nil, fmt.Errorf("results: control %q appears twice", c.ID)
		}
		gotControls[c.ID] = true
		if c.Check != def.Check {
			return nil, fmt.Errorf("results: control %q names check %q, the spec says %q", c.ID, c.Check, def.Check)
		}
		switch c.Outcome {
		case "red":
		case "green":
			blind = append(blind, c.ID+" (check "+c.Check+")")
		default:
			return nil, fmt.Errorf("results: control %q has outcome %q; want red or green", c.ID, c.Outcome)
		}
	}
	for _, c := range s.Controls {
		if !gotControls[c.ID] {
			return nil, fmt.Errorf("results: control %q was not run", c.ID)
		}
	}
	if len(blind) > 0 {
		return nil, fmt.Errorf("results: negative controls that did NOT turn their check red, so those checks cannot tell a defect from a pass: %s", strings.Join(blind, ", "))
	}
	return passed, nil
}
