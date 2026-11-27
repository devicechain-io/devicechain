// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// 🔴 A TOOL'S OUTPUT QUOTES THE URLS dcctl GAVE IT. OpenTofu's plan prints the restore
// source and the backup endpoint; ko quotes the repository it could not push to. What
// dcctl prints of a tool passes the URL stream filter, on every presenter. Every
// credential below carries the marker "ZX"; the clean lines must come through byte for
// byte.

// toolTranscript is tool output with credentials in it, in the forms a tool prints
// them, and clean lines around them.
const toolTranscript = `Terraform will perform the following actions:
  + resource "terraform_data" "restore_guard" {
      + input  = "s3://dcuserZX:dcPwZX@bucket/archive"
    }
  + endpointURL = "https://AKIDZX:dcSecretZX@s3.example.com"
  ~ destinationPath = "s3://bucket/dc-tsdb?X-Amz-Signature=dcSigZX"
pushing user:dcPwZX@registry.example/org/frontend failed
Plan: 1 to add, 0 to change, 0 to destroy.
`

// cleanToolLines are toolTranscript's lines that hold no credential.
var cleanToolLines = []string{
	"Terraform will perform the following actions:",
	`  + resource "terraform_data" "restore_guard" {`,
	"Plan: 1 to add, 0 to change, 0 to destroy.",
}

func assertToolOutputRedacted(t *testing.T, where, out string) {
	t.Helper()
	if !strings.Contains(out, "Plan: 1 to add") {
		t.Fatalf("%s never received the tool's output, so this proves nothing:\n%s", where, out)
	}
	assertNoCredential(t, where, out)
	if !strings.Contains(out, installflow.URLRedaction) {
		t.Errorf("%s shows no redaction:\n%s", where, out)
	}
	for _, l := range cleanToolLines {
		if !strings.Contains(out, l+"\n") {
			t.Errorf("%s changed a line with no credential in it (%q):\n%s", where, l, out)
		}
	}
}

