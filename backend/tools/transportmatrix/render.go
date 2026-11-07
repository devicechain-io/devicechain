// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package transportmatrix

import (
	"fmt"
	"strings"
)

// The marker that binds a table to the spec. It is an MDX comment, so it renders as nothing, and it
// sits on a line of its own directly above the table it governs.
const (
	markerOpen  = "{/* conformance:table "
	markerClose = " */}"
)

// CheckCells refuses a spec/results pair in which a cell relies on something that did not happen: a
// piece of evidence that did not pass, or a limitation that has stopped holding. Every offending cell is
// listed, so one run says everything that is wrong.
func CheckCells(s *Spec, passed map[string]bool) error {
	var problems []string
	for _, t := range s.Tables {
		for _, r := range t.Rows {
			for i, c := range r.Cells {
				where := fmt.Sprintf("table %s, row %s, cell %d (%s)", t.ID, r.Key, i, c.Symbol)
				for _, id := range c.Evidence {
					if !passed[id] {
						problems = append(problems, fmt.Sprintf("%s rests on check %s, which did not pass", where, id))
					}
				}
				for _, id := range c.Limits {
					if !passed[id] {
						problems = append(problems, fmt.Sprintf("%s states a limitation that check %s no longer observes; the cell is stale, revisit it", where, id))
					}
				}
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the results do not support the matrix:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// Render rewrites the symbol cells of every marked table in doc from the spec and returns the new text.
// Every table in the spec must be marked exactly once, every marker must name a spec table, and the
// table under it must have exactly the spec's rows; anything else is an error, so the page and the spec
// cannot drift apart by one of them gaining or losing a row.
func Render(doc string, s *Spec) (string, error) {
	lines := strings.Split(doc, "\n")
	byID := map[string]TableDef{}
	for _, t := range s.Tables {
		byID[t.ID] = t
	}
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		id, ok := markerID(lines[i])
		if !ok {
			continue
		}
		t, known := byID[id]
		if !known {
			return "", fmt.Errorf("line %d: marker names table %q, which is not in the spec", i+1, id)
		}
		if seen[id] {
			return "", fmt.Errorf("line %d: table %q is marked twice", i+1, id)
		}
		seen[id] = true
		// The table starts on the next non-blank line.
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j+1 >= len(lines) || !isRow(lines[j]) || !isRow(lines[j+1]) || !isSeparator(lines[j+1]) {
			return "", fmt.Errorf("line %d: the marker for table %q is not followed by a table (a header row and a separator)", i+1, id)
		}
		first := j + 2
		last := first
		for last < len(lines) && isRow(lines[last]) {
			last++
		}
		if got := last - first; got != len(t.Rows) {
			return "", fmt.Errorf("line %d: table %q has %d rows on the page and %d in the spec", i+1, id, got, len(t.Rows))
		}
		for k, row := range t.Rows {
			out, err := rewriteRow(lines[first+k], t.FirstCell, row)
			if err != nil {
				return "", fmt.Errorf("line %d: table %q row %q: %w", first+k+1, id, row.Key, err)
			}
			lines[first+k] = out
		}
		i = last
	}
	for _, t := range s.Tables {
		if !seen[t.ID] {
			return "", fmt.Errorf("table %q is in the spec but the page has no marker for it", t.ID)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func markerID(line string) (string, bool) {
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(l, markerOpen) || !strings.HasSuffix(l, markerClose) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(l, markerOpen), markerClose)), true
}

func isRow(line string) bool {
	l := strings.TrimSpace(line)
	return strings.HasPrefix(l, "|") && strings.HasSuffix(l, "|") && len(l) > 1
}

func isSeparator(line string) bool {
	l := strings.Trim(strings.TrimSpace(line), "|")
	for _, c := range strings.Split(l, "|") {
		c = strings.TrimSpace(c)
		if c == "" || strings.Trim(c, "-:") != "" {
			return false
		}
	}
	return true
}

// rewriteRow replaces the symbol cells of one table row, keeping every other character, including each
// cell's padding and any trailing carriage return.
func rewriteRow(line string, firstCell int, row RowDef) (string, error) {
	trail := ""
	body := line
	if strings.HasSuffix(body, "\r") {
		trail = "\r"
		body = strings.TrimSuffix(body, "\r")
	}
	indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	core := strings.TrimSpace(body)
	// Drop the outer pipes, then split on the pipes between cells. None of the generated tables holds a
	// pipe inside a cell, and one that did would change the cell count and be refused below.
	cells := strings.Split(core[1:len(core)-1], "|")
	if firstCell+len(row.Cells) > len(cells) {
		return "", fmt.Errorf("the page row has %d cells and the spec needs cells %d to %d", len(cells), firstCell, firstCell+len(row.Cells)-1)
	}
	for k, c := range row.Cells {
		idx := firstCell + k
		old := strings.TrimSpace(cells[idx])
		switch old {
		case Full, Partial, None, NA:
		default:
			return "", fmt.Errorf("cell %d holds %q, which is not one of the four symbols", idx, old)
		}
		lead := cells[idx][:len(cells[idx])-len(strings.TrimLeft(cells[idx], " "))]
		tail := cells[idx][len(strings.TrimRight(cells[idx], " ")):]
		cells[idx] = lead + c.Symbol + tail
	}
	return indent + "|" + strings.Join(cells, "|") + "|" + trail, nil
}

// Diff lists the lines at which two renderings differ, for a drift report. Both have the same number of
// lines, since Render only changes cells within lines.
func Diff(have, want string) []string {
	h := strings.Split(have, "\n")
	w := strings.Split(want, "\n")
	var out []string
	for i := 0; i < len(h) && i < len(w); i++ {
		if h[i] != w[i] {
			out = append(out, fmt.Sprintf("line %d:\n    page: %s\n    want: %s", i+1, strings.TrimRight(h[i], "\r"), strings.TrimRight(w[i], "\r")))
		}
	}
	return out
}
