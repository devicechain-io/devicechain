// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// respondingWith builds a peer that answers every GraphQL call with the given status and
// body, so a test can say exactly how large a response is.
func respondingWith(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// envelopeOfSize builds a well-formed GraphQL response whose total length is exactly n
// bytes, padding a string field to make up the difference. It fails the test rather than
// returning something approximate: a boundary test that is off by a byte proves nothing
// about the boundary.
func envelopeOfSize(t *testing.T, n int) string {
	t.Helper()
	const prefix = `{"data":{"pad":"`
	const suffix = `"}}`
	if n < len(prefix)+len(suffix) {
		t.Fatalf("cannot build an envelope of %d bytes; the smallest well-formed one is %d",
			n, len(prefix)+len(suffix))
	}
	body := prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
	if len(body) != n {
		t.Fatalf("envelope is %d bytes, wanted %d", len(body), n)
	}
	return body
}

// TestAnOversizedResponseSaysSoInsteadOfFailingToParse is the whole point of the typed
// error.
//
// 🔴 THE OLD BEHAVIOUR WAS NOT AN ABSENT ERROR, IT WAS A MISLEADING ONE. io.LimitReader
// stops at its limit without complaint, so a response past the cap arrived as half a JSON
// document and surfaced as "decode response: unexpected end of JSON input" — which reads
// as a broken peer. Every caller's error branch then treats a caller-side problem (this
// query asks for more than the transport carries) as somebody else's outage, and the fix
// nobody applies is paging.
func TestAnOversizedResponseSaysSoInsteadOfFailingToParse(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()

	target := respondingWith(http.StatusOK, envelopeOfSize(t, maxResponseBytes+1))
	defer target.Close()

	client := clientFor(mint, "shh")
	var out map[string]any
	err := client.Query(context.Background(), target.URL, "acme", "{ pad }", nil, &out)
	if err == nil {
		t.Fatal("a response past the cap must be an error, not a silent partial read")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("an oversized response must report ErrResponseTooLarge, got %v", err)
	}
	// The message has to name the peer, or an operator holding it cannot tell which
	// service outgrew the cap.
	if !strings.Contains(err.Error(), target.URL) {
		t.Fatalf("the error must name the peer, got %v", err)
	}
}

// TestAResponseOfExactlyTheCapIsOrdinary is the counterweight, and it is not optional.
//
// 🔴 DETECTING OVERFLOW MEANS READING ONE BYTE PAST THE CAP, WHICH IS EXACTLY THE PLACE
// AN OFF-BY-ONE LIVES. Rejecting the oversized case is only safe while a response that
// is precisely as large as the limit still decodes untouched — otherwise the fix for a
// truncation bug is a new truncation bug at a different byte. Same shape as the fork's
// TestValidInputIsNotRejected: the rejection test and the acceptance test are a pair.
func TestAResponseOfExactlyTheCapIsOrdinary(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()

	target := respondingWith(http.StatusOK, envelopeOfSize(t, maxResponseBytes))
	defer target.Close()

	client := clientFor(mint, "shh")
	var out struct {
		Pad string `json:"pad"`
	}
	if err := client.Query(context.Background(), target.URL, "acme", "{ pad }", nil, &out); err != nil {
		t.Fatalf("a response of exactly the cap must decode normally, got %v", err)
	}
	if out.Pad == "" {
		t.Fatal("the boundary response decoded to nothing; it was not actually read")
	}
}

// TestALargeErrorPageReportsTheStatusNotTheSize keeps the two diagnoses apart.
//
// 🔑 THE SAME CAPPED READ FEEDS THE NON-200 ERROR MESSAGE. Check size before status and a
// peer returning a long 500 page is reported as a caller asking for too much — which
// sends whoever reads it to page a query that was never the problem, while the failing
// peer goes unmentioned. A large error page is a peer that is failing.
func TestALargeErrorPageReportsTheStatusNotTheSize(t *testing.T) {
	var mints int32
	mint := mintServer(t, "shh", &mints)
	defer mint.Close()

	target := respondingWith(http.StatusInternalServerError, strings.Repeat("y", maxResponseBytes+1))
	defer target.Close()

	client := clientFor(mint, "shh")
	err := client.Query(context.Background(), target.URL, "acme", "{ pad }", nil, nil)
	if err == nil {
		t.Fatal("a 500 must be an error")
	}
	if errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("a failing peer must be reported as a failing peer, not as an oversized response: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(http.StatusInternalServerError)) {
		t.Fatalf("the error must name the status, got %v", err)
	}
}
