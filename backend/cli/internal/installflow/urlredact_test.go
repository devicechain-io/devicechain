// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// urlSecrets are the parts a credential-bearing URL must never show, keyed by the
// URL that carries them. Every case is checked raw and in every encoding the
// canary harness looks for, so a leak through an escape is a leak.
var urlSecretCases = []struct {
	name, in, want string
	secrets        []string
}{
	{"s3 user:pass", "s3://dcuser:dcCanaryPw_93Kq@bucket/archive",
		"s3://REDACTED@bucket/archive", []string{"dcuser", "dcCanaryPw_93Kq"}},
	{"short user:pass", "s3://u:p@bucket/archive", "s3://REDACTED@bucket/archive", []string{"u:p"}},
	{"user only", "https://ghp_dcCanaryTok93Kq@github.example/org", "https://REDACTED@github.example/org",
		[]string{"ghp_dcCanaryTok93Kq"}},
	{"percent-encoded userinfo", "gs://dc%75ser%3Ap%40ss%2Fw0rd@bucket/x", "gs://REDACTED@bucket/x",
		[]string{"dc%75ser", "p%40ss", "ser:p@ss"}},
	{"raw slash in the password", "s3://u:pa/ss@bucket/x", "s3://REDACTED@bucket/x", []string{"pa/ss"}},
	{"raw @ in the password", "s3://u:dcCanary@Pw93@bucket/x", "s3://REDACTED@bucket/x", []string{"dcCanary", "Pw93"}},
	{"raw # in the password", "s3://u:pa#ss@bucket/x", "s3://REDACTED@bucket/x", []string{"pa#ss", "ss@"}},
	{"amz signature", "https://bucket.s3.example/archive?X-Amz-Credential=AKIDCANARY&X-Amz-Signature=dcCanarySig93Kq",
		"https://bucket.s3.example/archive?REDACTED", []string{"AKIDCANARY", "dcCanarySig93Kq", "X-Amz"}},
	{"sas sig", "https://acct.blob.example/c/archive?sv=2024&sig=dcCanarySas%2B93Kq%3D",
		"https://acct.blob.example/c/archive?REDACTED", []string{"dcCanarySas", "sig="}},
	{"token query", "https://host/hook?token=dcCanaryTok", "https://host/hook?REDACTED", []string{"dcCanaryTok"}},
	{"raw fragment", "https://bucket/archive#dcCanaryToken_93Kq", "https://bucket/archive#REDACTED",
		[]string{"dcCanaryToken_93Kq"}},
	{"escaped fragment", "https://bucket/archive#dc%43anaryToken_93Kq", "https://bucket/archive#REDACTED",
		[]string{"anaryToken_93Kq", "dcCanaryToken_93Kq"}},
	{"query and fragment", "https://h/a?sig=dcCanaryQ#dcCanaryF", "https://h/a?REDACTED",
		[]string{"dcCanaryQ", "dcCanaryF"}},
	{"everything", "oci://u:dcCanaryPw@[2001:db8::1]:5000/org?token=dcCanaryQ#dcCanaryF",
		"oci://REDACTED@[2001:db8::1]:5000/org?REDACTED", []string{"dcCanaryPw", "dcCanaryQ", "dcCanaryF"}},
	{"ipv6 userinfo", "https://u:dcCanaryPw@[::1]:9000/x", "https://REDACTED@[::1]:9000/x", []string{"dcCanaryPw"}},
	{"no scheme registry", "user:dcCanaryPw@registry.example/org", "REDACTED@registry.example/org",
		[]string{"dcCanaryPw", "user:"}},
	{"scheme-relative", "//u:dcCanaryPw@host/x", "//REDACTED@host/x", []string{"dcCanaryPw"}},
	{"unparseable escape", "s3://u:dcCanary%zzPw@bucket/x", "s3://REDACTED@bucket/x", []string{"dcCanary"}},
	{"wholly encoded", "https%3A%2F%2Fu%3AdcCanaryPw%40host%2Fx", "https://REDACTED@host/x",
		[]string{"dcCanaryPw", "dcCanary"}},
	{"doubly encoded", "https%253A%252F%252Fu%253AdcCanaryPw%2540host%252Fx", "https://REDACTED@host/x",
		[]string{"dcCanaryPw"}},
	{"encoded query", "s3://bucket/archive%3Fsig%3DdcCanarySig", "s3://bucket/archive?REDACTED",
		[]string{"dcCanarySig"}},
	{"encoded fragment", "s3://bucket/archive%23dcCanaryFrag", "s3://bucket/archive#REDACTED",
		[]string{"dcCanaryFrag"}},
	{"url inside a query", "dc-rdb?next=https://u:dcCanaryPw@h", "dc-rdb?REDACTED", []string{"dcCanaryPw", "next"}},
	{"@ inside the query", "https://host/archive?token=@dcCanarySecret", "https://host/archive?REDACTED",
		[]string{"dcCanarySecret", "token"}},
	{"@ inside the fragment", "https://host/archive#@dcCanarySecret", "https://host/archive#REDACTED",
		[]string{"dcCanarySecret"}},
	{"@ inside a no-scheme query", "registry.example/org?t=@dcCanarySecret", "registry.example/org?REDACTED",
		[]string{"dcCanarySecret"}},
	{":// inside a no-scheme password", "user:dcCanarySecret://suffix@host/path", "REDACTED@host/path",
		[]string{"dcCanarySecret", "suffix", "user"}},
	{"space in an unbracketed authority", "s3://dc canary/pw@bucket/x", "s3://REDACTED@bucket/x", []string{"canary", "pw"}},
	{"raw ? in the password", "s3://u:pa?dcCanaryPw@bucket/x", "s3://REDACTED@bucket/x", []string{"dcCanaryPw", "pa"}},
	// A password that starts with '/' or with digits makes the authority before the
	// raw delimiter read as host[:port]: the userinfo '@' must still win.
	{"slash-leading password", "s3://AKIDEXAMPLE:/wJalrXUtnFEMI+K7MDENG@bucket/archive", "s3://REDACTED@bucket/archive",
		[]string{"AKIDEXAMPLE", "wJalrXUtnFEMI", "K7MDENG"}},
	{"digit-leading password", "s3://admin:2024/dcCanarySecret@bucket/archive", "s3://REDACTED@bucket/archive",
		[]string{"admin", "2024", "dcCanarySecret"}},
	{"all-digit password segment", "https://admin:12345/678@host.example:9000/x", "https://REDACTED@host.example:9000/x",
		[]string{"admin", "12345", "678"}},
	{"digit-leading password, no scheme", "admin:2024/dcCanarySecret@registry.example/org", "REDACTED@registry.example/org",
		[]string{"admin", "dcCanarySecret"}},
	{"slash-leading password, no scheme", "user:/dcCanarySecret@registry.example/org", "REDACTED@registry.example/org",
		[]string{"user", "dcCanarySecret"}},
	{"digits then ? in the password", "s3://u:12?dcCanaryPw@bucket/x", "s3://REDACTED",
		[]string{"u:12", "dcCanaryPw"}},
	{"digits then # in the password", "s3://dcUser:12#dcCanaryPw@bucket/x", "s3://REDACTED",
		[]string{"dcUser", "dcCanaryPw"}},
	{"empty port then ? in the password", "s3://dcUser:?dcCanaryPw@bucket/x", "s3://REDACTED",
		[]string{"dcUser", "dcCanaryPw"}},
	{"@ in a query after a port", "https://host.example:8443/x?token=@dcCanarySecret", "https://REDACTED",
		[]string{"dcCanarySecret", "token"}},
	{"colon in the password", "s3://dcUser:dc:Canary:Pw@bucket/x", "s3://REDACTED@bucket/x",
		[]string{"dcUser", "dc:Canary:Pw", "Canary"}},
	{"colons and a slash in the password", "s3://dcUser:20:24/dcCanaryPw@bucket/x", "s3://REDACTED@bucket/x",
		[]string{"dcUser", "dcCanaryPw", "20:24"}},
	{"colon after a slash-leading password", "s3://dcUser:/dc:CanaryPw@bucket/x", "s3://REDACTED@bucket/x",
		[]string{"dcUser", "CanaryPw"}},
	{"digit-leading password, ipv6 host", "https://dcUser:123/dcCanaryPw@[2001:db8::1]:9000/x",
		"https://REDACTED@[2001:db8::1]:9000/x", []string{"dcUser", "dcCanaryPw"}},
	{"slash-leading password, ipv6 host", "https://dcUser:/dcCanaryPw@[::1]/x", "https://REDACTED@[::1]/x",
		[]string{"dcUser", "dcCanaryPw"}},
	// An empty port is in doubt whatever follows the '@': here an IPv6 zone that the
	// host reading does not accept.
	{"slash-leading password, ipv6 zone host", "https://dcUser:/dcCanaryPw@[fe80::1%25eth0]:9000/x",
		"https://REDACTED@[fe80::1%25eth0]:9000/x", []string{"dcUser", "dcCanaryPw"}},
	{"colon in the password, ipv6 host", "oci://dcUser:dc:CanaryPw@[2001:db8::1]:5000/org", "oci://REDACTED@[2001:db8::1]:5000/org",
		[]string{"dcUser", "CanaryPw"}},
}

