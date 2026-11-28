// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"regexp"
	"strconv"
	"text/scanner"

	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// rootOperation is one operation of a document, as far as the work limit needs it: its
// type, its name, the defaults its variable definitions declare, and its selection tree.
type rootOperation struct {
	kind, name string
	roots      []rootSelection
	// varDefaults holds the default value of each variable definition that declares
	// one; the cost count reads a page size from it when the request supplies none.
	varDefaults map[string]any
}

// rootSelection is one selection in a selection set: a field (key is its response key,
// name the field it selects, args its arguments, sub its own selection set, nil for a
// leaf), an inline fragment (inline holds its selections, on its type condition, empty
// when it has none), or a fragment spread (spread is the fragment's name). The name
// says "root" for history: the root-field ceiling reads only the top level, while the
// cost count (cost.go) reads the whole tree.
type rootSelection struct {
	key    string
	name   string
	args   []argValue
	sub    []rootSelection
	inline []rootSelection
	on     string
	spread string
	// conds are the selection's @skip and @include conditions.
	conds []condition
}

// condition is one @skip(if:) or @include(if:) on a selection: skip says which, value is
// the if argument as written (a literal or a variable).
type condition struct {
	skip  bool
	value any
}

// fragmentDef is one fragment definition: its type condition and its selections.
type fragmentDef struct {
	on   string
	sels []rootSelection
}

// argValue is one argument, or one entry of an input object, as the document writes it.
//
// A value is one of: int64 (an Int literal), float64 (a Float literal), litString (a
// string or block string, as its token is written), litName (true, false,
// null or an enum value), varRef (a $variable), []any (a list) or []argValue (an input
// object, in document order, duplicates kept).
type argValue struct {
	name  string
	value any
}

type (
	litString string
	litName   string
	varRef    string
)

// readDocument is the ONE entry to the document reader: the length ceiling, then
// readRootFields. The work limit (checkWork) and the WebSocket's operation-type gate
// (operationType) both read through it, so neither can read a document the other
// would have read differently, and neither can read one before its length is checked.
func readDocument(query string, maxLen int) ([]rootOperation, map[string][]fragmentDef, *gqlerrors.QueryError) {
	if len(query) > maxLen {
		return nil, nil, gqlerrors.Errorf("query length %d exceeds the maximum allowed query length of %d bytes", len(query), maxLen)
	}
	ops, fragments, err := readRootFields(query)
	if err != nil {
		return nil, nil, gqlerrors.Errorf("the document could not be parsed: %s", err.Error())
	}
	return ops, fragments, nil
}

// selectOperation picks the operation graphql-go's getOperation would execute for
// name: with no name, the document must hold exactly one operation; with a name, the
// operation of that name, which an anonymous operation never matches.
//
// graphql-go picks the FIRST operation of a given name and relies on its validation to
// refuse a duplicate. This refuses a duplicate outright, which can only ever be the
// stricter answer. It is used only by the WebSocket gate, so over HTTP a duplicate
// name is still refused by graphql-go's validation and not here.
func selectOperation(ops []rootOperation, name string) (rootOperation, error) {
	if name == "" {
		if len(ops) != 1 {
			return rootOperation{}, fmt.Errorf("the document holds %d operations and no operation name was given", len(ops))
		}
		return ops[0], nil
	}
	found := -1
	for i, op := range ops {
		if op.name != name {
			continue
		}
		if found >= 0 {
			return rootOperation{}, fmt.Errorf("more than one operation is named %q", name)
		}
		found = i
	}
	if found < 0 {
		return rootOperation{}, fmt.Errorf("no operation is named %q", name)
	}
	return ops[found], nil
}

// readRootFields reads a document into its operations and its fragments' root-level
// selections, or fails. Callers enter through readDocument, which checks the length
// first.
//
// This mirrors graphql-go's query parser over its actual scanner mode, rather
// than a second specification parser: agreement about token boundaries is what
// makes both the root-field limit and the operation-type gate safe. graphql-go's
// Init resets text/scanner to GoTokens, which skips Go comments and reads raw
// strings and character literals as single tokens. opLexer refuses those tokens
// because the Unicode-normalising pass (internal/common/norm) has only code, #
// comment, string and block-string states and does not see them the same way.
//
// A non-empty string followed immediately by a quote is also refused: the upstream
// lexer opens a block string after ANY string token followed by a quote, while its
// normalising pass sees adjacent strings. Only the empty string plus a quote may
// open a block string here. Historically, a quote in a Go comment or adjacent
// strings let the normalising pass rewrite braces in what the scanner read as code.
//
// v1.11.1 fixes another disagreement: consumeTripleQuoteComment now honours the
// escaped triple quote, matching the normalising pass. opLexer deliberately keeps
// refusing that previously excluded lexeme rather than expanding the subset. The
// same release replaces unchecked literals after '-' with consumeNumber; literal
// and number below follow its stricter grammar, bounds and token-boundary checks.
//
// This reader does not copy Unicode normalisation itself. Once the excluded
// lexemes are gone, rewriting a string escape never moves a token boundary: it
// consumes and emits backslashes, u/U, hex digits and braces, never a quote.
// A braced Unicode escape is still refused by our scanner although upstream accepts
// it; a lone surrogate is accepted here but refused upstream; a surrogate pair is
// accepted by both. None can cause a differently classified operation to execute.
//
// Anything this cannot read is refused. FuzzRootFieldLimit and FuzzOperationType use
// graphql-go as the arbiter: a false refusal is safe, while reading a document as a
// different set of fields or operation type could let work past a transport gate.
func readRootFields(document string) (ops []rootOperation, fragments map[string][]fragmentDef, err error) {
	r := &rootReader{l: newOpLexer(document), fragments: map[string][]fragmentDef{}}
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
	fragments map[string][]fragmentDef
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
			on := r.ident()
			r.directives()
			r.fragments[name] = append(r.fragments[name], fragmentDef{on: on, sels: r.selectionSet()})
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
			if name, def, ok := r.inputValue(); ok {
				if op.varDefaults == nil {
					op.varDefaults = map[string]any{}
				}
				op.varDefaults[name] = def
			}
		}
		r.token(')')
	}
	r.directives()
	op.roots = r.selectionSet()
	return op
}

