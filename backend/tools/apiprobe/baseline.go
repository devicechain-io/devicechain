// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// baseline is the schema tree a SEED is measured against, and it exists because
// of the one thing an upgrade drill cannot avoid: the coverage table is built
// from the NEW platform, and the seed runs against the OLD one.
//
// # THE PROBLEM IT SOLVES, MEASURED RATHER THAN GUESSED
//
// apiprobe covers every create mutation HEAD serves. Two of them — createGeoFence
// and createCommandBatch — did not exist in v0.11.0. Seeding the whole table into
// a v0.11.0 instance therefore dies at the fifth row with REFUSED, and the drill
// never reaches the upgrade it was built to test. The failure is loud, but it is
// about the tool's own vocabulary rather than about the platform, and every future
// release will add more of them.
//
// So `seed --baseline-schemas <dir>` points at the OLD version's served schemas —
// the rig already extracts that tree to build the matching dcctl — and every
// entity the old schema cannot express is SKIPPED, by name, with the reason. What
// remains is exactly the set of rows that release could hold, which is exactly the
// set an upgrade can be asked to carry forward.
//
// # WHAT IT DELIBERATELY DOES NOT DO
//
// It checks that the MUTATION, the READ QUERY and the INPUT TYPE exist. It does
// NOT check the individual fields a row selects or sends.
//
// That is a decision, not an omission. If a later release adds a field to a type
// the table already covers, field-checking would skip the WHOLE entity — trading
// one new field for the loss of every other field on that row, silently, in a tool
// whose entire job is to notice loss. The honest answer there is that the table's
// selections have to stay expressible by the oldest baseline a drill runs from,
// and that is a maintainer's call. Until it is made, the seed refuses the create
// and says which entity and why — which is the loud failure, in the right place.
//
// # THE FAIL-OPEN THIS COULD BECOME
//
// 🔴 A `supports` that answered "no" too readily would skip the entire table and
// leave a receipt with nothing in it, and verify would then pass instantly having
// checked nothing. Two things stop that: seeding zero rows is an error, and a test
// points a baseline at the CURRENT tree and requires that nothing at all is
// skipped. The second is the one that matters — it is the only check that can tell
// a working filter from one that rejects everything.
type baseline struct {
	dir string
	// raw is the concatenated served schema text per functional area, and
	// stripped the same text with all whitespace removed. Both are kept because
	// a type declaration is matched with its spacing (`input X {`) and a field
	// signature without it (`createX(request:`), and re-deriving either per
	// lookup would re-scan the tree for every row.
	raw      map[string]string
	stripped map[string]string
}

// loadBaseline reads every tenant-plane schema under dir, which is expected to be
// a `backend/services` directory from the release being upgraded FROM.
//
// Admin schemas are excluded for the same reason the coverage test excludes them:
// they are a separate identity-token surface, and nothing in the table is served
// there.
func loadBaseline(dir string) (*baseline, error) {
	b := &baseline{dir: dir, raw: map[string]string{}, stripped: map[string]string{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, failWith(exitSetup, "read baseline schemas at %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, e.Name(), "graphql", "*.graphql"))
		if err != nil {
			return nil, failWith(exitSetup, "scan %s: %w", e.Name(), err)
		}
		var body strings.Builder
		for _, m := range matches {
			if strings.Contains(filepath.Base(m), "admin") {
				continue
			}
			part, err := os.ReadFile(m)
			if err != nil {
				return nil, failWith(exitSetup, "read %s: %w", m, err)
			}
			body.Write(part)
			body.WriteString("\n")
		}
		if body.Len() > 0 {
			b.raw[e.Name()] = body.String()
			b.stripped[e.Name()] = stripAllSpace(body.String())
		}
	}
	if len(b.raw) == 0 {
		// An empty tree would mark every entity unsupported and produce a receipt
		// with nothing on it — a verify that passes having checked nothing. Refuse
		// it here, where the path is still in hand to name.
		return nil, failWith(exitSetup, "%s holds no served schemas; a baseline that declares nothing would skip the whole table", dir)
	}
	return b, nil
}

// supports reports whether the baseline can express this entity, and if not, why
// — the reason is printed beside the skipped row, because "24 of 26" with no
// explanation is indistinguishable from a tool quietly giving up.
func (b *baseline) supports(e entity) (bool, string) {
	stripped, ok := b.stripped[e.Area]
	if !ok {
		return false, "the baseline serves no " + e.Area + " schema"
	}
	if !strings.Contains(stripped, e.Mutation+"("+e.arg()+":") {
		return false, "the baseline does not declare " + e.Mutation + "(" + e.arg() + ":…)"
	}
	// The read is matched the way readDoc SPELLS it, so a query that changed from
	// a list lookup to a single one counts as unsupported rather than being read
	// with the wrong document.
	readArg := "tokens:"
	if e.Single {
		readArg = "token:"
	}
	if !strings.Contains(stripped, e.Read+"("+readArg) {
		return false, "the baseline does not declare " + e.Read + "(" + strings.TrimSuffix(readArg, ":") + ":…)"
	}
	if input := inputTypeName(e.Input); !strings.Contains(b.raw[e.Area], "input "+input+" {") {
		return false, "the baseline does not declare input " + input
	}
	return true, ""
}

// plan decides what a seed should do with one entity: write it, skip it, or
// refuse to run at all.
//
// 🔴 THE THIRD ANSWER IS THE ONE THAT MATTERS. An entity whose token LATER rows
// reference cannot be skipped — the dependents would send an empty string and be
// refused by the platform for a reason that names them rather than the hole,
// several rows after the decision that made it. The drill would report a finding
// about the wrong entity, and the real cause would be a skip printed minutes
// earlier and scrolled past.
//
// A nil baseline means no filtering at all: a seed against the current release
// writes the whole table, which is what `verify` on a fresh install expects.
func plan(e entity, base *baseline) (write bool, why string, err error) {
	if base == nil {
		return true, "", nil
	}
	ok, reason := base.supports(e)
	if ok {
		return true, "", nil
	}
	if e.Record != nil {
		return false, reason, failWith(exitSetup,
			"%s cannot be seeded against this baseline (%s), and later entities reference it; "+
				"this drill cannot skip it, so the table must be pinned to what the baseline can express",
			e.Name, reason)
	}
	return false, reason, nil
}

// inputTypeName reduces a full GraphQL input reference to the bare type name:
// "[EntityRelationshipCreateRequest!]!" is a list of the same type a
// non-list entry names directly, and only the name is declared.
func inputTypeName(input string) string {
	return strings.Trim(input, "[]!")
}

func stripAllSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}
