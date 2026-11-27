// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// execPluginHelperOS is the helper's terminal modes:
//
//   - needs-tty is a plugin that issues its credential only to someone at a terminal: it
//     opens the controlling terminal, and failing that looks at its stdin.
//   - tty-runner is dcctl at a terminal: it runs the advisory checks against the API
//     server in DCCTL_TEST_API, with a needs-tty plugin, and prints the authentication
//     row's verdict.
func execPluginHelperOS(mode string, credential func(string, time.Time, string, string) int) int {
	switch mode {
	case "needs-tty":
		if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			_ = f.Close()
			return credential(pluginToken, time.Now().Add(time.Hour), "", "")
		}
		if _, err := unix.IoctlGetTermios(0, unix.TCGETS); err == nil {
			return credential(pluginToken, time.Now().Add(time.Hour), "", "")
		}
		fmt.Fprintln(os.Stderr, "this plugin needs a terminal")
		return 1
	case "escaper":
		// A plugin whose child leaves its session, and so its process group, and holds
		// its stdout open.
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), execPluginHelperEnv+"=sleeper")
		c.Stdout = os.Stdout
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			return 5
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(os.Getenv(pluginPIDFileEnv) + ".child"); err == nil {
				defer stampExit()
				return credential(pluginToken, time.Now().Add(time.Hour), "", "")
			}
		}
		return 5
	case "tty-runner":
		cfg := &rest.Config{Host: os.Getenv("DCCTL_TEST_API"), ExecProvider: &clientcmdapi.ExecConfig{
			Command: os.Args[0], APIVersion: pluginAPIVersion, InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
			Env: []clientcmdapi.ExecEnvVar{{Name: execPluginHelperEnv, Value: "needs-tty"}}}}
		p, err := cloudProbesFrom(context.Background(), cfg)
		if err != nil {
			fmt.Println("error", err)
			return 1
		}
		p.http = &http.Client{Transport: refuseAll{}}
		verdict := "none"
		d := &doctor{observe: func(o installflow.PreflightObservation) {
			if o.Observation == installflow.ObserveCheck && strings.HasPrefix(o.Text.String(), "authentication") {
				verdict = string(o.Verdict)
			}
		}}
		d.checkCloud(context.Background(), prodTarget, p)
		fmt.Println("auth=" + verdict)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
	return 2
}

type refuseAll struct{}

func (refuseAll) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }

// 🔴 A PLUGIN NEVER GETS A TERMINAL: dcctl runs at one, but the plugin runs in its own
// session, with no controlling terminal and stdin on the null device, so a plugin that
// would prompt cannot, and a credential it issues only to a person at a terminal is not
// issued. (Run with the plugin in dcctl's session, or with dcctl's stdin, and the
// needs-tty plugin authenticates: that is the negative control.)
func TestAPluginGetsNoTerminal(t *testing.T) {
	m, s := openCmdPTY(t)
	srv, api := pluginCluster(t, false)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), execPluginHelperEnv+"=tty-runner", "DCCTL_TEST_API="+srv.URL)
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s, &out, s
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	go func() { _, _ = io.Copy(io.Discard, m) }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the runner failed: %v (%s)", err, out.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the runner did not finish")
	}
	got := strings.TrimSpace(out.String())
	if got != "auth="+string(installflow.CheckUnknown) {
		t.Errorf("a plugin that needs a terminal reads %q", got)
	}
	if _, auth := api.snapshot(); len(auth) != 0 {
		t.Errorf("a credential issued at the terminal was sent: %v", auth)
	}
}

// 🔴 A DESCENDANT THAT LEAVES THE PLUGIN'S PROCESS GROUP (it starts a session of its own)
// is out of reach of the group kill, and holds the plugin's stdout open. It is still
// ended with the run (the run's marker finds it), so it neither holds the run nor
// outlives it.
func TestAnEscapedDescendantDoesNotHoldTheRun(t *testing.T) {
	old := execPluginTimeout
	t.Cleanup(func() { execPluginTimeout = old })
	execPluginTimeout = 8 * time.Second
	srv, _ := pluginCluster(t, false)
	cfg, pidFile := pluginConfig(t, srv.URL, "escaper")
	t.Cleanup(func() {
		for _, pid := range pids(pidFile) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	run := runWithPlugin(t, cfg, nil)
	// The pipes are given up on commandWaitDelay after the plugin's exit; without that
	// bound the run would wait out execPluginTimeout.
	if after := sinceExit(t, pidFile, run.end); after >= commandWaitDelay/2 {
		t.Errorf("the run ended %v after the plugin exited", after)
	}
	assertNoProcessLeft(t, pidFile, 2)
	if r := authRow(t, run); r.Verdict != installflow.CheckPass {
		t.Errorf("authentication reads %s %q (%s)", r.Verdict, r.Text, r.Fix)
	}
}