// Values with no credential must come back BYTE FOR BYTE: every golden of a run
// that names a registry, an archive or an endpoint depends on it.
var urlCleanCases = []string{
	"", "dc-rdb", "dc-tsdb-restored-20260727T135900Z", "ghcr.io/devicechain-io", "localhost:5001",
	"https://s3.us-east-1.amazonaws.com", "http://minio.minio.svc:9000", "s3://bucket/archive",
	"gs://bucket/path/to/archive", "oci://registry.example:5000/org", "https://[2001:db8::1]:9000/x",
	"nats://127.0.0.1:4222", "s3://bucket/a%20b", "C:\\backups\\set", "https://h/x?", "https://h/x#",
	"s3://@bucket/x", "https://host/a@b/archive", "s3://bucket/a%2540dcHost", "s3://bucket/a%40b",
	"ghcr.io/org/img@sha256:abc", "localhost:5001/frontend@sha256:abc",
	// A digest after a port, and an '@' in the path of a bracketed IP literal, are
	// not userinfo: the far side of the '@' is no host, or the authority cannot be a
	// user name.
	"oci://registry.example:5000/org/img@sha256:abc", "registry.example:5000/org/img:tag@sha256:0123abc",
	"https://[2001:db8::1]:9000/org/img@sha256:abc", "https://[::1]:9000/a@b/archive",
	"http://[::1]/x", "http://[::1]:8080",
}

