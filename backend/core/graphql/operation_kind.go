// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"fmt"
	"strings"
	"text/scanner"
)

// GraphQL operation types, as the keywords that introduce them.
const (
	opQuery        = "query"
	opMutation     = "mutation"
	opSubscription = "subscription"
)

// operationType reports the type of the operation graphql-go would EXECUTE for
// document and operationName: "query", "mutation" or "subscription". Anything it
// cannot classify with certainty is an error, and the caller refuses the operation.
//
// It exists so the WebSocket transport can refuse every operation but a subscription
// BEFORE the document reaches Schema.Subscribe. That call runs a query or mutation to
// completion inside itself, so there is no later point at which to refuse one.
//
// 🔴 WHY A SECOND READER OF THE DOCUMENT, AND WHAT IT HAS TO AGREE WITH. graphql-go
// exposes no parsed operation type: its parser is internal, and neither its Tracer nor
// its AST accessors can stop an execution. Two alternatives were weighed and not taken:
//
//   - a second full GraphQL parser (vektah/gqlparser) is a much larger reader that
//     could disagree in many more places, and it would add a require to every module;
//   - a change to the devicechain-io graphql-go fork (an exported OperationType, or a
//     Subscribe that refuses non-subscription operations) would let graphql-go decide
//     for itself, so nothing could disagree. But the fork is meant to carry exactly the
//     one commit it has upstream-proposed, and to be DROPPED when upstream releases
//     that fix; a second, fork-only commit would outlive that exit. That trade is a
//     standing decision to revisit, not a settled one.
//
// So this reader only has to find the top-level structure (which definitions exist,
// their types and their names), and it tokenises EXACTLY as graphql-go's own lexer
// does, so that "where does this definition end" has the same answer in both.
// FuzzOperationType holds it to that, with graphql-go itself as the arbiter: whenever
// this function says "subscription", graphql-go must not execute a query or mutation.
//
// The one direction that matters is that one. A document this function refuses that
// graphql-go would have accepted is a false refusal, not a hole — and it is the
// designed outcome for the few things graphql-go reads through a normalising pass
// this function does not repeat (the GraphQL-only `\u{…}` and surrogate-pair string
// escapes): text/scanner reports those as errors, and an error here is a refusal.
func operationType(document, operationName string) (string, error) {
	l := newOpLexer(document)

	type operation struct{ kind, name string }
	var ops []operation

	for l.tok != scanner.EOF {
		// An optional description string, which graphql-go accepts before a full
		// operation or a fragment. It rejects one before the `{ … }` shorthand; this
		// refuses that too, just less precisely (see the case below).
		described := false
		if l.tok == scanner.String {
			described = true
			l.next()
		}

		switch {
		case l.tok == '{':
			if described {
				return "", errors.New("a description is not allowed on a shorthand query")
			}
			// The `{ … }` shorthand: an anonymous query.
			if err := l.skipDefinition(); err != nil {
				return "", err
			}
			ops = append(ops, operation{kind: opQuery})
		case l.tok == scanner.Ident:
			keyword := l.text
			l.next()
			switch keyword {
			case opQuery, opMutation, opSubscription:
				// graphql-go takes the name only when the very next token is an identifier.
				name := ""
				if l.tok == scanner.Ident {
					name = l.text
					l.next()
				}
				if err := l.skipDefinition(); err != nil {
					return "", err
				}
				ops = append(ops, operation{kind: keyword, name: name})
			case "fragment":
				if err := l.skipDefinition(); err != nil {
					return "", err
				}
			default:
				return "", fmt.Errorf("unexpected %q at the top level of the document", keyword)
			}
		default:
			return "", fmt.Errorf("unexpected %s at the top level of the document", scanner.TokenString(l.tok))
		}
		if l.err != nil {
			return "", l.err
		}
	}
	if l.err != nil {
		return "", l.err
	}

	// Selection, as graphql-go's getOperation makes it. graphql-go picks the FIRST
	// operation of a given name and relies on validation to reject duplicates; this
	// refuses a duplicate outright, which can only ever be the stricter answer.
	if operationName == "" {
		if len(ops) != 1 {
			return "", fmt.Errorf("the document holds %d operations and no operation name was given", len(ops))
		}
		return ops[0].kind, nil
	}
	found := -1
	for i, op := range ops {
		if op.name != operationName {
			continue
		}
		if found >= 0 {
			return "", fmt.Errorf("more than one operation is named %q", operationName)
		}
		found = i
	}
	if found < 0 {
		return "", fmt.Errorf("no operation is named %q", operationName)
	}
	return ops[found].kind, nil
}

