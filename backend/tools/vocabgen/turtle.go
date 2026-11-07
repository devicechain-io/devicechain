// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is a deliberately small Turtle reader. vocabgen is the one place in
// the repository allowed to parse RDF (the runtime never does), and the inputs
// are a handful of pinned, hash-verified upstream files, so a dependency-free
// reader that FAILS on anything it does not understand is a better trade than
// pulling a parser library into the workspace. It covers the Turtle 1.1 grammar
// the pinned files use: prefixes, base, predicate and object lists, blank-node
// property lists, collections, all four string forms, numbers and booleans.
// Anything outside that is a parse error, never a silent skip.

type nodeKind int

const (
	nodeIRI nodeKind = iota
	nodeBlank
	nodeLiteral
)

// Node is one RDF term. For a literal, Value is the lexical form.
type Node struct {
	Kind     nodeKind
	Value    string
	Datatype string
	Lang     string
}

// Triple is one parsed statement.
type Triple struct {
	S, P, O Node
}

const (
	rdfNS  = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	rdfsNS = "http://www.w3.org/2000/01/rdf-schema#"
	owlNS  = "http://www.w3.org/2002/07/owl#"
	xsdNS  = "http://www.w3.org/2001/XMLSchema#"
)

type turtleParser struct {
	src      string
	pos      int
	base     string
	prefixes map[string]string
	triples  []Triple
	bnodes   int
}

// parseTurtle reads a whole document.
func parseTurtle(src string) ([]Triple, error) {
	p := &turtleParser{src: strings.TrimPrefix(src, "\xef\xbb\xbf"), prefixes: map[string]string{}}
	for {
		p.skipWS()
		if p.eof() {
			return p.triples, nil
		}
		if err := p.statement(); err != nil {
			line := 1 + strings.Count(p.src[:min(p.pos, len(p.src))], "\n")
			return nil, fmt.Errorf("turtle: line %d: %w", line, err)
		}
	}
}

func (p *turtleParser) eof() bool { return p.pos >= len(p.src) }

func (p *turtleParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *turtleParser) skipWS() {
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.pos++
		case c == '#':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *turtleParser) hasKeyword(kw string) bool {
	if len(p.src)-p.pos < len(kw) || !strings.EqualFold(p.src[p.pos:p.pos+len(kw)], kw) {
		return false
	}
	end := p.pos + len(kw)
	return end >= len(p.src) || (!isNameChar(rune(p.src[end])) && p.src[end] != ':')
}

func (p *turtleParser) statement() error {
	switch {
	case strings.HasPrefix(p.src[p.pos:], "@prefix"):
		p.pos += len("@prefix")
		return p.prefixDecl(true)
	case strings.HasPrefix(p.src[p.pos:], "@base"):
		p.pos += len("@base")
		return p.baseDecl(true)
	case p.hasKeyword("PREFIX"):
		p.pos += len("PREFIX")
		return p.prefixDecl(false)
	case p.hasKeyword("BASE"):
		p.pos += len("BASE")
		return p.baseDecl(false)
	}
	var subj Node
	if p.peek() == '[' {
		n, _, err := p.blankNodePropertyList()
		if err != nil {
			return err
		}
		subj = n
		p.skipWS()
		if p.peek() == '.' { // `[ p o ] .`: the property list was the whole statement
			p.pos++
			return nil
		}
	} else {
		n, err := p.subjectOrObject()
		if err != nil {
			return err
		}
		subj = n
	}
	if err := p.predicateObjectList(subj); err != nil {
		return err
	}
	p.skipWS()
	if p.peek() != '.' {
		return fmt.Errorf("expected '.' after statement, found %q", p.near())
	}
	p.pos++
	return nil
}

func (p *turtleParser) near() string {
	end := min(p.pos+20, len(p.src))
	return p.src[p.pos:end]
}

func (p *turtleParser) prefixDecl(needDot bool) error {
	p.skipWS()
	start := p.pos
	for !p.eof() && p.src[p.pos] != ':' {
		p.pos++
	}
	if p.eof() {
		return fmt.Errorf("unterminated prefix declaration")
	}
	name := strings.TrimSpace(p.src[start:p.pos])
	p.pos++ // ':'
	p.skipWS()
	iri, err := p.iriRef()
	if err != nil {
		return err
	}
	p.prefixes[name] = iri
	if needDot {
		p.skipWS()
		if p.peek() != '.' {
			return fmt.Errorf("expected '.' after @prefix")
		}
		p.pos++
	}
	return nil
}

