// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

	// Skipped names the entities the seed could NOT write, because the release it
	// was seeding did not declare them (see baseline.go). It travels on the
	// receipt so that verify's report is about the same coverage the seed had —
	// otherwise "everything read back" would be measured against a table the
	// other half of the drill never used, and the gap would exist only in the
	// scrollback of a run that finished hours earlier.
	Skipped []string `json:"skipped,omitempty"`
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

	// Fields is the selection Object was produced with, and it travels because
	// the two halves of this drill are DIFFERENT BUILDS of apiprobe — nothing
	// requires them to agree.
	//
	// 🔴 WITHOUT IT, EDITING A SELECTION MID-DRILL PRODUCES A CONFIDENT LIE.
	// Verify reads with the CURRENT build's Fields and compares against an Object
	// the seed captured with the OLD one, so a selection that gained or lost a
	// field yields two objects with different keys — reported as
	// "CHANGED across the upgrade", naming a row and a token, with a diff. It is
	// the single most convincing output this tool can produce and it would be
	// about the tool. Recorded here so verify can refuse instead: an
	// inconclusive setup error, which is what a tool that changed underneath its
	// own measurement deserves.
	Fields string `json:"fields"`
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
	out, err := json.Marshal(normalizeEmbedded(v))
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(out), nil
}

// normalizeEmbedded rewrites every string that is ITSELF a JSON object or array
// into its canonical form.
//
// 🔑 WITHOUT THIS THE PROBE WOULD REPORT ITS OWN FORMATTING AS DATA LOSS. The
// platform's opaque documents — metadata, a rule definition, a connector config,
// a dashboard, a command payload — are `String` in the schema and `jsonb` in
// Postgres. jsonb does not store bytes; it stores a parsed value and prints it
// back with its own spacing and key order. A create response is built from the
// struct that was just written, so it echoes the caller's exact bytes; the
// read-back a version later comes from the column. `{"probe":"x"}` in,
// `{"probe": "x"}` out — same data, different string, and a MISMATCH naming a
// field nobody touched.
//
// Nothing real is hidden by this. Canonicalising two documents does not make
// different documents equal: a changed key, a changed value, an added or dropped
// entry all still differ. Only the rendering stops counting — which is the
// rendering this tool never had an opinion about.
//
// Only objects and arrays are rewritten. A bare scalar string is left exactly as
// it is, so an ordinary field that happens to hold "42" or "true" is compared as
// the text it is rather than quietly reinterpreted as a number.
func normalizeEmbedded(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			t[k] = normalizeEmbedded(sub)
		}
		return t
	case []any:
		for i, sub := range t {
			t[i] = normalizeEmbedded(sub)
		}
		return t
	case string:
		trimmed := strings.TrimSpace(t)
		if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
			return t
		}
		var inner any
		if err := json.Unmarshal([]byte(trimmed), &inner); err != nil {
			// It opens like a document and is not one. Leave it verbatim: this
			// is a normalizer, not a validator, and rejecting here would turn a
			// field the platform stores happily into a probe failure.
			return t
		}
		out, err := json.Marshal(normalizeEmbedded(inner))
		if err != nil {
			return t
		}
		return string(out)
	default:
		return v
	}
}
