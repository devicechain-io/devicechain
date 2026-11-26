// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/spf13/pflag"
)

// parseChoices uses the production Cobra command and its production bindings.
// It resets values AND presence, including pflag's accumulating StringSlices.
func parseChoices(t *testing.T, args []string) installchoice.Choices {
	t.Helper()
	installCmd.InitDefaultHelpFlag()
	fs := installCmd.Flags()
	fs.VisitAll(func(f *pflag.Flag) {
		if v, ok := f.Value.(pflag.SliceValue); ok {
			if err := v.Replace(nil); err != nil {
				t.Fatal(err)
			}
		} else if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatal(err)
		}
		f.Changed = false
	})
	if err := installCmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	if err := installCmd.Args(installCmd, fs.Args()); err != nil {
		t.Fatal(err)
	}
	c, err := installChoices(installCmd)
	if err != nil {
		t.Fatal(err)
	}
	c.Provider = fs.Args()[0]
	return c
}

func saveChoiceFlags(t *testing.T) {
	t.Helper()
	installCmd.InitDefaultHelpFlag()
	installCmd.Flags().VisitAll(func(f *pflag.Flag) {
		changed, value := f.Changed, f.Value.String()
		if v, ok := f.Value.(pflag.SliceValue); ok {
			slice := append([]string(nil), v.GetSlice()...)
			t.Cleanup(func() {
				if err := v.Replace(slice); err != nil {
					t.Fatal(err)
				}
				f.Changed = changed
			})
		} else {
			t.Cleanup(func() {
				if err := f.Value.Set(value); err != nil {
					t.Fatal(err)
				}
				f.Changed = changed
			})
		}
	})
}

