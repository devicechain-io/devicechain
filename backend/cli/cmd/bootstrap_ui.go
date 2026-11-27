// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// bootstrapUI is --ui on dcctl bootstrap: the same values, default, help and selection
// as install's. Without it every existing invocation prints, prompts and exits exactly
// as it always has.
var bootstrapUI string

// bootstrapRunFor builds the run a bootstrap command line describes: bootstrapRun, for
// the command and for every set of choices the instance screens settle. Tests replace
// it, to stand fakes in for the cluster work behind the real flags and screens.
var bootstrapRunFor = bootstrapRun

// newBootstrapTUI builds the terminal-UI presenter for a bootstrap on the process's own
// streams: S17's instance screens, the instance review and its approval, the progress
// screen with the bootstrap's stage table, the passphrase form and the screens after
// the run. Tests replace it, so that the choice can be checked without a terminal.
var newBootstrapTUI = func(caps terminal.Capabilities, scr *installtui.BootstrapScreens) installPresenter {
	return installtui.Presenter{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps,
		OwnProcessStreams: true, Bootstrap: scr}
}

// personAtTheTerminal is BootstrapEngine.PersonAtTerminal under the terminal UI. The UI
// is chosen only when stdin, stdout and stderr are all an interactive terminal, and it
// puts the null device on stdin while the run is live so that no child can read the
// terminal: the person the escrow passphrase is asked of is at the terminal, on the
// passphrase form. It moves nothing in the order: a file, then the environment, first.
func personAtTheTerminal() bool { return true }

// runBootstrapEngine runs a bootstrap through the presenter --ui selects, with
// install's own selection (selectInstallPresenter): --ui=plain, and --ui=tui or auto off
// an interactive terminal, under CI or TERM=dumb, or with --dry-run, run the plain
// presenter on the process's own streams, byte for byte as dcctl always has, with the
// escrow passphrase read from the terminal when no file or environment variable supplies
// it, and the generated superuser seed shown once, where the access report showed it.
//
// The terminal UI shows no logo for a bootstrap: it opens on the instance screens, or
// with --yes or --dev on the progress screen alone.
func runBootstrapEngine(ctx context.Context, ui terminal.UI, e *bootstrap.BootstrapEngine,
	preflight installflow.PreflightRunner, screens func(progressOnly bool) *installtui.BootstrapScreens) error {
	if ctx == nil { // RunE called directly, as tests do, rather than through Execute
		ctx = context.Background()
	}
	req := installflow.Request{Operation: installflow.OperationBootstrap, DryRun: e.DryRun, AssumeYes: e.AssumeYes}
	// A console mode the terminal probe changed and could not put back is restored on the way out.
	defer restoreProbedConsole()
	caps, tui := selectInstallPresenter(installUIRequest{UI: ui, Yes: e.AssumeYes, Dev: bootstrapDev,
		DryRun: e.DryRun, NoSplash: true}, probeInstallTerminal)
	if !tui {
		return installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}.Run(ctx, e, req, preflight)
	}
	caps.Splash, caps.Animate = terminal.NoArt, false
	e.PersonAtTerminal = personAtTheTerminal
	return newBootstrapTUI(caps, screens(caps.ProgressOnly)).Run(ctx, e, req, preflight)
}

// bootstrapScreenFlags are the flags the instance screens show and choose, with the
// target, which they show and keep. Every other flag the command line gave is carried
// into the run exactly as it was given (carriedBootstrapFlags). --ui and --yes are the
// presenter's and the approval's; neither is part of the plan.
var bootstrapScreenFlags = map[string]bool{
	"profile": true, "enable-area": true, "host": true, "no-tls": true, "object-store": true,
	"escrow-file": true, "no-escrow": true, "restore-root-key": true,
	"kube-context": true, "cluster": true, "ui": true, "yes": true, "help": true,
}

