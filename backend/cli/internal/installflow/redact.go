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

// redactor replaces known secret values, in the encodings they commonly take
// in tool output, with a placeholder. It is defence in depth: the primary
// defence is that producers only put allowlisted values into payloads.
type redactor struct {
	mu       sync.RWMutex
	forms    map[string]struct{}
	replacer *strings.Replacer
}

func newRedactor() *redactor { return &redactor{forms: map[string]struct{}{}} }

// add registers secret and its derived forms: raw, JSON-escaped (with and
// without HTML escaping), URL query- and path-escaped, base64, its sanitised
// display form, and each substantial line of a multi-line value.
func (r *redactor) add(secret string) bool {
	if len(secret) < MinSecretBytes {
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
		if len(c) >= MinSecretBytes {
			r.forms[c] = struct{}{}
		}
	}
	forms := make([]string, 0, len(r.forms))
	for f := range r.forms {
		forms = append(forms, f)
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
	r.replacer = strings.NewReplacer(pairs...)
	return true
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
func (r *redactor) contains(s string) bool { return s != "" && r.redact(s) != s }
