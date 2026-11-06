// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	fmt.Println(renderControlBody(strings.TrimSpace(string(body)), stdoutIsTerminal()))
	return nil
}

// stdoutIsTerminal is the seam for "is a person reading this", indirected so a test can
// drive either branch (a `go test` run from a shell has a real terminal on stdout).
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// renderControlBody is what `sim status|start|stop` print.
//
// The sim's control API answers one line of JSON, and that line is what a script gets
// when it pipes or captures this command: it is passed through BYTE FOR BYTE, because a
// script may parse it and dcctl output is a served interface. Only when stdout is a
// terminal is it indented, since there a one-line object is unreadable and nothing can
// be parsing it. A body that is not valid JSON is printed as received either way.
func renderControlBody(body string, toTerminal bool) string {
	if !toTerminal || !json.Valid([]byte(body)) {
		return body
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(body), "", "  "); err != nil {
		return body
	}
	return out.String()
}
