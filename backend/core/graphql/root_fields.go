// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"text/scanner"
)

// rootOperation is one operation of a document, as far as the work limit needs it: its
// type, its name, and the selections directly under its root.
type rootOperation struct {
	kind, name string
	roots      []rootSelection
}

// rootSelection is one selection in a root-level selection set: a field (key is its
// response key), an inline fragment (inline holds its selections), or a fragment spread
// (spread is the fragment's name). Anything below the root is read and discarded.
type rootSelection struct {
	key    string
	inline []rootSelection
	spread string
}

// readRootFields reads a document into its operations and its fragments' root-level
// selections, or fails.
//
// 🔴 IT IS A MIRROR OF graphql-go's OWN QUERY PARSER, OVER A LEXER THAT TOKENISES AS
// graphql-go's DOES — not a GraphQL parser. The work limit is only worth anything if
// the fields it counts are the fields graphql-go goes on to execute, and two readers of
// the same text agree only where they tokenise it the same way. A spec-conformant parser
// does not: graphql-go's lexer is Go's text/scanner in its default mode, so it skips
// `//` and `/* */` comments, reads backquoted raw strings and `'c'` literals as single
// tokens, and closes a block string at the first `"""` even after a backslash. A document
// written to be read one way by a conformant parser and another way by graphql-go —
// three mutations that a conformant parser reads as one field holding a block string,
// say — would be counted as one and executed as three. So this reuses opLexer, whose
// doc comment records why it tokenises exactly as graphql-go runs, and each function
// below is the graphql-go function of the same shape (internal/query/query.go and
// internal/common/{values,literals,types,directive}.go), consuming the same tokens in
// the same order and failing where it fails.
//
// Anything it cannot read, it refuses, and the caller refuses the document. That is the
// safe direction: a document refused here that graphql-go would have run is a false
// refusal; a document read here differently from graphql-go is a hole. The one place the
// two can legitimately differ is graphql-go's Unicode-escape normalisation of string
// literals (`\u{…}`, surrogate pairs), which this does not repeat. That rewrite starts
// at a backslash and never introduces or removes a quote, backquote, comment delimiter
// or line break, so inside a string, raw string or comment it cannot move a token
// boundary; outside them the backslash is a token no GraphQL production accepts, and
// graphql-go refuses the whole document. FuzzRootFieldLimit holds the pair to the only
// property that matters, with graphql-go as the arbiter.
func readRootFields(document string) (ops []rootOperation, fragments map[string][][]rootSelection, err error) {
	r := &rootReader{l: newOpLexer(document), fragments: map[string][][]rootSelection{}}
	defer func() {
		if p := recover(); p != nil {
			perr, ok := p.(rootReadError)
			if !ok {
				panic(p)
			}
			ops, fragments, err = nil, nil, perr
		}
	}()
	r.check()
	r.document()
	return r.ops, r.fragments, nil
}

// rootReadError is how the reader abandons a document, as graphql-go's SyntaxError does.
type rootReadError struct{ error }

type rootReader struct {
	l         *opLexer
	ops       []rootOperation
	fragments map[string][][]rootSelection
}

// check abandons the read on a scanner error, which graphql-go turns into a syntax error
// on the spot.
func (r *rootReader) check() {
	if r.l.err != nil {
		panic(rootReadError{r.l.err})
	}
}

func (r *rootReader) fail(format string, args ...any) {
	panic(rootReadError{fmt.Errorf("syntax error: "+format, args...)})
}

// advance is graphql-go's ConsumeWhitespace: the next significant token.
func (r *rootReader) advance() {
	r.l.next()
	r.check()
}

// token is ConsumeToken.
func (r *rootReader) token(expected rune) {
	if r.l.tok != expected {
		r.fail("unexpected %q, expecting %s", r.l.text, scanner.TokenString(expected))
	}
	r.advance()
}

// ident is ConsumeIdent.
func (r *rootReader) ident() string {
	name := r.l.text
	r.token(scanner.Ident)
	return name
}

// description is DescString (and, since graphql-go builds its query lexer with string
// descriptions on, DescComment): one optional string token. opLexer has already folded
// a block string into that one token.
func (r *rootReader) description() {
	if r.l.tok == scanner.String {
		r.advance()
	}
}

// document is parseExecutableDefinition.
func (r *rootReader) document() {
	for r.l.tok != scanner.EOF {
		described := r.l.tok == scanner.String
		r.description()
		if r.l.tok == '{' {
			if described {
				// graphql-go refuses a NON-EMPTY description here; refusing any is
				// stricter, and costs nothing, since no client describes a shorthand.
				r.fail("a description is not allowed on a shorthand query")
			}
			r.ops = append(r.ops, rootOperation{kind: opQuery, roots: r.selectionSet()})
			continue
		}
		switch keyword := r.ident(); keyword {
		case opQuery, opMutation, opSubscription:
			r.ops = append(r.ops, r.operation(keyword))
		case "fragment":
			name := r.ident()
			if r.l.tok != scanner.Ident || r.l.text != "on" {
				r.fail("unexpected %q, expecting %q", r.l.text, "on")
			}
			r.advance()
			r.ident()
			r.directives()
			r.fragments[name] = append(r.fragments[name], r.selectionSet())
		default:
			r.fail("unexpected %q, expecting %q", keyword, "fragment")
		}
	}
}

