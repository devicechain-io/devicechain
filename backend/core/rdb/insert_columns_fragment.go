// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"strings"
)

// A Fragment is accepted only if every token of it is on an ALLOWLIST. A blocklist of words
// cannot work here: SQL can spell a sub-select without the word (`query_to_xml('sel'||'ect …')`,
// `(table devices)`, `(values (1))`), call functions that sleep, lock or write (`pg_sleep`,
// `set_config`, `nextval`, `dblink`), and open a string whose escape rules a scanner that does
// not model them reads wrongly (`E'\''`). So the grammar here is small enough to be read whole:
//
//   - `t.<column>` and `excluded.<column>`: the existing and the incoming row. The column is
//     checked against the model when the table is planned;
//   - numeric literals, and '…' strings (a doubled '' for a quote) with no backslash and no
//     E / U& / B / X prefix, which are not identifiers here and so are refused as such;
//   - the keywords AND OR NOT IS NULL TRUE FALSE DISTINCT CASE WHEN THEN ELSE END, and FROM only
//     as the second word of IS [NOT] DISTINCT FROM;
//   - the functions COALESCE and NULLIF, which Postgres and SQLite both have. GREATEST and LEAST
//     are NOT allowed: they exist on Postgres only (SQLite spells them max()/min()), and one
//     fragment text must mean the same on both. Use CASE WHEN … for a maximum;
//   - the operators = <> != < > <= >= + - * / % ||, and parentheses; a comma only directly
//     inside a function's parentheses.
//
// Everything else — any other identifier, a quote character other than ', a colon (a cast), a
// semicolon, a comment opener, a parameter or placeholder, a backslash — is refused.

var fragmentKeywords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "NULL": true, "TRUE": true, "FALSE": true,
	"DISTINCT": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true,
}

var fragmentFuncs = map[string]bool{"COALESCE": true, "NULLIF": true}

var fragmentOps = map[string]bool{
	"=": true, "<>": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true,
	"+": true, "-": true, "*": true, "/": true, "%": true, "||": true,
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentChar(c byte) bool  { return isIdentStart(c) || isDigit(c) }

// validateFragment checks s against the grammar above and returns the model columns it
// names (the `t.x` / `excluded.x` references), for the plan to check against the schema.
// set marks a SET expression, which may not be empty.
func validateFragment(what, s string, set bool) ([]string, error) {
	bad := func(format string, args ...any) ([]string, error) {
		return nil, fmt.Errorf("%w: the %s fragment %q %s", ErrColumnarModel, what, s, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(s) == "" {
		if set {
			return bad("is empty")
		}
		return nil, nil
	}
	// A pre-filter on characters and openers no token may contain, so none of them can ride
	// inside a string either: the statement text goes through gorm's '?' / '@name' rewriting
	// and Postgres' $n numbering, and a comment opener would swallow the tenant term.
	for _, p := range []string{";", "--", "/*", "*/", "$", "?", "@", `\`, `"`, "`", ":"} {
		if strings.Contains(s, p) {
			return bad("contains %q, which a fragment may not", p)
		}
	}
	var (
		refs  []string
		stack []bool // one entry per open parenthesis: true for a function call's
		prev  string // the previous token, upper-cased; "" at the start
	)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			j := i + 1
			for {
				if j >= len(s) {
					return bad("has an unterminated string")
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i, prev = j+1, "'"
		case isDigit(c):
			j := i
			for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
				j++
			}
			if j < len(s) && isIdentChar(s[j]) {
				return bad("has a malformed number at offset %d", i)
			}
			i, prev = j, "0"
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			word := s[i:j]
			up := strings.ToUpper(word)
			switch {
			case j < len(s) && s[j] == '.':
				if !strings.EqualFold(word, "t") && !strings.EqualFold(word, "excluded") {
					return bad("qualifies a column with %q; only t. and excluded. are allowed", word)
				}
				k := j + 1
				for k < len(s) && isIdentChar(s[k]) {
					k++
				}
				if k == j+1 || !isIdentStart(s[j+1]) {
					return bad("has %s. with no column", word)
				}
				refs = append(refs, s[j+1:k])
				i, prev = k, "col"
				continue
			case fragmentFuncs[up]:
				k := j
				for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
					k++
				}
				if k >= len(s) || s[k] != '(' {
					return bad("uses %q without calling it", word)
				}
				stack = append(stack, true)
				i, prev = k+1, "("
				continue
			case fragmentKeywords[up]:
			case up == "FROM" && prev == "DISTINCT":
			default:
				return bad("uses %q, which is not an allowed word, column or function", word)
			}
			i, prev = j, up
		case c == '(':
			stack = append(stack, false)
			i, prev = i+1, "("
		case c == ')':
			if len(stack) == 0 {
				return bad("closes a parenthesis it did not open")
			}
			stack = stack[:len(stack)-1]
			i, prev = i+1, ")"
		case c == ',':
			if len(stack) == 0 || !stack[len(stack)-1] {
				return bad("has a comma outside a function's arguments, which would start another assignment")
			}
			i, prev = i+1, ","
		case strings.IndexByte("=<>!+-*/%|", c) >= 0:
			j := i
			for j < len(s) && strings.IndexByte("=<>!+-*/%|", s[j]) >= 0 {
				j++
			}
			if !fragmentOps[s[i:j]] {
				return bad("has the operator %q, which is not allowed", s[i:j])
			}
			i, prev = j, "op"
		default:
			return bad("has the character %q, which a fragment may not", string(c))
		}
	}
	if len(stack) != 0 {
		return bad("has an unclosed parenthesis")
	}
	return refs, nil
}
