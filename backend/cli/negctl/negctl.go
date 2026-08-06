// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package negctl is a TEMPORARY negative control for the CodeQL gate, and is
// deleted in the following commit. It exists to answer one question that cannot
// be answered locally: does the CodeQL workflow actually run queries against our
// Go code, or does it merely extract it and report nothing?
//
// A scanner that has never reported anything is indistinguishable from one that
// is misconfigured. This plants a textbook go/command-injection — an HTTP query
// parameter flowing unsanitised into a shell — so the gate has to say "some"
// once before its "none" is worth believing.
//
// DO NOT COPY THIS CODE. It is deliberately vulnerable.
package negctl

import (
	"net/http"
	"os/exec"
)

// Handler passes an attacker-controlled request parameter straight to a shell.
func Handler(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("sh", "-c", "echo "+r.URL.Query().Get("name")).Output()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(out)
}
