// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var simStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show a running sim's lifecycle state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return simControl(cmd, args[0], http.MethodGet, "/status")
	},
	SilenceUsage: true,
}

var simStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start (resume) a stopped sim's emit loop",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return simControl(cmd, args[0], http.MethodPost, "/start")
	},
	SilenceUsage: true,
}

var simStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a running sim's emit loop (tenant + data are kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return simControl(cmd, args[0], http.MethodPost, "/stop")
	},
	SilenceUsage: true,
}

func init() {
	simCmd.AddCommand(simStatusCmd, simStartCmd, simStopCmd)
}

// simControl calls the running sim actor's control API (hosted by the sim process,
// not the platform) and prints its JSON response. dcctl drives lifecycle here; it
// never reaches into the sim's storage — there is none.
func simControl(cmd *cobra.Command, name, method, path string) error {
	rec, err := sim.Load(name)
	if err != nil {
		return err
	}
	url := strings.TrimRight(rec.ControlAddr, "/") + path

	req, err := http.NewRequestWithContext(cmd.Context(), method, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach sim control API at %s (is `dc-simulator` running for %q?): %w", rec.ControlAddr, name, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sim control %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Println(strings.TrimSpace(string(body)))
	return nil
}