// opLexer tokenises a document the way graphql-go's lexer does
// (internal/common/lexer.go in graphql-go), keeping only what the top-level walk needs.
type opLexer struct {
	sc   scanner.Scanner
	tok  rune
	text string
	err  error
}

func newOpLexer(document string) *opLexer {
	l := &opLexer{}
	// 🔴 Init IS CALLED, AND THE MODE IT SETS IS LEFT ALONE, BECAUSE THAT IS WHAT
	// graphql-go ACTUALLY RUNS WITH. Its lexer writes a narrower Mode into the struct
	// literal and then calls Init, which resets Mode to scanner.GoTokens (and
	// Whitespace to GoWhitespace). So graphql-go really does skip Go-style `//` and
	// `/* */` comments and read `'c'` and backquoted raw strings as single tokens.
	// Copying the Mode it WRITES rather than the one it RUNS would make this reader
	// see `{`s inside a `/* … */` comment that graphql-go never sees.
	l.sc.Init(strings.NewReader(document))
	l.sc.Error = func(_ *scanner.Scanner, msg string) {
		// graphql-go turns the first scanner error into a syntax error; so does this.
		if l.err == nil {
			l.err = errors.New("syntax error: " + msg)
		}
	}
	l.next()
	return l
}

// next advances to the next significant token, mirroring graphql-go's
// ConsumeWhitespace: commas are insignificant, and `#` starts a comment that runs to
// the end of the line. A string token immediately followed by `"` opens a block
// string, which graphql-go reads rune by rune (consumeTripleQuoteComment); it is
// consumed here the same way and reported as a single string token.
func (l *opLexer) next() {
	for {
		tok := l.sc.Scan()
		switch tok {
		case ',':
			continue
		case '#':
			for {
				r := l.sc.Next()
				if r == '\r' || r == '\n' || r == scanner.EOF {
					break
				}
			}
			continue
		case scanner.String:
			if l.sc.Peek() == '"' {
				l.skipBlockString()
			}
		}
		l.tok, l.text = tok, l.sc.TokenText()
		return
	}
}

// skipBlockString consumes the rest of a block string the way graphql-go does: the
// third opening quote, then every rune up to and including the first run of three
// consecutive quotes. graphql-go does NOT honour `\"""` as an escape here — the
// backslash resets its quote count and the three quotes after it still close the
// string — so neither does this. An unterminated block string is an error, where
// graphql-go reads to EOF and fails later; either way nothing executes.
func (l *opLexer) skipBlockString() {
	l.sc.Next() // the third opening quote
	quotes := 0
	for {
		r := l.sc.Next()
		switch r {
		case scanner.EOF:
			if l.err == nil {
				l.err = errors.New("syntax error: unterminated block string")
			}
			return
		case '"':
			quotes++
			if quotes == 3 {
				return
			}
		default:
			quotes = 0
		}
	}
}

// skipDefinition consumes the rest of one top-level definition: everything up to and
// including the close of its first `{` at depth zero, which is its selection set.
// Parentheses and brackets (variable definitions, directive arguments, default
// values) are tracked so a `{` inside them is not mistaken for the selection set.
func (l *opLexer) skipDefinition() error {
	var closers []rune
	for {
		if l.err != nil {
			return l.err
		}
		switch l.tok {
		case scanner.EOF:
			return errors.New("syntax error: unexpected end of document")
		case '{':
			closers = append(closers, '}')
		case '(':
			closers = append(closers, ')')
		case '[':
			closers = append(closers, ']')
		case '}', ')', ']':
			if len(closers) == 0 || closers[len(closers)-1] != l.tok {
				return fmt.Errorf("syntax error: unbalanced %q", l.tok)
			}
			closers = closers[:len(closers)-1]
			if len(closers) == 0 && l.tok == '}' {
				l.next()
				return l.err
			}
		}
		l.next()
	}
}