// fakeTool writes an executable POSIX script called name into a fresh directory that
// prints toolTranscript on stdout and on stderr and exits with rc, and returns its path.
func fakeTool(t *testing.T, name string, rc string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX fake tool")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "transcript"), []byte(toolTranscript), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64","provider_selections":{}}'; exit 0 ;;
esac
cat "$(dirname "$0")/transcript"
cat "$(dirname "$0")/transcript" >&2
exit ` + rc + `
`
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A plain apply streams its plan; a failed one also carries its stderr in its error.
func TestAPlainTofuCommandRedactsCredentialsItStreams(t *testing.T) {
	testKube(t)
	for _, rc := range []string{"0", "1"} {
		t.Run("exit "+rc, func(t *testing.T) {
			bin := fakeTool(t, "tofu", rc)
			var applyErr error
			stdout, stderr := captureStdoutAndStderr(t, func() {
				tf, err := newTofuExec(t.TempDir(), bin, "")
				if err != nil {
					applyErr = err
					return
				}
				applyErr = tf.Apply(context.Background())
			})
			assertToolOutputRedacted(t, "the apply's stdout", stdout)
			assertToolOutputRedacted(t, "the apply's stderr", stderr)
			if rc == "0" && applyErr != nil {
				t.Fatalf("apply: %v", applyErr)
			}
			if rc == "1" {
				if applyErr == nil {
					t.Fatal("a failed apply returned no error")
				}
				assertNoCredential(t, "the apply's error", applyErr.Error())
			}
		})
	}
}

// Destroy streams through its outputs filter, and the URL filter behind it.
func TestATofuDestroyRedactsCredentialsItStreams(t *testing.T) {
	testKube(t)
	bin := fakeTool(t, "tofu", "0")
	stdout, stderr := captureStdoutAndStderr(t, func() {
		tf, err := newTofuExec(t.TempDir(), bin, "")
		if err != nil {
			t.Error(err)
			return
		}
		if err := tf.Destroy(context.Background()); err != nil {
			t.Error(err)
		}
	})
	assertToolOutputRedacted(t, "the destroy's stdout", stdout)
	assertToolOutputRedacted(t, "the destroy's stderr", stderr)
}

// An engine run's console (the JSON and TUI presenters' log lines) is where a tool
// writes when the runner was given it.
func TestATofuCommandOnTheRunsConsoleRedactsCredentials(t *testing.T) {
	testKube(t)
	bin := fakeTool(t, "tofu", "1")
	p := newProgressRun(t, false, installflow.StepInstallPrerequisites)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	tf.useConsole(out(p.ctx))
	applyErr := tf.Apply(p.ctx)
	if applyErr == nil {
		t.Fatal("a failed apply returned no error")
	}
	assertToolOutputRedacted(t, "the console's stdout", p.out.String())
	assertToolOutputRedacted(t, "the console's stderr", p.err.String())
	assertNoCredential(t, "the apply's error", applyErr.Error())
}

// The observed apply (what the JSON and TUI presenters ask for) prints the messages'
// lines and the diagnostics' text, and appends the diagnostics to its error.
func TestAnObservedApplyRedactsCredentialsInItsMessages(t *testing.T) {
	testKube(t)
	stream := readTofuFixture(t, "failed.jsonl")
	stream = bytes.Replace(stream, []byte(`Refreshing state... [id=`), []byte(`Refreshing state... [from=s3://u:dcPwZX@bucket/x id=`), -1)
	stream = append(stream, []byte(`{"@level":"error","@message":"Error: restoring from s3://u:dcPwZX@bucket/archive",`+
		`"type":"diagnostic","diagnostic":{"severity":"error","summary":"restoring from s3://u:dcPwZX@bucket/archive",`+
		`"detail":"the archive https://AKIDZX:dcSecretZX@s3.example.com/x was refused"}}`+"\n")...)
	bin, _ := fakeTofuJSON(t, struct {
		stream []byte
		rc     int
	}{stream, 1})
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	tf.useConsole(out(p.ctx))
	applyErr := tf.Apply(p.ctx)
	if applyErr == nil {
		t.Fatal("a failed apply returned no error")
	}
	if !strings.Contains(p.out.String()+p.err.String(), "restoring from") {
		t.Fatalf("the messages never reached the console, so this proves nothing:\n%s\n%s", p.out.String(), p.err.String())
	}
	assertNoCredential(t, "the console", p.out.String()+p.err.String())
	assertNoCredential(t, "the apply's error", applyErr.Error())
}

// ko quotes the repository it could not push to, user:password@registry and all.
func TestAFailedKoBuildRedactsCredentialsInItsOutput(t *testing.T) {
	bin := fakeTool(t, "ko", "1")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := koBuild(t.Context(), t.TempDir(), &State{ImageVersion: "dev"}, t.TempDir(), "registry.example/org/operator")
	if err == nil {
		t.Fatal("the build did not fail")
	}
	assertToolOutputRedacted(t, "the ko build error", err.Error())
}

func TestAFailedDockerCommandRedactsCredentialsInItsOutput(t *testing.T) {
	bin := fakeTool(t, "docker", "1")
	err := run(t.Context(), bin, "push", "registry.example/org/frontend:dev")
	if err == nil {
		t.Fatal("the command did not fail")
	}
	assertToolOutputRedacted(t, "the docker error", err.Error())
}

// kind streams its own progress through the console's command streams.
func TestTheCommandStreamsRedactCredentials(t *testing.T) {
	var o, e bytes.Buffer
	stdout, stderr, flush := console{out: &o, err: &e}.commandStreams()
	// Written in pieces, and the last line with no newline, as a tool writes.
	half := len(toolTranscript) / 2
	for _, w := range []interface{ Write([]byte) (int, error) }{stdout, stderr} {
		_, _ = w.Write([]byte(toolTranscript[:half]))
		_, _ = w.Write([]byte(toolTranscript[half:]))
		_, _ = w.Write([]byte("last words user:dcPwZX@registry.example/x"))
	}
	flush()
	for name, b := range map[string]string{"stdout": o.String(), "stderr": e.String()} {
		assertToolOutputRedacted(t, "the command's "+name, b)
		if !strings.HasSuffix(b, "last words "+installflow.URLRedaction+"@registry.example/x") {
			t.Errorf("the command's %s lost its last, unterminated line:\n%s", name, b)
		}
	}
}