// carriedFlags are the flags the command line gave that the instance screens do not
// choose, as words of a command line.
type carriedFlags struct {
	// run are the words the run is built with, every carried flag as it was given.
	run []string
	// command are the same words for the equivalent command, which names the escrow
	// passphrase file only by a placeholder.
	command []string
	// names are the carried flags' names, for the review.
	names []string
}

// carriedBootstrapFlags is every flag the command line gave that the instance screens do
// not choose, as words that parse back to the same values: one word per value of a
// repeatable flag. 🔴 NOTHING GIVEN IS DROPPED: the run a person approves on screen is
// the command line's, with only the screens' choices changed.
func carriedBootstrapFlags(fs *pflag.FlagSet) carriedFlags {
	var c carriedFlags
	// VisitAll and Changed, never Visit: Visit walks every flag the set was ever told to
	// set, and the flags are put back to their defaults (and Changed cleared) between the
	// command lines the screens build (bootstrapRunFromWords), which Visit cannot see.
	fs.VisitAll(func(f *pflag.Flag) {
		if !f.Changed || bootstrapScreenFlags[f.Name] {
			return
		}
		words := flagWords(f)
		c.run = append(c.run, words...)
		if f.Name != "escrow-passphrase-file" {
			c.command = append(c.command, words...)
		}
		c.names = append(c.names, "--"+f.Name)
	})
	return c
}

// flagWords is a flag as the command line gave it, as words that parse back to the same
// value AND the same presence: one word per value of a repeatable flag, and an explicitly
// empty one as "--name=", which is given, unlike an omitted flag.
func flagWords(f *pflag.Flag) []string {
	sv, ok := f.Value.(pflag.SliceValue)
	if !ok {
		return []string{"--" + f.Name + "=" + f.Value.String()}
	}
	var words []string
	for _, item := range sv.GetSlice() {
		if f.Value.Type() == "stringSlice" {
			item = csvField(item)
		}
		words = append(words, "--"+f.Name+"="+item)
	}
	if len(words) == 0 {
		words = []string{"--" + f.Name + "="}
	}
	return words
}

// csvField is one value of a comma-separated flag as that flag parses it back: quoted
// when it holds a comma, a quote or a line break.
func csvField(s string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write([]string{s}); err != nil {
		return s
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\r\n")
}

// givenTargetArgs are the target flags exactly as the command line gave them, one word
// each: none when it named neither, as dcctl bootstrap then resolves its default.
func givenTargetArgs(fs *pflag.FlagSet) []string {
	var out []string
	if fs.Changed("kube-context") {
		out = append(out, "--kube-context="+bootstrapKubeContext)
	}
	if fs.Changed("cluster") {
		out = append(out, "--cluster="+bootstrapCluster)
	}
	return out
}

// escrowPassphraseSource is where the run will take the escrow passphrase from, in the
// engine's order: --escrow-passphrase-file, then the environment, then a person.
func escrowPassphraseSource() installtui.PassphraseSource {
	if bootstrapEscrowPassFile != "" {
		return installtui.PassphraseFromFile
	}
	if _, ok := os.LookupEnv(bootstrap.EscrowPassphraseEnv); ok {
		return installtui.PassphraseFromEnv
	}
	return installtui.PassphraseFromPerson
}

// defaultEscrowPathFor is the engine's default escrow path for an instance name, or ""
// when it cannot be resolved (no home directory): the screen then asks for one.
func defaultEscrowPathFor(instance string) string {
	p, err := bootstrap.DefaultEscrowPath(instance)
	if err != nil {
		return ""
	}
	return p
}

// escrowPathRule is the escrow screen's check of a path: the bootstrap's own rule
// (bootstrap.CheckEscrowPath), so a path the screen accepts is one the run's escrow
// stage accepts too. refuseExisting also refuses a file already there, which only a new
// instance's run would refuse (it writes with O_EXCL); a re-run on an existing instance
// checks the artifact there against its key instead.
func escrowPathRule(refuseExisting bool) func(instance, path string) error {
	return func(instance, path string) error {
		abs, err := bootstrap.CheckEscrowPath(instance, path)
		if err != nil {
			return err
		}
		if refuseExisting {
			if _, err := os.Stat(abs); err == nil {
				return fmt.Errorf("%s already exists; an escrow file is never overwritten", path)
			}
		}
		return nil
	}
}

// validateInstanceAreas is the instance screen's check of the extra areas.
func validateInstanceAreas(areas []string) error {
	_, err := bootstrap.ResolveEnabledAreas("default", areas)
	return err
}

// instanceFactsRecorder keeps what a finished bootstrap run settled, for the screens
// after it. 🔴 ONLY WHAT THOSE SCREENS STATE IS COPIED OUT; the passphrase and the root
// key, which the state also holds, never are.
type instanceFactsRecorder struct {
	mu    sync.Mutex
	facts installtui.InstanceFacts
}

// observe is bootstrapRun's observer.
func (r *instanceFactsRecorder) observe(st *bootstrap.State, runErr error) {
	if st == nil {
		return
	}
	f := installtui.InstanceFacts{KubeContext: st.KubeContext, Account: auth.DefaultSuperuserEmail,
		EscrowPath: st.Escrow.Path, EscrowWritten: runErr == nil && st.Escrow.Path != ""}
	if host := st.Values["ingressHost"]; host != "" {
		f.ConsoleURL = bootstrap.ConsoleURL(st.KubeContext, st.Values["scheme"], host)
	}
	r.mu.Lock()
	r.facts = f
	r.mu.Unlock()
}

// read is BootstrapScreens.Facts.
func (r *instanceFactsRecorder) read(installflow.Summary) installtui.InstanceFacts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.facts
}

