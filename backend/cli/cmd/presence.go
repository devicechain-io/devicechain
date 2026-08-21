// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/presence"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Presence command flags.
var (
	presenceTenant    string
	presenceEmail     string
	presencePassword  string
	presenceSource    string
	presenceDevices   []string
	presenceReason    string
	presencePageSize  int
	presenceDryRun    bool
	presenceAssumeYes bool
)

// presenceCmd is the parent for operations on the live device-state presence
// projection. There is exactly one today, and it writes.
var presenceCmd = &cobra.Command{
	Use:   "presence",
	Short: "Inspect and repair device presence",
	Long: `Operate on the live presence projection of a tenant's devices.

A device's presence is either INFERRED — derived from its own traffic, and swept to
offline after a period of silence — or ASSERTED, meaning some event source is telling
the platform directly whether that device is connected.

An asserted device is judged only by that source. If the source stops running, the
platform stops hearing about the device and neither of the two mechanisms that would
ordinarily repair a stale row can touch it: the inactivity sweep skips asserted rows,
and a data event cannot flip one. The device keeps whatever presence it last had, for
as long as the source stays away.

'presence demote' is the way back: it releases a source's custody of its devices and
hands them to inference again.`,
}

// presenceDemoteCmd walks demoteAssertedPresence to the end of one source.
var presenceDemoteCmd = &cobra.Command{
	Use:   "demote",
	Short: "Return an event source's asserted devices to inferred presence",
	Long: `Release an event source's custody of every device it asserts presence for,
returning those devices to inferred presence.

Use this when a presence source has stopped running and its devices are frozen. A
device that was connected when the source went away reads connected forever — the
console, agents and rules all report a live device, and commands are dispatched into a
transport that drops them. A device that was disconnected has its commands held, and
those held commands count against the tenant's undelivered ceiling, so one wedged
device can block enqueues for healthy ones.

Demoting asserts NOTHING about connectivity. It does not mark anything offline: it
changes who decides. A stale-online device becomes eligible for the inactivity sweep
again and is judged on its own traffic; a wedged-offline device is reconnected by its
next event.

THE BLAST RADIUS IS AN ENTIRE EVENT SOURCE unless you narrow it with --device, so
--source is required and is never guessed. Pass it exactly as a device's reported
source: for MQTT and HTTP that is the event source's own configured id ("mqtt1",
"http1"), for Sparkplug "sparkplug:<hostId>", for LwM2M "lwm2m". A source nobody uses
is not an error — it simply matches nothing — so a misspelling looks exactly like a
finished run, and this command says so when the first page matches no rows.

The server answers one page at a time; this command walks the whole source and reports
the totals. It is safe to re-run: an interrupted walk resumes by running it again, and
a device that is already inferred is not matched a second time.

Start with --dry-run, which writes nothing and lists the devices in scope.`,
	Args:         cobra.NoArgs,
	RunE:         runPresenceDemote,
	SilenceUsage: true,
}

