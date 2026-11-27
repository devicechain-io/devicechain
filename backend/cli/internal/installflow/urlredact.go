// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"bytes"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// URLRedaction is what RedactURL puts where a credential could be.
const URLRedaction = "REDACTED"

// RedactURL is a user-supplied URL (or URL-like value) as it may be PRINTED: its
// userinfo, its query and its fragment are replaced with URLRedaction, and
// everything else is left exactly as it was given. It is the one display form for
// every URL a person typed into dcctl — a restore source, a registry, a backup
// endpoint, a broker URL — on the plain console, in the --output json stream and in
// the terminal UI alike, which all draw the same words.
//
//	s3://user:pass@bucket/archive    → s3://REDACTED@bucket/archive
//	https://token@host/x             → https://REDACTED@host/x
//	https://host/x?X-Amz-Signature=… → https://host/x?REDACTED
//	https://host/x#dc%43anary…       → https://host/x#REDACTED
//	dc-rdb                           → dc-rdb   (untouched, byte for byte)
//
// 🔴 IT IS STRUCTURAL, NOT A LIST OF SECRETS. Registering the parts of a URL with the
// hub's redactor cannot work: a password like "p" is below any length the redactor
// can match without corrupting ordinary words, and one like "install" would be cut
// out of every word that holds it. So the places a credential can STAND in a URL are cut out instead,
// whatever is standing there and however it is encoded: a percent-encoded password
// or fragment token is removed with the part that holds it.
//
// 🔴 IT NEVER PARSES, so a value net/url rejects or misreads is redacted exactly as a
// valid one is. url.Parse reads "user:pass@registry/org" as an opaque URL with the
// scheme "user" and no userinfo, and refuses "s3://u:p%zz@bucket" outright, and a
// display helper built on it prints both whole (url.URL.Redacted keeps a username,
// which can itself be a token, and keeps the query). The rules here are read off the
// delimiters alone, and lean towards hiding more:
//
//   - THE AUTHORITY is found first. A value starting with an RFC 3986 scheme
//     (ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )) followed by "://", or with a bare
//     "//", has its authority after the slashes; any other value is read as having
//     no scheme, its authority from its first byte. The authority ends at the first
//     '/', '?' or '#'. Everything after it is path, query and fragment.
//   - USERINFO is the authority up to its LAST '@', a username alone
//     ("https://ghp_token@host") included: it is often the credential. An '@' in
//     the path ("https://host/a@b/archive"), the query or the fragment is not
//     userinfo, and such a value's path is shown as it is.
//   - QUERY AND FRAGMENT: everything after the first '?' or '#' that follows the
//     authority.
//   - 🔴 WHEN IN DOUBT, MORE IS HIDDEN, AND A USERINFO '@' WINS. An authority with
//     no '@' may be a username and the start of a password cut short by a raw
//     '/', '?', '#' or "://" inside the password ("s3://u:pa/ss@bucket",
//     "user:secret://x@host"). When the value has an '@' after such an authority,
//     everything from the authority's start to the value's LAST '@' is taken as
//     userinfo if the authority is not a well-formed host[:port] at all, if it is
//     a name and a ':' with nothing after it ("s3://AKID:/wJalr…@bucket" — a base64
//     secret starts with '/' one time in 64), or if it is a name and an all-digit
//     port ("s3://admin:2024/Secret@bucket") and what follows that '@' is anything
//     but an image digest. A name and a port look exactly like a user and a
//     password that starts with digits; the far side of the '@' tells them apart
//     only when it is a digest ("localhost:5001/img@sha256:…"), which follows a
//     path and never a userinfo. A host there, a scoped IPv6 literal or anything
//     else unclear is read as userinfo.
//     A bracketed IP literal, or a name with no ':', is read as a host. The one
//     form this cannot see is a username alone holding a raw '/'
//     ("https://tok/en@host"), which is written exactly as a path holding an '@'
//     ("https://host/a@b/archive") is; the second is the commoner by far.
//   - When an authority with a port is in doubt and a '?' or '#' comes before
//     that '@', the value is either a password holding the '?' or a query holding
//     the '@', and the two readings hide different parts. Everything after the
//     scheme is hidden then, which covers both.
//   - What is left after that is decoded (percent-escapes, repeatedly, so a doubly
//     encoded value is read too), and if the DECODED form still holds something the
//     rules above would hide — an '@', '?' or '#' written as %40, %3F or %23 — the
//     redaction of the whole decoded value is what is shown. A percent-encoded URL is
//     therefore not a way past this.
//
// A value with none of those delimiters at any level of encoding — an archive name, a
// registry host, a plain endpoint — is returned unchanged, so output for a URL that
// carries no credential is byte-identical to what it was before this existed. A value
// that is not a URL at all is treated the same way: there is no "invalid, so print it
// raw" branch, because the invalid ones are exactly the ones a parser would misread.
func RedactURL(s string) string {
	out := redactURLForm(s)
	// A credential the raw form hides only behind an escape ("%40" for the '@') is
	// still there once decoded. Then the decoded form is redacted and shown instead.
	if d := percentDecodeAll(out); d != out && redactURLForm(d) != d {
		return redactURLForm(percentDecodeAll(s))
	}
	return out
}

