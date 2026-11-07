// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package physpr is dcctl's EXPERIMENTAL client of the physical-pull-request operations of
// event-processing: a diff of two rule versions by what each would have done to recorded traffic,
// and a bisect that names the version that changed an outcome.
//
// A "ledger" is the history those verbs work on: an ordered list of published rule versions, each
// with the principal who published it. Today it is a JSON file the caller keeps; the verbs read
// it, send the versions to the server, and print the server's own report, so the wording a
// reviewer sees is the same wherever the operation is run from. Nothing here is persisted by the
// server: both operations are read-only replays.
package physpr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Client is the one method this package needs of a tenant session.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// Version is one published version of a rule.
type Version struct {
	Commit         string          `json:"commit"`
	Author         string          `json:"author"`
	AuthorKind     string          `json:"authorKind,omitempty"`
	Message        string          `json:"message,omitempty"`
	PublishedAt    string          `json:"publishedAt"`
	RuleDefinition json.RawMessage `json:"ruleDefinition"`
}

// Ledger is the ordered history of one profile's rule, oldest first.
type Ledger struct {
	Profile  string    `json:"profile"`
	Versions []Version `json:"versions"`
}

// LoadLedger reads and checks a ledger file. Unknown keys are rejected: a misspelt field in a
// file that decides what gets replayed must not be silently ignored.
func LoadLedger(path string) (*Ledger, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Ledger
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Profile == "" {
		return nil, fmt.Errorf("%s: profile is required", path)
	}
	if len(l.Versions) == 0 {
		return nil, fmt.Errorf("%s: versions is empty", path)
	}
	seen := map[string]bool{}
	for i, v := range l.Versions {
		switch {
		case v.Commit == "":
			return nil, fmt.Errorf("%s: versions[%d].commit is required", path, i)
		case seen[v.Commit]:
			return nil, fmt.Errorf("%s: commit %q appears twice", path, v.Commit)
		case v.Author == "":
			return nil, fmt.Errorf("%s: versions[%d] (%s) has no author: a version nobody published cannot be blamed", path, i, v.Commit)
		case len(v.RuleDefinition) == 0:
			return nil, fmt.Errorf("%s: versions[%d] (%s) has no ruleDefinition", path, i, v.Commit)
		}
		if _, err := time.Parse(time.RFC3339, v.PublishedAt); err != nil {
			return nil, fmt.Errorf("%s: versions[%d] (%s).publishedAt: %w", path, i, v.Commit, err)
		}
		seen[v.Commit] = true
	}
	return &l, nil
}

// Find returns the version with the given commit id.
func (l *Ledger) Find(commit string) (Version, error) {
	for _, v := range l.Versions {
		if v.Commit == commit {
			return v, nil
		}
	}
	ids := make([]string, len(l.Versions))
	for i, v := range l.Versions {
		ids[i] = v.Commit
	}
	return Version{}, fmt.Errorf("no commit %q in the ledger (have: %s)", commit, strings.Join(ids, ", "))
}

// Side is one side of a diff.
type Side struct {
	Label, Author string
	Definition    json.RawMessage
}

// SideOf builds a diff side from a ledger version.
func SideOf(v Version) Side {
	return Side{Label: v.Commit, Author: v.Author, Definition: v.RuleDefinition}
}

type diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

const diffQuery = `query ($input: ExperimentalRuleDiffInput!) {
  experimentalPreviewRuleDiff(input: $input) {
    ok
    empty
    report
    degraded
    diagnostics { severity message }
    summary { alarmsAdded alarmsSuppressed alarmsMoved alarmsUnchanged resolutionsChanged devicesAffected }
  }
}`

// DiffSummary is the machine-readable headline.
type DiffSummary struct {
	AlarmsAdded        int `json:"alarmsAdded"`
	AlarmsSuppressed   int `json:"alarmsSuppressed"`
	AlarmsMoved        int `json:"alarmsMoved"`
	AlarmsUnchanged    int `json:"alarmsUnchanged"`
	ResolutionsChanged int `json:"resolutionsChanged"`
	DevicesAffected    int `json:"devicesAffected"`
}

// DiffResult is the server's answer to a diff.
type DiffResult struct {
	Ok          bool         `json:"ok"`
	Empty       bool         `json:"empty"`
	Report      string       `json:"report"`
	Degraded    *string      `json:"degraded"`
	Diagnostics []diagnostic `json:"diagnostics"`
	Summary     DiffSummary  `json:"summary"`
}

