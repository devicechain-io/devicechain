// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

func wizardFor(t *testing.T, args ...string) (*installtui.Wizard, installchoice.Choices) {
	t.Helper()
	c := parseChoices(t, append([]string{"local", "--version", "v1.12.0"}, args...))
	prepared, err := c.Prepare(Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	return installWizardFor(provider, c, prepared), c
}

func replanned(t *testing.T, w *installtui.Wizard, ed installtui.Edits) (installtui.Replanned, bootstrap.InstallOptions) {
	t.Helper()
	r, err := w.Replan(ed)
	if err != nil {
		t.Fatalf("%+v: %v", ed, err)
	}
	ie, ok := r.Engine.(*bootstrap.InstallEngine)
	if !ok || r.Preflight == nil {
		t.Fatalf("%+v: replan returned %T and preflight %v", ed, r.Engine, r.Preflight)
	}
	opts, err := ie.ResolveOptions()
	if err != nil {
		t.Fatal(err)
	}
	return r, opts
}

// Every choice the screens make is resolved through the command line's own resolver,
// and the run it returns installs exactly that.
func TestTheWizardReplansEveryChoiceThroughTheResolver(t *testing.T) {
	saveChoiceFlags(t)
	on := true
	w, orig := wizardFor(t, "--kube-context", "prod")
	if w.Facts.TargetFlag != "--kube-context prod" || w.Contexts == nil {
		t.Fatalf("facts %+v", w.Facts)
	}

	r, opts := replanned(t, w, installtui.Edits{HA: &on})
	if !opts.HA || !r.Facts.HA || r.Facts.TopologyFlag != "" || r.Facts.TargetFlag != "--kube-context prod" {
		t.Errorf("HA: options %v, facts %+v", opts.HA, r.Facts)
	}

	r, opts = replanned(t, w, installtui.Edits{Target: &installtui.Target{KubeContext: "staging"}})
	if opts.KubeContext != "staging" || r.Facts.KubeContext != "staging" || r.Facts.TargetFlag != "" {
		t.Errorf("context: options %q, facts %+v", opts.KubeContext, r.Facts)
	}

	r, opts = replanned(t, w, installtui.Edits{Target: &installtui.Target{Cluster: "lab"}})
	if opts.KubeContext != "" || opts.Cluster != "lab" || r.Facts.KubeContext != "" || r.Facts.Cluster != "lab" {
		t.Errorf("kind: options %q/%q, facts %+v", opts.KubeContext, opts.Cluster, r.Facts)
	}

	_, opts = replanned(t, w, installtui.Edits{Toggles: map[string]bool{"no-monitoring": true, "no-cnpg": true}})
	if !opts.NoMonitoring || !opts.NoCNPG {
		t.Errorf("toggles: monitoring off %v, cnpg off %v", opts.NoMonitoring, opts.NoCNPG)
	}

	// Compact, keeping TLS (and with it backups) explicitly.
	_, opts = replanned(t, w, installtui.Edits{Compact: &on, Toggles: map[string]bool{"no-tls": false}})
	if !opts.Compact || opts.NoTLS || !opts.NoMonitoring {
		t.Errorf("compact + TLS: compact %v, no-tls %v, no-monitoring %v", opts.Compact, opts.NoTLS, opts.NoMonitoring)
	}

	// The command line's own choices are untouched by any of it.
	if orig.KubeContext != "prod" || orig.HA || orig.Given.Has("ha") || orig.Given.Has("no-monitoring") {
		t.Errorf("a replan changed the parsed choices: %+v", orig)
	}
}

// A combination the command line refuses is refused on the screen too, and nothing is
// repaired behind the operator's back.
func TestTheWizardRefusesWhatTheCommandLineRefuses(t *testing.T) {
	saveChoiceFlags(t)
	w, _ := wizardFor(t)
	for name, ed := range map[string]installtui.Edits{
		"--no-tls on Standard":  {Toggles: map[string]bool{"no-tls": true}},
		"a nameless kind":       {Target: &installtui.Target{}},
		"an editor that is not": {Toggles: map[string]bool{"skip-preflight": true}},
	} {
		if r, err := w.Replan(ed); err == nil || r.Engine != nil {
			t.Errorf("%s: replanned to %+v", name, r)
		}
	}
}

// Edits set exactly the flags they touch, explicitly; every other flag keeps the
// explicitness the command line gave it.
func TestEditsKeepTheCommandLinesExplicitness(t *testing.T) {
	saveChoiceFlags(t)
	orig := parseChoices(t, []string{"local", "--kube-context", "prod", "--no-monitoring=false"})
	c, err := applyWizardEdits(orig, installtui.Edits{})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range orig.Given {
		if c.Given[k] != v {
			t.Errorf("no edits changed the presence of --%s to %v", k, c.Given[k])
		}
	}
	on := true
	c, err = applyWizardEdits(orig, installtui.Edits{HA: &on, Target: &installtui.Target{Cluster: orig.Cluster}})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Given["ha"] || c.Given["kube-context"] || c.KubeContext != "" || c.Given["cluster"] || c.Given["compact"] ||
		!c.Given["no-monitoring"] || c.NoMonitoring {
		t.Errorf("edited presence %+v (context %q)", c.Given, c.KubeContext)
	}
}

// 🔴 The drawer drops no flag: every flag the command line gave is either on the
// cluster choices screen, an editor, or carried by name.
func TestTheDrawerCarriesEveryFlagGiven(t *testing.T) {
	saveChoiceFlags(t)
	presentation := map[string]bool{"help": true}
	for _, n := range installchoice.PresentationFlags() {
		presentation[n] = true
	}
	var args []string
	installCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if presentation[f.Name] {
			return
		}
		switch f.Value.Type() {
		case "bool":
			args = append(args, "--"+f.Name+"=true")
		case "int":
			args = append(args, "--"+f.Name+"=200")
		case "stringSlice":
			args = append(args, "--"+f.Name+"=k=v", "--"+f.Name+"=k2=v2")
		default:
			args = append(args, "--"+f.Name+"=value-"+f.Name)
		}
	})
	c := parseChoices(t, append([]string{"local"}, args...))
	shown := map[string]installtui.Setting{}
	for _, s := range wizardSettings(c, c, installtui.Edits{}) {
		shown[s.Flag] = s
	}
	n := 0
	for _, f := range c.Flags() {
		if choicesScreenFlags[f.Name] {
			continue
		}
		n++
		s, ok := shown["--"+f.Name]
		if !ok {
			t.Errorf("--%s was given and the drawer drops it", f.Name)
			continue
		}
		if s.Group == groupOther {
			t.Errorf("--%s has no section", f.Name)
		}
	}
	if n < 30 {
		t.Fatalf("only %d flags were checked", n)
	}
	if s := shown["--database-toleration"]; s.Value != "k=v, k2=v2" {
		t.Errorf("a repeated flag lost its order: %q", s.Value)
	}
}