func TestRedactURLHidesEveryCredentialPart(t *testing.T) {
	for _, tc := range urlSecretCases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.in)
			if got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, s := range tc.secrets {
				for _, form := range []string{s, url.QueryEscape(s), url.PathEscape(s)} {
					if strings.Contains(got, form) {
						t.Errorf("RedactURL(%q) = %q still shows %q", tc.in, got, form)
					}
				}
			}
			if again := RedactURL(got); again != got {
				t.Errorf("RedactURL is not idempotent: %q → %q", got, again)
			}
		})
	}
}

func TestRedactURLLeavesACleanValueByteIdentical(t *testing.T) {
	for _, in := range urlCleanCases {
		if got := RedactURL(in); got != in {
			t.Errorf("RedactURL(%q) = %q: a value with no credential changed", in, got)
		}
	}
}

// url.Parse quotes the whole URL in its error, so a library handed a user's URL
// returns a print site. The chain survives the rewrite.
func TestRedactURLErrorRewritesTheQuotedURL(t *testing.T) {
	_, perr := url.Parse("nats://u:dcCanaryPw@host:x1")
	if perr == nil {
		t.Fatal("the fixture parsed: the test proves nothing")
	}
	sentinel := errors.New("sentinel")
	err := fmt.Errorf("connecting: %w", errors.Join(perr, sentinel))
	if !strings.Contains(err.Error(), "dcCanaryPw") {
		t.Fatalf("the fixture error does not quote the password: %v", err)
	}
	got := RedactURLError(err)
	if strings.Contains(got.Error(), "dcCanaryPw") {
		t.Errorf("the error still quotes the password: %v", got)
	}
	if !strings.Contains(got.Error(), "nats://REDACTED@host:x1") {
		t.Errorf("the error no longer names the URL it was about: %v", got)
	}
	var ue *url.Error
	if !errors.As(got, &ue) || !errors.Is(got, sentinel) {
		t.Errorf("the chain was not kept: %v", got)
	}
	if RedactURLError(nil) != nil {
		t.Error("a nil error became non-nil")
	}
}

