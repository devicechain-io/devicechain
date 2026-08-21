// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package nldraft

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The drafting prompt is a SECOND declaration of a vocabulary the DETECT compiler already
// declares, and a THIRD statement of rules the compiler already enforces. Nothing made any
// of them agree, and they did not: `geo` and its containment function shipped in the
// environment as schema v3 while this prompt kept telling the model "No other identifiers
// exist in either" over a list of five — so the one surface meant to make geofencing
// discoverable actively asserted it did not exist.
//
// 🔴 A DRIFT LIKE THAT CANNOT BE CAUGHT BY TESTING THE PROMPT AGAINST ITSELF, AND THE FIRST
// VERSION OF THIS FILE PROVED THE POINT BY GETTING IT WRONG TWICE. It walked a hand-written
// identifier list (which can only ever check the identifiers somebody remembered to add to
// it) and it made no assertion at all about the prompt's semantic claims — so the paragraph
// it shipped alongside, telling models that a fence test combined with a measurement merely
// NARROWS a rule, went unchecked. The compiler REFUSES that construct outright. The prose
// that was supposed to stop a model getting the scoping wrong quietly had the scoping wrong
// itself.
//
// So the prompt is now checked against the two things that actually bind:
//
//   - the ENVIRONMENT, for what identifiers exist — read out of env.go's source with go/ast,
//     never from a list maintained here; and
//   - the COMPILER, for what the prompt claims about legality — by compiling the exact
//     expression the prompt forbids and the exact one it recommends.

// declaredIdentifiers reads every variable predicate.Env() binds, by parsing env.go rather
// than by listing them here.
//
// 🔴 A HAND LIST CANNOT DETECT ITS OWN OMISSION, WHICH IS THE ENTIRE FAILURE BEING FIXED. An
// earlier draft paired one with a pin on predicate.SchemaVersion, on the theory that the
// version's documented "bump on any change to the declarations below" contract would stop
// anyone adding a variable without passing through here. It would not: nothing enforces the
// bump — SchemaVersion has no mechanical consumer, and its own doc records ext.Bindings() as
// a deliberate NON-bump, so "does this need a bump?" is a judgement call rather than a
// reflex. Add a variable, skip the bump, and the pin passes, the list stays short, and the
// prompt goes on denying the new identifier exists. Reading the declarations is the only
// form of this check that cannot be silently outrun.
func declaredIdentifiers(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "detect", "predicate", "env.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoErrorf(t, err, "parse %s", path)

	// Every `VarX = "x"` constant, so a cel.Variable(VarX, …) can be resolved to its spelling.
	consts := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil {
					consts[name.Name] = v
				}
			}
		}
	}

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Variable" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "cel" {
			return true
		}
		if id, ok := call.Args[0].(*ast.Ident); ok {
			if v, found := consts[id.Name]; found {
				out = append(out, v)
			} else {
				t.Errorf("cel.Variable(%s, …) names a constant this test could not resolve to a "+
					"string in %s; the parse is measuring less than it appears to", id.Name, path)
			}
		} else {
			t.Errorf("cel.Variable in %s takes a non-identifier first argument; this parse cannot "+
				"see it and would silently under-report the vocabulary", path)
		}
		return true
	})

	// A parse that finds nothing must fail loudly rather than vacuously pass every assertion
	// below. It has to find at least the identifiers that predate this test.
	require.GreaterOrEqualf(t, len(out), 6, "parsed only %v from %s — the instrument is broken, "+
		"not the prompt", out, path)
	for _, must := range []string{predicate.VarDevice, predicate.VarM, predicate.VarGeo} {
		require.Containsf(t, out, must, "parse of %s missed %q, which it certainly declares", path, must)
	}
	return out
}

func TestPromptIntroducesEveryDeclaredIdentifier(t *testing.T) {
	vocab := vocabularySection(t)
	for _, id := range declaredIdentifiers(t) {
		// Introduced, not merely mentioned. The prompt's own format glosses each identifier in
		// parentheses ("attr (map of device-attribute key to number)"), and requiring the gloss
		// is what keeps this from passing on an accident: a bare substring test for "m" matches
		// most English sentences in the section, so it would report green for a vocabulary line
		// that never named it.
		assert.Containsf(t, vocab, id+" (",
			"the CEL vocabulary section never introduces %q, which predicate.Env() declares. "+
				"A model told an identifier does not exist will not use it.", id)
	}
}

// TestPromptDocumentsTheContainmentFunction covers what the identifier list structurally
// cannot: `geo` is an opaque value whose entire usefulness is one function on it, so naming
// the variable without naming the call teaches the model nothing it can write.
func TestPromptDocumentsTheContainmentFunction(t *testing.T) {
	call := predicate.VarGeo + "." + predicate.FuncInFence
	assert.Contains(t, systemPromptBase, call,
		"the prompt names the geo binding but never shows the %s call, which is the only "+
			"operation it has", call)
}