// RedactURLError is err with the URL every *url.Error in its chain quotes shown
// through RedactURL. url.Parse quotes the whole input in its error ("parse
// \"nats://user:pass@host:x\": invalid port"), so an error from a library handed a
// user's URL is a print site too.
//
// The message is rebuilt rather than only the *url.Error edited, because an error
// that wraps one with fmt.Errorf has already rendered its text: the quoted URL is
// replaced in the whole message, in both the forms %s and %q give it. The chain is
// kept, so errors.Is and errors.As still see what they did (and an *url.Error found
// through it carries the redacted URL too). An error with no *url.Error in it is
// returned as it is.
func RedactURLError(err error) error {
	var raws []string
	collectURLErrors(err, 0, &raws)
	if len(raws) == 0 {
		return err
	}
	msg := err.Error()
	for _, raw := range raws {
		shown := RedactURL(raw)
		msg = strings.ReplaceAll(msg, raw, shown)
		q, qs := strconv.Quote(raw), strconv.Quote(shown)
		msg = strings.ReplaceAll(msg, q[1:len(q)-1], qs[1:len(qs)-1])
	}
	return &redactedURLError{msg: msg, err: err}
}

type redactedURLError struct {
	msg string
	err error
}

func (e *redactedURLError) Error() string { return e.msg }
func (e *redactedURLError) Unwrap() error { return e.err }

// collectURLErrors records the URL of every *url.Error in err's tree, and redacts it
// in place.
func collectURLErrors(err error, depth int, raws *[]string) {
	if err == nil || depth > 64 {
		return
	}
	if ue, ok := err.(*url.Error); ok && ue.URL != "" {
		*raws = append(*raws, ue.URL)
		ue.URL = RedactURL(ue.URL)
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		collectURLErrors(u.Unwrap(), depth+1, raws)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			collectURLErrors(e, depth+1, raws)
		}
	}
}

// redactURLForm applies the rules above to s as it stands. It is idempotent: its
// own output comes back unchanged, which is what lets RedactURL ask whether a
// decoded form still holds something to hide.
func redactURLForm(s string) string {
	start := authorityStart(s)
	end := start + strings.IndexAny(s[start:], "/?#")
	if end < start {
		end = len(s)
	}
	userEnd := start + strings.LastIndexByte(s[start:end], '@')
	if userEnd < start {
		// The doubt: an authority with no '@' may be a username and a password cut
		// short by a raw delimiter inside it. Its userinfo then runs to the value's
		// last '@'.
		if at := strings.LastIndexByte(s, '@'); at > end {
			switch authorityDoubt(s[start:end], s[at+1:]) {
			case doubtNotAHost:
				userEnd = at
			case doubtHostPort:
				if strings.ContainsAny(s[end:at], "?#") {
					return s[:start] + URLRedaction
				}
				userEnd = at
			}
		}
	}
	out, rest := s, start
	if userEnd > start {
		out = s[:start] + URLRedaction + s[userEnd:]
		rest = start + len(URLRedaction) + 1
	} else if userEnd == start {
		rest = start + 1
	}
	// The query and the fragment are looked for after the userinfo, past whatever it
	// held.
	if i := strings.IndexAny(out[rest:], "?#"); i >= 0 {
		if cut := rest + i + 1; cut < len(out) {
			out = out[:cut] + URLRedaction
		}
	}
	return out
}

// authorityStart is where the authority of s begins: after "<scheme>://" when s
// starts with an RFC 3986 scheme, after a leading "//", or at 0 when it has neither
// (a value written without a scheme, such as "user:pass@registry.example/org").
func authorityStart(s string) int {
	if strings.HasPrefix(s, "//") {
		return 2
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isAlpha(c):
		case i > 0 && (isDigit(c) || c == '+' || c == '-' || c == '.'):
		case i > 0 && c == ':':
			if strings.HasPrefix(s[i:], "://") {
				return i + 3
			}
			return 0
		default:
			return 0
		}
	}
	return 0
}

// doubt is how an authority with no '@' reads when an '@' follows it.
type doubt int

const (
	// noDoubt: the authority is a host, and the '@' is in its path.
	noDoubt doubt = iota
	// doubtNotAHost: the authority is no well-formed host[:port].
	doubtNotAHost
	// doubtHostPort: the authority is a well-formed name and port that may be a
	// user name and the start of a password.
	doubtHostPort
)

