// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/dcdir"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The presenter flags. --ui defaults to plain: without it, every existing invocation —
// on a terminal or off one — prints, prompts and exits exactly as it always has.
var (
	installUI       string
	installNoSplash bool
)

const (
	installUIUsage = "how the install presents itself: plain (the text output dcctl has always printed), " +
		"tui (a full-screen terminal UI, when stdin, stdout and stderr are all an interactive terminal; " +
		"otherwise plain, unchanged), or auto (the terminal UI on an interactive terminal, and plain " +
		"everywhere else, including with --yes or --dev). --dry-run is always plain"
	installNoSplashUsage = "with the terminal UI: skip the DeviceChain logo shown before the install starts"
)

// parseInstallUI refuses a --ui value that is not one of the three, before anything is
// printed or touched.
func parseInstallUI(v string) (terminal.UI, error) {
	switch ui := terminal.UI(v); ui {
	case terminal.Plain, terminal.TUI, terminal.Auto:
		return ui, nil
	}
	return "", fmt.Errorf("--ui must be plain, tui or auto (got %q)", v)
}

// installUIRequest is what the presenter choice reads from the command line.
type installUIRequest struct {
	UI                         terminal.UI
	Yes, Dev, DryRun, NoSplash bool
}

// restoreProbedConsole puts back a console mode the probe could not restore at the time.
var restoreProbedConsole = installtui.RestoreProbedConsole

// probeInstallTerminal observes the process's streams. Tests replace it.
var probeInstallTerminal = func() terminal.Inputs { return installtui.Probe(os.Stdin, os.Stdout, os.Stderr) }

// selectInstallPresenter is the presenter selection table:
//
//	--ui           terminal                                  presenter
//	plain (default) anything                                  plain, byte for byte as today
//	tui            stdin, stdout and stderr all a terminal,   terminal UI (progress-only with
//	               not CI, TERM not dumb, a VT console          --yes or --dev)
//	tui            anything else                              plain, byte for byte
//	auto           as tui, without --yes or --dev             terminal UI
//	auto           with --yes or --dev, or not eligible       plain, byte for byte
//	any            --dry-run                                  plain (today's rehearsal)
//
// 🔴 PLAIN NEVER PROBES. --ui=plain returns before the terminal is looked at, so the
// default path cannot be changed by anything the probe does or finds.
func selectInstallPresenter(r installUIRequest, probe func() terminal.Inputs) (terminal.Capabilities, bool) {
	if r.UI != terminal.TUI && r.UI != terminal.Auto {
		return terminal.Capabilities{}, false
	}
	// auto is the default-to-be: it must leave every unattended run exactly as it is.
	// --yes (and --dev, which implies it) is how a script runs install, so auto keeps it
	// plain; --ui=tui --yes asks for the progress-only screen explicitly.
	if r.UI == terminal.Auto && (r.Yes || r.Dev) {
		return terminal.Capabilities{}, false
	}
	in := probe()
	in.UI, in.Yes, in.Dev, in.DryRun, in.NoSplash = r.UI, r.Yes, r.Dev, r.DryRun, r.NoSplash
	caps := terminal.Evaluate(in)
	return caps, caps.TUI
}

// installPresenter runs one install and renders it: installplain.Presenter or
// installtui.Presenter.
type installPresenter interface {
	Run(ctx context.Context, e installflow.Engine, req installflow.Request, preflight installflow.PreflightRunner) error
}

// newInstallTUI builds the terminal-UI presenter on the process's own streams. Tests
// replace it, so that the choice can be checked without a terminal to take over.
var newInstallTUI = func(caps terminal.Capabilities, wiz *installtui.Wizard) installPresenter {
	// It owns the process's streams while the screen is up: a credential plugin or any
	// other child that reaches them writes to the log pane, never over the screen, and
	// finds no terminal to read.
	return installtui.Presenter{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps,
		OwnProcessStreams: true, Wizard: wiz}
}

