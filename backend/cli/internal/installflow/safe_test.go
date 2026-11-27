// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextStripsTerminalControl(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                                  "plain",
		"\x1b[31mred\x1b[0m":                     "red",
		"\x1b]0;title\x07after":                  "after",
		"\x1b]52;c;Y2xpcA==\x1b\\after":          "after", // OSC 52 clipboard write
		"\x1b]8;;http://x\x1b\\link\x1b]8;;\x07": "link",
		"\x1bPdcs body\x1b\\x":                   "x",
		"\u009b31mc1":                            "c1",
		"a\x1bcb":                                "ab",
		"g0\x1b(Bset":                            "g0set", // character-set designation: 3 bytes
		"dec\x1b#8x":                             "decx",
		"two\x1b $Bmore":                         "twomore", // several intermediates
		"line1\nline2\r\n\tx":                    "line1 line2   x",
		"bell\x07del\x7fnul\x00":                 "belldelnul",
		"bidi‮evil⁦x⁩":                           "bidievilx",
		"bad\xffutf8":                            "bad�utf8",
		"abc\u200bdef":                           "abcdef", // zero-width space
		"z\u200cw\u200dj\u2060w\ufeffb\u00ads":   "zwjwbs", // ZWNJ, ZWJ, word joiner, BOM, soft hyphen
		"lrm\u200e\u200f\u061cx":                 "lrmx",   // LRM, RLM, Arabic letter mark
		"ls\u2028ps\u2029":                       "ls ps ",
	} {
		if got := Text(in).String(); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextIsCapped(t *testing.T) {
	got := Text(strings.Repeat("é", MaxTextBytes)).String()
	if len(got) > MaxTextBytes || !strings.HasSuffix(got, truncationMark) || !utf8.ValidString(got) {
		t.Fatalf("len %d, suffix %q", len(got), got[len(got)-8:])
	}
	if s := Text(strings.Repeat("a", 100)).String(); s != strings.Repeat("a", 100) {
		t.Fatal("short text changed")
	}
}

func TestSafeValueTypesNeverPrintTheirValue(t *testing.T) {
	const v = "hunter2-very-secret"
	r := Redact(v)
	s := NewSecret([]byte(v))
	type holder struct {
		R Redacted
		S Secret
	}
	for _, out := range []string{
		fmt.Sprint(r), fmt.Sprintf("%v %+v %#v %s %q %x", r, r, r, r, r, r),
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s),
		fmt.Sprintf("%+v %#v", holder{r, s}, holder{r, s}),
	} {
		if strings.Contains(out, v) || strings.Contains(out, fmt.Sprintf("%x", v)) {
			t.Fatalf("printed: %s", out)
		}
	}
	j, err := json.Marshal(r)
	if err != nil || string(j) != `"[redacted]"` {
		t.Fatalf("Redacted JSON %s %v", j, err)
	}
	if _, err := json.Marshal(holder{r, s}); err == nil {
		t.Fatal("a Secret must refuse to marshal")
	}
	if r.Expose() != v || string(s.Expose()) != v {
		t.Fatal("Expose")
	}
	cp := s
	s.Zero()
	if !cp.IsEmpty() {
		t.Fatal("Zero must clear every copy")
	}
}

func TestRedactorCoversEncodedForms(t *testing.T) {
	r := newRedactor()
	secret := `p@ss "word"/with+chars&more`
	multi := "line-one-of-a-secret\nkey: line-two-value-xyz\n"
	if !r.add(secret) || !r.add(multi) {
		t.Fatal("add")
	}
	if r.add("short") {
		t.Fatal("a value too short to redact safely was accepted")
	}
	js, _ := json.Marshal(secret)
	for _, form := range []string{
		secret, string(js[1 : len(js)-1]), url.QueryEscape(secret), url.PathEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)),
		"line-one-of-a-secret", "line-two-value-xyz", "key: line-two-value-xyz", sanitize(multi),
	} {
		in := "before " + form + " after"
		if got := r.redact(in); strings.Contains(got, form) || !strings.Contains(got, redactedMark) {
			t.Errorf("form %q survived: %q", form, got)
		}
	}
	if got := r.redact("nothing to see"); got != "nothing to see" {
		t.Fatalf("changed %q", got)
	}
}

type nopSink struct{}

func (nopSink) Deliver(Envelope) {}

func TestLineWriterAssemblesSplitsAndCapsLines(t *testing.T) {
	h, err := NewHub(Options{Sink: nopSink{}})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-token-value"
	if err := h.AddSecret(secret); err != nil {
		t.Fatal(err)
	}
	if err := h.Emit(Event{Payload: RunStarted{Target: Target{Mode: "m"}, Manifest: []ManifestStep{{ID: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	w, err := h.LogWriter("a", "tool", LogInfo)
	if err != nil {
		t.Fatal(err)
	}
	// A secret split across writes is assembled before redaction.
	for _, chunk := range []string{"token=s3cr3t-to", "ken-value\r\n", "second", " line\n", strings.Repeat("z", 3*MaxTextBytes), "\nlast"} {
		if n, err := io.WriteString(w, chunk); err != nil || n != len(chunk) {
			t.Fatalf("write %d %v", n, err)
		}
	}
	_ = w.Close()
	lines := h.Logs().Lines
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	if got := lines[0].Text.String(); got != "token="+redactedMark {
		t.Fatalf("line 0 %q", got)
	}
	if lines[1].Text.String() != "second line" || lines[3].Text.String() != "last" {
		t.Fatalf("lines %q %q", lines[1].Text, lines[3].Text)
	}
	if l := lines[2].Text.String(); len(l) > MaxTextBytes || !strings.HasSuffix(l, truncationMark) {
		t.Fatalf("over-long line: %d bytes", len(l))
	}
	if _, err := h.LogWriter("a", "Not An ID", LogInfo); err == nil {
		t.Fatal("bad source accepted")
	}
}

func TestLogRingBounds(t *testing.T) {
	r := logRing{maxLines: 3, maxBytes: 10}
	for _, s := range []string{"aaaa", "bbbb", "cc", "d"} {
		r.add(LogEntry{Text: Text(s)})
	}
	if len(r.lines) != 3 || r.bytes != 7 || r.evicted != 1 {
		t.Fatalf("%d lines %d bytes %d evicted", len(r.lines), r.bytes, r.evicted)
	}
	r.add(LogEntry{Text: Text("eeeeeeee")})
	if r.bytes > 10 || r.evicted != 3 {
		t.Fatalf("%d bytes %d evicted", r.bytes, r.evicted)
	}
}