func TestInstallChoicesCobraRoundTrip(t *testing.T) {
	saveChoiceFlags(t)
	oldImageVersion, oldVersion, oldEngine := bootstrap.DefaultImageVersion, Version, installEngine
	bootstrap.DefaultImageVersion, Version = "v1.9.0", "1.9.0"
	t.Cleanup(func() { bootstrap.DefaultImageVersion, Version, installEngine = oldImageVersion, oldVersion, oldEngine })
	dir := t.TempDir()
	write := func(name, contents string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pass := write("smtp '雪'.txt", "dcCanarySmtpPw_Lq4Hd9Xe6Gu\n")
	backup := write("backup '雪'.json", `{"endpointUrl":"https://s3.example.test","bucketRdb":"rdb","bucketTsdb":"tsdb","accessKeyId":"key","secretAccessKey":"dcCanaryS3SecretKey/Rb8Nf3Jw6Yc1Tg5"}`)
	config := write("alerts '雪'.yaml", "route: {receiver: r}\nreceivers:\n- name: r\n  webhook_configs:\n  - url: https://hooks.example.invalid/services/dcCanaryHOOK/T9xK2mP7vQ4\n")
	cases := []struct {
		name string
		args []string
	}{
		{"defaults", nil},
		{"explicit false and zero", []string{"--compact", "--no-tls=false", "--no-monitoring=false", "--mqtt-gateway=false", "--relational-store-max-wal-mib=0", "--max-connections=0", "--yes=false"}},
		{"repeated scalar and slices", []string{"--cluster=first", "--cluster=last", "--database-node-selector=z=last", "--database-node-selector=a=first", "--database-toleration=z:NoSchedule", "--database-toleration=a:NoExecute"}},
		{"spaces quotes unicode and shell syntax", []string{"--kube-context=~ O'Brien \"雪\" $x` $(touch nope); &", "--cluster=", "--registry=registry.example.test/雪 space", "--version=release-'\"雪"}},
		{"CSV recipients", []string{"--alert-email=\"\"\"雪, Ops\"\" <ops@example.test>\"", "--alert-email=other@example.test", "--alert-smtp-host=smtp.example.test:587", "--alert-smtp-from=from@example.test", "--alert-smtp-user=mailer", "--alert-smtp-password-file=" + pass}},
		{"file references", []string{"--backup-credentials-file=" + backup, "--backup-snapshot-class=fast", "--alert-config=" + config}},
		{"recovery", []string{"--restore-rdb-from=dc-rdb", "--restore-rdb-at=2026-01-02T03:04:05Z", "--relational-store-max-wal-mib=2048", "--max-connections=900"}},
		{"dev", []string{"--dev", "--compact", "--ha", "--dry-run"}},
		{"size alias", []string{"--size=compact", "--no-tls=false"}},
		{"remaining flags", []string{"--allow-legacy-db-removal=false", "--aws-nlb=traefik", "--build=false", "--clear-alert-routing=false", "--compact=false", "--dev=false", "--dry-run=false", "--ha=false", "--help=false", "--no-cnpg=false", "--no-monitoring=false", "--no-tls=false", "--remove-legacy-ingress=false", "--alert-webhook=", "--alert-watchdog-webhook=", "--version=v1.9.0", "-y"}},
		{"empty repeatable", []string{"--alert-email=", "--database-node-selector=", "--database-toleration="}},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"local", "--skip-preflight"}, tc.args...)
			before := parseChoices(t, args)
			for name, given := range before.Given {
				covered[name] = covered[name] || given
			}
			plan, err := before.Resolve(Version)
			if err != nil {
				t.Fatal(err)
			}
			// Prove RunE actually uses that same resolver and engine options.
			called := false
			installEngine = func(_ctx context.Context, _ bootstrap.Provider, opts bootstrap.InstallOptions) error {
				called = true
				if !reflect.DeepEqual(opts, plan.Options) {
					t.Fatal("CLI and choice resolver produced different options")
				}
				return nil
			}
			if err := installCmd.RunE(installCmd, []string{"local"}); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("CLI did not hand resolved options to engine")
			}
			for _, shell := range []installchoice.Shell{installchoice.POSIX, installchoice.PowerShell} {
				command, err := plan.Command(shell, false)
				if err != nil {
					t.Fatal(err)
				}
				if command.Template {
					t.Fatal("file references and empty credentials must stay reproducible")
				}
				testkit.AssertNoCanary(t, "command", testkit.Dump(command))
				argv := splitChoiceCommand(t, command.Line, shell)
				if !reflect.DeepEqual(argv, command.Argv) {
					t.Fatalf("shell command decoded to %q, want %q", argv, command.Argv)
				}
				after := parseChoices(t, argv[2:])
				if !reflect.DeepEqual(before, after) {
					t.Fatal("Cobra round trip changed choices or presence")
				}
				resolved, err := after.Resolve(Version)
				if err != nil || !reflect.DeepEqual(plan.Options, resolved.Options) {
					t.Fatalf("Cobra round trip changed InstallOptions: %v", err)
				}
			}
		})
	}
	// The committed S1 inventory is independent of the new choice schema.
	data, err := os.ReadFile("testdata/install_golden/flags-install.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "--") && !covered[strings.TrimSpace(line[2:])] {
			t.Errorf("round-trip table misses %s", line)
		}
	}
}

// Decode the emitted literal shell subset independently of production quoting.
// POSIX supports adjacent single/double quoted spans; PowerShell doubles a quote
// inside a literal. Expansions and operators are forbidden outside literals.
func splitChoiceCommand(t *testing.T, line string, shell installchoice.Shell) []string {
	t.Helper()
	if shell == installchoice.PowerShell {
		if !strings.HasPrefix(line, "& ") {
			t.Fatal("missing PowerShell call operator")
		}
		line = line[2:]
	}
	var args []string
	var word strings.Builder
	var quote byte
	active := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				if shell == installchoice.PowerShell && i+1 < len(line) && line[i+1] == quote {
					word.WriteByte(ch)
					i++
				} else {
					quote = 0
				}
			} else {
				word.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote, active = ch, true
		case ' ':
			if active {
				args = append(args, word.String())
				word.Reset()
				active = false
			}
		default:
			t.Fatalf("unquoted shell syntax in %q", line)
		}
	}
	if quote != 0 {
		t.Fatal("unterminated shell literal")
	}
	if active {
		args = append(args, word.String())
	}
	return args
}
