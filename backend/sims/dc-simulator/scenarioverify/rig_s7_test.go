// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Execute the rig's real cleanup, with only external operations stubbed.
func TestS7RigOAuthCleanup(t *testing.T) {
	raw, err := os.ReadFile("../../../../hack/harborpulse-rig.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	start, end := strings.Index(script, "destroy_sims()"), strings.Index(script, "trap stop_all EXIT")
	if start < 0 || end < start {
		t.Fatal("rig cleanup functions not found")
	}
	for _, tc := range []struct {
		name        string
		succeedAt   int
		body        string
		wantFailure bool
	}{
		{"retry", 3, "exit 0", false},
		{"unresolved-exit", 99, "exit 0", true},
		{"unresolved-explicit", 99, "stop_all", true},
		{"unresolved-prior-failure", 99, "exit 7", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			harness := `set -euo pipefail
state_dir="$TEST_STATE"
s7_oauth_client=test-client
s7_peer_handshake=peer.json
created_sims=()
DC_HPRIG_KEEP=1
note() { echo "$*" >&2; }
sleep() { :; }
stop_procs() { :; }
stop_forwards() { touch "$state_dir/forwards-stopped"; }
s7_registry() {
  printf x >>"$state_dir/attempts"
  [[ $(wc -c <"$state_dir/attempts") -ge "$TEST_SUCCEED_AT" ]]
}
` + script[start:end] + "\ntrap stop_all EXIT\n" + tc.body + "\n"
			cmd := exec.Command("bash", "-c", harness)
			cmd.Env = append(os.Environ(), "TEST_STATE="+dir, "TEST_SUCCEED_AT="+strconv.Itoa(tc.succeedAt))
			out, runErr := cmd.CombinedOutput()
			if (runErr != nil) != tc.wantFailure {
				t.Fatalf("cleanup error=%v, want failure=%v: %s", runErr, tc.wantFailure, out)
			}
			attempts, _ := os.ReadFile(filepath.Join(dir, "attempts"))
			if len(attempts) < 3 {
				t.Fatalf("delete tried %d times, want at least 3: %s", len(attempts), out)
			}
			if _, err := os.Stat(filepath.Join(dir, "forwards-stopped")); err != nil {
				t.Fatal("cleanup did not stop forwards after OAuth deletion")
			}
			recovery, readErr := os.ReadFile(filepath.Join(dir, "test-client.oauth-client-id"))
			if tc.wantFailure {
				if readErr != nil || strings.TrimSpace(string(recovery)) != "test-client" || !strings.Contains(string(out), "FAIL:") {
					t.Fatalf("unresolved delete lost recovery information or FAIL: %q, %v, %s", recovery, readErr, out)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("successful delete left recovery file: %v", readErr)
			}
		})
	}
}
