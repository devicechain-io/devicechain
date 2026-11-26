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
// one to three bytes cannot be removed from text without removing ordinary
// characters with it, so it is refused, and the caller must say it is not covered.
const MinRedactionBytes = 4

// redactor replaces known secret values, in the encodings they commonly take
// in tool output, with a placeholder. It is defence in depth: the primary
// defence is that producers only put allowlisted values into payloads.
type redactor struct {
	mu sync.RWMutex
	// forms maps every registered form to whether it also refuses an identifier:
	// true for AddSecret values, false for AddRedaction (redact-only) values.
	forms    map[string]bool
	replacer *strings.Replacer // every form: what text is redacted of
	strict   *strings.Replacer // identifier forms only: what an identifier is refused for
}

func newRedactor() *redactor { return &redactor{forms: map[string]bool{}} }

// add registers secret and its derived forms: raw, JSON-escaped (with and
// without HTML escaping), URL query- and path-escaped, base64, its sanitised
// display form, and each substantial line of a multi-line value.
func (r *redactor) add(secret string) bool { return r.register(secret, MinSecretBytes, true) }

// addTextOnly registers secret for redaction from text only: an identifier that
// contains it is not refused. It is for values short or ordinary enough (a
// four-byte password, a credential that is also a common word) that refusing
// every code containing them would refuse ordinary runs.
func (r *redactor) addTextOnly(secret string) bool {
	return r.register(secret, MinRedactionBytes, false)
}

func (r *redactor) register(secret string, minimum int, identifier bool) bool {
	if len(secret) < minimum {
		return false
	}
	candidates := []string{
		secret,
		sanitize(secret),
		url.QueryEscape(secret),
		url.PathEscape(secret),
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
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range candidates {
		if len(c) >= minimum {
			r.forms[c] = r.forms[c] || identifier
		}
	}
	r.replacer = buildReplacer(r.forms, false)
	r.strict = buildReplacer(r.forms, true)
	return true
}

// buildReplacer replaces every form, or with strictOnly only the identifier forms.
func buildReplacer(all map[string]bool, strictOnly bool) *strings.Replacer {
	forms := make([]string, 0, len(all))
	for f, identifier := range all {
		if identifier || !strictOnly {
			forms = append(forms, f)
		}
	}
	// Longest first: strings.Replacer tries pairs in argument order at each
	// position, so a value is replaced whole before any of its lines is.
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
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
	rep := r.replacer
	r.mu.RUnlock()
	if rep == nil || s == "" {
		return s
	}
	return rep.Replace(s)
}

func (r *redactor) text(t SafeText) SafeText {
	if t.s == "" {
		return t
	}
	return SafeText{s: capText(r.redact(t.s))}
}

// contains reports whether s holds a known secret form. Identifier fields use
// it to refuse, rather than redact, an event whose code is a secret.
func (r *redactor) contains(s string) bool {
	r.mu.RLock()
	rep := r.strict
	r.mu.RUnlock()
	return s != "" && rep != nil && rep.Replace(s) != s
}