// authorityDoubt reads the authority a, which holds no '@', given tail: what
// follows the value's last '@', which comes after a. See RedactURL.
func authorityDoubt(a, tail string) doubt {
	if !validHostPort(a) {
		return doubtNotAHost
	}
	colon := strings.IndexByte(a, ':')
	if strings.HasPrefix(a, "[") || colon < 0 {
		return noDoubt
	}
	if colon == len(a)-1 || !readsAsDigest(tail) {
		return doubtHostPort
	}
	return noDoubt
}

// readsAsDigest reports whether tail, what follows an '@', is an image digest as
// the OCI distribution grammar writes one ("sha256:0d1f…"): an algorithm of
// lower-case letters and digits joined by '+', '.', '_' or '-', a ':', and an
// encoded part of letters, digits, '=', '_' and '-', and nothing else (closing
// punctuation of a sentence aside). It is the one thing a name:port/path@… value
// may carry after its '@' and still be shown: anything else — a host, a scoped IPv6
// literal, a word — is read as the far side of a userinfo, because when in doubt,
// more is hidden.
func readsAsDigest(tail string) bool {
	tail = strings.TrimRight(tail, ".,;:)]}")
	alg, enc, ok := strings.Cut(tail, ":")
	if !ok || alg == "" || enc == "" || allDigits(enc) {
		// All digits is a port: "host.example:9000" is a host, not a digest.
		return false
	}
	for i := 0; i < len(alg); i++ {
		c := alg[i]
		switch {
		case 'a' <= c && c <= 'z' || isDigit(c):
		case strings.IndexByte("+._-", c) >= 0 && i > 0 && i < len(alg)-1 && strings.IndexByte("+._-", alg[i-1]) < 0:
		default:
			return false
		}
	}
	for i := 0; i < len(enc); i++ {
		if c := enc[i]; !(isAlpha(c) || isDigit(c) || c == '=' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validHostPort reports whether a is a well-formed host with an optional port: a
// bracketed IP literal, with an optional zone after a '%' ("[fe80::1%25eth0]", or
// the raw "%eth0" a decoded value holds), or a reg-name (unreserved, percent-encoded
// and sub-delims characters), then ":" and digits, or nothing.
func validHostPort(a string) bool {
	host, port := a, ""
	if strings.HasPrefix(a, "[") {
		end := strings.IndexByte(a, ']')
		if end < 0 {
			return false
		}
		host, port = a[1:end], a[end+1:]
		if z := strings.IndexByte(host, '%'); z >= 0 {
			zone := host[z+1:]
			if zone == "" {
				return false
			}
			for i := 0; i < len(zone); i++ {
				if c := zone[i]; !(isAlpha(c) || isDigit(c) || strings.IndexByte("-._~%", c) >= 0) {
					return false
				}
			}
			host = host[:z]
		}
		if host == "" {
			return false
		}
		for i := 0; i < len(host); i++ {
			if c := host[i]; !(isHex(c) || c == ':' || c == '.') {
				return false
			}
		}
		if port == "" {
			return true
		}
		if port[0] != ':' {
			return false
		}
		port = port[1:]
	} else {
		if i := strings.IndexByte(a, ':'); i >= 0 {
			host, port = a[:i], a[i+1:]
		}
		for i := 0; i < len(host); i++ {
			c := host[i]
			if !(isAlpha(c) || isDigit(c) || strings.IndexByte("-._~%!$&'()*+,;=", c) >= 0) {
				return false
			}
		}
	}
	for i := 0; i < len(port); i++ {
		if !isDigit(port[i]) {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// percentDecodeAll decodes every well-formed %XX escape in s, repeatedly, until
// nothing changes (bounded, so a pathological value cannot spin). A malformed escape
// is left as it is rather than failing the whole decode: url.PathUnescape refuses
// "%zz" and would hand back nothing to look at.
func percentDecodeAll(s string) string {
	for range 8 {
		d := percentDecodeOnce(s)
		if d == s {
			break
		}
		s = d
	}
	return s
}

func percentDecodeOnce(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// RedactURLsInText is s with every URL in it shown through RedactURL, and every
// scheme-less "user:password@host" with its userinfo replaced. It is for text dcctl
// did not write — a tool's output, an error a tool returned — where a credential a
// person typed into dcctl comes back quoted inside a sentence: ko quotes the
// repository it could not push to, user:password@registry and all.
//
// Words are split at whitespace, quotes, backticks and angle brackets, with one
// exception: a ' is legal in a userinfo ("https://hook:it's@host"), so a ' does not
// end a word that already holds a ':' when an '@' follows it before the next of the
// other delimiters. The quotes a tool puts around a value still end it, since no '@'
// follows the closing one. A word holding "://" is a URL from its scheme on (from the word's start when what comes before
// the scheme could itself be userinfo); a word with no scheme is redacted only when
// it reads as user:password@host, so an address (ops@example.com, git@github.com:org)
// or an image digest (registry:5000/img@sha256:…) is left as it is. Text with none of
// '@', '?' or '#' is returned unchanged.
func RedactURLsInText(s string) string {
	if !strings.ContainsAny(s, "@?#") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if isTextDelimiter(s[i]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && !isTextDelimiter(s[j]) {
			j++
		}
		redactQuotedWords(&b, s[i:j])
		i = j
	}
	return b.String()
}

// isTextDelimiter is what always ends a word. A ' may, see redactQuotedWords.
func isTextDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f', '"', '`', '<', '>':
		return true
	}
	return false
}

// redactQuotedWords writes seg, a run with no isTextDelimiter in it, to b with each
// of its words redacted. A ' ends a word unless the word already holds a ':' and an
// '@' follows the ' in seg: then it is a ' inside a userinfo.
func redactQuotedWords(b *strings.Builder, seg string) {
	lastAt := strings.LastIndexByte(seg, '@')
	start, colon := 0, false
	for k := 0; k < len(seg); k++ {
		switch seg[k] {
		case ':':
			colon = true
		case '\'':
			if colon && k < lastAt {
				continue
			}
			b.WriteString(redactWord(seg[start:k]))
			b.WriteByte('\'')
			start, colon = k+1, false
		}
	}
	b.WriteString(redactWord(seg[start:]))
}

// redactWord applies RedactURLsInText's rule to one word.
func redactWord(w string) string {
	if !strings.ContainsAny(w, "@?#") {
		return w
	}
	if i := strings.Index(w, "://"); i >= 0 {
		start := i
		for start > 0 && (isAlpha(w[start-1]) || isDigit(w[start-1]) || strings.IndexByte("+-.", w[start-1]) >= 0) {
			start--
		}
		for start < i && !isAlpha(w[start]) {
			start++
		}
		if prefix := w[:start]; strings.ContainsAny(prefix, ":@") {
			// What precedes the scheme could be the start of a password
			// ("user:secret://x@host"): the whole word is read as one value.
			return RedactURL(w)
		}
		return w[:start] + RedactURL(w[start:])
	}
	at := strings.LastIndexByte(w, '@')
	if at < 0 {
		return w
	}
	head := w[:at]
	c := strings.IndexByte(head, ':')
	if c < 0 {
		return w
	}
	if after := head[c+1:]; strings.IndexByte(after, '/') > 0 && allDigits(after[:strings.IndexByte(after, '/')]) &&
		readsAsDigest(w[at+1:]) {
		// host:port/path@digest — not a password. Anything else after the '@' makes
		// it user:password@host with a password that starts with digits.
		return w
	}
	// A bracket or parenthesis the tool put around the value is kept.
	lead := 0
	for lead < len(head) && (head[lead] == '[' || head[lead] == '(') {
		lead++
	}
	return w[:lead] + URLRedaction + RedactURL(w[at:])
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return s != ""
}

// RedactURLsInError is err with its message shown through RedactURLsInText, or err
// itself when that changes nothing. The chain is kept.
func RedactURLsInError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if shown := RedactURLsInText(msg); shown != msg {
		return &redactedURLError{msg: shown, err: err}
	}
	return err
}

// URLRedactingWriter passes what is written to it on to W a line at a time, each
// line through RedactURLsInText: the stream filter for a tool's output. A line ends
// at '\n' or '\r'. A line that has not ended is held until it does, until Flush, or
// until it passes maxHeldLine bytes (then it is passed on, filtered, as it stands).
type URLRedactingWriter struct {
	mu      sync.Mutex
	w       io.Writer
	pending []byte
}

// maxHeldLine bounds what a writer holds of a line that does not end.
const maxHeldLine = 64 << 10

// NewURLRedactingWriter returns a stream filter in front of w.
func NewURLRedactingWriter(w io.Writer) *URLRedactingWriter { return &URLRedactingWriter{w: w} }

func (f *URLRedactingWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, p...)
	for {
		i := bytes.IndexAny(f.pending, "\n\r")
		if i < 0 {
			break
		}
		line := f.pending[:i+1]
		if _, err := io.WriteString(f.w, RedactURLsInText(string(line))); err != nil {
			f.pending = f.pending[i+1:]
			return len(p), err
		}
		f.pending = f.pending[i+1:]
	}
	if len(f.pending) > maxHeldLine {
		return len(p), f.flushLocked()
	}
	return len(p), nil
}

// Flush passes on, filtered, a last line that has not ended.
func (f *URLRedactingWriter) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushLocked()
}

func (f *URLRedactingWriter) flushLocked() error {
	if len(f.pending) == 0 {
		return nil
	}
	line := string(f.pending)
	f.pending = nil
	_, err := io.WriteString(f.w, RedactURLsInText(line))
	return err
}
