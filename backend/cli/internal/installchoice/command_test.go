// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

func TestCommandApprovalAndFileReferences(t *testing.T) {
	file := filepath.Join(t.TempDir(), "~ smtp '雪'.txt")
	var contents strings.Builder
	for _, c := range testkit.Canaries() {
		contents.WriteString(c.Value + "\n")
	}
	if err := os.WriteFile(file, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.AssumeYes = false
	c.Alerts.SMTPPassFile, c.Alerts.Config, c.BackupCredentials = file, file, file
	for _, name := range []string{"yes", "alert-smtp-password-file", "alert-config", "backup-credentials-file"} {
		c.Given[name] = true
	}
	for _, shell := range []Shell{POSIX, PowerShell} {
		got, err := (Plan{choices: c}).Command(shell, true)
		if err != nil || got.Template {
			t.Fatalf("file reference command: template=%v, error=%v", got.Template, err)
		}
		if !strings.Contains(strings.Join(got.Argv, " "), "--yes=true") || !strings.Contains(strings.Join(got.Argv, " "), file) {
			t.Fatal("approval or protected-file reference was lost")
		}
		testkit.AssertNoCanary(t, "protected-file command", testkit.Dump(got))
	}
	if c.AssumeYes || !c.Given.Has("yes") {
		t.Fatal("approval modified original choices")
	}
}

func TestCommandRedactsCredentialURLs(t *testing.T) {
	for _, value := range []string{"https://user:secret-value@example.test/path", "https://example.test/path?token=secret-value", "https://example.test/path?custom-key=secret-value", "https://example.test/path#secret-value", "https://user:secret-value@example.test/%invalid"} {
		c := Choices{Registry: value, Given: Presence{"registry": true}}
		got, err := (Plan{choices: c}).Command(PowerShell, false)
		if err != nil || !got.Template || strings.Contains(got.Line, "secret-value") {
			t.Fatal("URL credentials escaped redaction")
		}
	}
	if _, err := (Plan{}).Command(Shell(255), false); err == nil {
		t.Fatal("unknown shell accepted")
	}
}

func TestCommandEmptyStringSliceValue(t *testing.T) {
	c := Choices{Alerts: Alerts{Email: []string{"", "\"雪, Ops\" <ops@example.test>"}}, Given: Presence{"alert-email": true}}
	got, err := (Plan{choices: c}).Command(POSIX, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Argv[3] != "--alert-email=\"\"" {
		t.Fatal("one empty slice value was confused with an empty slice")
	}
}

func TestCommandPresenceOrderAndQuoting(t *testing.T) {
	c := Choices{Cluster: "~ O'Brien 雪 $x`", Compact: true,
		Given: Presence{"cluster": true, "compact": true, "no-tls": true, "mqtt-gateway": true,
			"database-node-selector": true}, DBNodeSelector: []string{"z=last", "a=first"}}
	p := Plan{choices: c}
	for _, shell := range []Shell{POSIX, PowerShell} {
		got, err := p.Command(shell, false)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"dcctl", "install", "local", "--cluster=" + c.Cluster, "--compact=true",
			"--database-node-selector=z=last", "--database-node-selector=a=first", "--mqtt-gateway=false", "--no-tls=false"}
		if !reflect.DeepEqual(got.Argv, want) {
			t.Fatalf("argv = %q, want %q", got.Argv, want)
		}
		if shell == PowerShell && !strings.Contains(got.Line, "O''Brien") {
			t.Fatal("PowerShell single quote was not doubled")
		}
		if shell == POSIX && !strings.Contains(got.Line, "O'\"'\"'Brien") {
			t.Fatal("POSIX single quote was not escaped")
		}
		// The words are the line's own, so a screen can break it only between them.
		lead := 0
		if shell == PowerShell {
			lead = 1
			if got.Words[0] != "&" {
				t.Fatalf("words %q", got.Words)
			}
		}
		if strings.Join(got.Words, " ") != got.Line || len(got.Words) != len(got.Argv)+lead {
			t.Fatalf("words %q do not make line %q", got.Words, got.Line)
		}
	}
}

func TestCommandSecretCanaries(t *testing.T) {
	for _, secret := range testkit.Canaries() {
		for _, shell := range []Shell{POSIX, PowerShell} {
			p := Plan{choices: Choices{Alerts: Alerts{Webhook: secret.Value, Watchdog: secret.Value},
				Given: Presence{"alert-webhook": true, "alert-watchdog-webhook": true}}}
			cmd, err := p.Command(shell, true)
			if err != nil {
				t.Fatal(err)
			}
			if !cmd.Template || !strings.Contains(cmd.Line, "<redacted: supply privately>") {
				t.Fatal("credential command must be labelled template")
			}
			testkit.AssertNoCanary(t, "command", testkit.Dump(cmd))
		}
	}
}

// Windows PowerShell 5.1 passes a value with a double quote to a native program
// lossily, so the PowerShell command renders it as a placeholder and is a template;
// the POSIX command, which passes it exactly, is not.
func TestPowerShellRefusesWhatItCannotPassExactly(t *testing.T) {
	c := Choices{Given: Presence{"alert-email": true, "kube-context": true},
		Alerts: Alerts{Email: []string{`"Ops, Team" <ops@example.test>`}}, KubeContext: `C:\dir with space\`}
	ps, err := Plan{choices: c}.Command(PowerShell, false)
	if err != nil {
		t.Fatal(err)
	}
	if !ps.Template || strings.Contains(ps.Line, "Ops") || strings.Contains(ps.Line, `space\`) ||
		strings.Count(ps.Line, powerShellValue) != 2 {
		t.Fatalf("PowerShell %+v", ps)
	}
	sh, err := Plan{choices: c}.Command(POSIX, false)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Template || !strings.Contains(sh.Line, "Ops") {
		t.Fatalf("POSIX %+v", sh)
	}
}
