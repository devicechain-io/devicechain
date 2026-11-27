// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
)

// 🔴 THE REVIEW'S COMMAND IS THE PLAN. For the command line's request and for every
// edit the screens can make, the equivalent command, parsed back through the real Cobra
// flag set and resolved through the real resolver, is the plan the run was built from:
// the same options and the same explicitness, with --yes for the approval and no
// presentation flag.
func TestTheReviewCommandResolvesToThePlanItShows(t *testing.T) {
	saveChoiceFlags(t)
	on, off := true, false
	cases := []struct {
		name string
		args []string
		ed   installtui.Edits
	}{
		{"defaults", nil, installtui.Edits{}},
		{"compact keeping TLS", []string{"--no-monitoring=false"},
			installtui.Edits{Compact: &on, Toggles: map[string]bool{"no-tls": false}}},
		{"size alias replaced by the screen", []string{"--size", "compact"}, installtui.Edits{Compact: &off}},
		{"ha on an existing context", []string{"--kube-context", "prod"}, installtui.Edits{HA: &on}},
		{"back to kind, renamed", []string{"--kube-context", "prod", "--cluster", "first"},
			installtui.Edits{Target: &installtui.Target{Cluster: "lab 'x'"}}},
		{"toggles and carried flags", []string{"--max-connections", "900", "--database-node-selector", "pool=db", "--database-toleration", "z:NoSchedule",
			"--database-toleration", "a:NoExecute", "--yes=false"},
			installtui.Edits{Toggles: map[string]bool{"no-monitoring": true, "no-cnpg": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveChoiceFlags(t)
			w, orig := wizardFor(t, append([]string{"--ui", "tui", "--no-splash"}, tc.args...)...)
			_, want := replanned(t, w, tc.ed)
			edited, err := applyWizardEdits(orig, tc.ed)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := edited.Prepare(Version, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, sh := range []struct {
				result result.Shell
				choice installchoice.Shell
			}{{result.POSIX, installchoice.POSIX}, {result.PowerShell, installchoice.PowerShell}} {
				cmd := wizardCommand(prepared, sh.result)
				if cmd.Line == "" || strings.Join(cmd.Args, " ") != cmd.Line || cmd.Template {
					t.Fatalf("command %+v", cmd)
				}
				for _, flag := range []string{"--ui", "--no-splash"} {
					if strings.Contains(cmd.Line, flag) {
						t.Fatalf("the command carries %s: %s", flag, cmd.Line)
					}
				}
				argv := splitChoiceCommand(t, cmd.Line, sh.choice)
				if sh.choice == installchoice.PowerShell {
					argv = ps51Native(argv) // what dcctl.exe receives from Windows PowerShell 5.1
				}
				if argv[0] != "dcctl" || argv[1] != "install" || argv[2] != "local" {
					t.Fatalf("argv %q", argv)
				}
				saveChoiceFlags(t)
				back := parseChoices(t, argv[2:])
				// The approval is --yes; everything else is exactly the edited request.
				wantChoices := edited.Clone()
				wantChoices.AssumeYes, wantChoices.Given["yes"] = true, true
				if !reflect.DeepEqual(back, wantChoices) {
					t.Fatalf("parsed back:\n%+v\nwant:\n%+v", back, wantChoices)
				}
				plan, err := back.Resolve(Version)
				if err != nil {
					t.Fatal(err)
				}
				got := plan.Options
				wantOpts := want
				wantOpts.AssumeYes = true
				if !reflect.DeepEqual(got, wantOpts) {
					t.Fatalf("the command resolves to\n%+v\nthe plan is\n%+v", got, wantOpts)
				}
			}
		})
	}
}

// Each replan carries its own review and command, so the review never shows the plan
// an edit replaced.
func TestEveryReplanCarriesItsOwnReview(t *testing.T) {
	saveChoiceFlags(t)
	on := true
	w, _ := wizardFor(t)
	if !strings.Contains(w.Command.Line, "--yes=true") || strings.Contains(w.Command.Line, "--compact") {
		t.Fatalf("the command line's command: %s", w.Command.Line)
	}
	r, _ := replanned(t, w, installtui.Edits{Compact: &on})
	if !strings.Contains(r.Command.Line, "--compact=true") {
		t.Fatalf("the compact plan's command: %s", r.Command.Line)
	}
	text := reviewText(r.Review)
	for _, want := range []string{"Compact footprint, Single", "Monitoring: off", "TLS: off", "Backups: off",
		"no database backups", "clusters", assignedAfterIdentification, "infra", "created if it does not exist"} {
		if !strings.Contains(text, want) {
			t.Errorf("the compact review does not say %q:\n%s", want, text)
		}
	}
}

func reviewText(r installtui.Review) string {
	var b strings.Builder
	for _, row := range r.Rows {
		b.WriteString(row.Label + ": " + row.Value + "\n")
	}
	for _, w := range r.Warnings {
		b.WriteString("warning: " + w + "\n")
	}
	return b.String()
}

// A credential never reaches the review: the webhook is a placeholder in the command,
// which is then a template, and no row carries it.
func TestTheReviewHidesCredentials(t *testing.T) {
	saveChoiceFlags(t)
	const hook = "https://hooks.example.invalid/services/dcCanaryHOOK/T9xK2mP7vQ4"
	w, _ := wizardFor(t, "--alert-webhook", hook, "--skip-preflight", "--kube-context", "prod")
	if !w.Command.Template || strings.Contains(w.Command.Line, "dcCanary") ||
		!strings.Contains(w.Command.Line, "<redacted: supply privately>") {
		t.Fatalf("command %+v", w.Command)
	}
	testkit.AssertNoCanary(t, "review", []byte(reviewText(w.Review)))
	text := reviewText(w.Review)
	for _, want := range []string{"existing cluster, context prod", "--skip-preflight"} {
		if !strings.Contains(text, want) {
			t.Errorf("the review does not say %q:\n%s", want, text)
		}
	}
}

// 🔴 A URL'S CREDENTIAL NEVER REACHES THE REVIEW. --restore-rdb-from carrying userinfo
// passes preparation; the review row hides it by the serialiser's own classification,
// the command has a placeholder and is a template, and its credential parts are handed
// to the presenter to register, so every other surface is redacted of them too.
func TestTheReviewHidesAURLsUserinfo(t *testing.T) {
	saveChoiceFlags(t)
	const uri = "s3://dcuser:dcCanaryRestorePw_93Kq@bucket/archive"
	w, _ := wizardFor(t, "--restore-rdb-from", uri)
	text := reviewText(w.Review)
	if strings.Contains(text, "dcCanaryRestorePw_93Kq") || strings.Contains(text, "dcuser") ||
		!strings.Contains(text, configuredHidden) {
		t.Fatalf("the review shows the restore source:\n%s", text)
	}
	if !w.Command.Template || strings.Contains(w.Command.Line, "dcCanaryRestorePw_93Kq") {
		t.Fatalf("command %+v", w.Command)
	}
	var registered bool
	for _, s := range w.Secrets {
		registered = registered || s == "dcCanaryRestorePw_93Kq"
	}
	if !registered {
		t.Fatalf("the password is not handed to the hub: %q", w.Secrets)
	}
}

// 🔴 THE REVIEW PROMISES NO RESTORE IT CANNOT KNOW HAPPENS. A restore is read only when
// the relational database is created; the plan does not read the cluster, so it says so.
func TestTheReviewWordsARestoreConditionally(t *testing.T) {
	saveChoiceFlags(t)
	w, _ := wizardFor(t, "--restore-rdb-from", "dc-rdb")
	text := reviewText(w.Review)
	if !strings.Contains(text, "restores the relational store from dc-rdb if this install creates it") ||
		!strings.Contains(text, "an existing relational database is left as it is") ||
		strings.Contains(text, "is restored from") || strings.Contains(text, "is rebuilt from") {
		t.Fatalf("the restore is promised unconditionally:\n%s", text)
	}
}

// 🔴 COPYING THE DRAWN LINES RUNS THE SAME COMMAND. At every width the review can be
// drawn at, the lines joined as the shell joins continued lines decode to the exact
// argv; an argument is never split, and one too wide for the width is not drawn.
func TestTheDrawnCommandCopiesExactly(t *testing.T) {
	saveChoiceFlags(t)
	path := "/home/op/a-rather-long-directory-name/alerting/alertmanager-routing-configuration.yaml"
	prepared, err := parseChoices(t, []string{"local", "--version", "v1.12.0", "--kube-context", "prod cluster",
		"--alert-config", path, "--max-connections", "900"}).Prepare(Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sh := range []struct {
		result result.Shell
		choice installchoice.Shell
		cont   string
	}{{result.POSIX, installchoice.POSIX, " \\"}, {result.PowerShell, installchoice.PowerShell, " `"}} {
		c := wizardCommand(prepared, sh.result)
		want := splitChoiceCommand(t, c.Line, sh.choice)
		for width := 20; width <= 200; width++ {
			lines, ok := result.CommandLines(c, sh.result, nil, width)
			longest := 0
			for _, a := range c.Args {
				longest = max(longest, len(a))
			}
			if !ok {
				if longest+len(sh.cont) <= width {
					t.Fatalf("%d columns: not drawn, though every word fits", width)
				}
				continue
			}
			var b strings.Builder
			for i, l := range lines {
				if len(l) > width {
					t.Fatalf("%d columns: line %q is wider", width, l)
				}
				if i < len(lines)-1 {
					if !strings.HasSuffix(l, sh.cont) {
						t.Fatalf("%d columns: line %q has no continuation", width, l)
					}
					// The shell removes the continuation character and the line break.
					l = strings.TrimSuffix(l, sh.cont[1:])
				}
				b.WriteString(l)
			}
			if got := splitChoiceCommand(t, b.String(), sh.choice); !reflect.DeepEqual(got, want) {
				t.Fatalf("%d columns: copied %q, want %q", width, got, want)
			}
		}
	}
}

// 🔴 AN EMPTY --cluster= REVIEWS AND APPROVES THE CLUSTER IT RESOLVES TO. The local
// provider takes an empty name as the default kind cluster, so the review names that
// one, and the approval (made from the screens' facts) answers the run's question to
// create it.
func TestAnEmptyClusterNameReviewsTheDefaultCluster(t *testing.T) {
	saveChoiceFlags(t)
	w, _ := wizardFor(t, "--cluster=")
	if w.Facts.Cluster != bootstrap.DefaultClusterName {
		t.Fatalf("facts name kind cluster %q", w.Facts.Cluster)
	}
	text := reviewText(w.Review)
	if !strings.Contains(text, "local kind cluster "+bootstrap.DefaultClusterName+": created") {
		t.Fatalf("the review names another cluster:\n%s", text)
	}
}