// inputValue is ParseInputValue. It returns the name and, when one is declared, the
// default value.
func (r *rootReader) inputValue() (name string, def any, hasDefault bool) {
	r.description()
	name = r.ident()
	r.token(':')
	r.typeRef()
	if r.l.tok == '=' {
		r.token('=')
		def, hasDefault = r.literal(true), true
	}
	r.directives()
	return name, def, hasDefault
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
				return rootSelection{spread: name, conds: r.directives()}
			}
			sel.on = r.ident()
		}
		sel.conds = r.directives()
		// selectionSet always holds at least one selection, so a non-nil inline is
		// what tells an inline fragment apart from a field.
		sel.inline = r.selectionSet()
		return sel
	}
	key := r.ident()
	name := key
	if r.l.tok == ':' {
		r.token(':')
		name = r.ident()
	}
	sel := rootSelection{key: key, name: name}
	if r.l.tok == '(' {
		sel.args = r.arguments()
	}
	sel.conds = r.directives()
	if r.l.tok == '{' {
		sel.sub = r.selectionSet()
	}
	return sel
}

// arguments is ParseArgumentList.
func (r *rootReader) arguments() []argValue {
	var args []argValue
	r.token('(')
	for r.l.tok != ')' {
		name := r.ident()
		r.token(':')
		args = append(args, argValue{name: name, value: r.literal(false)})
		r.directives()
	}
	r.token(')')
	return args
}

// directives is ParseDirectives.
//
// It returns the @skip and @include conditions among them, which the cost count honours
// as execution does; every other directive is read and discarded.
func (r *rootReader) directives() []condition {
	var conds []condition
	for r.l.tok == '@' {
		r.token('@')
		name := r.ident()
		var args []argValue
		if r.l.tok == '(' {
			args = r.arguments()
		}
		if name != "skip" && name != "include" {
			continue
		}
		for _, a := range args {
			if a.name == "if" {
				conds = append(conds, condition{skip: name == "skip", value: a.value})
			}
		}
	}
	return conds
}

// literal is ParseLiteral. It returns the value read, in argValue's representation.
func (r *rootReader) literal(constOnly bool) any {
	switch r.l.tok {
	case '$':
		if constOnly {
			r.fail("variable not allowed")
		}
		r.token('$')
		return varRef(r.ident())
	case scanner.Int, scanner.Float:
		return r.number(false)
	case scanner.String:
		text := r.l.text
		r.advance()
		return litString(text)
	case scanner.Ident:
		name := r.l.text
		r.advance()
		return litName(name)
	case '-':
		// v1.11.1 ParseLiteral calls consumeNumber(true) after '-', replacing
		// v1.11.0's unchecked ConsumeLiteral. Names and structural tokens must now
		// fail here too, even for Any; swallowing one would lose parser agreement.
		r.token('-')
		return r.number(true)
	case '[':
		list := []any{}
		r.token('[')
		for r.l.tok != ']' {
			list = append(list, r.literal(constOnly))
		}
		r.token(']')
		return list
	case '{':
		obj := []argValue{}
		r.token('{')
		for r.l.tok != '}' {
			name := r.ident()
			r.token(':')
			obj = append(obj, argValue{name: name, value: r.literal(constOnly)})
		}
		r.token('}')
		return obj
	default:
		r.fail("invalid value")
		return nil
	}
}

// v1.11.1's consumeNumber checks this grammar before parsing the signed text.
// text/scanner alone also accepts Go numbers (hex, underscores, leading zeroes,
// missing integer/fraction parts), so its token kind cannot establish agreement.
var opNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (r *rootReader) number(negative bool) any {
	if r.l.tok != scanner.Int && r.l.tok != scanner.Float {
		r.fail("expected number")
	}
	text := r.l.text
	if negative {
		text = "-" + text
	}
	if !opNumberPattern.MatchString(text) {
		r.fail("invalid numeric literal")
	}
	// Upstream consumeNumber also rejects int64/float64 overflow. Include the
	// sign before parsing so -9223372036854775808 remains valid, as upstream does.
	var (
		val any
		err error
	)
	if r.l.tok == scanner.Int {
		val, err = strconv.ParseInt(text, 10, 64)
	} else {
		val, err = strconv.ParseFloat(text, 64)
	}
	if err != nil {
		r.fail("numeric literal out of range")
	}
	// v1.11.1 ConsumeWhitespace rejects an ASCII name-start immediately after a
	// numeric token. Without this, [123abc] becomes two values in our reader.
	next := r.l.sc.Peek()
	if next == '_' || next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z' {
		r.fail("a number directly followed by a name is not accepted")
	}
	r.advance()
	return val
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
func countRootKeys(roots []rootSelection, fragments map[string][]fragmentDef) int {
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
					walk(def.sels)
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