// instancePlan builds the run a set of instance choices describes, through the bootstrap
// command c's own flags and bootstrapRun (bootstrapRunFromWords): asGiven, when set, puts
// back the words of the flags the screens left as the command line gave them; runArgs are the flags the run
// is built with beside the choices, commandArgs the same for the equivalent command. A
// refusal is the review's to show, and Approve is then unavailable.
func instancePlan(c *cobra.Command, p installtui.BootstrapPlanned, runArgs, commandArgs []string, review installtui.Review,
	asGiven func([]string) []string, observe func(*bootstrap.State, error)) (installtui.BootstrapPlan, error) {
	words := installtui.BootstrapArgs(p.Instance, p.Escrow, runArgs)
	commandWords := installtui.BootstrapArgs(p.Instance, p.Escrow, commandArgs)
	if asGiven != nil {
		words, commandWords = asGiven(words), asGiven(commandWords)
	}
	e, preflight, err := bootstrapRunFromWords(c, words, observe)
	if err != nil {
		return installtui.BootstrapPlan{}, err
	}
	// The escrow flags' contradictions are refused by the run's escrow stage; said here
	// first, so that a combination the screens made is refused before its approval.
	if err := e.Escrow.Validate(); err != nil {
		return installtui.BootstrapPlan{}, err
	}
	e.PersonAtTerminal = personAtTheTerminal
	return installtui.BootstrapPlan{Engine: e, Preflight: preflight, Review: review,
		Command: installtui.BootstrapCommandOf(commandWords, p.Escrow)}, nil
}