func (p *turtleParser) baseDecl(needDot bool) error {
	p.skipWS()
	raw, err := p.iriRefRaw()
	if err != nil {
		return err
	}
	resolved, err := p.resolve(raw)
	if err != nil {
		return err
	}
	p.base = resolved
	if needDot {
		p.skipWS()
		if p.peek() != '.' {
			return fmt.Errorf("expected '.' after @base")
		}
		p.pos++
	}
	return nil
}

func (p *turtleParser) iriRefRaw() (string, error) {
	if p.peek() != '<' {
		return "", fmt.Errorf("expected IRI reference, found %q", p.near())
	}
	p.pos++
	var sb strings.Builder
	for {
		if p.eof() {
			return "", fmt.Errorf("unterminated IRI reference")
		}
		c := p.src[p.pos]
		if c == '>' {
			p.pos++
			return sb.String(), nil
		}
		if c == '\\' {
			r, err := p.uchar()
			if err != nil {
				return "", err
			}
			sb.WriteRune(r)
			continue
		}
		sb.WriteByte(c)
		p.pos++
	}
}

func (p *turtleParser) iriRef() (string, error) {
	raw, err := p.iriRefRaw()
	if err != nil {
		return "", err
	}
	return p.resolve(raw)
}

// resolve turns an IRI reference into an absolute IRI by RFC 3986 reference
// resolution against the current @base. An absolute IRI is returned exactly as
// written; a relative one with no base in force is an error, because guessing at
// it would silently change a term's identity.
func (p *turtleParser) resolve(raw string) (string, error) {
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad IRI reference %q: %w", raw, err)
	}
	if ref.IsAbs() {
		return raw, nil
	}
	if p.base == "" {
		return "", fmt.Errorf("relative IRI %q with no @base in force", raw)
	}
	base, err := url.Parse(p.base)
	if err != nil {
		return "", fmt.Errorf("bad @base %q: %w", p.base, err)
	}
	return base.ResolveReference(ref).String(), nil
}

// uchar reads \uXXXX or \UXXXXXXXX at p.pos.
func (p *turtleParser) uchar() (rune, error) {
	if p.pos+1 >= len(p.src) {
		return 0, fmt.Errorf("dangling escape")
	}
	var n int
	switch p.src[p.pos+1] {
	case 'u':
		n = 4
	case 'U':
		n = 8
	default:
		return 0, fmt.Errorf("unsupported escape \\%c", p.src[p.pos+1])
	}
	if p.pos+2+n > len(p.src) {
		return 0, fmt.Errorf("truncated unicode escape")
	}
	v, err := strconv.ParseUint(p.src[p.pos+2:p.pos+2+n], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("bad unicode escape: %w", err)
	}
	if !utf8.ValidRune(rune(v)) {
		return 0, fmt.Errorf("\\%c%s is not a Unicode scalar value", p.src[p.pos+1], p.src[p.pos+2:p.pos+2+n])
	}
	p.pos += 2 + n
	return rune(v), nil
}

func (p *turtleParser) newBlank() Node {
	p.bnodes++
	return Node{Kind: nodeBlank, Value: "b" + strconv.Itoa(p.bnodes)}
}

func (p *turtleParser) emit(s, pr, o Node) {
	p.triples = append(p.triples, Triple{S: s, P: pr, O: o})
}

func (p *turtleParser) blankNodePropertyList() (Node, bool, error) {
	p.pos++ // '['
	n := p.newBlank()
	p.skipWS()
	if p.peek() == ']' {
		p.pos++
		return n, true, nil
	}
	if err := p.predicateObjectList(n); err != nil {
		return n, false, err
	}
	p.skipWS()
	if p.peek() != ']' {
		return n, false, fmt.Errorf("expected ']', found %q", p.near())
	}
	p.pos++
	return n, false, nil
}