// operation is parseOperation, after its keyword.
func (r *rootReader) operation(kind string) rootOperation {
	op := rootOperation{kind: kind}
	if r.l.tok == scanner.Ident {
		op.name = r.ident()
	}
	if r.l.tok == '(' {
		r.token('(')
		for r.l.tok != ')' {
			r.description()
			r.token('$')
			r.inputValue()
		}
		r.token(')')
	}
	r.directives()
	op.roots = r.selectionSet()
	return op
}

// inputValue is ParseInputValue.
func (r *rootReader) inputValue() {
	r.description()
	r.ident()
	r.token(':')
	r.typeRef()
	if r.l.tok == '=' {
		r.token('=')
		r.literal(true)
	}
	r.directives()
}

// typeRef is ParseType.
func (r *rootReader) typeRef() {
	if r.l.tok == '[' {
		r.token('[')
		r.typeRef()
		r.token(']')
	} else {
		r.ident()
	}
	if r.l.tok == '!' {
		r.token('!')
	}
}

// selectionSet is parseSelectionSet: a `{`, at least one selection, a `}`.
func (r *rootReader) selectionSet() []rootSelection {
	var sels []rootSelection
	r.token('{')
	sels = append(sels, r.selection())
	for r.l.tok != '}' {
		sels = append(sels, r.selection())
	}
	r.token('}')
	return sels
}

// selection is parseSelection, parseFieldDef and parseSpread.
func (r *rootReader) selection() rootSelection {
	if r.l.tok == '.' {
		r.token('.')
		r.token('.')
		r.token('.')
		var sel rootSelection
		if r.l.tok == scanner.Ident {
			if name := r.ident(); name != "on" {
				r.directives()
				return rootSelection{spread: name}
			}
			r.ident()
		}
		r.directives()
		// selectionSet always holds at least one selection, so a non-nil inline is
		// what tells an inline fragment apart from a field.
		sel.inline = r.selectionSet()
		return sel
	}
	key := r.ident()
	if r.l.tok == ':' {
		r.token(':')
		r.ident()
	}
	if r.l.tok == '(' {
		r.arguments()
	}
	r.directives()
	if r.l.tok == '{' {
		r.selectionSet()
	}
	return rootSelection{key: key}
}

// arguments is ParseArgumentList.
func (r *rootReader) arguments() {
	r.token('(')
	for r.l.tok != ')' {
		r.ident()
		r.token(':')
		r.literal(false)
		r.directives()
	}
	r.token(')')
}

// directives is ParseDirectives.
func (r *rootReader) directives() {
	for r.l.tok == '@' {
		r.token('@')
		r.ident()
		if r.l.tok == '(' {
			r.arguments()
		}
	}
}

// literal is ParseLiteral.
func (r *rootReader) literal(constOnly bool) {
	switch r.l.tok {
	case '$':
		if constOnly {
			r.fail("variable not allowed")
		}
		r.token('$')
		r.ident()
	case scanner.Int, scanner.Float, scanner.String, scanner.Ident:
		r.advance()
	case '-':
		// 🔴 graphql-go's ConsumeLiteral after a minus takes WHATEVER token comes next
		// — a brace, a bracket, a parenthesis — without checking its type. Reading a
		// structural token here as a structure would put this reader out of step with
		// graphql-go for the rest of the document, so it is consumed as one token too.
		r.token('-')
		if r.l.tok == scanner.EOF {
			r.fail("unexpected end of document")
		}
		r.advance()
	case '[':
		r.token('[')
		for r.l.tok != ']' {
			r.literal(constOnly)
		}
		r.token(']')
	case '{':
		r.token('{')
		for r.l.tok != '}' {
			r.ident()
			r.token(':')
			r.literal(constOnly)
		}
		r.token('}')
	default:
		r.fail("invalid value")
	}
}

// countRootKeys counts the DISTINCT response keys an operation's root selection set
// produces — which is exactly what graphql-go executes: its field collection merges
// fields sharing a response key (the alias, or the field name when there is none) into
// one resolver call, and expands inline fragments and fragment spreads into the set.
//
// So a key repeated ten times is ONE field, and aliases hidden behind a fragment are
// counted as though written inline. @skip and @include are NOT evaluated, and neither
// is a fragment's type condition: a field that would not run is counted as though it
// did. That over-counts, which is the safe direction, and costs a legitimate client
// nothing, because none sends more root fields than the limit even unconditionally.
// A fragment name defined more than once contributes every definition's keys, which
// again can only over-count.
//
// A fragment is expanded at most once per operation. A fragment spread twice
// contributes the same keys twice, which the distinct count absorbs, and the visited
// set is also what stops a cyclic spread — invalid, but not yet validated here — from
// recursing forever.
func countRootKeys(roots []rootSelection, fragments map[string][][]rootSelection) int {
	keys := map[string]struct{}{}
	visited := map[string]bool{}
	var walk func([]rootSelection)
	walk = func(sels []rootSelection) {
		for _, s := range sels {
			switch {
			case s.spread != "":
				if visited[s.spread] {
					continue
				}
				visited[s.spread] = true
				for _, def := range fragments[s.spread] {
					walk(def)
				}
			case s.inline != nil:
				walk(s.inline)
			default:
				keys[s.key] = struct{}{}
			}
		}
	}
	walk(roots)
	return len(keys)
}
