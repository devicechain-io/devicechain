// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package transportmatrix

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const goodSHA = "0123456789abcdef0123456789abcdef01234567"

// miniSpec is a small but complete spec: one filled circle resting on two checks, one half-filled circle
// with a limit check, and prose cells.
func miniSpec() *Spec {
	return &Spec{
		Checks: []CheckDef{
			{ID: "a", Title: "a works"},
			{ID: "b", Title: "b works"},
			{ID: "lim", Title: "the limitation holds"},
		},
		Controls: []ControlDef{
			{ID: "Na", Check: "a", Kind: "planted", Defect: "break a"},
			{ID: "Rb", Check: "b", Kind: "record", Defect: "fake b"},
			{ID: "Rl", Check: "lim", Kind: "record", Defect: "fake lim"},
		},
		Tables: []TableDef{{
			ID: "t", FirstCell: 1,
			Rows: []RowDef{
				{Key: "r1", Cells: []Cell{
					{Symbol: None, Basis: "prose"},
					{Symbol: Partial, Evidence: []string{"a"}, Limits: []string{"lim"}},
					{Symbol: Full, Evidence: []string{"a", "b"}}}},
				{Key: "r2", Cells: []Cell{
					{Symbol: NA, Basis: "prose"},
					{Symbol: Partial, Basis: "prose"},
					{Symbol: None, Basis: "prose"}}},
			},
		}},
	}
}

func goodResults(s *Spec) *Results {
	r := &Results{
		Schema: ResultsSchema, Commit: goodSHA, GeneratedAt: fixedNow.Add(-time.Hour).Format(time.RFC3339),
		SpecDigest: s.Digest(),
	}
	for _, c := range s.Checks {
		r.Checks = append(r.Checks, CheckResult{ID: c.ID, Status: "pass", Evidence: "read the store"})
	}
	for _, c := range s.Controls {
		r.Controls = append(r.Controls, ControlResult{ID: c.ID, Check: c.Check, Outcome: "red", Detail: "went red"})
	}
	return r
}

const page = `# Title

{/* conformance:table t */}

| Transport | Read | Write | Subscribe |
| --- | :---: | :---: | :---: |
| [One](x.md) | ○ | ◐ | ● |
| Two | — | ◐ | ○ |

Prose after.
`