// A credential is shown as configured, never by its value.
func TestTheDrawerHidesCredentials(t *testing.T) {
	saveChoiceFlags(t)
	const canary = "canary-hook-tok-5531"
	c := parseChoices(t, []string{"local", "--alert-webhook", "https://hooks.example/" + canary,
		"--alert-smtp-user", "ops-" + canary, "--aws-nlb", "https://u:" + canary + "@x.example"})
	for _, s := range wizardSettings(c, c, installtui.Edits{}) {
		if strings.Contains(fmt.Sprintf("%+v", s), canary) {
			t.Errorf("a credential reached the drawer: %+v", s)
		}
		if s.Flag == "--alert-webhook" && s.Value != configuredHidden {
			t.Errorf("the webhook reads %q", s.Value)
		}
	}
}

// An editor says where its value came from, and Compact's presets say so.
func TestTheDrawerSaysWhereEachValueCameFrom(t *testing.T) {
	saveChoiceFlags(t)
	w, _ := wizardFor(t, "--compact", "--no-cnpg=false")
	by := map[string]installtui.Setting{}
	for _, s := range w.Settings {
		by[s.Flag] = s
	}
	if s := by["--no-monitoring"]; !s.Toggle || s.Value != "off" || s.Source != "preset by Compact" {
		t.Errorf("monitoring %+v", s)
	}
	if s := by["--no-cnpg"]; s.Value != "on" || s.Source != "from --no-cnpg=false" {
		t.Errorf("cnpg %+v", s)
	}
	if s := by["--no-tls"]; s.Disabled != "" || s.Value != "off" {
		t.Errorf("tls on compact %+v", s)
	}
	r, _ := replanned(t, w, installtui.Edits{Toggles: map[string]bool{"no-monitoring": false}})
	for _, s := range r.Settings {
		if s.Flag == "--no-monitoring" && (s.Value != "on" || s.Source != "changed in this installer") {
			t.Errorf("after the toggle %+v", s)
		}
	}
	w, _ = wizardFor(t)
	for _, s := range w.Settings {
		if s.Flag == "--no-tls" && s.Disabled == "" {
			t.Errorf("TLS is editable on Standard, where --no-tls is refused: %+v", s)
		}
	}
}

// The context picker lists the kubeconfig's names, sorted, and nothing else.
func TestTheContextPickerReadsTheKubeconfig(t *testing.T) {
	cfg := strings.Join([]string{
		"apiVersion: v1",
		"kind: Config",
		"contexts:",
		"- name: zeta",
		"  context: {cluster: c, user: u}",
		"- name: alpha",
		"  context: {cluster: c, user: u}",
		"clusters:",
		"- name: c",
		"  cluster: {server: \"https://127.0.0.1:1\"}",
		"users:",
		"- name: u",
		"  user: {token: canary-kube-token-88}",
		"current-context: zeta",
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	names, err := kubeContextNames()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("contexts %v", names)
	}
}
