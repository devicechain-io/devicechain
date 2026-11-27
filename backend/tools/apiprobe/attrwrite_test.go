// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// attrWriteFixture runs the REAL attrWriteTo, through the real userclient session, against a fake
// device-management whose answer to each write is decided by answer(n) for the n-th write request.
func attrWriteFixture(t *testing.T, answer func(n int32, w http.ResponseWriter), count string, extra ...string) (out string, writes int32, err error) {
	t.Helper()
	// Keep the real bound, shrink the wait.
	oldMin, oldMax := attrBusyBackoffMin, attrBusyBackoffMax
	attrBusyBackoffMin, attrBusyBackoffMax = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { attrBusyBackoffMin, attrBusyBackoffMax = oldMin, oldMax })

	um := &fakeInstance{t: t, selectOK: func(int) bool { return true }}
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", um.userManagement)
	mux.HandleFunc("/dm/graphql", func(w http.ResponseWriter, r *http.Request) {
		answer(n.Add(1), w)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "receipt.json")
	if werr := writeReceipt(path, Receipt{
		Tenant:   "apiprobe",
		Identity: Credential{Email: "probe@apiprobe.invalid", Password: "pw"},
		Written:  time.Now(),
		// readReceipt refuses an empty receipt; the write never reads these.
		Entities: []Recorded{{Name: "device", Area: "device-management", Token: "apiprobe-device"}},
	}); werr != nil {
		t.Fatal(werr)
	}
	var buf bytes.Buffer
	err = attrWriteTo(context.Background(), append([]string{
		"--receipt", path, "--server", strings.TrimPrefix(srv.URL, "http://"), "--scheme", "http",
		"--url", srv.URL + "/dm/graphql", "--count", count,
	}, extra...), &buf)
	return buf.String(), n.Load(), err
}

func writeOK(w http.ResponseWriter) {
	_, _ = fmt.Fprintf(w, `{"data":{"setEntityAttribute":{"attrKey":%q}}}`, probeFacetKey)
}

func writeErr(w http.ResponseWriter, code string) {
	_, _ = fmt.Fprintf(w, `{"errors":[{"message":"device attributes are busy; retry the write","extensions":{"code":%q}}]}`, code)
}

// BUSY then OK passes, and says how many retries it took.
func TestAttrWriteRetriesBusyThenPasses(t *testing.T) {
	out, writes, err := attrWriteFixture(t, func(n int32, w http.ResponseWriter) {
		if n <= 2 {
			writeErr(w, codeAttributeWriteBusy)
			return
		}
		writeOK(w)
	}, "1")
	if err != nil {
		t.Fatalf("a write that was BUSY twice then accepted must pass: %v", err)
	}
	if writes != 3 {
		t.Fatalf("server saw %d write requests, want 3", writes)
	}
	if !strings.Contains(out, "(2 ATTRIBUTE_WRITE_BUSY retries)") {
		t.Fatalf("the report does not count the retries:\n%s", out)
	}
}

// BUSY forever fails, after exactly the bound.
func TestAttrWriteBusyForeverFailsAfterTheBound(t *testing.T) {
	_, writes, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) { writeErr(w, codeAttributeWriteBusy) }, "1")
	assertCode(t, err, exitMismatch)
	if int(writes) != attrBusyMaxTries {
		t.Fatalf("server saw %d write requests, want exactly the bound %d", writes, attrBusyMaxTries)
	}
}

// Any other error fails at once: one request, no retry.
func TestAttrWriteOtherErrorFailsAtOnce(t *testing.T) {
	_, writes, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) { writeErr(w, "FORBIDDEN") }, "1")
	assertCode(t, err, exitMismatch)
	if writes != 1 {
		t.Fatalf("server saw %d write requests for a non-BUSY error, want 1", writes)
	}
}

// A clean run reports zero retries (the control for the count above).
func TestAttrWriteCleanRunReportsNoRetries(t *testing.T) {
	out, writes, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) { writeOK(w) }, "3")
	if err != nil || writes != 3 || !strings.Contains(out, "(0 ATTRIBUTE_WRITE_BUSY retries)") {
		t.Fatalf("err=%v writes=%d out=%q", err, writes, out)
	}
}

// --delete passes when the platform answers that it deleted the row.
func TestAttrWriteDeletePassesWhenTheRowWasThere(t *testing.T) {
	out, writes, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) {
		_, _ = fmt.Fprint(w, `{"data":{"deleteEntityAttribute":true}}`)
	}, "1", "--key", "drill-old-delete", "--delete")
	if err != nil || writes != 1 || !strings.Contains(out, `1 delete(s) of "drill-old-delete"`) {
		t.Fatalf("err=%v writes=%d out=%q", err, writes, out)
	}
}

// THE CONTROL for the one above: a delete that found nothing is MISSING, never a pass, and so is a
// delete answered as though it were a write.
func TestAttrWriteDeleteOfNothingFails(t *testing.T) {
	_, _, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) {
		_, _ = fmt.Fprint(w, `{"data":{"deleteEntityAttribute":false}}`)
	}, "1", "--key", "drill-old-delete", "--delete")
	assertCode(t, err, exitMissing)
	_, _, err = attrWriteFixture(t, func(_ int32, w http.ResponseWriter) { writeOK(w) }, "1", "--key", "drill-old-delete", "--delete")
	assertCode(t, err, exitShape)
}

// --key writes that key, and an answer naming another key is a SHAPE failure.
func TestAttrWriteKeyIsTheKeyAnswered(t *testing.T) {
	_, _, err := attrWriteFixture(t, func(_ int32, w http.ResponseWriter) { writeOK(w) }, "1", "--key", "another-key")
	assertCode(t, err, exitShape)
	_, _, err = attrWriteFixture(t, func(_ int32, w http.ResponseWriter) {
		_, _ = fmt.Fprint(w, `{"data":{"setEntityAttribute":{"attrKey":"another-key"}}}`)
	}, "1", "--key", "another-key")
	if err != nil {
		t.Fatalf("a write of --key answered with that key must pass: %v", err)
	}
}
