// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tm "github.com/devicechain-io/dc-transportmatrix"
)

const specJSON = `{
  "checks": [{"id": "a", "title": "a works"}],
  "controls": [{"id": "N1", "check": "a", "kind": "planted", "defect": "break a"}],
  "tables": [{"id": "t", "firstCell": 1, "rows": [
    {"key": "r", "cells": [{"symbol": "●", "evidence": ["a"]}]}]}]
}`

const pageText = "{/* conformance:table t */}\n\n| Row | Cell |\n| --- | :---: |\n| r | ● |\n"

type fixture struct {
	dir, spec, results, page string
}

func newFixture(t *testing.T, mutate func(*tm.Results)) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{dir: dir, spec: filepath.Join(dir, "matrix.json"), results: filepath.Join(dir, "results.json"), page: filepath.Join(dir, "page.md")}
	if err := os.WriteFile(f.spec, []byte(specJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := tm.ParseSpec([]byte(specJSON))
	if err != nil {
		t.Fatal(err)
	}
	r := &tm.Results{
		Schema: 1, Commit: strings.Repeat("a", 40), GeneratedAt: time.Now().UTC().Format(time.RFC3339), SpecDigest: spec.Digest(),
		Checks:   []tm.CheckResult{{ID: "a", Status: "pass", Evidence: "x"}},
		Controls: []tm.ControlResult{{ID: "N1", Check: "a", Outcome: "red"}},
	}
	if mutate != nil {
		mutate(r)
	}
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(f.results, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.page, []byte(pageText), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) args(cmd string, extra ...string) []string {
	return append([]string{cmd, "-spec", f.spec, "-results", f.results, "-docs", f.page}, extra...)
}

func do(args []string) (int, string, string) {
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

func TestCheckPasses(t *testing.T) {
	f := newFixture(t, nil)
	if code, out, errs := do(f.args("check")); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
}

func TestCheckFailsOnAHandEditAndRenderFixesIt(t *testing.T) {
	f := newFixture(t, nil)
	if err := os.WriteFile(f.page, []byte(strings.Replace(pageText, "| r | ● |", "| r | ○ |", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := do(f.args("check"))
	if code != 1 || !strings.Contains(errs, "do not match") || !strings.Contains(errs, "do not edit them by hand") {
		t.Fatalf("a hand edit was not reported (exit %d): %s", code, errs)
	}
	if code, _, errs := do(f.args("render")); code != 0 {
		t.Fatalf("render exit %d: %s", code, errs)
	}
	got, _ := os.ReadFile(f.page)
	if string(got) != pageText {
		t.Fatalf("render did not restore the page:\n%s", got)
	}
	if code, _, errs := do(f.args("check")); code != 0 {
		t.Fatalf("check after render: exit %d: %s", code, errs)
	}
}

func TestCheckRefusesBadResults(t *testing.T) {
	cases := map[string]func(*tm.Results){
		"dirty":       func(r *tm.Results) { r.Dirty = true },
		"blind":       func(r *tm.Results) { r.Controls[0].Outcome = "green" },
		"failed":      func(r *tm.Results) { r.Checks[0].Status = "fail" },
		"wrong spec":  func(r *tm.Results) { r.SpecDigest = strings.Repeat("0", 64) },
		"bad commit":  func(r *tm.Results) { r.Commit = "nope" },
		"from future": func(r *tm.Results) { r.GeneratedAt = time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339) },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, m)
			code, _, errs := do(f.args("check"))
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, errs)
			}
			if errs == "" {
				t.Fatal("no explanation was printed")
			}
		})
	}
}

func TestMaxAge(t *testing.T) {
	f := newFixture(t, func(r *tm.Results) { r.GeneratedAt = time.Now().Add(-100 * time.Hour).UTC().Format(time.RFC3339) })
	if code, _, errs := do(f.args("check")); code != 0 {
		t.Fatalf("with no age limit old results were refused: %s", errs)
	}
	code, _, errs := do(f.args("check", "-max-age", "24h"))
	if code != 1 || !strings.Contains(errs, "stale") {
		t.Fatalf("old results were accepted under -max-age (exit %d): %s", code, errs)
	}
}

func TestVerifyAndDigest(t *testing.T) {
	f := newFixture(t, nil)
	code, out, errs := do([]string{"verify", "-spec", f.spec, "-results", f.results})
	if code != 0 || !strings.Contains(out, "results ok") {
		t.Fatalf("verify: exit %d %s %s", code, out, errs)
	}
	code, out, _ = do([]string{"digest", "-spec", f.spec})
	if code != 0 || len(strings.TrimSpace(out)) != 64 {
		t.Fatalf("digest: exit %d %q", code, out)
	}
}

func TestUsageAndUnreadableInputs(t *testing.T) {
	if code, _, _ := do(nil); code != 2 {
		t.Fatalf("no arguments: exit %d, want 2", code)
	}
	f := newFixture(t, nil)
	if code, _, _ := do([]string{"frobnicate", "-spec", f.spec, "-results", f.results}); code != 2 {
		t.Fatalf("unknown command: exit %d, want 2", code)
	}
	if code, _, _ := do([]string{"check", "-spec", filepath.Join(f.dir, "missing.json")}); code != 2 {
		t.Fatalf("missing spec: exit %d, want 2", code)
	}
	if code, _, _ := do([]string{"check", "-spec", f.spec, "-results", filepath.Join(f.dir, "missing.json")}); code != 2 {
		t.Fatalf("missing results: exit %d, want 2", code)
	}
	if code, _, _ := do(f.args("check", "-docs", filepath.Join(f.dir, "missing.md"))); code != 2 {
		t.Fatalf("missing page: exit %d, want 2", code)
	}
	if code, _, _ := do([]string{"check", "-nonsense"}); code != 2 {
		t.Fatalf("bad flag: exit %d, want 2", code)
	}
}

func TestPageWithoutMarkerIsAnError(t *testing.T) {
	f := newFixture(t, nil)
	if err := os.WriteFile(f.page, []byte("# nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := do(f.args("check")); code != 1 || !strings.Contains(errs, "no marker") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}
