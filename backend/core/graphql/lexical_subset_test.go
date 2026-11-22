// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"text/scanner"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusedLexemes hold scanner/normaliser disagreements or historically excluded
// escapes whose refusal we retain after v1.11.1 fixed upstream's block strings.
// Each is refused by the document reader, whatever it counts. reason
// is a fragment of the refusal's message.
//
// The limit these run at (5) is above every row's field count, so a refusal here is
// the lexeme's, never the count's.
var refusedLexemes = []struct{ name, doc, reason string }{
	{"Go line comment", "mutation { a: bump // x\n }", "comment is not accepted"},
	{"Go block comment", "mutation { a: bump /* x */ }", "comment is not accepted"},
	{"unterminated Go block comment", "mutation { a: bump } /* x", "comment not terminated"},
	{"backquoted raw string", "mutation { a: m(s: `x`) }", "backquoted string is not accepted"},
	{"raw string after a minus", "mutation { a: bump(a: -`x`) }", "backquoted string is not accepted"},
	{"single-quoted character", "mutation { a: m(s: 'x') }", "single-quoted character is not accepted"},
	// v1.11.1 honours `\"""` in a block string. Keep our established refusal even
	// though the upstream lexer and normalising pass now agree on this escape.
	{"escaped closing triple quote", `mutation { a: m(s: """x\""") }`, `closing """ follows a backslash`},
	// Upstream's new suffix check ignores backslash parity, just like its
	// normalising pass and our existing refusal.
	{"escaped backslash before the closing triple quote", `mutation { a: m(s: """x\\""") }`, `closing """ follows a backslash`},
	{"escaped triple quote and one more", `mutation { a: m(s: """x\"""") }`, `closing """ follows a backslash`},
	// The documents that showed the disagreement is real: the Go comment holding a
	// quote puts a normalising pass that does not know Go comments inside a "string",
	// where it rewrites escapes that graphql-go's lexer reads as bare tokens.
	{"comment quote with a lone surrogate", `/* " */ mutation { a: bump(s: [-\uDC00]) }`, "comment is not accepted"},
	{"comment quote with an overlong braced escape", `/* " */ mutation { a: bump(s: [-\u{aaaaaaaaa]) }`, "comment is not accepted"},
	{"comment quote with a braced escape", `/* " */ mutation { a: bump(a: -\u{41}) b: bump }`, "comment is not accepted"},
	// A non-empty string directly followed by a quote: graphql-go's lexer opens a block
	// string there, its normalising pass reads a string and then a new one.
	{"string then a block string", `mutation { a: m(s: "x""" b: bump """) }`, "directly followed by a quote"},
	{"string then a string", `mutation { a: m(s: "x""y") }`, "directly followed by a quote"},
	{"description string then a quote", `"d""" x """ mutation { a: bump }`, "directly followed by a quote"},
	// The document that showed it: the `#` comment's quotes leave the lexer in code
	// and the normalising pass in a string after the newline.
	{"string then a quote with a comment and a lone surrogate",
		"mutation { z: bump(s: \"x\"\"a\" \"\"\") # \"\"\" \"\n a: bump(s: [-\\uDC00]) b: bump }",
		"directly followed by a quote"},
}

// lexemeFuzzSeeds seed both fuzzers with the class the refused lexemes were found in:
// the documents that showed the disagreement, their nearest neighbours without the
// Go comment (which the mutator reaches first), and strings the normalising pass
// rewrites, with and without a `#` comment holding a quote in front of them.
var lexemeFuzzSeeds = []string{
	`/* " */ mutation { a: bump(s: [-\uDC00]) }`,
	`/* " */ mutation { a: bump(s: [-\u{aaaaaaaaa]) }`,
	`/* " */ mutation { a: bump(a: -\u{41}) b: bump }`,
	`mutation { a: bump(s: [-\uDC00]) }`,
	`mutation { a: bump(s: [-\u{aaaaaaaaa]) }`,
	`mutation { a: bump(a: -\u{41}) b: bump }`,
	`mutation { a: bump(a: -"\uDC00") b: bump }`,
	`mutation { a: bump(l: [-"\u{41}", -{]) b: bump }`,
	`mutation { a: bump(a: "😀") b: bump c: bump }`,
	"# \"\n mutation { a: bump(a: \"\\uDC00\") b: bump }",
	"mutation { z: bump(s: \"x\"\"a\" \"\"\") # \"\"\" \"\n a: bump(s: [-\\uDC00]) b: bump }",
	"mutation { z: bump(s: \"x\" \"a\" \"\"\") # \"\"\" \"\n a: bump(s: [-\\uDC00]) b: bump }",
}