// 🔴 THE PROMPT'S LEGALITY CLAIMS, CHECKED AGAINST THE COMPILER THAT DECIDES THEM.
//
// This is the assertion whose absence let the first version ship prose that told models to
// emit a construct `errFenceAndMetricLeaf` hard-refuses. A string test can confirm the
// prompt SAYS something; only compiling it can confirm the something is true. Both
// expressions below are copied from the prompt verbatim.
func TestPromptsLegalityClaimsMatchTheCompiler(t *testing.T) {
	compile := func(cel string) error {
		_, err := rules.Compile(rules.Rule{
			ID: "acme/prompt-claim", Name: "n", Type: rules.TypeThreshold,
			When: rules.Condition{CEL: cel},
		}, rules.DefaultLimits())
		return err
	}

	const forbidden = `geo.inFence("restricted") && m["tempC"] > 5.0`
	require.Contains(t, systemPromptBase, `geo.inFence("restricted") && m["tempC"] > 5`,
		"the prompt no longer shows the fence+measurement construct it warns against; if the "+
			"warning moved, move this test with it rather than deleting it")
	assert.Errorf(t, compile(forbidden),
		"the prompt tells models this construct is REJECTED, and it compiled. Either the "+
			"compiler stopped refusing it or the prompt is now wrong: %s", forbidden)

	const recommended = `geo.inFence("restricted") && attr["tempC"] > 5.0`
	require.Contains(t, systemPromptBase, `geo.inFence("restricted") && attr["tempC"] > 5`,
		"the prompt no longer recommends the attribute form")
	assert.NoErrorf(t, compile(recommended),
		"the prompt tells models to use this instead, and it does not compile — the prompt is "+
			"steering every geofence-plus-condition draft into a guaranteed rejection: %s", recommended)

	// And the plain containment case, so a compiler change that refused ALL fence rules could
	// not satisfy the assertion above by making everything fail.
	assert.NoError(t, compile(`geo.inFence("yard")`),
		"a bare containment test must still compile; without this the refusal assertion above "+
			"would pass just as well against a compiler that rejects every rule")
}

// TestGuardVocabularyRejectionListCoversEveryEventIdentifier checks the OTHER direction. The
// guard environment is a different, much smaller vocabulary, and the prompt warns which
// identifiers are rejected there. That warning is only useful while it is complete: an
// identifier declared for `when` but missing from the rejection list is exactly the one a
// model will try in a guard.
//
// 🔴 IT PARSES THE SENTENCE RATHER THAN SEARCHING IT, because searching it does not work. An
// earlier version asked `strings.Contains(line, id)` — and for `m` that is satisfied by the
// word "naming" in "A guard naming device, …". `m` is the single likeliest identifier for a
// model to reach for in a guard (`m["tempC"] > 80` is the archetype), so the check was blind
// in precisely its most important case, while reading as though it covered everything.
func TestGuardVocabularyRejectionListCoversEveryEventIdentifier(t *testing.T) {
	const marker = "A guard naming "
	i := strings.Index(systemPromptBase, marker)
	require.NotEqual(t, -1, i, "the guard rejection sentence is gone; this test is measuring nothing")
	line := systemPromptBase[i+len(marker):]
	if end := strings.Index(line, "\n"); end != -1 {
		line = line[:end]
	}
	line = strings.TrimSuffix(strings.TrimSpace(line), " is REJECTED.")

	named := map[string]bool{}
	for _, part := range strings.Split(strings.ReplaceAll(line, " or ", ", "), ",") {
		if w := strings.TrimSpace(part); w != "" {
			named[w] = true
		}
	}
	require.NotEmptyf(t, named, "parsed no identifiers out of the rejection sentence: %q", line)

	for _, id := range declaredIdentifiers(t) {
		assert.Truef(t, named[id],
			"the guard rejection sentence omits %q, which IS declared for \"when\" and is "+
				"therefore precisely what a model would reach for in a guard. Sentence named: %v", id, named)
	}
}

// vocabularySection returns the prompt text describing the `when.cel` vocabulary — from the
// heading down to the guard paragraph that begins the next section.
func vocabularySection(t *testing.T) string {
	t.Helper()
	const start = `CEL vocabulary inside a "when.cel"`
	const end = `An action "guard" is evaluated LATER`
	i := strings.Index(systemPromptBase, start)
	require.NotEqualf(t, -1, i, "the vocabulary heading %q is gone from the prompt; this test "+
		"would otherwise silently measure an empty string", start)
	rest := systemPromptBase[i:]
	j := strings.Index(rest, end)
	require.NotEqualf(t, -1, j, "the guard paragraph %q is gone from the prompt", end)
	return rest[:j]
}