// installPresenterFor settles which presenter renders this install, from --ui,
// --no-splash, --dev and the options the run was resolved to.
func installPresenterFor(opts bootstrap.InstallOptions, wiz *installtui.Wizard) (installPresenter, error) {
	ui, err := parseInstallUI(installUI)
	if err != nil {
		return nil, err
	}
	if caps, ok := selectInstallPresenter(installUIRequest{UI: ui, Yes: opts.AssumeYes, Dev: installDev,
		DryRun: opts.DryRun, NoSplash: installNoSplash}, probeInstallTerminal); ok {
		return newInstallTUI(caps, wiz), nil
	}
	return installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}, nil
}

// installWizardFor is what the terminal UI's screens before the run show and may change,
// from the request as the command line resolved it. Every change is re-resolved through
// the same Prepare the command line used, so a combination the command line refuses is
// refused on the screen too, and the run a change returns is built exactly as the
// command line's own is.
func installWizardFor(provider bootstrap.Provider, choices installchoice.Choices, prepared installchoice.Prepared) *installtui.Wizard {
	shell := wizardShell()
	return &installtui.Wizard{
		Facts:    wizardFacts(choices, prepared, installtui.Edits{}),
		Settings: wizardSettings(choices, prepared.Effective, installtui.Edits{}),
		Review:   wizardReview(prepared),
		Command:  wizardCommand(prepared, shell),
		Shell:    shell,
		Contexts: kubeContextNames,
		Advisory: advisoryFor(prepared),
		Replan: func(ed installtui.Edits) (installtui.Replanned, error) {
			c, err := applyWizardEdits(choices, ed)
			if err != nil {
				return installtui.Replanned{}, err
			}
			p, err := c.Prepare(Version, nil)
			if err != nil {
				return installtui.Replanned{}, err
			}
			e, preflight := installRunFor(provider, choices.Provider, p)
			return installtui.Replanned{Engine: e, Preflight: preflight, Advisory: advisoryFor(p),
				Facts: wizardFacts(choices, p, ed), Settings: wizardSettings(choices, p.Effective, ed),
				Review: wizardReview(p), Command: wizardCommand(p, shell)}, nil
		},
	}
}

// kindClusterName is the kind cluster a --cluster value names: an empty one is the
// default, exactly as the local provider resolves it.
func kindClusterName(cluster string) string {
	if cluster == "" {
		return bootstrap.DefaultClusterName
	}
	return cluster
}

// wizardShell is the shell the review's equivalent command is quoted for: PowerShell on
// Windows, POSIX everywhere else.
func wizardShell() result.Shell {
	if runtime.GOOS == "windows" {
		return result.PowerShell
	}
	return result.POSIX
}

// wizardCommand is the equivalent non-interactive command for a prepared request: its
// choices and their explicitness, --yes for the approval the review gives, no
// presentation flag, credentials replaced by a placeholder (then it is a template).
// 🔴 IT IS SERIALISED FROM THE PLAN THE RUN IS BUILT FROM, so running it resolves to the
// same plan. A command that cannot be composed is left empty, and the review says so.
func wizardCommand(prepared installchoice.Prepared, shell result.Shell) result.Command {
	sh := installchoice.POSIX
	if shell == result.PowerShell {
		sh = installchoice.PowerShell
	}
	c, err := prepared.Command(sh, true)
	if err != nil {
		return result.Command{}
	}
	return result.Command{Line: c.Line, Args: c.Words, Template: c.Template}
}

// assignedAfterIdentification stands for the cluster's UID in the state path, which is
// not known until the install has read it from the cluster.
const assignedAfterIdentification = "<assigned after identification>"

// reviewValue is a flag's value as the review may state it, structurally: a URL the
// equivalent command would replace with a placeholder (installchoice.SecretValue, the
// serialiser's own classification) is shown with its userinfo, query and fragment
// replaced, and a value that cannot be shown that way safely is hidden whole.
func reviewValue(flag, value string) string {
	if shown, ok := installchoice.DisplayValue(flag, value); ok {
		return shown
	}
	return configuredHidden
}

