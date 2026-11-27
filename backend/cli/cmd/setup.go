// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// dcctl setup local flags. They only seed the screens: every choice is made, reviewed
// and approved on them.
var (
	setupKubeContext string
	setupCluster     string
	setupCompact     bool
	setupHA          bool
	setupNoSplash    bool
)

// setupError is a dcctl setup local failure with its exit status (installtui.SetupExit*).
type setupError struct {
	code int
	err  error
}

func (e *setupError) Error() string { return e.err.Error() }
func (e *setupError) Unwrap() error { return e.err }
func (e *setupError) ExitCode() int { return e.code }

// refusedBeforeStart is a setup that refused before anything started: nothing changed.
func refusedBeforeStart(err error) error {
	return &setupError{code: installtui.SetupExitNothingChanged, err: err}
}

// errSetupNeedsTerminal is the refusal without an interactive terminal.
var errSetupNeedsTerminal = errors.New("dcctl setup local is guided: it needs stdin, stdout and stderr to be an " +
	"interactive terminal (not CI, not TERM=dumb), and it never approves anything on its own, so it has nothing " +
	"to run here. Nothing was changed.\n" +
	"  Run the two steps yourself instead:\n" +
	"    dcctl install local [flags]\n" +
	"    dcctl bootstrap local <instance> [flags]")

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Preview. Guided set-up: a cluster, then an instance on it",
	Long: `Preview. Guided set-up of DeviceChain on one terminal. See "dcctl setup local".

dcctl install and dcctl bootstrap are unchanged by it, and stay the way to script either step.`,
}

var setupLocalCmd = &cobra.Command{
	Use:   "local",
	Short: "Preview. A guided install, then bootstrap, each with its own approval",
	Long: `Preview. Prepares a cluster and creates an instance on it, in two stages on one terminal
screen, each with its own review and its own approval:

  1. the cluster: the same screens as "dcctl install local --ui=tui", ending on its review;
     Approve installs the cluster.
  2. the instance: once the cluster is installed, a hand-over screen, then the instance and
     root-key escrow choices, ending on the instance's review; Approve creates the instance.

Nothing of the instance runs without its own approval, and no key typed before a review has
settled approves it. Leaving after the cluster stage leaves the cluster installed, and setup
prints the dcctl bootstrap command that creates the instance later.

It is guided only. It needs stdin, stdout and stderr to be an interactive terminal (not CI,
not TERM=dumb), has no --yes, and refuses rather than approve anything on its own. To script
the same result, run "dcctl install local" and then "dcctl bootstrap local".

Exit status:
  0  the cluster is installed and the instance was created
  1  the cluster stage failed, or was interrupted after its approval (as dcctl install exits 1)
  2  nothing was changed: stopped before the cluster's approval, or refused before starting
  3  the cluster is installed; no instance was created (left before the instance's approval)
  4  the cluster is installed; creating the instance failed, or was interrupted after its approval`,
	Example: `  # a local kind cluster named "devicechain", then an instance on it
  dcctl setup local
  # a small-footprint cluster, for a laptop
  dcctl setup local --compact
  # an existing cluster: dcctl never creates or deletes it
  dcctl setup local --kube-context prod`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return refusedBeforeStart(fmt.Errorf("dcctl setup local takes no arguments (got %q); "+
				"the instance is named on its screen", strings.Join(args, " ")))
		}
		return nil
	},
	RunE:         runSetupLocal,
	SilenceUsage: true,
}

// runSetup runs the two stages. Tests replace it, to check the command's wiring
// without a terminal to take over.
var runSetup = func(ctx context.Context, s installtui.Setup) installtui.SetupResult { return s.Run(ctx) }

