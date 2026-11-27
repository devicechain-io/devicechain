// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

// A replanned run's preflight is the doctor, and the form the screen runs (bound to its
// context) runs its host commands under the context the screen hands it: when the
// screen replaces the plan and cancels that context, the commands end and the run
// returns. Nothing is left running behind it.
func TestAReplannedPreflightStopsWithItsContext(t *testing.T) {
	saveChoiceFlags(t)
	saveRun, saveLook, saveHost := runCmd, lookPath, hostCommand
	t.Cleanup(func() { runCmd, lookPath, hostCommand = saveRun, saveLook, saveHost })
	runCmd = nil
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	var started, afterCancel atomic.Int32
	cancelled := make(chan struct{})
	hostCommand = func(ctx context.Context, _ string, _ ...string) string {
		started.Add(1)
		<-ctx.Done() // a tool that ends only when its context kills it
		select {
		case <-cancelled:
			afterCancel.Add(1)
		default:
		}
		return ""
	}
	w, _ := wizardFor(t)
	on := true
	r, err := w.Replan(installtui.Edits{Compact: &on})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	b, ok := r.Preflight.(interface {
		BoundToContext() installflow.PreflightRunner
	})
	if !ok {
		t.Fatalf("the replanned preflight %T has no form bound to the screen's context", r.Preflight)
	}
	go func() {
		_, err := b.BoundToContext().RunPreflight(ctx, func(installflow.PreflightObservation) {})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for started.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the replanned preflight ran no host command")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(cancelled)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the replanned preflight kept running after its context was cancelled")
	}
	if afterCancel.Load() == 0 {
		t.Fatal("no host command ended with the cancelled context")
	}
}

// Backups are read from the plan through the function the install decides them with:
// --no-cnpg turns them off on Standard too, and Compact keeps them only with TLS.
func TestBackupsFollowTheEffectivePlan(t *testing.T) {
	saveChoiceFlags(t)
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--no-cnpg"}, false},
		{[]string{"--compact"}, false},
		{[]string{"--compact", "--no-tls=false"}, true},
		{[]string{"--compact", "--no-tls=false", "--no-cnpg"}, false},
	} {
		w, _ := wizardFor(t, c.args...)
		eff := choicesEffective(t, c.args)
		if want := bootstrap.DatabaseBackupsEnabled(eff.NoCNPG, eff.Compact, eff.NoTLS); want != c.want {
			t.Fatalf("%v: the case expects %v, the plan says %v", c.args, c.want, want)
		}
		if w.Facts.Backups != c.want {
			t.Errorf("%v: facts say backups %v", c.args, w.Facts.Backups)
		}
		found := false
		for _, s := range w.Settings {
			if s.Label == "Database backups" {
				found = true
				if s.Value != onOff(c.want) || s.Toggle {
					t.Errorf("%v: the drawer says backups %+v", c.args, s)
				}
			}
			if s.Flag == "--no-tls" && s.Label != "TLS (cert-manager)" {
				t.Errorf("%v: the TLS editor still claims backups: %+v", c.args, s)
			}
		}
		if !found {
			t.Errorf("%v: no backups row", c.args)
		}
	}
	// And after a change on screen.
	w, _ := wizardFor(t)
	r, _ := replanned(t, w, installtui.Edits{Toggles: map[string]bool{"no-cnpg": true}})
	if r.Facts.Backups {
		t.Error("--no-cnpg chosen in the drawer left backups on")
	}
}

func choicesEffective(t *testing.T, args []string) effectiveShape {
	t.Helper()
	c := parseChoices(t, append([]string{"local", "--version", "v1.12.0"}, args...))
	p, err := c.Prepare(Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	return effectiveShape{p.Effective.NoCNPG, p.Effective.Compact, p.Effective.NoTLS}
}

type effectiveShape struct{ NoCNPG, Compact, NoTLS bool }

// Compact's monitoring and TLS are told from the plan: kept when kept explicitly, on the
// command line or in the drawer.
func TestCompactsKeptSettingsAreToldFromThePlan(t *testing.T) {
	saveChoiceFlags(t)
	w, _ := wizardFor(t, "--compact")
	if w.Facts.Monitoring || w.Facts.TLS {
		t.Fatalf("compact facts %+v", w.Facts)
	}
	w, _ = wizardFor(t, "--compact", "--no-monitoring=false", "--no-tls=false")
	if !w.Facts.Monitoring || !w.Facts.TLS || !w.Facts.Backups {
		t.Fatalf("kept facts %+v", w.Facts)
	}
	w, _ = wizardFor(t, "--compact")
	r, _ := replanned(t, w, installtui.Edits{Toggles: map[string]bool{"no-monitoring": false}})
	if !r.Facts.Monitoring {
		t.Fatal("monitoring kept in the drawer is told as gone")
	}
}

// Switching to a local kind cluster from --kube-context with --cluster= resolves the
// name the way the command line does: empty is the default cluster, never a refusal.
func TestSwitchingToKindDefaultsTheNameAsTheCommandLineDoes(t *testing.T) {
	saveChoiceFlags(t)
	w, orig := wizardFor(t, "--kube-context", "prod", "--cluster=")
	if w.Facts.Cluster != bootstrap.DefaultClusterName || w.Facts.TargetFlag != "--kube-context prod" {
		t.Fatalf("facts %+v", w.Facts)
	}
	for _, name := range []string{w.Facts.Cluster, ""} {
		r, opts := replanned(t, w, installtui.Edits{Target: &installtui.Target{Cluster: name}})
		if opts.KubeContext != "" || kindClusterName(opts.Cluster) != bootstrap.DefaultClusterName ||
			r.Facts.Cluster != bootstrap.DefaultClusterName {
			t.Errorf("kind %q: options %q/%q, facts %+v", name, opts.KubeContext, opts.Cluster, r.Facts)
		}
		c, err := applyWizardEdits(orig, installtui.Edits{Target: &installtui.Target{Cluster: name}})
		if err != nil || c.Cluster != "" || !c.Given["cluster"] {
			t.Errorf("kind %q: --cluster became %q (given %v): %v", name, c.Cluster, c.Given["cluster"], err)
		}
	}
	_, opts := replanned(t, w, installtui.Edits{Target: &installtui.Target{Cluster: "lab"}})
	if opts.Cluster != "lab" {
		t.Errorf("a named kind cluster: %q", opts.Cluster)
	}
}
