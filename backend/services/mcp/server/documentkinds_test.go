// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// 🔴 EVERY DOCUMENT A READ TOOL SENDS MUST BE EXACTLY ONE `query` OPERATION.
//
// What keeps this server read-only is the token it forwards, which carries no write
// authority. This ratchet is the second wall, on the DOCUMENT: a read tool whose document were
// edited into a `mutation` would still be refused downstream today, but a refusal that depends
// on a single wall is the shape the first write tool will arrive through. So the test reads the
// document itself.
//
// graphql-go's own parser is internal and cannot be imported, and the schema validation in
// documents_test.go does not say which KIND of operation a document holds. So this is a small
// lexer and a strict top-level reader: it understands exactly as much GraphQL as it needs to
// find the definitions of a document, and it FAILS CLOSED on anything it cannot read, including
// a header that is not made of the pieces GraphQL allows there (a name, variable definitions,
// directives), because a lax header is a place an operation can hide. An unreadable document is
// reported, never assumed to be a query.

type gqlToken struct {
	kind byte // 'n' name, 'p' punctuator, 's' string, 'd' number
	text string
}

var gqlNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// lexGraphQL splits a document into names, numbers, punctuators and strings, dropping
// whitespace, commas, a byte-order mark and comments. It returns an error for a string that
// never ends or holds an invalid escape, a malformed number, or a character GraphQL has no use
// for outside a string.
func lexGraphQL(src string) ([]gqlToken, error) {
	var out []gqlToken
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case strings.HasPrefix(src[i:], "\xef\xbb\xbf"):
			i += 3
		case c == '#':
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
		case strings.HasPrefix(src[i:], `"""`):
			// A block string ends at the first terminator not preceded by a backslash.
			j := i + 3
			for {
				k := strings.Index(src[j:], `"""`)
				if k < 0 {
					return nil, fmt.Errorf("unterminated block string")
				}
				if k > 0 && src[j+k-1] == '\\' {
					j += k + 3
					continue
				}
				j += k + 3
				break
			}
			out = append(out, gqlToken{'s', src[i:j]})
			i = j
		case c == '"':
			j := i + 1
			for {
				if j >= len(src) || src[j] == '\n' || src[j] == '\r' {
					return nil, fmt.Errorf("unterminated string")
				}
				if src[j] == '\\' {
					if j+1 >= len(src) || !strings.ContainsRune(`"\/bfnrtu`, rune(src[j+1])) {
						return nil, fmt.Errorf("invalid string escape")
					}
					j += 2
					continue
				}
				if src[j] == '"' {
					break
				}
				j++
			}
			out = append(out, gqlToken{'s', src[i : j+1]})
			i = j + 1
		case c == '.':
			if !strings.HasPrefix(src[i:], "...") {
				return nil, fmt.Errorf("stray '.'")
			}
			out = append(out, gqlToken{'p', "..."})
			i += 3
		case strings.ContainsRune("{}()[]:=!@$&|", rune(c)):
			out = append(out, gqlToken{'p', string(c)})
			i++
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i + 1
			for j < len(src) {
				d := src[j]
				if d == '_' || (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') || (d >= '0' && d <= '9') {
					j++
					continue
				}
				break
			}
			out = append(out, gqlToken{'n', src[i:j]})
			i = j
		case (c >= '0' && c <= '9') || c == '-':
			j := i + 1
			for j < len(src) {
				d := src[j]
				if (d >= '0' && d <= '9') || d == '.' || d == 'e' || d == 'E' || d == '-' || d == '+' {
					j++
					continue
				}
				break
			}
			if !gqlNumber.MatchString(src[i:j]) {
				return nil, fmt.Errorf("malformed number %q", src[i:j])
			}
			out = append(out, gqlToken{'d', src[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", string(c))
		}
	}
	return out, nil
}

var closerOf = map[string]string{"{": "}", "(": ")", "[": "]"}

// skipGroup returns the index just past the group opened at toks[i] (a '{', '(' or '['),
// checking that EVERY bracket inside it nests and closes correctly, and the number of tokens
// between the outer pair. A mismatch is an error, so a stray closer cannot end a group early
// and let what follows be read as a new definition.
func skipGroup(toks []gqlToken, i int) (next, inner int, err error) {
	var stack []string
	start := i
	for ; i < len(toks); i++ {
		if toks[i].kind != 'p' {
			continue
		}
		switch tx := toks[i].text; tx {
		case "{", "(", "[":
			stack = append(stack, closerOf[tx])
		case "}", ")", "]":
			if len(stack) == 0 || stack[len(stack)-1] != tx {
				return 0, 0, fmt.Errorf("mismatched %q", tx)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, i - start - 1, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("unbalanced %s", toks[start].text)
}

// skipSelection skips a non-empty selection set opened at toks[i].
func skipSelection(toks []gqlToken, i int) (int, error) {
	if i >= len(toks) || toks[i].kind != 'p' || toks[i].text != "{" {
		return 0, fmt.Errorf("expected a selection set")
	}
	next, inner, err := skipGroup(toks, i)
	if err != nil {
		return 0, err
	}
	if inner == 0 {
		return 0, fmt.Errorf("empty selection set")
	}
	return next, nil
}

// skipDirectives skips zero or more `@name` / `@name(...)` directives.
func skipDirectives(toks []gqlToken, i int) (int, error) {
	for i < len(toks) && toks[i].kind == 'p' && toks[i].text == "@" {
		i++
		if i >= len(toks) || toks[i].kind != 'n' {
			return 0, fmt.Errorf("a directive has no name")
		}
		i++
		if i < len(toks) && toks[i].kind == 'p' && toks[i].text == "(" {
			var err error
			if i, _, err = skipGroup(toks, i); err != nil {
				return 0, err
			}
		}
	}
	return i, nil
}

// documentOperations returns the kind of every operation definition in a document, in order:
// "query", "mutation" or "subscription". Fragments are definitions but not operations and are
// not listed. It decides the kind from the token that STARTS a definition, so a field, an
// operation name or a directive that happens to be spelled "mutation" is not mistaken for one,
// and it reads each header strictly:
//
//	operation: keyword Name? ( VariableDefinitions )? Directives? { selection }
//	fragment:  "fragment" Name "on" Name Directives? { selection }
func documentOperations(src string) ([]string, error) {
	toks, err := lexGraphQL(src)
	if err != nil {
		return nil, err
	}
	var ops []string
	for i := 0; i < len(toks); {
		t := toks[i]
		switch {
		case t.kind == 'p' && t.text == "{": // shorthand query
			ops = append(ops, "query")
			if i, err = skipSelection(toks, i); err != nil {
				return nil, err
			}
		case t.kind == 'n' && (t.text == "query" || t.text == "mutation" || t.text == "subscription"):
			ops = append(ops, t.text)
			i++
			if i < len(toks) && toks[i].kind == 'n' { // the operation's name
				i++
			}
			if i < len(toks) && toks[i].kind == 'p' && toks[i].text == "(" {
				if i, _, err = skipGroup(toks, i); err != nil {
					return nil, err
				}
			}
			if i, err = skipDirectives(toks, i); err != nil {
				return nil, err
			}
			if i, err = skipSelection(toks, i); err != nil {
				return nil, err
			}
		case t.kind == 'n' && t.text == "fragment":
			// fragment Name on Type Directives? { ... }
			if i+3 >= len(toks) || toks[i+1].kind != 'n' || toks[i+2].text != "on" || toks[i+3].kind != 'n' {
				return nil, fmt.Errorf("a fragment header is not `fragment Name on Type`")
			}
			i += 4
			if i, err = skipDirectives(toks, i); err != nil {
				return nil, err
			}
			if i, err = skipSelection(toks, i); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected %q where a definition should start", t.text)
		}
	}
	return ops, nil
}

// notExactlyOneQuery reports every document that is not exactly one query operation, with the
// reason. A document the lexer cannot read is reported too.
func notExactlyOneQuery(docs []document) []string {
	var bad []string
	for _, d := range docs {
		ops, err := documentOperations(d.text)
		name := operationName(d)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s (%s): unreadable: %v", name, d.area, err))
		case len(ops) != 1 || ops[0] != "query" && !(d.area == "device-management" && d.text == allowedProposalMutation && ops[0] == "mutation"):
			bad = append(bad, fmt.Sprintf("%s (%s): operations %v, want exactly [query]", name, d.area, ops))
		}
	}
	return bad
}

// Every document is exactly one query, except the explicitly pinned alarm proposal mutation.
func TestReadToolsIssueOnlyQueries(t *testing.T) {
	if len(documents) < 10 {
		t.Fatalf("only %d documents are registered; a ratchet over an empty registry passes anything", len(documents))
	}
	for _, msg := range notExactlyOneQuery(documents) {
		t.Errorf("a read tool's document is not a single query: %s", msg)
	}
}

// The negative control: the check is shown to FAIL. A mutation, a second operation, a
// subscription, a document it cannot read, an operation hiding in a malformed header, and the
// words that merely LOOK like operations in positions the naive "contains mutation" test would
// misjudge in both directions.
func TestTheDocumentKindRatchetCatchesWhatItIsFor(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantBad bool
	}{
		{"a plain query", `query Q($a: String!) { devices(x: $a) { token } }`, false},
		{"shorthand query", `{ devices { token } }`, false},
		{"a query with a fragment", `query Q { a { ...F } } fragment F on T { token }`, false},
		{"a mutation", `mutation M { deleteDevice(token: "x") { token } }`, true},
		{"a mutation hidden behind an alias on a query", `query Q { deleteDevice: devices { token } } mutation M { x }`, true},
		{"two queries", `query A { a } query B { b }`, true},
		{"a query then a mutation", `query A { a } mutation M { b }`, true},
		{"a subscription", `subscription S { events { token } }`, true},
		{"an unbalanced document", `query Q { a { b }`, true},
		{"an unterminated string", `query Q { a(x: "oops) { b } }`, true},
		{"an unexpected token at the top", `devices { token }`, true},
		{"empty", ``, true},
		{"an empty selection set", `query Q {}`, true},
		// A lax header is a place an operation can hide.
		{"a mutation swallowed by a query header", `query Q mutation M { x }`, true},
		{"a subscription swallowed by a query header", `query Q subscription S { x }`, true},
		{"a second query swallowed by the first header", `query A query B { x }`, true},
		{"a mutation swallowed by a fragment header", `query Q { a } fragment F on T mutation M { x }`, true},
		{"a second query swallowed by a fragment header", `query Q { a } fragment F on T query R { x }`, true},
		{"an unclosed object in a variable default", `query Q($x: I = {a: 1) { a }`, true},
		{"mismatched delimiters in a selection", `query Q { f(x: [1) }`, true},
		{"a stray closer ending a group early", `query Q { a ) } mutation M { x }`, true},
		{"an invalid string escape", `query Q { f(x: "\q") }`, true},
		{"an escaped literal newline", "query Q { f(x: \"a\\\nb\") }", true},
		{"a literal carriage return in a string", "query Q { f(x: \"a\rb\") }", true},
		{"a malformed number", `query Q { f(x: +1) }`, true},
		{"a fragment with no type condition", `query Q { a } fragment F { x }`, true},
		// False-positive guards: the word appears but is not an operation.
		{"mutation in a comment", "# mutation M { x }\nquery Q { a }", false},
		{"mutation in a string", `query Q { a(note: "mutation M { x }") { b } }`, false},
		{"mutation in a block string", "query Q { a(note: \"\"\"mutation M { x }\"\"\") { b } }", false},
		{"an operation NAMED mutation", `query mutation { a }`, false},
		{"a field named mutation", `query Q { mutation { token } }`, false},
		{"an object default value with braces", `query Q($c: In = {a: 1, b: {c: 2}}) { a(c: $c) }`, false},
		{"a directive with arguments", `query Q @live(x: {y: 1}) { a }`, false},
		{"a byte-order mark", "\xef\xbb\xbfquery Q { __typename }", false},
		{"numbers and escapes that are valid", `query Q { f(x: -1.5e+3, y: "a\n\u00e9", z: [1, 2]) { b } }`, false},
	}
	for _, c := range cases {
		bad := notExactlyOneQuery([]document{{area: "test", text: c.text}})
		if (len(bad) > 0) != c.wantBad {
			t.Errorf("%s: reported=%v (%v), want reported=%v", c.name, len(bad) > 0, bad, c.wantBad)
		}
	}
}

const allowedProposalMutation = `mutation ProposeAlarmAction($input: AgentAlarmProposalInput!) {
 proposeAlarmAction(input: $input) { status verdict requiredApprovals reasons digest expiresAt targetCount preview { alarmToken state acknowledged hidesLiveCondition } proposal { token kind state targets digest impact targetCount proposerPrincipal proposerClient clientAttested rationale policyVersion requiredApprovals approvals { principal approvedAt } reasons createdAt expiresAt decidedAt decidedBy note } }
}`

func TestProposalMutationExceptionIsExact(t *testing.T) {
	if bad := notExactlyOneQuery([]document{proposeAlarmActionMutation}); len(bad) != 0 {
		t.Fatal(bad)
	}
	for _, d := range []document{
		{area: "other-area", text: proposeAlarmActionMutation.text},
		{area: "device-management", text: proposeAlarmActionMutation.text + " mutation Execute { execute }"},
		{area: "device-management", text: "mutation ProposeAlarmAction { approveAgentProposal(token: \"p\", digest: \"d\") { token } }"},
	} {
		if len(notExactlyOneQuery([]document{d})) == 0 {
			t.Fatal("mutation exception admitted an unrelated document")
		}
	}
	count := 0
	for _, d := range documents {
		ops, err := documentOperations(d.text)
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) == 1 && ops[0] == "mutation" {
			count++
			if d != proposeAlarmActionMutation {
				t.Fatal("unexpected mutation")
			}
		}
	}
	if count != 1 {
		t.Fatalf("mutations=%d want 1", count)
	}
}