// A tool's output quotes the URLs dcctl gave it, inside sentences.
func TestRedactURLsInTextHidesCredentialsInFreeText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`pushing user:dcPwZX@registry.example/org failed`, `pushing REDACTED@registry.example/org failed`},
		{`input = "s3://u:dcPwZX@bucket/archive"`, `input = "s3://REDACTED@bucket/archive"`},
		{`repo=https://AKIDZX:dcSecretZX@s3.example.com/x,`, `repo=https://REDACTED@s3.example.com/x,`},
		{`get https://bucket/x?X-Amz-Signature=dcSigZX: 403`, `get https://bucket/x?REDACTED 403`},
		{`refers to repository [user:dcPwZX@reg.example/x]`, `refers to repository [REDACTED@reg.example/x]`},
		{"line one\nuser:dcPwZX@reg/x\r\n", "line one\nREDACTED@reg/x\r\n"},
		{`from user:dcCanaryZX://x@host/p`, `from REDACTED@host/p`},
		{`pushing user:dc/PwZX@reg/x`, `pushing REDACTED@reg/x`},
		{`input = "s3://admin:2024/dcPwZX@bucket/archive"`, `input = "s3://REDACTED@bucket/archive"`},
		{`from s3://AKIDZX:/wJalrZX@bucket/archive`, `from s3://REDACTED@bucket/archive`},
		{`pushing admin:2024/dcPwZX@reg.example/x failed`, `pushing REDACTED@reg.example/x failed`},
	} {
		if got := RedactURLsInText(tc.in); got != tc.want {
			t.Errorf("RedactURLsInText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Text with no credential is returned byte for byte, '@', '?' and '#' included.
func TestRedactURLsInTextLeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"Plan: 1 to add, 0 to change, 0 to destroy.",
		"Do you want to perform these actions?",
		"mail ops@example.com about issue #3214",
		"git@github.com:devicechain-io/devicechain.git",
		"pushed ghcr.io/devicechain-io/frontend@sha256:abc and localhost:5001/frontend@sha256:def",
		"see https://kind.sigs.k8s.io/docs/user/quick-start/",
		"at 12:00:00 the archive https://host/a@b/archive was read",
	} {
		if got := RedactURLsInText(in); got != in {
			t.Errorf("RedactURLsInText(%q) = %q: ordinary text changed", in, got)
		}
	}
}

// The stream filter holds a line until it ends, so a URL split across two writes is
// still read whole, and gives up a last line with no end on Flush.
func TestURLRedactingWriterFiltersWholeLines(t *testing.T) {
	var b strings.Builder
	w := NewURLRedactingWriter(&b)
	for _, piece := range []string{"pushing user:dcP", "wZX@reg.example/x\nprogress 50%\r", "last user:dcPwZX@reg/y"} {
		if n, err := w.Write([]byte(piece)); err != nil || n != len(piece) {
			t.Fatalf("Write(%q) = %d, %v", piece, n, err)
		}
	}
	if !strings.HasSuffix(b.String(), "progress 50%\r") {
		t.Fatalf("a line ended by a carriage return was held: %q", b.String())
	}
	if strings.Contains(b.String(), "last") {
		t.Fatalf("an unterminated line was passed on before Flush: %q", b.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "pushing REDACTED@reg.example/x\nprogress 50%\rlast REDACTED@reg/y"
	if b.String() != want {
		t.Errorf("the writer passed on %q, want %q", b.String(), want)
	}
	// A line that never ends is passed on once it passes the bound, filtered.
	var big strings.Builder
	w = NewURLRedactingWriter(&big)
	long := "user:dcPwZX@reg/x " + strings.Repeat("y", maxHeldLine)
	_, _ = w.Write([]byte(long))
	if !strings.HasPrefix(big.String(), "REDACTED@reg/x ") {
		t.Errorf("an over-long line was held or passed on unfiltered (%d bytes out)", big.Len())
	}
}

func TestRedactURLsInErrorKeepsTheChain(t *testing.T) {
	sentinel := errors.New("exit status 1")
	err := fmt.Errorf("ko build: %w\npushing user:dcPwZX@reg/x", sentinel)
	got := RedactURLsInError(err)
	if strings.Contains(got.Error(), "dcPwZX") || !errors.Is(got, sentinel) {
		t.Errorf("RedactURLsInError = %v (chain kept: %v)", got, errors.Is(got, sentinel))
	}
	clean := errors.New("nothing to see")
	if RedactURLsInError(clean) != clean || RedactURLsInError(nil) != nil {
		t.Error("an error with nothing to redact was replaced")
	}
}