// wizardReview is the plan as the review screen states it, from the resolved request:
// what the run will do, never what a form holds. It reads nothing and contacts nothing.
func wizardReview(prepared installchoice.Prepared) installtui.Review {
	eff := prepared.Effective
	opts := prepared.BootstrapOptions()
	var rows []installtui.ReviewRow
	add := func(label, value string) { rows = append(rows, installtui.ReviewRow{Label: label, Value: value}) }
	if eff.KubeContext != "" {
		add("Target", "existing cluster, context "+reviewValue("kube-context", eff.KubeContext)+
			": reused as it is; dcctl never creates or deletes it")
	} else {
		add("Target", "local kind cluster "+reviewValue("cluster", kindClusterName(eff.Cluster))+
			": created if it does not exist, reused if it does")
	}
	add("Identity", "the cluster's UID, read when the install starts; a context name is not an identity")
	if opts.BuildImages {
		add("Release", fmt.Sprintf("dcctl %s; images built from this source checkout, pushed to %s as %s",
			Version, reviewValue("registry", opts.ImageRegistry), reviewValue("version", opts.ImageVersion)))
	} else {
		add("Release", fmt.Sprintf("dcctl %s; published images %s from %s", Version,
			reviewValue("version", opts.ImageVersion), reviewValue("registry", opts.ImageRegistry)))
	}
	shape := "Standard footprint"
	if eff.Compact {
		shape = "Compact footprint"
	}
	if eff.HA {
		shape += ", HA"
	} else {
		shape += ", Single"
	}
	add("Shape", shape)
	add("TLS", onOff(!eff.NoTLS)+" (cert-manager)")
	add("Monitoring", onOff(!eff.NoMonitoring))
	backups := bootstrap.DatabaseBackupsEnabled(eff.NoCNPG, eff.Compact, eff.NoTLS)
	switch {
	case !backups:
		add("Backups", "off: this plan leaves the cluster with no database backups")
	case eff.BackupCredentials != "":
		add("Backups", "on, with an off-site archive (credentials file "+configuredHidden+")")
	default:
		add("Backups", "on, to a store the cluster provisions for itself (no off-site archive)")
	}
	if eff.BackupSnapClass != "" {
		add("Snapshots", "volume snapshots with class "+reviewValue("backup-snapshot-class", eff.BackupSnapClass))
	}
	if eff.RestoreRdbFrom != "" {
		// 🔴 A RESTORE IS READ ONLY WHEN THE DATABASE IS CREATED. The plan does not read
		// the cluster, so it cannot say whether this install creates dc-rdb; an existing
		// database keeps its data and ignores the restore settings.
		add("Recovery", "restores the relational store from "+reviewValue("restore-rdb-from", eff.RestoreRdbFrom)+
			" if this install creates it; an existing relational database is left as it is")
	}
	state := filepath.Join("~", dcdir.DirName, dcdir.Clusters, assignedAfterIdentification, "infra")
	if dir, err := dcdir.Sibling(dcdir.Clusters); err == nil {
		state = filepath.Join(dir, assignedAfterIdentification, "infra")
	}
	add("State", state)

	var warnings []string
	if eff.SkipPreflight {
		warnings = append(warnings, "--skip-preflight: the read-only checks are not run, here or by the install")
	}
	if !backups {
		warnings = append(warnings, "no database backups: a lost volume cannot be recovered")
	}
	if eff.AllowLegacyDb {
		warnings = append(warnings, "--allow-legacy-db-removal: a pre-CloudNativePG relational StatefulSet (dc-postgresql) is "+
			"destroyed, and an empty database comes up in its place")
	}
	if eff.RestoreRdbFrom != "" {
		warnings = append(warnings, "--restore-rdb-from: if this install creates the relational database, "+
			"it is built from the backup instead of starting empty; an existing one is not rebuilt")
	}
	return installtui.Review{Rows: rows, Warnings: warnings}
}