func (p *turtleParser) predicateObjectList(subj Node) error {
	for {
		p.skipWS()
		verb, err := p.verb()
		if err != nil {
			return err
		}
		for {
			p.skipWS()
			obj, err := p.object()
			if err != nil {
				return err
			}
			p.emit(subj, verb, obj)
			p.skipWS()
			if p.peek() == ',' {
				p.pos++
				continue
			}
			break
		}
		p.skipWS()
		if p.peek() != ';' {
			return nil
		}
		for p.peek() == ';' { // a run of ';' is legal
			p.pos++
			p.skipWS()
		}
		if c := p.peek(); c == '.' || c == ']' {
			return nil
		}
	}
}

func (p *turtleParser) verb() (Node, error) {
	if p.peek() == 'a' && p.pos+1 < len(p.src) {
		if n := p.src[p.pos+1]; n == ' ' || n == '\t' || n == '\r' || n == '\n' {
			p.pos++
			return Node{Kind: nodeIRI, Value: rdfNS + "type"}, nil
		}
	}
	return p.iriNode()
}

func (p *turtleParser) iriNode() (Node, error) {
	if p.peek() == '<' {
		iri, err := p.iriRef()
		return Node{Kind: nodeIRI, Value: iri}, err
	}
	return p.prefixedName()
}

func (p *turtleParser) prefixedName() (Node, error) {
	start := p.pos
	for !p.eof() && p.src[p.pos] != ':' && isNameChar(rune(p.src[p.pos])) {
		p.pos++
	}
	if p.eof() || p.src[p.pos] != ':' {
		return Node{}, fmt.Errorf("expected a prefixed name, found %q", p.src[start:min(start+20, len(p.src))])
	}
	prefix := p.src[start:p.pos]
	ns, ok := p.prefixes[prefix]
	if !ok {
		return Node{}, fmt.Errorf("undeclared prefix %q", prefix)
	}
	p.pos++ // ':'
	var local strings.Builder
loop:
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == '\\':
			if p.pos+1 >= len(p.src) {
				return Node{}, fmt.Errorf("dangling escape in prefixed name")
			}
			local.WriteByte(p.src[p.pos+1])
			p.pos += 2
		case c == '%':
			if p.pos+2 >= len(p.src) {
				return Node{}, fmt.Errorf("truncated %%-escape in prefixed name")
			}
			local.WriteString(p.src[p.pos : p.pos+3])
			p.pos += 3
		case c == '.':
			// A '.' belongs to the name only when a name character follows it;
			// otherwise it is the statement terminator.
			if p.pos+1 < len(p.src) {
				n := p.src[p.pos+1]
				if isNameChar(rune(n)) || n == ':' || n == '\\' || n == '%' {
					local.WriteByte(c)
					p.pos++
					continue loop
				}
			}
			break loop
		case c == ':' || isNameChar(rune(c)) || c >= utf8.RuneSelf:
			local.WriteByte(c)
			p.pos++
		default:
			break loop
		}
	}
	return Node{Kind: nodeIRI, Value: ns + local.String()}, nil
}

func isNameChar(r rune) bool {
	return r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x80
}

func (p *turtleParser) subjectOrObject() (Node, error) {
	switch c := p.peek(); {
	case c == '_' && strings.HasPrefix(p.src[p.pos:], "_:"):
		p.pos += 2
		start := p.pos
		for !p.eof() && (isNameChar(rune(p.src[p.pos])) || p.src[p.pos] == '.' && p.pos+1 < len(p.src) && isNameChar(rune(p.src[p.pos+1]))) {
			p.pos++
		}
		return Node{Kind: nodeBlank, Value: "l:" + p.src[start:p.pos]}, nil
	case c == '(':
		return p.collection()
	default:
		return p.iriNode()
	}
}

func (p *turtleParser) collection() (Node, error) {
	p.pos++ // '('
	head := Node{Kind: nodeIRI, Value: rdfNS + "nil"}
	var items []Node
	for {
		p.skipWS()
		if p.peek() == ')' {
			p.pos++
			break
		}
		if p.eof() {
			return head, fmt.Errorf("unterminated collection")
		}
		o, err := p.object()
		if err != nil {
			return head, err
		}
		items = append(items, o)
	}
	for i := len(items) - 1; i >= 0; i-- {
		cell := p.newBlank()
		p.emit(cell, Node{Kind: nodeIRI, Value: rdfNS + "first"}, items[i])
		p.emit(cell, Node{Kind: nodeIRI, Value: rdfNS + "rest"}, head)
		head = cell
	}
	return head, nil
}

