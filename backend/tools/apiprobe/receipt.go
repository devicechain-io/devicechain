// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Receipt is what seed hands to verify, and the two halves run in different
// processes against different builds of the platform — before an upgrade and
// after it. So it records the ANSWER the API gave, not the request that was sent:
// what has to survive is what a client can read back.
type Receipt struct {
	Instance string     `json:"instance"`
	Tenant   string     `json:"tenant"`
	Identity Credential `json:"identity"`
	Written  time.Time  `json:"written"`
	Entities []Recorded `json:"entities"`
}

// Credential is the tenant identity seed created, carried so verify can log in
// as the same principal.
//
// ⚠️ It is a drill credential in a drill tenant on a throwaway instance, and the
// receipt is a local file. It is NOT a pattern to copy anywhere a real password
// is involved.
type Credential struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Recorded is one entity as the platform returned it at seed time.
type Recorded struct {
	Name  string `json:"name"`
	Area  string `json:"area"`
	Token string `json:"token"`

	// Object is the create response, verbatim. Verify re-runs the read query and
	// compares against this — so the comparison is against what the platform
	// SAID, and a field this tool never selected can change without anyone
	// noticing. That limit is real, and it is why Read must select the same
	// fields as Create.
	Object json.RawMessage `json:"object"`
}

func writeReceipt(path string, r Receipt) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return failWith(exitSetup, "encode receipt: %w", err)
	}
	// 0600: it carries a password, drill-scoped or not.
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return failWith(exitSetup, "write receipt %s: %w", path, err)
	}
	return nil
}

func readReceipt(path string) (Receipt, error) {
	var r Receipt
	body, err := os.ReadFile(path)
	if err != nil {
		return r, failWith(exitSetup, "read receipt %s: %w", path, err)
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, failWith(exitSetup, "parse receipt %s: %w", path, err)
	}
	if len(r.Entities) == 0 {
		// A receipt with no entities would make verify pass instantly, having
		// checked nothing — the vacuous-green shape this whole rig exists to
		// refuse. Treat it as a broken instrument, not an empty result.
		return r, failWith(exitSetup, "receipt %s records no entities; nothing to verify", path)
	}
	return r, nil
}

// canonical renders a JSON value with map keys sorted, so two responses that
// differ only in key order compare equal. encoding/json sorts object keys when
// marshalling a map, which is the whole trick.
func canonical(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(out), nil
}