func init() {
	presenceCmd.PersistentFlags().StringVarP(&presenceTenant, "tenant", "t", "", "tenant whose devices are affected (required)")
	presenceCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	presenceCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	presenceCmd.PersistentFlags().StringVar(&presenceEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	presenceCmd.PersistentFlags().StringVar(&presencePassword, "password", "", "password for the identity (required)")

	presenceDemoteCmd.Flags().StringVar(&presenceSource, "source", "", "event source whose devices are released, exactly as they report it (required)")
	presenceDemoteCmd.Flags().StringArrayVar(&presenceDevices, "device", nil, "limit the run to this device token, within the source; repeat for more (default: the whole source)")
	presenceDemoteCmd.Flags().StringVar(&presenceReason, "reason", "", "why this source is being released; recorded on every event written (required)")
	presenceDemoteCmd.Flags().IntVar(&presencePageSize, "page", presence.DefaultPageSize,
		fmt.Sprintf("devices per server round-trip, 1-%d", presence.MaxPageSize))
	presenceDemoteCmd.Flags().BoolVar(&presenceDryRun, "dry-run", false, "list the devices in scope and write nothing")
	presenceDemoteCmd.Flags().BoolVarP(&presenceAssumeYes, "yes", "y", false, "assume yes for prompts")

	presenceCmd.AddCommand(presenceDemoteCmd)
	rootCmd.AddCommand(presenceCmd)
}

func runPresenceDemote(cmd *cobra.Command, args []string) error {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")

	if strings.TrimSpace(presenceTenant) == "" {
		return fmt.Errorf("--tenant is required: presence is per-tenant, and there is no default")
	}
	if strings.TrimSpace(presenceEmail) == "" || strings.TrimSpace(presencePassword) == "" {
		return fmt.Errorf("--email and --password are required: a demotion is authorized as the operator who runs it, not as dcctl")
	}

	// The chart's /api/<area> ingress convention has ONE derivation in this binary,
	// and this is it. Re-deriving the URL here would be a second copy of a convention
	// that moves with the chart; the sim-specific arguments are left empty and the
	// fields they fill are not read.
	endpoints := sim.ResolveEndpoints(server, "", "", tls)

	opts := presence.Options{
		Endpoint: endpoints.DeviceStateGraphQL,
		Source:   presenceSource,
		Devices:  presenceDevices,
		Reason:   presenceReason,
		PageSize: presencePageSize,
		DryRun:   presenceDryRun,
		Out:      cmd.OutOrStdout(),
		// Asked once, and only for a real run — Run does not ask on a dry run, because
		// a confirmation on something that writes nothing teaches an operator to answer
		// the prompt without reading it, which is the habit the real one depends on
		// not having.
		Approve: func() (bool, error) {
			return confirmDemotion(cmd.OutOrStdout(), presenceSource, presenceTenant,
				len(presenceDevices), presenceAssumeYes)
		},
	}

	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, presenceEmail, presencePassword, presenceTenant)
	sum, err := presence.Run(cmd.Context(), session, opts)
	if errors.Is(err, presence.ErrDeclined) {
		fmt.Fprintln(cmd.OutOrStdout(), color.YellowString("Aborted."))
		return nil
	}
	// The totals are printed on the failure path too, and that is the point of
	// printing them here rather than inside Run. A walk that failed on page nine
	// already wrote eight pages; an operator deciding whether to re-run has to be told
	// that happened, and an error alone reads as "nothing was done".
	if sum.Pages > 0 {
		presence.Print(cmd.OutOrStdout(), presenceSource, sum)
	}
	return err
}

// promptDemotion and stdinIsTerminal are the two terminal seams, indirected so tests
// can drive either branch without a terminal.
//
// stdinIsTerminal matters for a sharper reason than testability, and the escrow
// passphrase prompt learned it first: `go test` hands the test binary the developer's
// own stdin, so a test that expects "no terminal" finds a real one when run from a
// shell and BLOCKS on a prompt — green in CI, hung on the desk of whoever wrote it.
var (
	promptDemotion  = terminalConfirm
	stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// confirmDemotion decides whether a real (non-dry) run may proceed.
//
// 🔴 THIS COMMAND GETS A PROMPT AND `dcctl sim create` DOES NOT, because of what the
// argument list does and does not make obvious. --source names an event source, not a
// device count: nothing in the command line an operator types tells them whether that
// source holds four devices or forty thousand, and the effect lands on every one of
// them. That is the case a confirmation is for.
//
// It is skippable two ways, and both are deliberate. --yes is the explicit one, named
// as destroy names it. Piping to a script is the implicit one, and it is REFUSED
// rather than assumed: a run with nowhere to read an answer from is not a run that
// said yes. Blocking on a pipe would hang a cron job forever, and defaulting to yes
// would make a fleet-wide write the thing that happens when nobody is watching — so
// it fails, and names the flag that means it.
func confirmDemotion(w io.Writer, source, tenant string, narrowed int, assumeYes bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	scope := fmt.Sprintf("EVERY asserted device of source %q in tenant %q", source, tenant)
	if narrowed > 0 {
		scope = fmt.Sprintf("%d named device(s) of source %q in tenant %q", narrowed, source, tenant)
	}
	if !stdinIsTerminal() {
		return false, fmt.Errorf("this would demote %s, and there is no terminal to confirm on; "+
			"pass --yes to proceed non-interactively, or --dry-run to see what it would touch", scope)
	}
	fmt.Fprintf(w, "This will demote %s.\n", scope)
	fmt.Fprintln(w, "Their presence becomes inferred again; nothing is marked offline by this.")
	return promptDemotion("Proceed?"), nil
}

// terminalConfirm asks a yes/no question on stdin, defaulting to no.
func terminalConfirm(prompt string) bool {
	fmt.Print(color.WhiteString("%s [y/N]: ", prompt))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
