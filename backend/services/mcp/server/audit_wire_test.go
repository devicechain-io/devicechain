// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
)

// These tests drive real tools/call requests through the handler New serves and read
// back what the process LOGGED, because the audit record is a log line and nothing else:
// it is asserted where an operator's pipeline would find it, on the global logger's
// output (captured by the package's shared sink, main_test.go), rather than through any
// hook this package could offer a test and then forget to call.

// logs is what a capture has collected so far.
type logs interface{ String() string }

// captureLog collects the global logger's output for the rest of the test.
func captureLog(t *testing.T) logs {
	t.Helper()
	return logSink.Capture(t)
}

// auditLines returns every captured line that is a tool-call audit record, decoded.
func auditLines(t *testing.T, buf logs) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a captured log line is not JSON: %q (%v)", line, err)
		}
		if m["audit"] == "mcp_tool_call" {
			out = append(out, m)
		}
	}
	return out
}

// onlyAuditLine requires exactly one audit record and returns it. One call, one line:
// a second line would double every count an operator builds on the record.
func onlyAuditLine(t *testing.T, buf logs) map[string]any {
	t.Helper()
	lines := auditLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("want exactly one tool-call audit record, got %d; the log was:\n%s",
			len(lines), buf.String())
	}
	return lines[0]
}

// auditedSession is servedToolSession with a choice of superuser marker, so the record's
// sudo field can be seen to follow the TOKEN rather than default to one value.
func auditedSession(t *testing.T, upstreamURL string, sudo bool) *mcp.ClientSession {
	t.Helper()
	iss, validator := mustIssuerValidator(t)
	mcpHandler, _ := New(testResource, "https://as.example.com", validator, testClient(upstreamURL), DefaultLimits())
	ts := httptest.NewServer(mcpHandler)
	t.Cleanup(ts.Close)

	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"}, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, sudo, "claude-desktop", "j-audit")
	if err != nil {
		t.Fatalf("issuing a read-only token: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL,
			HTTPClient:           &http.Client{Transport: bearerTransport{token: tok.Token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("client connect through the served handler: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// sha256Hex is the expected digest, computed here from a literal rather than by the
// function under test, so a digest of the wrong bytes cannot agree with itself.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestAToolCallWritesOneAuditRecordNamingTheCaller is the record itself, field by field.
// Every identity field is checked against what the TOKEN says, with values chosen so no
// field can be satisfied by a default: a tenant, an email, an OAuth client id that is not
// "mcp", and sudo set.
func TestAToolCallWritesOneAuditRecordNamingTheCaller(t *testing.T) {
	_, url := newRecordingUpstream(t, `{"data":{"devices":{"results":[],"pagination":{"totalRecords":0}}}}`)
	cs := auditedSession(t, url, true)
	buf := captureLog(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_devices",
		Arguments: map[string]any{"pageSize": 7, "deviceType": "truck-secret-looking-filter"},
	})
	mustSucceed(t, res, err)

	rec := onlyAuditLine(t, buf)
	want := map[string]any{
		"schema":    float64(1),
		"tenant":    "acme",
		"actor":     "a@b.c",
		"client_id": "claude-desktop",
		"sudo":      true,
		"tool":      "list_devices",
		"outcome":   "ok",
		// The client encodes a map with its keys sorted, which is also the canonical form.
		"args_sha256": sha256Hex(`{"deviceType":"truck-secret-looking-filter","pageSize":7}`),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("audit record %s = %#v, want %#v; record: %v", k, rec[k], v, rec)
		}
	}
	if _, ok := rec["duration_ms"].(float64); !ok {
		t.Errorf("audit record carries no numeric duration_ms: %v", rec)
	}
	// The arguments are recorded only as their digest.
	if strings.Contains(buf.String(), "truck-secret-looking-filter") {
		t.Errorf("the audit record copied an argument value into the log:\n%s", buf.String())
	}
}

// TestTheAuditRecordIsWrittenWhateverTheLogLevel pins the record below the reach of the
// verbosity setting: at the quietest level an operator can configure, the line is still
// written. A record at Info would vanish the moment someone set the level to warn.
func TestTheAuditRecordIsWrittenWhateverTheLogLevel(t *testing.T) {
	saved := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(saved) })

	_, url := newRecordingUpstream(t, `{"data":{"devices":{"results":[],"pagination":{"totalRecords":0}}}}`)
	cs := auditedSession(t, url, false)
	buf := captureLog(t)
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_devices"})
	mustSucceed(t, res, err)

	rec := onlyAuditLine(t, buf)
	if rec["sudo"] != false {
		t.Errorf("audit record sudo = %#v for a token without the superuser marker", rec["sudo"])
	}
	// The client sends a call with no arguments as an empty object.
	if got, want := rec["args_sha256"], sha256Hex(`{}`); got != want {
		t.Errorf("args_sha256 for a call with no arguments = %v, want %v", got, want)
	}
}

// TestAFailedToolCallIsRecordedAsAnError: a call whose arguments the tool refuses is
// recorded, with the tool's name and outcome error. A record written only on success
// would hide exactly the calls worth looking at.
func TestAFailedToolCallIsRecordedAsAnError(t *testing.T) {
	_, url := newRecordingUpstream(t, `{"data":{"devicesByToken":[]}}`)
	cs := auditedSession(t, url, false)
	buf := captureLog(t)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_device",
		Arguments: map[string]any{"tokens": []string{}},
	})
	if err != nil || !res.IsError {
		t.Fatalf("precondition: get_device with no tokens should be a tool error; err=%v", err)
	}

	rec := onlyAuditLine(t, buf)
	if rec["tool"] != "get_device" || rec["outcome"] != "error" {
		t.Errorf("audit record tool/outcome = %v/%v, want get_device/error; record: %v",
			rec["tool"], rec["outcome"], rec)
	}
}

// TestACallToAnUnknownToolIsRecordedWithoutItsName: the call is refused before any tool
// runs and still recorded, and the name the client sent — free text — is not copied.
func TestACallToAnUnknownToolIsRecordedWithoutItsName(t *testing.T) {
	_, url := newRecordingUpstream(t, `{}`)
	cs := auditedSession(t, url, false)
	buf := captureLog(t)

	const probe = "drop_everything_and_write_this_into_the_log"
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: probe}); err == nil {
		t.Fatalf("precondition: a call to an unknown tool should be a protocol error")
	}

	rec := onlyAuditLine(t, buf)
	if rec["outcome"] != "rejected" || rec["tool"] != "(unregistered)" || rec["actor"] != "a@b.c" {
		t.Errorf("audit record outcome/tool/actor = %v/%v/%v, want rejected/(unregistered)/a@b.c",
			rec["outcome"], rec["tool"], rec["actor"])
	}
	if strings.Contains(buf.String(), probe) {
		t.Errorf("the audit record copied a client-supplied tool name into the log:\n%s", buf.String())
	}
}

// TestOnlyToolCallsAreRecorded: listing the tools is not a read of tenant data and writes
// no record, so the record's count is a count of calls.
func TestOnlyToolCallsAreRecorded(t *testing.T) {
	_, url := newRecordingUpstream(t, `{}`)
	cs := auditedSession(t, url, false)
	buf := captureLog(t)

	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if lines := auditLines(t, buf); len(lines) != 0 {
		t.Errorf("tools/list wrote %d tool-call audit record(s): %v", len(lines), lines)
	}
}