func runSetupLocal(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil { // RunE called directly, as tests do, rather than through Execute
		ctx = context.Background()
	}
	// 🔴 GUIDED ONLY, AND FAIL CLOSED. Without an interactive terminal there is nobody to
	// approve either stage, and setup has no flag that approves for them: it refuses,
	// before anything is touched.
	//
	// The console's restore is deferred BEFORE the probe, as the install does: the one
	// case the probe leaves a console mode changed (the VT flag took and the original mode
	// could not be put back) is a terminal it judged ineligible, which is exactly the
	// refusal below.
	defer restoreProbedConsole()
	in := probeInstallTerminal()
	in.UI, in.NoSplash = terminal.TUI, setupNoSplash
	caps := terminal.Evaluate(in)
	if !caps.TUI || caps.ProgressOnly {
		return refusedBeforeStart(errSetupNeedsTerminal)
	}

	provider, err := bootstrap.Get("local")
	if err != nil {
		return refusedBeforeStart(err)
	}
	choices := setupInstallChoices(cmd.Flags())
	prepared, err := choices.Prepare(Version, func(string) {})
	if err != nil {
		return refusedBeforeStart(err)
	}
	e, preflight := installRunFor(provider, choices.Provider, prepared)

	// The cluster the install ran on is the one the instance is built on: the options the
	// engine that actually ran resolved say which, whatever the screens changed on the way.
	var ranMu sync.Mutex
	eff := prepared.Effective
	ran := setupTarget{KubeContext: eff.KubeContext, Cluster: kindClusterName(eff.Cluster), Compact: eff.Compact, HA: eff.HA}
	record := func(eng installflow.Engine) installflow.Engine {
		ie, ok := eng.(*bootstrap.InstallEngine)
		if !ok || ie.ResolveOptions == nil {
			return eng
		}
		recorded, resolve := *ie, ie.ResolveOptions
		recorded.ResolveOptions = func() (bootstrap.InstallOptions, error) {
			o, err := resolve()
			if err == nil {
				ranMu.Lock()
				ran = setupTargetOf(o)
				ranMu.Unlock()
			}
			return o, err
		}
		return &recorded
	}
	wiz := installWizardFor(provider, choices, prepared)
	replan := wiz.Replan
	wiz.Replan = func(ed installtui.Edits) (installtui.Replanned, error) {
		r, err := replan(ed)
		if err == nil {
			r.Engine = record(r.Engine)
		}
		return r, err
	}
	target := func() setupTarget {
		ranMu.Lock()
		defer ranMu.Unlock()
		return ran
	}

	res := runSetup(ctx, installtui.Setup{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps, OwnProcessStreams: true,
		Cluster: record(e), ClusterRequest: installRequest(e), ClusterPreflight: preflight, Wizard: wiz,
		Instance: func() *installtui.BootstrapScreens { return setupInstanceScreens(target()) },
		ContinueCommand: func(planned *installtui.BootstrapPlanned) string {
			return setupContinueCommand(target(), planned)
		},
	})
	if code := res.ExitCode(); code != installtui.SetupExitSucceeded {
		err := res.Err
		if err == nil {
			err = errors.New("dcctl setup local did not finish")
		}
		return &setupError{code: code, err: err}
	}
	return nil
}

// setupInstallChoices is the install request the setup flags seed: the install's own
// defaults, with each setup flag that was given set as explicitly as the same install
// flag would be.
func setupInstallChoices(fs *pflag.FlagSet) installchoice.Choices {
	c := installchoice.Defaults()
	c.Provider = "local"
	if fs.Changed("kube-context") {
		c.KubeContext, c.Given["kube-context"] = setupKubeContext, true
	}
	if fs.Changed("cluster") {
		c.Cluster, c.Given["cluster"] = setupCluster, true
	}
	if fs.Changed("compact") {
		c.Compact, c.Given["compact"] = setupCompact, true
	}
	if fs.Changed("ha") {
		c.HA, c.Given["ha"] = setupHA, true
	}
	return c
}

// setupTarget is the cluster an install ran on, and its shape.
type setupTarget struct {
	KubeContext string
	Cluster     string
	Compact, HA bool
}

func setupTargetOf(o bootstrap.InstallOptions) setupTarget {
	return setupTarget{KubeContext: o.KubeContext, Cluster: kindClusterName(o.Cluster), Compact: o.Compact, HA: o.HA}
}

// args are the target's flags for dcctl bootstrap, one word each.
func (t setupTarget) args() []string {
	if t.KubeContext != "" {
		return []string{"--kube-context=" + t.KubeContext}
	}
	return []string{"--cluster=" + t.Cluster}
}

