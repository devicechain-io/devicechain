// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

// simRunCmd runs the dc-simulator process for a sim that 'sim create' made, in the
// foreground, with the sim's own handshake file and control port. It exists so a run
// (a live capture, a recording) is one dcctl command rather than a hand-assembled
// command line, and so --play-devices is reachable without knowing where the handshake
// file lives.
var simRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Run the dc-simulator process for a sim, in the foreground",
	Long: `Run the dc-simulator process for a sim that 'sim create' made, in the foreground,
with that sim's handshake file and the control port 'sim status/start/stop' reach.

dc-simulator is looked up on PATH unless --simulator names it. Interrupt (Ctrl+C) stops
it gracefully.

--play-devices (sitepulse only) makes the simulator play the device plane itself, the
scene's machine model driving the scene's fleet tracks, instead of a Unity player. Do not
run a player against the same sim while it is on.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runSim,
	SilenceUsage: true,
}

func init() {
	simRunCmd.Flags().String("simulator", "", "path to the dc-simulator binary (default: dc-simulator on PATH)")
	simRunCmd.Flags().Bool("play-devices", false, "sitepulse only: play the device plane in the simulator instead of a Unity player")
	simRunCmd.Flags().String("publish-log", "", "with --play-devices: append every event the ingress accepted to this NDJSON file")
	simRunCmd.Flags().Duration("emit-interval", 0, "override the telemetry cadence, e.g. 3s (0 keeps the scenario's own)")
	simCmd.AddCommand(simRunCmd)
}

// simRunOptions is what 'sim run' passes on to dc-simulator.
type simRunOptions struct {
	handshake    string
	controlAddr  string
	playDevices  bool
	publishLog   string
	emitInterval time.Duration
}

// simRunArgs is dc-simulator's command line for a sim. The control port is taken from
// the record's control address, so 'sim status <name>' reaches the process this
// started; a record whose address names no port is refused rather than run on a port
// nothing will look for.
func simRunArgs(o simRunOptions) ([]string, error) {
	if o.publishLog != "" && !o.playDevices {
		return nil, errors.New("--publish-log logs what --play-devices publishes; pass both, or neither")
	}
	u, err := url.Parse(o.controlAddr)
	if err != nil || u.Port() == "" {
		return nil, fmt.Errorf("the sim's control address %q names no port", o.controlAddr)
	}
	args := []string{"--handshake", o.handshake, "--port", u.Port()}
	if o.playDevices {
		args = append(args, "--play-devices")
	}
	if o.publishLog != "" {
		args = append(args, "--publish-log", o.publishLog)
	}
	if o.emitInterval > 0 {
		args = append(args, "--emit-interval", o.emitInterval.String())
	}
	return args, nil
}

func runSim(cmd *cobra.Command, args []string) error {
	name := args[0]
	rec, err := sim.Load(name)
	if err != nil {
		return err
	}
	handshake, err := sim.RecordPath(name)
	if err != nil {
		return err
	}
	simulator, _ := cmd.Flags().GetString("simulator")
	if simulator == "" {
		if simulator, err = exec.LookPath("dc-simulator"); err != nil {
			return fmt.Errorf("dc-simulator is not on PATH; download it from the release page (dc-simulator_<version>_<os>_<arch>) and put it on PATH, or pass --simulator: %w", err)
		}
	}
	o := simRunOptions{handshake: handshake, controlAddr: rec.ControlAddr}
	o.playDevices, _ = cmd.Flags().GetBool("play-devices")
	o.publishLog, _ = cmd.Flags().GetString("publish-log")
	o.emitInterval, _ = cmd.Flags().GetDuration("emit-interval")
	argv, err := simRunArgs(o)
	if err != nil {
		return err
	}

	c := exec.CommandContext(cmd.Context(), simulator, argv...)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, nil
	// An interrupt asks the simulator to stop its emit loop and drain; where a process
	// cannot be sent one (Windows), it is killed.
	c.Cancel = func() error {
		if err := c.Process.Signal(os.Interrupt); err != nil {
			return c.Process.Kill()
		}
		return nil
	}
	c.WaitDelay = 10 * time.Second
	fmt.Fprintf(os.Stderr, "running %s for sim %q (control %s)\n", simulator, name, rec.ControlAddr)
	if err := c.Run(); err != nil && cmd.Context().Err() == nil {
		return fmt.Errorf("dc-simulator exited: %w", err)
	}
	return nil
}
