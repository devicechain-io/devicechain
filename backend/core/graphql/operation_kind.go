// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"strings"
	"text/scanner"

	gqlerrors "github.com/graph-gophers/graphql-go/errors"
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
// It reads the document with readDocument, the same reader the work limit counts with
// and entered the same way, so the gate and the limit cannot disagree about what the
// document holds. The length ceiling therefore comes first here too, for the reason
// checkWork gives: on the WebSocket this is the first thing to read a frame, and a
// frame may be far larger than the ceiling.
//
// 🔴 WHY A READER OF OUR OWN, AND WHAT IT HAS TO AGREE WITH. graphql-go exposes no
// parsed operation type: its parser is internal, and neither its Tracer nor its AST
// accessors can stop an execution. Two alternatives were weighed and not taken:
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
// FuzzOperationType holds it to the one direction that matters, with graphql-go itself
// as the arbiter: whenever this function says "subscription", graphql-go must not
// execute a query or mutation. A document this function refuses that graphql-go would
// have accepted is a false refusal, not a hole; readRootFields lists the ones that are
// refused by design.
func operationType(document, operationName string, maxLen int) (string, *gqlerrors.QueryError) {
	ops, _, qerr := readDocument(document, maxLen)
	if qerr != nil {
		return "", qerr
	}
	op, err := selectOperation(ops, operationName)
	if err != nil {
		return "", gqlerrors.Errorf("%s", err.Error())
	}
	return op.kind, nil
}

// opLexer tokenises a document the way graphql-go's lexer does
// (internal/common/lexer.go in graphql-go), and REFUSES the lexemes at which
// graphql-go's own two layers can disagree (see readRootFields). There is one reader
// over it, readRootFields, with two callers that both enter through readDocument: the
// work limit, and the WebSocket's operation-type gate.
type opLexer struct {
	sc   scanner.Scanner
	tok  rune
	text string
	err  error
}

// refusedLexemeReasons are the tokens text/scanner reads in the mode graphql-go runs it
// in but graphql-go's Unicode-normalising pass does not know about, each with the
// reason it is refused.
var refusedLexemeReasons = map[rune]string{
	scanner.Comment:   "a // or /* */ comment is not accepted; use #",
	scanner.RawString: "a backquoted string is not accepted",
	scanner.Char:      "a single-quoted character is not accepted",
}

func newOpLexer(document string) *opLexer {
	l := &opLexer{}
	// 🔴 Init IS CALLED, AND THE MODE IT SETS IS KEPT, BECAUSE THAT IS WHAT graphql-go
	// ACTUALLY RUNS WITH. Its lexer writes a narrower Mode into the struct literal and
	// then calls Init, which resets Mode to scanner.GoTokens (and Whitespace to
	// GoWhitespace). So graphql-go really does skip Go-style `//` and `/* */` comments
	// and read `'c'` and backquoted raw strings as single tokens, and this scans them
	// identically. The one change is that comments are RETURNED rather than skipped, so
	// that next can refuse them along with the other two.
	l.sc.Init(strings.NewReader(document))
	l.sc.Mode &^= scanner.SkipComments
	l.sc.Error = func(_ *scanner.Scanner, msg string) {
		// graphql-go turns the first scanner error into a syntax error; so does this.
		l.refuse(msg)
	}
	l.next()
	return l
}

// refuse records the first error; the reader abandons the document on it.
func (l *opLexer) refuse(msg string) {
	if l.err == nil {
		l.err = errors.New("syntax error: " + msg)
	}
}

// stringThenQuoteReason refuses a NON-EMPTY string directly followed by a quote.
// graphql-go opens a block string after ANY string token followed by `"` (its
// ConsumeLiteral and consumeDescription check only the token kind), so it reads
// `"x"""…` as a block string, while its normalising pass reads the string `"x"` and
// then a new string. The specification reads two adjacent strings, which no parser
// accepts where one value is expected, so refusing it costs a conformant document
// nothing. Only `""` followed by `"` is the opening `"""` of a block string.
const stringThenQuoteReason = `a string directly followed by a quote is not accepted; only """ opens a block string`

// next advances to the next significant token, mirroring graphql-go's
// ConsumeWhitespace: commas are insignificant, and `#` starts a comment that runs to
// the end of the line. The string `""` immediately followed by `"` opens a block
// string, which graphql-go reads rune by rune (consumeTripleQuoteComment); it is
// consumed here the same way and reported as a single string token. Any other string
// followed by `"` is refused (stringThenQuoteReason). A refused lexeme records the
// error and reads as the end of the document, so no loop can run past it even if a
// caller skipped the error check (TestOpLexerReadsARefusalAsTheEnd pins that).
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
		case scanner.Comment, scanner.RawString, scanner.Char:
			l.refuse(refusedLexemeReasons[tok])
			l.tok, l.text = scanner.EOF, ""
			return
		case scanner.String:
			if l.sc.Peek() == '"' {
				if l.sc.TokenText() != `""` {
					l.refuse(stringThenQuoteReason)
					l.tok, l.text = scanner.EOF, ""
					return
				}
				l.skipBlockString()
			}
		}
		l.tok, l.text = tok, l.sc.TokenText()
		return
	}
}

// skipBlockString consumes the rest of a block string the way graphql-go does: the
// third opening quote, then every rune up to and including the first run of three
// consecutive quotes. graphql-go does NOT honour `\"""` as an escape here: the three
// quotes after the backslash still close the string.
//
// 🔴 A BLOCK STRING WHOSE CLOSING RUN FOLLOWS A BACKSLASH IS REFUSED. `\"""` is the
// specification's escaped triple quote, and graphql-go's normalising pass honours it
// (it stays inside the block string), while its lexer closes the string there. The two
// layers then disagree about every byte that follows, so graphql-go never ran such a
// document as written. The rule is exact: it refuses when the rune before the closing
// run is a backslash, whether or not that backslash is itself escaped, because the
// normalising pass does not care either. A backslash before a shorter run (`\"`) is
// ordinary block-string content.
//
// An unterminated block string is an error, where graphql-go reads to EOF and fails
// later; either way nothing executes.
func (l *opLexer) skipBlockString() {
	l.sc.Next() // the third opening quote
	prev, quotes, escaped := rune('"'), 0, false
	for {
		r := l.sc.Next()
		switch r {
		case scanner.EOF:
			l.refuse("unterminated block string")
			return
		case '"':
			if quotes == 0 {
				escaped = prev == '\\'
			}
			quotes++
			if quotes == 3 {
				if escaped {
					l.refuse(`a block string whose closing """ follows a backslash is not accepted`)
				}
				return
			}
		default:
			quotes = 0
		}
		prev = r
	}
}
