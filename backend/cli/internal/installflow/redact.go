// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// MinSecretBytes is the shortest value AddSecret will redact. Shorter values
// would redact ordinary words; they are rejected rather than ignored so a
// caller learns that its secret is not covered.
const MinSecretBytes = 6

// MinRedactionBytes is the shortest value AddRedaction will redact. A value of
// one to three bytes cannot be told apart from ordinary text even as a whole
// token, so it is refused, and the caller must say it is not covered.
const MinRedactionBytes = 4

// redactor replaces known secret values, in the encodings they commonly take
// in tool output, with a placeholder. It is defence in depth: the primary
// defence is that producers only put allowlisted values into payloads.
//
// It holds two kinds of value, matched two ways:
//
//   - STRICT (AddSecret): a secret dcctl itself holds and controls (a root key, a
//     passphrase, a minted password). Replaced wherever it occurs, as a substring,
//     and an identifier that contains it is refused.
//   - TOKEN (AddRedaction): a value dcctl found rather than made — a kubeconfig's
//     credentials, an exec plugin's arguments and environment. Such a value can be
//     an ordinary word ("prod", "install"), so it is replaced only as a WHOLE TOKEN,
//     never adjacent to a letter, digit, '_' or '-', and it never refuses an
//     identifier. "prod" is redacted in "--profile prod" and left alone in
//     "production"; an unused user's password "install" never stops a run.
//
// Whole-token matching applies to every TOKEN value, not only short ones. A long
// value gains nothing from substring matching that a real leak needs — a leaked
// credential stands as a token between quotes, spaces, '=' or ':' — and keeping one
// rule means no length threshold decides whether a run's text is corrupted.
type redactor struct {
	mu sync.RWMutex
	// strictForms are the STRICT values' forms; tokenForms the TOKEN values',
	// longest first.
	strictForms map[string]struct{}
	tokenForms  []string
	tokenSet    map[string]struct{}
	strict      *strings.Replacer
}

func newRedactor() *redactor {
	return &redactor{strictForms: map[string]struct{}{}, tokenSet: map[string]struct{}{}}
}

// add registers secret strictly: see redactor.
func (r *redactor) add(secret string) bool {
	if len(secret) < MinSecretBytes {
		return false
	}
	forms := secretForms(secret, MinSecretBytes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		r.strictForms[f] = struct{}{}
	}
	r.strict = buildReplacer(r.strictForms)
	return true
}

// addToken registers secret as a TOKEN value: see redactor.
func (r *redactor) addToken(secret string) bool {
	if len(secret) < MinRedactionBytes {
		return false
	}
	forms := secretForms(secret, MinRedactionBytes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		if _, ok := r.tokenSet[f]; !ok {
			r.tokenSet[f] = struct{}{}
			r.tokenForms = append(r.tokenForms, f)
		}
	}
	sortLongestFirst(r.tokenForms)
	return true
}

// secretForms is secret and its derived forms: raw, sanitised for display, URL
// query-, path- and userinfo-escaped, base64, JSON-escaped (with and without HTML
// escaping), and each substantial line of a multi-line value. Forms shorter than
// minimum are dropped.
func secretForms(secret string, minimum int) []string {
	candidates := []string{
		secret,
		sanitize(secret),
		url.QueryEscape(secret),
		url.PathEscape(secret),
		userinfoEscaped(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
		base64.URLEncoding.EncodeToString([]byte(secret)),
		base64.RawURLEncoding.EncodeToString([]byte(secret)),
	}
	candidates = append(candidates, jsonEscaped(secret, true), jsonEscaped(secret, false))
	if strings.ContainsAny(secret, "\r\n") {
		for _, line := range strings.FieldsFunc(secret, func(c rune) bool { return c == '\n' || c == '\r' }) {
			line = strings.TrimSpace(line)
			parts := []string{line}
			// A "key: value" line (kubeconfig, YAML) also leaks its value
			// on its own, as "key=value" or inside another structure.
			if _, v, ok := strings.Cut(line, ": "); ok {
				parts = append(parts, strings.TrimSpace(v))
			}
			for _, p := range parts {
				if len(p) >= 2*MinSecretBytes {
					candidates = append(candidates, p, jsonEscaped(p, true), jsonEscaped(p, false))
				}
			}
		}
	}
	var out []string
	for _, c := range candidates {
		if len(c) >= minimum {
			out = append(out, c)
		}
	}
	return out
}

// userinfoEscaped is secret as a URL's userinfo password renders it: a password
// "foo+bar:baz" in a server or proxy URL is printed "foo+bar%3Abaz", which neither
// the query nor the path escaping produces.
func userinfoEscaped(secret string) string {
	_, p, _ := strings.Cut(url.UserPassword("u", secret).String(), ":")
	return p
}

func sortLongestFirst(forms []string) {
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
}

// buildReplacer replaces every form, longest first: strings.Replacer tries pairs
// in argument order at each position, so a value is replaced whole before any of
// its lines is.
func buildReplacer(set map[string]struct{}) *strings.Replacer {
	forms := make([]string, 0, len(set))
	for f := range set {
		forms = append(forms, f)
	}
	sortLongestFirst(forms)
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, redactedMark)
	}
	return strings.NewReplacer(pairs...)
}

func jsonEscaped(s string, html bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(html)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(buf.String(), "\n")
	return strings.TrimSuffix(strings.TrimPrefix(out, `"`), `"`)
}

func (r *redactor) redact(s string) string {
	r.mu.RLock()
	rep, tokens := r.strict, r.tokenForms
	r.mu.RUnlock()
	if s == "" {
		return s
	}
	if rep != nil {
		s = rep.Replace(s)
	}
	for _, f := range tokens {
		s = replaceToken(s, f)
	}
	return s
}

// tokenByte reports whether c continues a token: a letter, a digit, '_' or '-'.
func tokenByte(c byte) bool {
	return c == '_' || c == '-' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// replaceToken replaces every occurrence of form in s that stands as a whole
// token: the byte before it and the byte after it, where there are any, do not
// continue a token. A form that itself starts or ends in a non-token byte (a
// PEM line, a JSON-escaped value) is bounded by that byte already on that side.
func replaceToken(s, form string) string {
	if !strings.Contains(s, form) {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], form)
		if j < 0 {
			break
		}
		j += i
		end := j + len(form)
		before := j == 0 || !tokenByte(s[j-1]) || !tokenByte(form[0])
		after := end == len(s) || !tokenByte(s[end]) || !tokenByte(form[len(form)-1])
		if before && after {
			b.WriteString(s[i:j])
			b.WriteString(redactedMark)
			i = end
			continue
		}
		b.WriteString(s[i : j+1])
		i = j + 1
	}
	b.WriteString(s[i:])
	return b.String()
}

func (r *redactor) text(t SafeText) SafeText {
	if t.s == "" {
		return t
	}
	return SafeText{s: capText(r.redact(t.s))}
}

// contains reports whether s holds a STRICT value's form. Identifier fields use
// it to refuse, rather than redact, an event whose code is a secret. A TOKEN
// value never refuses one.
func (r *redactor) contains(s string) bool {
	r.mu.RLock()
	rep := r.strict
	r.mu.RUnlock()
	return s != "" && rep != nil && rep.Replace(s) != s
}
