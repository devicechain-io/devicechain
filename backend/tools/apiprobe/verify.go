// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func runVerify(ctx context.Context, argv []string) error {
	fs := flagSetFor("verify")
	var c connection
	c.bind(fs)
	var receipt string
	fs.StringVar(&receipt, "receipt", "", "path to the receipt seed wrote (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}

	rec, err := readReceipt(receipt)
	if err != nil {
		return err
	}
	// The receipt names the tenant, so honour it over the flag's default —
	// verifying the wrong tenant would report every entity missing, which is a
	// false FINDING rather than a setup error, and the loudest possible way to
	// mislead someone mid-upgrade.
	c.tenant = rec.Tenant
	session := c.session(rec.Identity)

	// byName lets a receipt be verified against the current table even if the
	// table has since grown: an entity the receipt does not carry was not seeded
	// by the build that wrote it, and is not this run's business.
	byName := map[string]entity{}
	for _, e := range entities {
		byName[e.Name] = e
	}

	checked := 0
	for _, r := range rec.Entities {
		e, ok := byName[r.Name]
		if !ok {
			// The receipt carries an entity this build does not know how to read.
			// Inconclusive rather than a finding: the tool changed, not the data.
			return failWith(exitSetup, "receipt records %q, which this build of apiprobe cannot read back", r.Name)
		}

		var envelope map[string]json.RawMessage
		if err := session.Query(ctx, c.areaURL(e.Area), e.readDoc(), map[string]any{"token": r.Token}, &envelope); err != nil {
			// The server rejected the QUERY. The row may be perfectly intact; a
			// field this tool selects no longer exists on the type. Reporting
			// that as missing data sends a reader into the migrations hunting for
			// something that was never wrong.
			return failWith(exitShape, "read %s %q back: %w", r.Name, r.Token, err)
		}

		found, err := readObject(e, envelope, r.Token)
		if err != nil {
			return err
		}

		want, err := canonical(r.Object)
		if err != nil {
			return failWith(exitSetup, "receipt entry %s is unreadable: %w", r.Name, err)
		}
		got, err := canonical(found)
		if err != nil {
			return failWith(exitShape, "read %s returned unparseable JSON: %w", r.Name, err)
		}
		if want != got {
			return failWith(exitMismatch, "%s %q CHANGED across the upgrade:\n   written: %s\n   read:    %s",
				r.Name, r.Token, want, got)
		}

		checked++
		fmt.Printf("  ok      %-26s %s\n", r.Name, r.Token)
	}

	// A pass is only worth reading if it says how much it covered. "verified"
	// with no number is how a run that checked nothing reads exactly like one
	// that checked everything.
	fmt.Printf("\n%d of %d recorded rows read back unchanged from tenant %q.\n",
		checked, len(rec.Entities), rec.Tenant)
	if len(entities) < tenantCreateMutations {
		fmt.Printf("⚠️  apiprobe covers %d of %d create mutations; run `apiprobe coverage` for the list.\n",
			len(entities), tenantCreateMutations)
	}
	return nil
}

// readObject pulls the one entity out of a read-back response, in whichever of
// the two shapes the schema serves — and decides what ABSENCE means.
//
// 🔑 THIS IS WHERE exitMissing IS DECIDED, and the two shapes reach it by
// different routes: an empty list, or an explicit null. Both are the platform
// saying "there is nothing under that token", which is the finding. Anything
// else — a key that vanished, a list that became an object — is the QUERY no
// longer matching the schema, and that is exitShape: the row may be perfectly
// intact, and reporting it as data loss sends a reader into the migrations
// hunting for something that was never wrong.
func readObject(e entity, envelope map[string]json.RawMessage, token string) (json.RawMessage, error) {
	raw, ok := envelope[e.Read]
	if !ok {
		return nil, failWith(exitShape, "read %s returned no %q key; the query's shape has changed", e.Name, e.Read)
	}

	if e.Single {
		if isJSONNull(raw) {
			return nil, failWith(exitMissing, "%s %q is GONE: the API created it, and %s now returns null for that token",
				e.Name, token, e.Read)
		}
		return raw, nil
	}

	var found []json.RawMessage
	if err := json.Unmarshal(raw, &found); err != nil {
		return nil, failWith(exitShape, "read %s returned a non-list under %q: %w", e.Name, e.Read, err)
	}
	if len(found) == 0 {
		return nil, failWith(exitMissing, "%s %q is GONE: the API created it, and now returns nothing for that token",
			e.Name, token)
	}
	return found[0], nil
}
