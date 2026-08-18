// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func runSeed(ctx context.Context, argv []string) error {
	fs := flagSetFor("seed")
	var c connection
	c.bind(fs)
	var instance, receipt string
	fs.StringVar(&instance, "instance", "", "instance id, recorded on the receipt (required)")
	fs.StringVar(&receipt, "receipt", "", "path to write the receipt verify will read (required)")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(instance) == "" || strings.TrimSpace(receipt) == "" {
		return failWith(exitSetup, "--instance and --receipt are both required")
	}

	session, cred, err := c.provision(ctx)
	if err != nil {
		return err
	}

	st := newState(c.tenant)
	out := Receipt{
		Instance: instance,
		Tenant:   c.tenant,
		Identity: cred,
		Written:  time.Now().UTC(),
	}

	for _, e := range entities {
		var envelope map[string]json.RawMessage
		if err := session.Query(ctx, c.areaURL(e.Area), e.Create, e.Vars(st), &envelope); err != nil {
			// exitRefused, not exitSetup: the API answered and said no. That is a
			// verdict about the platform — a validation rule that tightened, a
			// field that was renamed — and it must not be filed under "the
			// environment was not ready".
			return failWith(exitRefused, "create %s via %s: %w", e.Name, c.areaURL(e.Area), err)
		}

		object, ok := envelope[e.CreateKey]
		if !ok {
			return failWith(exitShape, "create %s returned no %q key; the mutation's shape has changed", e.Name, e.CreateKey)
		}

		var fields map[string]any
		if err := json.Unmarshal(object, &fields); err != nil {
			return failWith(exitShape, "create %s returned a non-object under %q: %w", e.Name, e.CreateKey, err)
		}
		token := str(fields["token"])
		if token == "" {
			// Every entity in the table is token-addressed, because that is how
			// verify finds it again. One that came back without a token cannot be
			// read back at all, and recording it would produce a receipt that
			// verify reports as missing forever.
			return failWith(exitShape, "create %s returned no token; verify would have nothing to look it up by", e.Name)
		}
		if e.Record != nil {
			e.Record(st, fields)
		}

		out.Entities = append(out.Entities, Recorded{
			Name: e.Name, Area: e.Area, Token: token, Object: object,
		})
		fmt.Printf("  seeded  %-16s %s\n", e.Name, token)
	}

	if err := writeReceipt(receipt, out); err != nil {
		return err
	}
	fmt.Printf("\n%d entities seeded into tenant %q; receipt at %s\n", len(out.Entities), c.tenant, receipt)
	return nil
}
