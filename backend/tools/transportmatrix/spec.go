// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package transportmatrix renders the symbol cells of the transport capability matrix
// (docs/docs/reference/transport-matrix.md) from the conformance rig's results
// (hack/conformance-rig.sh), and refuses to render from anything it should not trust.
//
// WHAT IT PROTECTS. A filled circle on the matrix is a claim about behaviour a customer's device will
// meet. Maintained by hand it can fall behind the code without anyone noticing, and a hand edit that
// overstates a cell is indistinguishable from a true one. Here every filled circle must name the checks
// that substantiate it, those checks must have passed in a rig run of a recent, clean tree, and every
// check must have been shown to fail first (its negative control). The page is then a function of three
// inputs: the spec (which cell claims what, and on what evidence), the results (what happened), and the
// current tree. The CI check re-renders and fails on any hand edit.
//
// WHAT IT DOES NOT DO. It does not write the prose around the tables, and it does not decide whether a
// partial or empty cell is right: a half-filled or empty circle is a declaration, optionally checked by
// a limit check (see Cell). Only a filled circle needs evidence.
package transportmatrix

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// The four symbols of the matrix. The page's own legend defines them.
const (
	Full    = "●"
	Partial = "◐"
	None    = "○"
	NA      = "—"
)

// Spec is hack/conformance/matrix.json: what the rig checks, which defects it plants, and which cell of
// which table is substantiated by which checks.
type Spec struct {
	Checks   []CheckDef   `json:"checks"`
	Controls []ControlDef `json:"controls"`
	Tables   []TableDef   `json:"tables"`
}

// CheckDef is one thing the rig judges. Title says what is asserted; the rig's checker is the judge.
type CheckDef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ControlDef is a negative control: a planted defect, or a fabricated record, that must turn Check red.
// Kind is "planted" (a defect introduced into a live scenario) or "record" (a fabricated record of the
// opposite verdict handed to the checker offline). Both are required to fail the named check before the
// suite counts.
type ControlDef struct {
	ID     string `json:"id"`
	Check  string `json:"check"`
	Kind   string `json:"kind"`
	Defect string `json:"defect"`
}

// TableDef is one generated table. Its ID is the one in the page's conformance:table marker; the table
// that follows the marker is rewritten, row by row in order, and only in the columns FirstCell onward.
type TableDef struct {
	ID string `json:"id"`
	// FirstCell is the zero-based index of the first symbol column among the row's cells, where cell 0
	// is the row's label.
	FirstCell int      `json:"firstCell"`
	Rows      []RowDef `json:"rows"`
}

// RowDef is one table row. Key names it for error messages; it is not matched against the page's text,
// which differs by locale, so the row's position is what binds it.
type RowDef struct {
	Key   string `json:"key"`
	Cells []Cell `json:"cells"`
}

// Cell is one symbol.
//
//   - Symbol is the claim the page makes.
//   - Evidence are the checks that must have passed for a filled or half-filled circle to be printed.
//     A filled circle with none is refused. A half-filled one may have none only if it says Basis
//     "prose": the limitation and the working part are then described by the page and not tested.
//   - Limits are checks that PASS while the stated limitation still holds (for example, "a LwM2M 1.0
//     client yields no telemetry"). If one fails, the limitation is gone and the cell is stale, so
//     rendering is refused until a human revisits it.
//   - Basis is "prose" for a cell the rig does not test, and is required for any cell that is not filled
//     and has no evidence, so an untested cell is a visible decision rather than an omission.
type Cell struct {
	Symbol   string   `json:"symbol"`
	Evidence []string `json:"evidence,omitempty"`
	Limits   []string `json:"limits,omitempty"`
	Basis    string   `json:"basis,omitempty"`
}

// LoadSpec reads and validates a spec file.
func LoadSpec(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the spec: %w", err)
	}
	return ParseSpec(raw)
}