// label names the target on screen.
func (t setupTarget) label() string {
	if t.KubeContext != "" {
		return "context " + t.KubeContext
	}
	return "kind cluster " + t.Cluster
}

func (t setupTarget) shape() string {
	s := "Standard footprint"
	if t.Compact {
		s = "Compact footprint"
	}
	if t.HA {
		return s + ", HA"
	}
	return s + ", Single"
}

// setupContinueCommand is the command that builds the instance later, by hand: the
// chosen instance when the choices were settled, otherwise a template to fill in.
func setupContinueCommand(t setupTarget, planned *installtui.BootstrapPlanned) string {
	if planned != nil {
		return installtui.BootstrapContinueCommand(planned.Instance, planned.Escrow, t.args())
	}
	words := append([]string{"dcctl", "bootstrap", "local", "<instance>"}, t.args()...)
	for i, w := range words[4:] {
		words[4+i] = shellWord(w)
	}
	return strings.Join(words, " ")
}

// shellWord quotes a word for a POSIX shell when it needs it.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// setupInstanceScreens is the instance stage on the cluster t: the hand-over, the
// instance and escrow choices, and the run each set of choices describes.
func setupInstanceScreens(t setupTarget) *installtui.BootstrapScreens {
	passphrase := installtui.PassphraseFromPerson
	if _, ok := os.LookupEnv(bootstrap.EscrowPassphraseEnv); ok {
		passphrase = installtui.PassphraseFromEnv
	}
	// What the finished run settled, for the success screens. Only what those screens
	// state is copied out; the passphrase and the root key never are.
	var mu sync.Mutex
	var facts installtui.InstanceFacts
	observe := func(st *bootstrap.State, runErr error) {
		if st == nil {
			return
		}
		f := installtui.InstanceFacts{KubeContext: st.KubeContext, Account: auth.DefaultSuperuserEmail,
			EscrowPath: st.Escrow.Path, EscrowWritten: runErr == nil && st.Escrow.Path != ""}
		if host := st.Values["ingressHost"]; host != "" {
			f.ConsoleURL = bootstrap.ConsoleURL(st.KubeContext, st.Values["scheme"], host)
		}
		mu.Lock()
		facts = f
		mu.Unlock()
	}
	return &installtui.BootstrapScreens{
		Setup: installtui.BootstrapSetup{
			Version: Version, Target: t.label(), TargetArgs: t.args(),
			Instance: installtui.InstanceSeed{
				FromFlag: map[string]bool{}, ClusterShape: t.shape(),
				ValidateName: bootstrap.ValidateInstanceName,
				ValidateAreas: func(areas []string) error {
					_, err := bootstrap.ResolveEnabledAreas("default", areas)
					return err
				},
			},
			Escrow: installtui.EscrowSeed{
				Mode: installtui.EscrowWrite, Passphrase: passphrase,
				DefaultPath: func(instance string) string {
					p, err := bootstrap.DefaultEscrowPath(instance)
					if err != nil {
						return ""
					}
					return p
				},
				ValidatePath: func(path string) error {
					// The engine refuses to overwrite an escrow artifact; said here first.
					if _, err := os.Stat(path); err == nil {
						return fmt.Errorf("%s already exists; an escrow file is never overwritten", path)
					}
					return nil
				},
			},
		},
		Handover: &installtui.Handover{Target: t.label() + " (" + t.shape() + ")", Continue: setupContinueCommand(t, nil)},
		Plan: func(p installtui.BootstrapPlanned) (installtui.BootstrapPlan, error) {
			e, preflight, err := setupBootstrapRun(installtui.BootstrapArgs(p.Instance, p.Escrow, t.args()), observe)
			if err != nil {
				return installtui.BootstrapPlan{}, err
			}
			// The terminal UI holds the terminal and puts the null device on stdin while
			// the run is live; the person it asks for the escrow passphrase is at the
			// terminal this command was refused without.
			e.PersonAtTerminal = func() bool { return true }
			return installtui.BootstrapPlan{Engine: e, Preflight: preflight,
				Review:  setupInstanceReview(t, p),
				Command: installtui.BootstrapCommand(p.Instance, p.Escrow, t.args())}, nil
		},
		Facts: func(installflow.Summary) installtui.InstanceFacts {
			mu.Lock()
			defer mu.Unlock()
			return facts
		},
	}
}