// The reader pins the lexical subset's refusals, including the historical escaped
// block-quote exclusion, before counting or running: the work limit refuses the
// document with the lexeme's reason, and no resolver runs.
func TestRefusedLexemes(t *testing.T) {
	schema := lexSchema(5)
	for _, tc := range refusedLexemes {
		t.Run(tc.name, func(t *testing.T) {
			qerr := checkWork(tc.doc, DefaultGraphQLMaxQueryLength, 5, 5)
			require.NotNil(t, qerr, "the work limit passed %q", tc.doc)
			assert.Contains(t, qerr.Message, tc.reason)
			assert.Nil(t, qerr.Extensions, "a lexeme refusal is a syntax error, not a count")

			resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
				return schema.Exec(ctx, tc.doc, "", nil)
			})
			require.Len(t, resp.Errors, 1, "%v", resp.Errors)
			assert.Contains(t, resp.Errors[0].Message, tc.reason)
			assert.Nil(t, resp.Data)
			assert.Equal(t, int32(0), calls, "no resolver may run for a refused document")
		})
	}
}

// The counterweight: the refusal is aware of where it is. The same characters inside
// a string, a block string, a `#` comment or a description are not lexemes at all,
// and each of these documents runs its one field at a limit of 1.
func TestRefusedLexemesAreStillAcceptedInsideStringsAndComments(t *testing.T) {
	schema := lexSchema(1)
	for _, doc := range []string{
		"mutation { a: m(s: \"// /* ' ` \\\\\") }",
		"mutation { a: m(s: \"\"\" // /* ' ` \\\\ \"\"\") }",
		"# // /* ' ` \\\"\"\"\n mutation { a: bump }",
		`"""it's // a description""" mutation { a: bump }`,
		`mutation { a: m(s: """a\"b \\ c""") }`,
		`mutation { a: m(s: """""") }`,
		// The only string a quote may directly follow is `""`, which with it is `"""`.
		`mutation { a: m(s: "") }`,
		`mutation { a: m(s: """x""") }`,
		`mutation { a: m(s: """"x""") }`,
		`"" mutation { a: bump }`,
	} {
		resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
			return schema.Exec(ctx, doc, "", nil)
		})
		require.Empty(t, resp.Errors, "%s", doc)
		assert.Equal(t, int32(1), calls, "%s", doc)
	}
}

// v1.11.1 rejects a non-number after '-' before Any scalar validation. Pin that
// change in both the upstream arbiter and our guard, rather than quietly losing the
// old fuzz reach when the parser becomes stricter.
func TestFuzzArbiterRefusesTheMinusAnyPath(t *testing.T) {
	const doc = `mutation { a: bump(a: -x) b: bump(l: [-y, -z]) }`
	resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
		return lexInner().Exec(ctx, doc, "", nil)
	})
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "syntax error: expected number")
	assert.Zero(t, calls)
	assert.Nil(t, resp.Data)
	qerr := checkWork(doc, DefaultGraphQLMaxQueryLength, 2, 2)
	require.NotNil(t, qerr)
	assert.Contains(t, qerr.Message, "expected number")
	assert.Nil(t, qerr.Extensions, "the refusal is lexical, not a field-count limit")
}

// 🔴 A REFUSED LEXEME READS AS THE END OF THE DOCUMENT, at the lexer itself and not
// only through the reader's error check: a loop over next() that skipped the check
// still stops there. Every refusal next() makes is covered, and the token after it
// is never produced.
func TestOpLexerReadsARefusalAsTheEnd(t *testing.T) {
	for _, doc := range []string{
		"a // x\n b",
		"a /* x */ b",
		"a `x` b",
		"a 'x' b",
		`a "x""y" b`,
	} {
		l := newOpLexer(doc)
		require.Equal(t, "a", l.text, "%q", doc)
		require.NoError(t, l.err, "%q", doc)
		l.next()
		assert.Error(t, l.err, "%q", doc)
		assert.Equal(t, rune(scanner.EOF), l.tok, "%q: the refused lexeme must read as the end", doc)
		assert.Empty(t, l.text, "%q", doc)
	}
}