// ParseSpec decodes a spec strictly (unknown keys are errors) and validates it.
func ParseSpec(raw []byte) (*Spec, error) {
	var s Spec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing the spec: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks the spec is internally consistent, independent of any results.
func (s *Spec) Validate() error {
	checks := map[string]bool{}
	for _, c := range s.Checks {
		if c.ID == "" || c.Title == "" {
			return fmt.Errorf("spec: a check needs an id and a title (got id %q)", c.ID)
		}
		if checks[c.ID] {
			return fmt.Errorf("spec: check %q is declared twice", c.ID)
		}
		checks[c.ID] = true
	}
	if len(checks) == 0 {
		return fmt.Errorf("spec: no checks declared")
	}
	controlled := map[string]bool{}
	controls := map[string]bool{}
	for _, c := range s.Controls {
		if c.ID == "" || c.Defect == "" {
			return fmt.Errorf("spec: a control needs an id and a defect (got id %q)", c.ID)
		}
		if controls[c.ID] {
			return fmt.Errorf("spec: control %q is declared twice", c.ID)
		}
		controls[c.ID] = true
		if !checks[c.Check] {
			return fmt.Errorf("spec: control %q names check %q, which is not declared", c.ID, c.Check)
		}
		if c.Kind != "planted" && c.Kind != "record" {
			return fmt.Errorf("spec: control %q has kind %q; want planted or record", c.ID, c.Kind)
		}
		controlled[c.Check] = true
	}
	for _, c := range s.Checks {
		if !controlled[c.ID] {
			return fmt.Errorf("spec: check %q has no negative control: a check that has never been shown to fail proves nothing", c.ID)
		}
	}
	tables := map[string]bool{}
	for _, t := range s.Tables {
		if t.ID == "" || tables[t.ID] {
			return fmt.Errorf("spec: table id %q is empty or repeated", t.ID)
		}
		tables[t.ID] = true
		if t.FirstCell < 1 {
			return fmt.Errorf("spec: table %q: firstCell must be at least 1 (cell 0 is the row label)", t.ID)
		}
		if len(t.Rows) == 0 {
			return fmt.Errorf("spec: table %q has no rows", t.ID)
		}
		width := -1
		for _, r := range t.Rows {
			if len(r.Cells) == 0 {
				return fmt.Errorf("spec: table %q row %q has no cells", t.ID, r.Key)
			}
			if width >= 0 && len(r.Cells) != width {
				return fmt.Errorf("spec: table %q row %q has %d cells, earlier rows have %d", t.ID, r.Key, len(r.Cells), width)
			}
			width = len(r.Cells)
			for i, c := range r.Cells {
				if err := c.validate(checks); err != nil {
					return fmt.Errorf("spec: table %q row %q cell %d: %w", t.ID, r.Key, i, err)
				}
			}
		}
	}
	return nil
}

func (c Cell) validate(checks map[string]bool) error {
	switch c.Symbol {
	case Full, Partial, None, NA:
	default:
		return fmt.Errorf("symbol %q is not one of the four", c.Symbol)
	}
	for _, id := range append(append([]string{}, c.Evidence...), c.Limits...) {
		if !checks[id] {
			return fmt.Errorf("names check %q, which is not declared", id)
		}
	}
	if c.Basis != "" && c.Basis != "prose" {
		return fmt.Errorf("basis %q is not recognised", c.Basis)
	}
	switch c.Symbol {
	case Full:
		if len(c.Evidence) == 0 {
			return fmt.Errorf("a filled circle needs evidence: it is a claim, and nothing substantiates it")
		}
		if len(c.Limits) > 0 {
			return fmt.Errorf("a filled circle has no limitation to check; use the half-filled one")
		}
		if c.Basis != "" {
			return fmt.Errorf("a filled circle cannot rest on prose")
		}
	case Partial:
		if len(c.Evidence) == 0 && c.Basis != "prose" {
			return fmt.Errorf(`a half-filled circle with no evidence must say basis "prose"`)
		}
		if len(c.Evidence) > 0 && c.Basis != "" {
			return fmt.Errorf("a cell with evidence does not also rest on prose")
		}
		if len(c.Limits) > 0 && len(c.Evidence) == 0 {
			return fmt.Errorf("limit checks without evidence for the part that works")
		}
	case None, NA:
		if len(c.Evidence) > 0 || len(c.Limits) > 0 {
			return fmt.Errorf("an empty cell has no evidence to cite")
		}
		if c.Basis != "prose" {
			return fmt.Errorf(`an empty cell must say basis "prose"`)
		}
	}
	return nil
}

// Digest identifies what the rig must have run: the checks and the controls, in a canonical order.
// Editing a table cell does not change it, so a docs-only change does not demand a rig run; adding,
// removing or redefining a check or a control does, and a results file made before that is stale.
func (s *Spec) Digest() string {
	type cd struct{ ID, Title string }
	type ctl struct{ ID, Check, Kind, Defect string }
	var cs []cd
	for _, c := range s.Checks {
		cs = append(cs, cd{c.ID, c.Title})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
	var ks []ctl
	for _, c := range s.Controls {
		ks = append(ks, ctl{c.ID, c.Check, c.Kind, c.Defect})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].ID < ks[j].ID })
	b, _ := json.Marshal(struct {
		Checks   []cd
		Controls []ctl
	}{cs, ks})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
