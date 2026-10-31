// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strconv"
	"strings"
)

// procState is what this machine can say about a process id.
type procState int

const (
	// procUnknown is the zero value ON PURPOSE: a platform that cannot tell and a
	// probe that was refused both read as "no opinion", and a caller that has no
	// opinion adds nothing to what it prints.
	procUnknown procState = iota
	procRunning
	procNotRunning
)

// probeProcess is the platform probe (procstate_unix.go, procstate_windows.go),
// held in a variable so the wording built on it can be driven without a real pid.
var probeProcess = osProbeProcess

// localHostname is os.Hostname, indirected for the same reason.
var localHostname = os.Hostname

// holderPidOnThisMachine returns the pid inside a holder identity when the identity
// names THIS machine. The identity is user@host/pid/nonce (newHolderIdentity); it is
// parsed from the right because the user part is the only one free to contain
// punctuation.
//
// 🔴 A HOLDER THAT DOES NOT PARSE, OR NAMES ANOTHER HOST, YIELDS NO PID. A pid is
// meaningful only on the machine that issued it; reading one from a foreign identity
// and probing it here would answer about an unrelated local process.
func holderPidOnThisMachine(holder string) (pid int, ok bool) {
	parts := strings.Split(holder, "/")
	if len(parts) < 3 {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil || pid <= 0 {
		return 0, false
	}
	userHost := strings.Join(parts[:len(parts)-2], "/")
	at := strings.LastIndex(userHost, "@")
	if at < 0 || at == len(userHost)-1 {
		return 0, false
	}
	here, err := localHostname()
	if err != nil || here == "" || userHost[at+1:] != here {
		return 0, false
	}
	return pid, true
}

// holderIsGoneFromThisMachine reports gone only when the holder identity names this
// machine AND its pid is positively not a running process. Every other answer —
// another host, unparseable, alive, or "could not tell" — is false, and the caller
// says nothing extra.
//
// It is evidence for the operator to read, never a verdict dcctl acts on: the lock is
// still taken only by an explicit `dcctl instances reclaim`. A recycled pid reads as
// running, which errs toward saying nothing.
func holderIsGoneFromThisMachine(holder string) (pid int, gone bool) {
	pid, ok := holderPidOnThisMachine(holder)
	if !ok {
		return 0, false
	}
	return pid, probeProcess(pid) == procNotRunning
}
