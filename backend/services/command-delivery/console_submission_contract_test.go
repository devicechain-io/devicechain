// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These source contracts complement the console's behavioral jsdom tests. The Go
// cache does not track frontend files; run these with -count=1, as CI does.
func consoleSubmissionSource(t *testing.T, route, file string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "frontend", "apps", "console", "src", "routes", route, file)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

func TestConsoleBatchUnknownOutcomeRetainsToken(t *testing.T) {
	source := consoleSubmissionSource(t, "command-batches", "CreateBatchForm.tsx")
	for _, contract := range []string{
		"const pendingAttempt = useRef<",
		"pendingAttempt.current?.body !== fingerprint",
		"token: pendingAttempt.current.token",
		"err instanceof GraphQLRequestError",
		"err.status >= 200 && err.status < 300",
		"(err.errors?.length ?? 0) > 0",
		"if (decided) pendingAttempt.current = null;",
		"kind: decided ? 'undecided' : 'unreadable'",
	} {
		if !strings.Contains(source, contract) {
			t.Errorf("batch retry safety contract missing: %s", contract)
		}
	}
}

func TestConsoleDeviceIssueIsSingleFlight(t *testing.T) {
	source := consoleSubmissionSource(t, "devices", "DeviceCommandsPanel.tsx")
	start := strings.Index(source, "const issue = async () => {")
	end := strings.Index(source, "const cancel = async")
	if start < 0 || end <= start {
		t.Fatal("cannot locate device issue handler")
	}
	issue := source[start:end]
	guard := strings.Index(issue, "if (inFlight.current) return;")
	acquire := strings.Index(issue, "inFlight.current = true;")
	send := strings.Index(issue, "await createCommand(")
	if guard < 0 || acquire <= guard || send <= acquire {
		t.Error("issue must synchronously guard and acquire before sending a command")
	}
	if !strings.Contains(issue, "finally {\n      inFlight.current = false;") {
		t.Error("issue must release its guard in finally, including after refusal or error")
	}
	if !strings.Contains(source, "e.key === 'Enter' && !e.repeat") {
		t.Error("command name input must ignore repeated Enter keydown events")
	}
}