// setupBootstrapRun builds the run a bootstrap command line describes, through the
// bootstrap command's own flags and bootstrapRun: words are what follows "dcctl" on
// that line. Every bootstrap flag is put back to its default first, so nothing a
// previous set of choices parsed is carried into this one.
func setupBootstrapRun(words []string, observe func(*bootstrap.State, error)) (*bootstrap.BootstrapEngine, installflow.PreflightRunner, error) {
	if len(words) < 2 || words[0] != "dcctl" || words[1] != "bootstrap" {
		return nil, nil, fmt.Errorf("not a bootstrap command line: %q", words)
	}
	fs := bootstrapCmd.Flags()
	var reset error
	fs.VisitAll(func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			reset = errors.Join(reset, sv.Replace(nil))
		} else {
			reset = errors.Join(reset, f.Value.Set(f.DefValue))
		}
		f.Changed = false
	})
	if reset != nil {
		return nil, nil, fmt.Errorf("resetting the bootstrap flags: %w", reset)
	}
	if err := fs.Parse(words[2:]); err != nil {
		return nil, nil, err
	}
	if err := cobra.ExactArgs(2)(bootstrapCmd, fs.Args()); err != nil {
		return nil, nil, err
	}
	return bootstrapRun(bootstrapCmd, fs.Args(), observe)
}

// setupInstanceReview is the instance's plan as its review states it.
func setupInstanceReview(t setupTarget, p installtui.BootstrapPlanned) installtui.Review {
	in, esc := p.Instance, p.Escrow
	var rows []installtui.ReviewRow
	add := func(label, value string) { rows = append(rows, installtui.ReviewRow{Label: label, Value: value}) }
	add("Instance", in.Name)
	add("Cluster", t.label()+" ("+t.shape()+"), installed by the first stage")
	profile := in.Profile
	if len(in.Areas) > 0 {
		profile += " + " + strings.Join(in.Areas, ", ")
	}
	add("Profile", profile)
	host := in.Host
	if host == "" {
		host = "the bootstrap's default (" + bootstrap.DefaultIngressHost + ")"
	}
	add("Host", host)
	add("TLS", onOff(!in.NoTLS))
	add("Object store", in.ObjectStore)
	var warnings []string
	switch esc.Mode {
	case installtui.EscrowNone:
		add("Escrow", "none (--no-escrow)")
		warnings = append(warnings, installtui.WordsNoEscrowWarning)
	default:
		path := esc.Path
		if path == "" {
			path = "the default location"
		}
		how := "asked for when the run reaches its escrow step, twice, and never shown"
		if esc.Passphrase == installtui.PassphraseFromEnv {
			how = "read from the environment; nothing is asked"
		}
		add("Escrow", path+"; passphrase "+how)
	}
	return installtui.Review{Rows: rows, Warnings: warnings}
}

func init() {
	setupLocalCmd.Flags().StringVar(&setupKubeContext, "kube-context", "", "Preview. Start from this existing cluster instead of a local kind cluster (never created or deleted by dcctl); it can be changed on screen")
	setupLocalCmd.Flags().StringVar(&setupCluster, "cluster", bootstrap.DefaultClusterName, "Preview. Start from this local kind cluster, created if it does not exist; it can be changed on screen")
	setupLocalCmd.Flags().BoolVar(&setupCompact, "compact", false, "Preview. Start with the small-footprint cluster preset (as dcctl install --compact); it can be changed on screen")
	setupLocalCmd.Flags().BoolVar(&setupHA, "ha", false, "Preview. Start with the HA cluster topology (as dcctl install --ha); it can be changed on screen")
	setupLocalCmd.Flags().BoolVar(&setupNoSplash, "no-splash", false, "Preview. Skip the DeviceChain logo shown before the first screen")
	setupLocalCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return refusedBeforeStart(err) })
	setupCmd.AddCommand(setupLocalCmd)
	rootCmd.AddCommand(setupCmd)
}