// kubeContextNames is the context picker's list: the kubeconfig's context names, sorted,
// read from disk without contacting any cluster. Tests replace it.
var kubeContextNames = func() ([]string, error) {
	names, _, err := bootstrap.KubeContexts()
	sort.Strings(names)
	return names, err
}

// wizardToggles are the drawer's editors: the flags it can change, each a bool whose
// explicit value it sets.
var wizardToggles = map[string]func(*installchoice.Choices) *bool{
	"no-monitoring": func(c *installchoice.Choices) *bool { return &c.NoMonitoring },
	"no-cnpg":       func(c *installchoice.Choices) *bool { return &c.NoCNPG },
	"no-tls":        func(c *installchoice.Choices) *bool { return &c.NoTLS },
}

// applyWizardEdits is the command line's choices with the screens' edits applied. Each
// edit sets its flag EXPLICITLY, as typing it would; a flag no edit touched keeps the
// explicitness the command line gave it.
func applyWizardEdits(orig installchoice.Choices, ed installtui.Edits) (installchoice.Choices, error) {
	c := orig.Clone()
	if ed.Compact != nil {
		// The footprint chosen on screen replaces --size as well as --compact: a flag that
		// pinned the other footprint gives way to the explicit choice, never contradicts it.
		c.Compact, c.Size = *ed.Compact, ""
		c.Given["compact"], c.Given["size"] = true, false
	}
	if ed.HA != nil {
		c.HA = *ed.HA
		c.Given["ha"] = true
	}
	if t := ed.Target; t != nil {
		switch {
		case t.KubeContext != "":
			c.KubeContext = t.KubeContext
			c.Given["kube-context"] = true
		default:
			// A local kind cluster: no context, as when --kube-context is omitted. A name
			// that resolves to the cluster the command line already names (an empty one
			// is the default, as --cluster= is) leaves --cluster exactly as it was given.
			c.KubeContext = ""
			c.Given["kube-context"] = false
			if name := strings.TrimSpace(t.Cluster); name != "" && name != kindClusterName(c.Cluster) {
				c.Cluster = name
				c.Given["cluster"] = true
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ed.Toggles)) {
		field, ok := wizardToggles[name]
		if !ok {
			return installchoice.Choices{}, fmt.Errorf("--%s cannot be changed here", name)
		}
		*field(&c) = ed.Toggles[name]
		c.Given[name] = true
	}
	return c, nil
}

// wizardFacts is the plan as the screens state it. A choice names the flag that set it
// only while the screens have left it as the command line gave it.
func wizardFacts(orig installchoice.Choices, prepared installchoice.Prepared, ed installtui.Edits) installtui.Facts {
	eff := prepared.Effective
	opts := prepared.BootstrapOptions()
	facts := installtui.Facts{
		Version: Version, Provider: orig.Provider, Providers: bootstrap.Names(),
		KubeContext: eff.KubeContext, Cluster: kindClusterName(eff.Cluster),
		Compact: eff.Compact, HA: eff.HA, SkipPreflight: eff.SkipPreflight,
		// Read from the settled plan, through the functions the install itself uses.
		Monitoring: !eff.NoMonitoring, TLS: !eff.NoTLS,
		Backups: bootstrap.DatabaseBackupsEnabled(eff.NoCNPG, eff.Compact, eff.NoTLS),
	}
	switch {
	case ed.Target != nil:
	case orig.Given.Has("kube-context"):
		facts.TargetFlag = "--kube-context " + orig.KubeContext
	case orig.Given.Has("cluster"):
		facts.TargetFlag = "--cluster " + orig.Cluster
		if orig.Cluster == "" {
			facts.TargetFlag = "--cluster="
		}
	}
	switch {
	case ed.Compact != nil:
	case orig.Given.Has("size"):
		facts.FootprintFlag = "--size " + orig.Size
	case orig.Given.Has("compact") && orig.Compact:
		facts.FootprintFlag = "--compact"
	case orig.Given.Has("compact"):
		facts.FootprintFlag = "--compact=false"
	}
	if ed.HA == nil && orig.Given.Has("ha") {
		facts.TopologyFlag = "--ha"
		if !orig.HA {
			facts.TopologyFlag = "--ha=false"
		}
	}
	if opts.BuildImages {
		facts.Images = fmt.Sprintf("built from this source checkout, pushed to %s as %s", opts.ImageRegistry, opts.ImageVersion)
	} else {
		facts.Images = fmt.Sprintf("published images %s from %s", opts.ImageVersion, opts.ImageRegistry)
	}
	return facts
}

// The drawer's sections, as the screen names them.
const (
	groupImages     = "Images and developer build"
	groupMonitoring = "Monitoring and alerts"
	groupDatabase   = "Database placement, connection budget and WAL"
	groupBackups    = "Backups and snapshots"
	groupRecovery   = "Relational recovery"
	groupFrontEnd   = "Gateway, MQTT and legacy ingress"
	groupEscape     = "Escape hatches"
	groupOther      = "Other settings"
)

// choicesScreenFlags are the flags the cluster choices screen itself shows; help is no
// choice at all.
var choicesScreenFlags = map[string]bool{"cluster": true, "kube-context": true, "compact": true, "size": true,
	"ha": true, "help": true}

// hiddenFlags carry credentials, or name the files that hold them: the drawer says they
// are configured and never shows the value.
var hiddenFlags = map[string]bool{"alert-webhook": true, "alert-watchdog-webhook": true,
	"alert-smtp-password-file": true, "alert-smtp-user": true, "alert-config": true, "backup-credentials-file": true}

// configuredHidden is how the drawer shows a credential.
const configuredHidden = "configured (hidden)"

func settingGroup(name string) string {
	switch {
	case name == "registry", name == "version", name == "build", name == "dev":
		return groupImages
	case strings.HasPrefix(name, "alert-"), name == "clear-alert-routing", name == "no-monitoring":
		return groupMonitoring
	case strings.HasPrefix(name, "database-"), name == "max-connections", name == "relational-store-max-wal-mib",
		name == "no-cnpg":
		return groupDatabase
	case strings.HasPrefix(name, "backup-"), name == "no-tls":
		return groupBackups
	case strings.HasPrefix(name, "restore-rdb-"):
		return groupRecovery
	case name == "mqtt-gateway", name == "aws-nlb", name == "remove-legacy-ingress":
		return groupFrontEnd
	case name == "allow-legacy-db-removal", name == "skip-preflight", name == "dry-run", name == "yes":
		return groupEscape
	}
	return groupOther
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// wizardSettings is the advanced drawer: its three editors, then every other flag the
// command line gave, carried with its flag name. 🔴 NOTHING GIVEN IS DROPPED: a flag the
// drawer has no editor for is still part of the plan, and the drawer says so.
func wizardSettings(orig, eff installchoice.Choices, ed installtui.Edits) []installtui.Setting {
	flags := orig.Flags()
	source := func(name string, preset bool) string {
		if _, ok := ed.Toggles[name]; ok {
			return "changed in this installer"
		}
		for _, f := range flags {
			if f.Name == name && f.Given {
				return "from --" + name + "=" + f.Values[0]
			}
		}
		if preset {
			return "preset by Compact"
		}
		return "default"
	}
	out := []installtui.Setting{
		{Group: groupMonitoring, Flag: "--no-monitoring", Label: "Monitoring stack", Value: onOff(!eff.NoMonitoring),
			Source: source("no-monitoring", eff.Compact), Toggle: true, Name: "no-monitoring", FlagValue: eff.NoMonitoring},
		{Group: groupDatabase, Flag: "--no-cnpg", Label: "Bundled CloudNativePG operator", Value: onOff(!eff.NoCNPG),
			Source: source("no-cnpg", false), Toggle: true, Name: "no-cnpg", FlagValue: eff.NoCNPG},
	}
	tls := installtui.Setting{Group: groupBackups, Flag: "--no-tls", Label: "TLS (cert-manager)",
		Value: onOff(!eff.NoTLS), Source: source("no-tls", eff.Compact), Toggle: true, Name: "no-tls", FlagValue: eff.NoTLS}
	if !eff.Compact {
		tls.Disabled = "Standard always installs cert-manager; choose plain HTTP per instance with dcctl bootstrap --no-tls"
	}
	// 🔴 BACKUPS ARE A CONSEQUENCE, NOT A SETTING, and the drawer reads them from the
	// same function the install decides them with: --no-cnpg turns them off on any
	// footprint, and Compact without TLS turns them off too.
	backups := installtui.Setting{Group: groupBackups, Label: "Database backups",
		Value:  onOff(bootstrap.DatabaseBackupsEnabled(eff.NoCNPG, eff.Compact, eff.NoTLS)),
		Source: "follows the bundled CloudNativePG operator and, on Compact, TLS"}
	out = append(out, tls, backups)
	for _, f := range flags {
		if !f.Given || choicesScreenFlags[f.Name] || wizardToggles[f.Name] != nil {
			continue
		}
		value := strings.Join(f.Values, ", ")
		if f.Repeated && len(f.Values) == 0 {
			value = "(empty)"
		}
		hidden := hiddenFlags[f.Name]
		for _, v := range f.Values {
			hidden = hidden || installchoice.SecretValue(f.Name, v)
		}
		if hidden {
			value = configuredHidden
		}
		out = append(out, installtui.Setting{Group: settingGroup(f.Name), Flag: "--" + f.Name, Value: value,
			Source: "from the command line"})
	}
	return out
}

// advisoryFor is a plan's advisory checks: for an existing cluster, the questions only
// the preflight screen asks it, which never fail and never run outside that screen.
// None for a local kind cluster, or under --skip-preflight. Every plan gets its own, so
// a change of target on screen asks the new cluster, never the old one.
func advisoryFor(p installchoice.Prepared) installflow.PreflightRunner {
	eff := p.Effective
	if eff.KubeContext == "" || eff.SkipPreflight {
		return nil
	}
	return cloudDiagnosticsRunner(cloudTargetFor(p), cloudProbesFor)
}

// cloudTargetFor is what a plan's advisory checks look at: its cluster, its image
// registry, and the optional components it installs, as the plan resolved them (Compact
// leaves monitoring out unless --no-monitoring=false, and cert-manager too unless TLS is
// kept; the drawer's --no-monitoring and --no-cnpg are explicit). cert-manager follows
// the same reading as the install's own (no cert-manager only for Compact without TLS).
func cloudTargetFor(p installchoice.Prepared) cloudTarget {
	eff := p.Effective
	opts := p.BootstrapOptions()
	return cloudTarget{KubeContext: eff.KubeContext, ImageRegistry: opts.ImageRegistry, Build: opts.BuildImages,
		CertManager: !(eff.Compact && eff.NoTLS), CNPG: !eff.NoCNPG, Monitoring: !eff.NoMonitoring,
		RemoveLegacyIngress: eff.RemoveLegacy}
}

// cloudProbesFor builds the advisory diagnostics' windows onto a target. Tests replace it
// with fakes; production never does.
var cloudProbesFor = liveCloudProbes

func init() {
	installCmd.Flags().StringVar(&installUI, "ui", string(terminal.Plain), installUIUsage)
	installCmd.Flags().BoolVar(&installNoSplash, "no-splash", false, installNoSplashUsage)
}
