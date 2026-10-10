// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"
)

// tokenGrammarCases are inputs at every edge of the grammar: each character class, each
// neighbour of a class boundary in ASCII, the first-character rule, and non-ASCII input.
func tokenGrammarCases() []string {
	cases := []string{
		"", "a", "Z", "0", "9", "_", "-", "a_", "a-", "_a", "-a", "a-b_c", "ABC-def_123",
		"dev.1", "dev*", "dev>", "a b", " a", "a ", "a\n", "a\x00", "a/b", "a+b", "a#b",
		"tenant-0001", "café", "é", "a\xff", "\xff", "aİ",
		strings.Repeat("a", MaxTokenLen), strings.Repeat("a", MaxTokenLen+1),
	}
	// Every single byte, alone and after a valid first character.
	for b := 0; b < 256; b++ {
		cases = append(cases, string([]byte{byte(b)}), "a"+string([]byte{byte(b)}))
	}
	return cases
}

// TestTokenGrammarMatcherAgreesWithTheRegexp holds the byte-loop matcher to the regexp
// that states the grammar: the same answer for every case, so replacing the engine on the
// hot path changed no token's verdict.
func TestTokenGrammarMatcherAgreesWithTheRegexp(t *testing.T) {
	for _, tok := range tokenGrammarCases() {
		if got, want := matchesTokenGrammar(tok), tokenGrammar.MatchString(tok); got != want {
			t.Errorf("token %q: matcher says %v, the grammar's regexp says %v", tok, got, want)
		}
	}
}

// FuzzTokenGrammarMatcher extends the agreement to anything the fuzzer finds. Under plain
// `go test` it runs the seed corpus.
func FuzzTokenGrammarMatcher(f *testing.F) {
	for _, tok := range tokenGrammarCases() {
		f.Add(tok)
	}
	f.Fuzz(func(t *testing.T, tok string) {
		if got, want := matchesTokenGrammar(tok), tokenGrammar.MatchString(tok); got != want {
			t.Fatalf("token %q: matcher says %v, the grammar's regexp says %v", tok, got, want)
		}
	})
}

// BenchmarkValidateToken is the per-publish tenant check, on a typical tenant id.
func BenchmarkValidateToken(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := ValidateToken("tenant-0001"); err != nil {
			b.Fatal(err)
		}
	}
}