func (p *turtleParser) object() (Node, error) {
	c := p.peek()
	switch {
	case c == '[':
		n, _, err := p.blankNodePropertyList()
		return n, err
	case c == '"' || c == '\'':
		return p.stringLiteral()
	case c == '+' || c == '-' || (c >= '0' && c <= '9'):
		return p.numberLiteral()
	case p.hasKeyword("true"):
		p.pos += 4
		return Node{Kind: nodeLiteral, Value: "true", Datatype: xsdNS + "boolean"}, nil
	case p.hasKeyword("false"):
		p.pos += 5
		return Node{Kind: nodeLiteral, Value: "false", Datatype: xsdNS + "boolean"}, nil
	}
	return p.subjectOrObject()
}

func (p *turtleParser) numberLiteral() (Node, error) {
	start := p.pos
	if c := p.peek(); c == '+' || c == '-' {
		p.pos++
	}
	dt := xsdNS + "integer"
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c >= '0' && c <= '9':
			p.pos++
		case c == '.' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9':
			dt = xsdNS + "decimal"
			p.pos++
		case c == 'e' || c == 'E':
			dt = xsdNS + "double"
			p.pos++
			if d := p.peek(); d == '+' || d == '-' {
				p.pos++
			}
		default:
			return p.numberDone(start, dt)
		}
	}
	return p.numberDone(start, dt)
}

func (p *turtleParser) numberDone(start int, dt string) (Node, error) {
	lex := p.src[start:p.pos]
	if lex == "" || lex == "+" || lex == "-" {
		return Node{}, fmt.Errorf("expected a number, found %q", p.near())
	}
	return Node{Kind: nodeLiteral, Value: lex, Datatype: dt}, nil
}

func (p *turtleParser) stringLiteral() (Node, error) {
	q := p.src[p.pos]
	triple := strings.Repeat(string(q), 3)
	long := strings.HasPrefix(p.src[p.pos:], triple)
	if long {
		p.pos += 3
	} else {
		p.pos++
	}
	var sb strings.Builder
	for {
		if p.eof() {
			return Node{}, fmt.Errorf("unterminated string literal")
		}
		c := p.src[p.pos]
		if c == q {
			if !long {
				p.pos++
				break
			}
			if strings.HasPrefix(p.src[p.pos:], triple) {
				p.pos += 3
				break
			}
			sb.WriteByte(c)
			p.pos++
			continue
		}
		if !long && (c == '\n' || c == '\r') {
			return Node{}, fmt.Errorf("newline in short string literal")
		}
		if c != '\\' {
			sb.WriteByte(c)
			p.pos++
			continue
		}
		if p.pos+1 >= len(p.src) {
			return Node{}, fmt.Errorf("dangling escape in string")
		}
		switch e := p.src[p.pos+1]; e {
		case 't':
			sb.WriteByte('\t')
			p.pos += 2
		case 'n':
			sb.WriteByte('\n')
			p.pos += 2
		case 'r':
			sb.WriteByte('\r')
			p.pos += 2
		case 'b':
			sb.WriteByte('\b')
			p.pos += 2
		case 'f':
			sb.WriteByte('\f')
			p.pos += 2
		case '"', '\'', '\\':
			sb.WriteByte(e)
			p.pos += 2
		case 'u', 'U':
			r, err := p.uchar()
			if err != nil {
				return Node{}, err
			}
			sb.WriteRune(r)
		default:
			return Node{}, fmt.Errorf("unsupported string escape \\%c", e)
		}
	}
	n := Node{Kind: nodeLiteral, Value: sb.String(), Datatype: xsdNS + "string"}
	if p.peek() == '@' {
		p.pos++
		start := p.pos
		for !p.eof() && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '-') {
			p.pos++
		}
		n.Lang = p.src[start:p.pos]
		n.Datatype = rdfNS + "langString"
	} else if strings.HasPrefix(p.src[p.pos:], "^^") {
		p.pos += 2
		dt, err := p.iriNode()
		if err != nil {
			return Node{}, err
		}
		n.Datatype = dt.Value
	}
	return n, nil
}
