// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
)

// calloutAuthLevels returns the level of every log line the auth callout wrote about
// an AuthenticateDevice failure, keyed on the two messages the call site can emit.
// It reads the LEVEL FIELD of each JSON line rather than matching a phrase alone, so
// the test pins which level the call site chose, not merely that it logged.
func calloutAuthLevels(t *testing.T, captured string) []string {
	t.Helper()
	var levels []string
	for _, line := range strings.Split(captured, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if strings.HasPrefix(entry.Message, "Auth-callout rejected a device connection") ||
			strings.HasPrefix(entry.Message, "Auth-callout could not authenticate a device connection") {
			levels = append(levels, entry.Level)
		}
	}
	return levels
}

// The callout's level choice is what decides whether an operator ever sees a broken
// credential store, and whether an unauthenticated client can write Warn lines just by
// sending a wrong password. Testing model.IsCredentialRefusal alone cannot see the call
// site: inverting its branch there passes every predicate test. So each case drives
// authorize() and asserts the level of the ONE line it wrote — a positive assertion in
// every case, so a muted or detached logger fails rather than reading as "quiet".
func TestAuthorizeLogsRefusalsAtDebugAndRealFailuresAtWarn(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"wrong secret is the device's answer", model.ErrCredentialSecretMismatch, "debug"},
		{"unknown credential is the device's answer", model.ErrCredentialNotResolved, "debug"},
		{"expired credential is the device's answer", fmt.Errorf("wrapped: %w", model.ErrCredentialExpired), "debug"},
		{"a malformed stored credential is the operator's", model.ErrCredentialMisconfigured, "warn"},
		{"a credential-store failure is the operator's", errors.New("connection refused"), "warn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWarnings(t)
			r, _ := newTestResponder(t, func(context.Context, *model.PresentedCredential) (*model.Device, error) {
				return nil, tc.err
			})
			if jwt, errMsg := r.authorize(testRequest(t, "acme-corp:dev1", "s3cret")); jwt != "" || errMsg != genericAuthFailure {
				t.Fatalf("expected the generic denial, got jwt=%q err=%q", jwt, errMsg)
			}
			levels := calloutAuthLevels(t, logs.String())
			if len(levels) != 1 || levels[0] != tc.want {
				t.Fatalf("want exactly one %s line from the callout's auth failure, got levels %v\ncaptured:\n%s",
					tc.want, levels, logs.String())
			}
		})
	}
}