func TestSpecValidate(t *testing.T) {
	if err := miniSpec().Validate(); err != nil {
		t.Fatalf("a good spec was refused: %v", err)
	}
	cases := map[string]struct {
		mutate func(*Spec)
		want   string
	}{
		"no checks":             {func(s *Spec) { s.Checks = nil }, "no checks"},
		"duplicate check":       {func(s *Spec) { s.Checks = append(s.Checks, s.Checks[0]) }, "declared twice"},
		"check without title":   {func(s *Spec) { s.Checks[0].Title = "" }, "needs an id and a title"},
		"check without control": {func(s *Spec) { s.Controls = s.Controls[1:] }, "no negative control"},
		"control unknown check": {func(s *Spec) { s.Controls[0].Check = "zz" }, "not declared"},
		"control bad kind":      {func(s *Spec) { s.Controls[0].Kind = "other" }, "kind"},
		"control no defect":     {func(s *Spec) { s.Controls[0].Defect = "" }, "needs an id and a defect"},
		"duplicate control":     {func(s *Spec) { s.Controls = append(s.Controls, s.Controls[0]) }, "declared twice"},
		"table repeated":        {func(s *Spec) { s.Tables = append(s.Tables, s.Tables[0]) }, "repeated"},
		"firstCell zero":        {func(s *Spec) { s.Tables[0].FirstCell = 0 }, "firstCell"},
		"table no rows":         {func(s *Spec) { s.Tables[0].Rows = nil }, "no rows"},
		"ragged rows":           {func(s *Spec) { s.Tables[0].Rows[1].Cells = s.Tables[0].Rows[1].Cells[:2] }, "cells, earlier rows"},
		"row without cells":     {func(s *Spec) { s.Tables[0].Rows[0].Cells = nil }, "no cells"},
		"bad symbol":            {func(s *Spec) { s.Tables[0].Rows[0].Cells[0].Symbol = "x" }, "not one of the four"},
		"unknown evidence":      {func(s *Spec) { s.Tables[0].Rows[0].Cells[2].Evidence = []string{"zz"} }, "not declared"},
		"unknown limit":         {func(s *Spec) { s.Tables[0].Rows[0].Cells[1].Limits = []string{"zz"} }, "not declared"},
		"filled without proof":  {func(s *Spec) { s.Tables[0].Rows[0].Cells[2].Evidence = nil }, "needs evidence"},
		"filled on prose":       {func(s *Spec) { s.Tables[0].Rows[0].Cells[2].Basis = "prose" }, "cannot rest on prose"},
		"filled with a limit":   {func(s *Spec) { s.Tables[0].Rows[0].Cells[2].Limits = []string{"lim"} }, "no limitation"},
		"partial no basis":      {func(s *Spec) { s.Tables[0].Rows[1].Cells[1].Basis = "" }, "basis"},
		"partial both":          {func(s *Spec) { s.Tables[0].Rows[0].Cells[1].Basis = "prose" }, "does not also rest on prose"},
		"limit without proof": {func(s *Spec) {
			c := &s.Tables[0].Rows[1].Cells[1]
			c.Limits = []string{"lim"}
		}, "limit checks without evidence"},
		"none with evidence": {func(s *Spec) { s.Tables[0].Rows[1].Cells[2].Evidence = []string{"a"} }, "no evidence to cite"},
		"none without basis": {func(s *Spec) { s.Tables[0].Rows[1].Cells[2].Basis = "" }, "must say basis"},
		"basis unknown":      {func(s *Spec) { s.Tables[0].Rows[1].Cells[1].Basis = "vibes" }, "not recognised"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := miniSpec()
			tc.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("the spec was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseSpecIsStrict(t *testing.T) {
	if _, err := ParseSpec([]byte(`{"checks":[],"surprise":1}`)); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if _, err := ParseSpec([]byte(`not json`)); err == nil {
		t.Fatal("garbage was accepted")
	}
}

func TestDigest(t *testing.T) {
	a := miniSpec()
	b := miniSpec()
	if a.Digest() != b.Digest() {
		t.Fatal("the digest is not deterministic")
	}
	// Order does not matter.
	b.Checks[0], b.Checks[1] = b.Checks[1], b.Checks[0]
	b.Controls[0], b.Controls[1] = b.Controls[1], b.Controls[0]
	if a.Digest() != b.Digest() {
		t.Fatal("the digest depends on declaration order")
	}
	// A table cell does not matter: a docs-only change must not demand a rig run.
	b.Tables[0].Rows[0].Cells[0].Symbol = Full
	if a.Digest() != b.Digest() {
		t.Fatal("the digest depends on the tables")
	}
	// A check, a control, their title or their defect does.
	for name, mut := range map[string]func(*Spec){
		"title":      func(s *Spec) { s.Checks[0].Title = "changed" },
		"new check":  func(s *Spec) { s.Checks = append(s.Checks, CheckDef{ID: "c", Title: "c"}) },
		"defect":     func(s *Spec) { s.Controls[0].Defect = "changed" },
		"ctl check":  func(s *Spec) { s.Controls[0].Check = "b" },
		"ctl kind":   func(s *Spec) { s.Controls[0].Kind = "record" },
		"ctl id":     func(s *Spec) { s.Controls[0].ID = "Nz" },
		"drop check": func(s *Spec) { s.Checks = s.Checks[:2] },
	} {
		c := miniSpec()
		mut(c)
		if c.Digest() == a.Digest() {
			t.Errorf("changing the %s did not change the digest", name)
		}
	}
}

func TestVerify(t *testing.T) {
	s := miniSpec()
	if _, err := goodResults(s).Verify(s, Options{Now: fixedNow, MaxAge: 24 * time.Hour}); err != nil {
		t.Fatalf("good results were refused: %v", err)
	}
	cases := map[string]struct {
		mutate func(*Results)
		opts   Options
		want   string
	}{
		"schema":          {func(r *Results) { r.Schema = 2 }, Options{}, "schema"},
		"short sha":       {func(r *Results) { r.Commit = "abc123" }, Options{}, "full SHA"},
		"upper sha":       {func(r *Results) { r.Commit = strings.ToUpper(goodSHA) }, Options{}, "full SHA"},
		"dirty":           {func(r *Results) { r.Dirty = true }, Options{}, "uncommitted"},
		"bad time":        {func(r *Results) { r.GeneratedAt = "yesterday" }, Options{}, "RFC 3339"},
		"future":          {func(r *Results) { r.GeneratedAt = fixedNow.Add(time.Hour).Format(time.RFC3339) }, Options{}, "future"},
		"stale":           {func(r *Results) { r.GeneratedAt = fixedNow.Add(-72 * time.Hour).Format(time.RFC3339) }, Options{MaxAge: 24 * time.Hour}, "stale"},
		"digest":          {func(r *Results) { r.SpecDigest = strings.Repeat("0", 64) }, Options{}, "different set of checks"},
		"unknown check":   {func(r *Results) { r.Checks = append(r.Checks, CheckResult{ID: "zz", Status: "pass"}) }, Options{}, "not in the spec"},
		"duplicate check": {func(r *Results) { r.Checks = append(r.Checks, r.Checks[0]) }, Options{}, "appears twice"},
		"missing check":   {func(r *Results) { r.Checks = r.Checks[1:] }, Options{}, "was not run"},
		"bad status":      {func(r *Results) { r.Checks[0].Status = "ok" }, Options{}, "status"},
		"unknown control": {func(r *Results) { r.Controls = append(r.Controls, ControlResult{ID: "zz", Outcome: "red"}) }, Options{}, "not in the spec"},
		"dup control":     {func(r *Results) { r.Controls = append(r.Controls, r.Controls[0]) }, Options{}, "appears twice"},
		"missing control": {func(r *Results) { r.Controls = r.Controls[1:] }, Options{}, "was not run"},
		"control check":   {func(r *Results) { r.Controls[0].Check = "b" }, Options{}, "the spec says"},
		"control outcome": {func(r *Results) { r.Controls[0].Outcome = "maybe" }, Options{}, "outcome"},
		"blind control":   {func(r *Results) { r.Controls[0].Outcome = "green" }, Options{}, "did NOT turn their check red"},
		"not ancestor":    {func(r *Results) {}, Options{IsAncestor: func(string) (bool, error) { return false, nil }}, "not an ancestor"},
		"ancestor error":  {func(r *Results) {}, Options{IsAncestor: func(string) (bool, error) { return false, errors.New("boom") }}, "boom"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := goodResults(s)
			tc.mutate(r)
			opts := tc.opts
			opts.Now = fixedNow
			_, err := r.Verify(s, opts)
			if err == nil {
				t.Fatal("the results were accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestVerifyAgeBoundary(t *testing.T) {
	s := miniSpec()
	r := goodResults(s)
	r.GeneratedAt = fixedNow.Add(-48 * time.Hour).Format(time.RFC3339)
	if _, err := r.Verify(s, Options{Now: fixedNow, MaxAge: 48 * time.Hour}); err != nil {
		t.Fatalf("results exactly at the limit were refused: %v", err)
	}
	if _, err := r.Verify(s, Options{Now: fixedNow, MaxAge: 48*time.Hour - time.Second}); err == nil {
		t.Fatal("results one second past the limit were accepted")
	}
	// MaxAge zero means the age is not checked at all.
	r.GeneratedAt = fixedNow.Add(-100000 * time.Hour).Format(time.RFC3339)
	if _, err := r.Verify(s, Options{Now: fixedNow}); err != nil {
		t.Fatalf("with no age limit, old results were refused: %v", err)
	}
	// A small clock skew into the future is tolerated; a large one is not.
	r.GeneratedAt = fixedNow.Add(5 * time.Minute).Format(time.RFC3339)
	if _, err := r.Verify(s, Options{Now: fixedNow}); err != nil {
		t.Fatalf("five minutes of clock skew was refused: %v", err)
	}
}

func TestVerifyPassedSet(t *testing.T) {
	s := miniSpec()
	r := goodResults(s)
	r.Checks[1].Status = "fail"
	passed, err := r.Verify(s, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if !passed["a"] || passed["b"] || !passed["lim"] {
		t.Fatalf("passed set is wrong: %v", passed)
	}
}

func TestParseResultsIsStrict(t *testing.T) {
	if _, err := ParseResults([]byte(`{"schema":1,"extra":true}`)); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if _, err := ParseResults([]byte(`{`)); err == nil {
		t.Fatal("garbage was accepted")
	}
}

func TestCheckCells(t *testing.T) {
	s := miniSpec()
	all := map[string]bool{"a": true, "b": true, "lim": true}
	if err := CheckCells(s, all); err != nil {
		t.Fatalf("a supported matrix was refused: %v", err)
	}
	err := CheckCells(s, map[string]bool{"a": true, "lim": true})
	if err == nil || !strings.Contains(err.Error(), "rests on check b") {
		t.Fatalf("a failed evidence check was not reported: %v", err)
	}
	err = CheckCells(s, map[string]bool{"a": true, "b": true})
	if err == nil || !strings.Contains(err.Error(), "no longer observes") {
		t.Fatalf("a gone limitation was not reported: %v", err)
	}
	// Every offending cell is listed.
	err = CheckCells(s, map[string]bool{})
	if err == nil || strings.Count(err.Error(), "\n  ") < 3 {
		t.Fatalf("not every problem was listed: %v", err)
	}
}

func TestRender(t *testing.T) {
	s := miniSpec()
	got, err := Render(page, s)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(page, "| [One](x.md) | ○ | ◐ | ● |", "| [One](x.md) | ○ | ◐ | ● |", 1)
	if got != want {
		t.Fatalf("an already-correct page changed:\n%s", got)
	}
	// A hand edit is put back, and nothing else moves.
	edited := strings.Replace(page, "| [One](x.md) | ○ | ◐ | ● |", "| [One](x.md) | ● | ◐ | ● |", 1)
	edited = strings.Replace(edited, "| Two | — | ◐ | ○ |", "| Two | — | ● | ○ |", 1)
	got, err = Render(edited, s)
	if err != nil {
		t.Fatal(err)
	}
	if got != page {
		t.Fatalf("a hand edit was not reverted:\n%s", got)
	}
	if d := Diff(edited, got); len(d) != 2 {
		t.Fatalf("expected two differing lines, got %v", d)
	}
	if d := Diff(page, got); len(d) != 0 {
		t.Fatalf("a matching page has a diff: %v", d)
	}
}

func TestRenderKeepsPaddingAndLineEndings(t *testing.T) {
	s := miniSpec()
	crlf := strings.ReplaceAll(page, "\n", "\r\n")
	crlf = strings.Replace(crlf, "| Two | — | ◐ | ○ |", "|   Two   |  ●  |  ◐   |  ○ |", 1)
	got, err := Render(crlf, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "|   Two   |  —  |  ◐   |  ○ |\r\n") {
		t.Fatalf("padding or line ending was not preserved:\n%q", got)
	}
	// An indented table keeps its indent.
	indented := strings.ReplaceAll(page, "\n| ", "\n  | ")
	got, err = Render(indented, s)
	if err != nil || !strings.Contains(got, "\n  | Two | — | ◐ | ○ |") {
		t.Fatalf("indent lost (%v):\n%s", err, got)
	}
}

func TestRenderRefusals(t *testing.T) {
	s := miniSpec()
	cases := map[string]struct {
		doc  string
		want string
	}{
		"unknown table": {strings.Replace(page, "table t ", "table zz ", 1), "not in the spec"},
		"marked twice":  {page + "\n{/* conformance:table t */}\n\n| H | R | W | S |\n| --- | --- | --- | --- |\n| a | ○ | ○ | ○ |\n| b | ○ | ○ | ○ |\n", "marked twice"},
		"no marker":     {strings.Replace(page, "{/* conformance:table t */}", "", 1), "no marker"},
		"no table":      {strings.Replace(page, "| Transport | Read | Write | Subscribe |\n| --- | :---: | :---: | :---: |\n", "", 1), "not followed by a table"},
		"extra row":     {strings.Replace(page, "| Two | — | ◐ | ○ |", "| Two | — | ◐ | ○ |\n| Three | ○ | ○ | ○ |", 1), "3 rows on the page and 2 in the spec"},
		"missing row":   {strings.Replace(page, "| Two | — | ◐ | ○ |\n", "", 1), "1 rows on the page and 2 in the spec"},
		"not a symbol":  {strings.Replace(page, "| Two | — | ◐ | ○ |", "| Two | — | partly | ○ |", 1), "not one of the four"},
		"too few cells": {strings.Replace(page, "| Two | — | ◐ | ○ |", "| Two | — |", 1), "cells"},
		"marker at end": {"{/* conformance:table t */}\n", "not followed by a table"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Render(tc.doc, s)
			if err == nil {
				t.Fatal("the page was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRenderIgnoresUnmarkedTables(t *testing.T) {
	s := miniSpec()
	doc := page + "\n| Connector | Read |\n| --- | :---: |\n| `httpCall` | ● |\n"
	got, err := Render(doc, s)
	if err != nil {
		t.Fatal(err)
	}
	if got != doc {
		t.Fatalf("an unmarked table was touched:\n%s", got)
	}
}

func TestIsSeparator(t *testing.T) {
	for _, l := range []string{"| --- | --- |", "|---|:---:|", "| :--- | ---: |"} {
		if !isSeparator(l) {
			t.Errorf("%q is a separator", l)
		}
	}
	for _, l := range []string{"| a | --- |", "| | --- |", "| ● | ○ |"} {
		if isSeparator(l) {
			t.Errorf("%q is not a separator", l)
		}
	}
}
