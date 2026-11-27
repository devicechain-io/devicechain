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
// It holds three kinds of value, matched two ways:
//
//   - SECRET (AddSecret): a secret dcctl holds — a passphrase, an SMTP password, a
//     webhook or backup credential, a typed answer. Replaced wherever it occurs, as
//     a substring, and it never refuses an identifier.
//   - GENERATED (AddGeneratedSecret): a secret dcctl itself made at random (a root
//     key, a minted password). Replaced exactly as a SECRET is, and an identifier
//     that contains it is refused as well.
//   - TOKEN (AddRedaction): a value dcctl found rather than made — a kubeconfig's
//     credentials, an exec plugin's arguments and environment. Such a value can be
//     an ordinary word ("prod", "install"), so it is replaced only as a WHOLE TOKEN,
//     never adjacent to a letter, digit, '_' or '-', and it never refuses an
//     identifier. "prod" is redacted in "--profile prod" and left alone in
//     "production"; an unused user's password "install" never stops a run.
//
// 🔴 ONLY A RANDOM VALUE MAY REFUSE AN IDENTIFIER. Identifiers (step ids, message
// and error codes, prompt ids, sources) are dcctl's own vocabulary, built from its
// constants and checked against a grammar that keeps free text out of them. A value
// a PERSON chose can be a word of that vocabulary: a passphrase "bootstrap" or an
// SMTP password "install" is a substring of "bootstrap.escrow" and "install.inputs",
// and refusing those identifiers made every later event of a run invalid, so a run
// that had built everything it was asked to exited 1. Such a coincidence cannot be
// told from a leak by looking at the identifier, and the identifier reveals nothing
// about the value either way: it is the same string whatever was registered. A value
// dcctl generated at random cannot coincide with a word, so for it — and only for it
// — an identifier holding it is evidence of a leak, and is refused. Free text is
// redacted the same way for SECRET and GENERATED values: nothing about text changed.
//
// Whole-token matching applies to every TOKEN value, not only short ones. A long
// value gains nothing from substring matching that a real leak needs — a leaked
// credential stands as a token between quotes, spaces, '=' or ':' — and keeping one
// rule means no length threshold decides whether a run's text is corrupted.
type redactor struct {
	mu sync.RWMutex
	// strictForms are the SECRET and GENERATED values' forms, idForms the
	// GENERATED values' alone; tokenForms the TOKEN values', longest first.
	strictForms map[string]struct{}
	idForms     map[string]struct{}
	tokenForms  []string
	tokenSet    map[string]struct{}
	strict      *strings.Replacer
	// ids matches the forms that refuse an identifier.
	ids *strings.Replacer
}

func newRedactor() *redactor {
	return &redactor{strictForms: map[string]struct{}{}, idForms: map[string]struct{}{}, tokenSet: map[string]struct{}{}}
}

// add registers secret as a SECRET value: see redactor.
func (r *redactor) add(secret string) bool { return r.addStrict(secret, false) }

// addGenerated registers secret as a GENERATED value: see redactor.
func (r *redactor) addGenerated(secret string) bool { return r.addStrict(secret, true) }

func (r *redactor) addStrict(secret string, refusesIDs bool) bool {
	if !longEnough(secret, MinSecretBytes) {
		return false
	}
	forms := secretForms(secret, MinSecretBytes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		r.strictForms[f] = struct{}{}
		if refusesIDs {
			r.idForms[f] = struct{}{}
		}
	}
	r.strict = buildReplacer(r.strictForms)
	if refusesIDs {
		r.ids = buildReplacer(r.idForms)
	}
	return true
}

// addToken registers secret as a TOKEN value: see redactor.
func (r *redactor) addToken(secret string) bool {
	if !longEnough(secret, MinRedactionBytes) {
		return false
	}
	forms := secretForms(secret, MinRedactionBytes)
	r.mu.Lock()
	defer r.mu.Unlock()
	// 🔴 COPY ON WRITE. redact reads tokenForms after letting go of the lock, so the
	// slice it took must never change under it: an append into its spare capacity
	// followed by a sort in place would move a credential past the length it holds.
	// A new slice is built and swapped in; every reader keeps the snapshot it took.
	next := append([]string(nil), r.tokenForms...)
	for _, f := range forms {
		if _, ok := r.tokenSet[f]; !ok {
			r.tokenSet[f] = struct{}{}
			next = append(next, f)
		}
	}
	sortLongestFirst(next)
	r.tokenForms = next
	return true
}

// longEnough reports whether secret is long enough to register, as it comes AND
// as it is drawn.
//
// 🔴 WHAT IS MATCHED IS WHAT IS DRAWN. Every text is sanitised before it is
// matched, so a value holding control or format characters is only ever seen in
// its sanitised form: "abc" + ZWJ is six bytes, and is drawn as the three of "abc".
// Such a value is REFUSED when its drawn form is under the minimum, exactly as a
// value that short is, rather than matched as a token: a token match of three
// visible bytes would redact ordinary words, which is the reason for the minimum,
// and accepting the value while matching less would tell the caller it is covered
// when it is not. Refused, the caller learns it is not covered and says so (the
// bootstrap's registration falls back to a token match, counting what neither
// takes).
func longEnough(secret string, minimum int) bool {
	return len(secret) >= minimum && len(sanitize(secret)) >= minimum
}

// secretForms is secret and its derived forms: raw, URL query-, path- and
// userinfo-escaped, base64, JSON-escaped (with and without HTML escaping), and
// each substantial line of a multi-line value, every one of them also as it is
// drawn once sanitised (a JSON-escaped value keeps a format character the
// escaping leaves alone, and is drawn with the escapes and without it). Forms
// shorter than minimum are dropped.
func secretForms(secret string, minimum int) []string {
	candidates := []string{
		secret,
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
		for _, f := range []string{c, sanitize(c)} {
			if len(f) >= minimum {
				out = append(out, f)
			}
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

// contains reports whether s holds a GENERATED value's form. Identifier fields
// use it to refuse, rather than redact, an event whose code is a secret. A SECRET
// or TOKEN value never refuses one: see redactor.
func (r *redactor) contains(s string) bool {
	r.mu.RLock()
	rep := r.ids
	r.mu.RUnlock()
	return s != "" && rep != nil && rep.Replace(s) != s
}