// DiffOptions parameterises a diff.
type DiffOptions struct {
	Endpoint     string
	Profile      string
	Base, Head   Side
	Start, End   time.Time
	ShiftSeconds int
}

func sideVars(s Side) map[string]any {
	m := map[string]any{"ruleDefinition": string(s.Definition), "label": s.Label}
	if s.Author != "" {
		m["author"] = s.Author
	}
	return m
}

// Diff asks the server to replay both sides over the window and report the difference.
func Diff(ctx context.Context, c Client, o DiffOptions) (*DiffResult, error) {
	in := map[string]any{
		"profileToken": o.Profile,
		"base":         sideVars(o.Base),
		"head":         sideVars(o.Head),
		"start":        o.Start.UTC().Format(time.RFC3339),
		"end":          o.End.UTC().Format(time.RFC3339),
	}
	if o.ShiftSeconds > 0 {
		in["shiftWindowSeconds"] = o.ShiftSeconds
	}
	var out struct {
		R DiffResult `json:"experimentalPreviewRuleDiff"`
	}
	if err := c.Query(ctx, o.Endpoint, diffQuery, map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	if !out.R.Ok {
		return &out.R, rejected(out.R.Diagnostics)
	}
	return &out.R, nil
}

const bisectQuery = `query ($input: ExperimentalBisectInput!) {
  experimentalBisectRule(input: $input) {
    ok
    found
    outcome
    replays
    report
    degraded
    diagnostics { severity message }
    commit { id author authorKind message publishedAt }
    previousCommit { id author authorKind message publishedAt }
  }
}`

// Commit is a version the server names.
type Commit struct {
	ID          string  `json:"id"`
	Author      string  `json:"author"`
	AuthorKind  *string `json:"authorKind"`
	Message     *string `json:"message"`
	PublishedAt string  `json:"publishedAt"`
}

// BisectResult is the server's answer to a bisect.
type BisectResult struct {
	Ok             bool         `json:"ok"`
	Found          bool         `json:"found"`
	Outcome        *string      `json:"outcome"`
	Replays        int          `json:"replays"`
	Report         string       `json:"report"`
	Degraded       *string      `json:"degraded"`
	Diagnostics    []diagnostic `json:"diagnostics"`
	Commit         *Commit      `json:"commit"`
	PreviousCommit *Commit      `json:"previousCommit"`
}

// BisectOptions parameterises a bisect.
type BisectOptions struct {
	Endpoint      string
	Ledger        *Ledger
	Series        string
	At            time.Time
	SlackSeconds  int
	LookbackHours int
}

// Bisect asks the server to search the ledger for the version that changed the outcome.
func Bisect(ctx context.Context, c Client, o BisectOptions) (*BisectResult, error) {
	versions := make([]any, len(o.Ledger.Versions))
	for i, v := range o.Ledger.Versions {
		m := map[string]any{"commit": v.Commit, "author": v.Author, "publishedAt": v.PublishedAt,
			"ruleDefinition": string(v.RuleDefinition)}
		if v.AuthorKind != "" {
			m["authorKind"] = v.AuthorKind
		}
		if v.Message != "" {
			m["message"] = v.Message
		}
		versions[i] = m
	}
	in := map[string]any{
		"profileToken": o.Ledger.Profile,
		"versions":     versions,
		"series":       o.Series,
		"at":           o.At.UTC().Format(time.RFC3339),
	}
	if o.SlackSeconds >= 0 {
		in["slackSeconds"] = o.SlackSeconds
	}
	if o.LookbackHours > 0 {
		in["lookbackHours"] = o.LookbackHours
	}
	var out struct {
		R BisectResult `json:"experimentalBisectRule"`
	}
	if err := c.Query(ctx, o.Endpoint, bisectQuery, map[string]any{"input": in}, &out); err != nil {
		return nil, err
	}
	if !out.R.Ok {
		return &out.R, rejected(out.R.Diagnostics)
	}
	return &out.R, nil
}

func rejected(ds []diagnostic) error {
	msgs := make([]string, 0, len(ds))
	for _, d := range ds {
		msgs = append(msgs, d.Message)
	}
	if len(msgs) == 0 {
		return fmt.Errorf("the server rejected the request without a reason")
	}
	return fmt.Errorf("the server rejected the request: %s", strings.Join(msgs, "; "))
}