// bootstrapScreensFor is dcctl bootstrap's instance journey under the terminal UI: the
// instance and escrow screens seeded from the command line, and the run each set of
// choices describes, built as the same command line with those choices would build it.
// A progress-only run (--yes, --dev) has no screens before it: the run is the command
// line's own, and only its seeds name the instance on screen.
func bootstrapScreensFor(cmd *cobra.Command, args []string, facts *instanceFactsRecorder, progressOnly bool) *installtui.BootstrapScreens {
	fs := cmd.Flags()
	target := setupTarget{KubeContext: bootstrapKubeContext, Cluster: kindClusterName(bootstrapCluster)}
	targetArgs := givenTargetArgs(fs)
	carried := carriedBootstrapFlags(fs)
	esc := installtui.EscrowSeed{
		Mode: installtui.EscrowWrite, Path: bootstrapEscrowFile, PathFromFlag: fs.Changed("escrow-file"),
		Passphrase: escrowPassphraseSource(), DefaultPath: defaultEscrowPathFor,
		ValidatePath: escrowPathRule(false),
	}
	switch {
	case bootstrapRestoreRootKey != "":
		esc.Mode, esc.RestoreFrom = installtui.EscrowRestore, bootstrapRestoreRootKey
	case bootstrapNoEscrow:
		esc.Mode = installtui.EscrowNone
	}
	commandArgs := append(append([]string(nil), targetArgs...), carried.command...)
	scr := &installtui.BootstrapScreens{
		Setup: installtui.BootstrapSetup{
			Version: Version, Target: target.label(), TargetArgs: commandArgs,
			Instance: installtui.InstanceSeed{
				Name: args[1], Host: bootstrapHost, Profile: bootstrapProfile, ObjectStore: bootstrapObjectStore.Mode,
				Areas: append([]string(nil), bootstrapEnableAreas...), NoTLS: bootstrapNoTLS,
				FromFlag: map[string]bool{"name": true, "host": fs.Changed("host"), "profile": fs.Changed("profile"),
					"enable-area": fs.Changed("enable-area"), "no-tls": fs.Changed("no-tls"),
					"object-store": fs.Changed("object-store")},
				ClusterShape:  "as dcctl install prepared the cluster; read from it when the run starts",
				ValidateName:  bootstrap.ValidateInstanceName,
				ValidateAreas: validateInstanceAreas,
			},
			Escrow: esc,
		},
		Facts: facts.read,
	}
	if progressOnly {
		return scr
	}
	runArgs := append(append([]string(nil), targetArgs...), carried.run...)
	given := givenScreenFlagsOf(fs, scr.Setup.Instance, esc)
	clusterLine := target.label() + ", as dcctl install prepared it; its footprint and topology are read from it " +
		"when the run starts"
	scr.Plan = func(p installtui.BootstrapPlanned) (installtui.BootstrapPlan, error) {
		return instancePlan(cmd, p, runArgs, commandArgs, instanceReview(clusterLine, p, carried.names),
			given.asGivenFor(p), facts.observe)
	}
	return scr
}

// instanceReview is an instance's plan as its review states it: clusterLine names the
// cluster it is built on, and carried the flags the command line gave that the screens
// did not choose.
func instanceReview(clusterLine string, p installtui.BootstrapPlanned, carried []string) installtui.Review {
	in, esc := p.Instance, p.Escrow
	var rows []installtui.ReviewRow
	add := func(label, value string) { rows = append(rows, installtui.ReviewRow{Label: label, Value: value}) }
	add("Instance", in.Name)
	add("Cluster", clusterLine)
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
	case installtui.EscrowRestore:
		add("Escrow", "the root key is restored from "+esc.Path+"; its passphrase is "+passphraseHow(esc, false))
	default:
		path := esc.Path
		if path == "" {
			path = "the default location"
		}
		add("Escrow", path+"; passphrase "+passphraseHow(esc, true))
	}
	if len(carried) > 0 {
		add("Also", strings.Join(carried, ", ")+", as the command line gave them")
	}
	return installtui.Review{Rows: rows, Warnings: warnings}
}

// passphraseHow says where the escrow passphrase comes from, for the review.
func passphraseHow(esc installtui.EscrowChoice, twice bool) string {
	switch esc.Passphrase {
	case installtui.PassphraseFromFile:
		return "read from --escrow-passphrase-file; nothing is asked"
	case installtui.PassphraseFromEnv:
		return "read from the environment; nothing is asked"
	}
	if twice {
		return "asked for when the run reaches its escrow step, twice, and never shown"
	}
	return "asked for when the run reaches its escrow step, and never shown"
}

