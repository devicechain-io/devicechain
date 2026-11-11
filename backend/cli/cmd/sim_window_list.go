// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var simWindowsCmd = &cobra.Command{
	Use: "windows <name>", Short: "Print the recorded episode windows (role, start, end)", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rec, err := sim.Load(args[0])
		if err != nil {
			return err
		}
		return printSimWindows(cmd.OutOrStdout(), rec)
	},
	SilenceUsage: true,
}

func init() { simCmd.AddCommand(simWindowsCmd) }
func printSimWindows(out io.Writer, rec *sim.Record) error {
	if rec.Episode != "night-shift" {
		return fmt.Errorf("sim %q has no review windows (episode %q)", rec.Name, rec.Episode)
	}
	if rec.StartedAt == nil || len(rec.Windows) != 2 {
		return fmt.Errorf("sim %q has no recorded episode start; run the simulator first", rec.Name)
	}
	for _, w := range rec.Windows {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", w.Role, w.Start.UTC().Format(time.RFC3339), w.End.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}
