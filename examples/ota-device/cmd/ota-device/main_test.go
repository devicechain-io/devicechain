// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devicechain.io/examples/ota-device/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.Cleanup()
	os.Exit(code)
}

// The version a device reports is the one the linker compiled into the binary, and `describe`
// answers it with an empty environment and no arguments beyond the word itself.
func TestDescribeReportsWhatWasCompiledIn(t *testing.T) {
	bin := testutil.Build(t, "./cmd/ota-device",
		"-X devicechain.io/examples/ota-device/internal/buildinfo.Version=7.7.7 -X devicechain.io/examples/ota-device/internal/buildinfo.Product=widget")
	cmd := exec.Command(bin, "describe")
	cmd.Env = []string{}
	out, err := cmd.Output()
	require.NoError(t, err)
	var id struct{ Product, Version string }
	require.NoError(t, json.Unmarshal(out, &id))
	assert.Equal(t, "7.7.7", id.Version)
	assert.Equal(t, "widget", id.Product)
}

func TestRefusesToRunWithoutItsConfiguration(t *testing.T) {
	bin := testutil.Build(t, "./cmd/ota-device", "")
	cmd := exec.Command(bin)
	cmd.Env = []string{}
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 2, ee.ExitCode())
}

// The broker carries the device credential and the update traffic: a scheme without TLS is refused
// unless the operator says otherwise.
func TestBrokerMustBeTLSUnlessAllowed(t *testing.T) {
	bin := testutil.Build(t, "./cmd/ota-device", "")
	base := []string{"--root", t.TempDir(), "--instance", "i", "--tenant", "t", "--device", "d", "--credential-id", "c",
		"--origin", "https://dc.example.com", "--allow-unsigned"}
	run := func(extra ...string) (int, string) {
		cmd := exec.Command(bin, append(base, extra...)...)
		cmd.Env = []string{}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), string(out)
		}
		return 0, string(out)
	}
	for _, b := range []string{"tcp://mqtt.example.com:1883", "mqtt://mqtt.example.com:1883", "ws://mqtt.example.com:80"} {
		code, out := run("--broker", b)
		assert.Equal(t, 2, code, b)
		assert.Contains(t, out, "not TLS", b)
	}
	// negative control: a TLS scheme gets past the check (and then fails later, for want of a broker, not with 2)
	code, out := run("--broker", "ssl://127.0.0.1:1", "--ca", filepath.Join(t.TempDir(), "missing.pem"))
	assert.NotContains(t, out, "not TLS")
	assert.Equal(t, 1, code)
}

type fakeToken struct {
	done bool
	err  error
}

func (f fakeToken) Wait() bool                     { return f.done }
func (f fakeToken) WaitTimeout(time.Duration) bool { return f.done }
func (f fakeToken) Done() <-chan struct{}          { c := make(chan struct{}); close(c); return c }
func (f fakeToken) Error() error                   { return f.err }

// A subscription that was never acknowledged, that errored, or whose grant cannot be read is not
// a subscription: the device must not run deaf.
func TestSubscribeGrantedRefusesWhatItCannotConfirm(t *testing.T) {
	assert.Error(t, subscribeGranted(fakeToken{done: false}, "f", time.Millisecond), "no SUBACK")
	assert.Error(t, subscribeGranted(fakeToken{done: true, err: errors.New("boom")}, "f", time.Millisecond), "error")
	assert.Error(t, subscribeGranted(fakeToken{done: true}, "f", time.Millisecond), "not a SubscribeToken, so no grant to read")
}