func init() {
	bootstrapCmd.Flags().StringVar(&bootstrapUI, "ui", string(terminal.Plain), uiUsage("bootstrap"))
}

// givenScreenFlags are the flags the instance screens choose that the command line gave,
// each as it gave them, with the seeds the screens started from.
type givenScreenFlags struct {
	words map[string][]string
	inst  installtui.InstanceSeed
	esc   installtui.EscrowSeed
}

// The screen flags: the instance screen's, then the escrow screen's three, which are one
// choice.
var (
	instanceScreenFlags = []string{"profile", "enable-area", "host", "no-tls", "object-store"}
	escrowScreenFlags   = []string{"escrow-file", "no-escrow", "restore-root-key"}
	screenChoiceFlags   = append(append([]string(nil), instanceScreenFlags...), escrowScreenFlags...)
)

func givenScreenFlagsOf(fs *pflag.FlagSet, inst installtui.InstanceSeed, esc installtui.EscrowSeed) givenScreenFlags {
	g := givenScreenFlags{words: map[string][]string{}, inst: inst, esc: esc}
	for _, name := range screenChoiceFlags {
		if f := fs.Lookup(name); f != nil && f.Changed {
			g.words[name] = flagWords(f)
		}
	}
	return g
}

// unchanged reports, for each screen flag, whether p leaves its choice as the seed had it.
func (g givenScreenFlags) unchanged(p installtui.BootstrapPlanned) map[string]bool {
	in, seed := p.Instance, g.inst
	defaulted := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	same := map[string]bool{
		"profile":      defaulted(in.Profile, "default") == defaulted(seed.Profile, "default"),
		"enable-area":  strings.Join(in.Areas, ",") == strings.Join(seed.Areas, ","),
		"host":         in.Host == seed.Host,
		"no-tls":       in.NoTLS == seed.NoTLS,
		"object-store": defaulted(in.ObjectStore, "auto") == defaulted(seed.ObjectStore, "auto"),
	}
	esc, seedMode := p.Escrow, g.esc.Mode
	if seedMode == "" {
		seedMode = installtui.EscrowWrite
	}
	escrowSame := esc.Mode == seedMode
	if escrowSame && esc.Mode == installtui.EscrowWrite {
		if g.esc.PathFromFlag && g.esc.Path != "" {
			escrowSame = esc.PathGiven && esc.Path == g.esc.Path
		} else {
			escrowSame = !esc.PathGiven
		}
	}
	for _, name := range escrowScreenFlags {
		same[name] = escrowSame
	}
	return same
}

// asGivenFor makes a command line for the choices p re-emit, for every screen flag p
// leaves unchanged, exactly the words the command line gave, and nothing for one it never
// gave. 🔴 A FLAG'S PRESENCE IS PART OF THE REQUEST, NOT ONLY ITS VALUE: an explicitly
// empty --object-store= is an object store CHOSEN (the default, recorded on the
// declaration), while an omitted one keeps whatever the instance already declares.
func (g givenScreenFlags) asGivenFor(p installtui.BootstrapPlanned) func([]string) []string {
	same := g.unchanged(p)
	return func(words []string) []string {
		if len(words) < 4 {
			return words
		}
		out := append([]string(nil), words[:4]...) // dcctl bootstrap <provider> <instance>
		for _, w := range words[4:] {
			if name, ok := screenFlagOf(w); ok && same[name] {
				continue // replaced by the words as given, below
			}
			out = append(out, w)
		}
		for _, name := range screenChoiceFlags {
			if same[name] {
				out = append(out, g.words[name]...)
			}
		}
		return out
	}
}

// screenFlagOf is the screen flag a word sets, if it sets one.
func screenFlagOf(w string) (string, bool) {
	for _, name := range screenChoiceFlags {
		if w == "--"+name || strings.HasPrefix(w, "--"+name+"=") {
			return name, true
		}
	}
	return "", false
}
