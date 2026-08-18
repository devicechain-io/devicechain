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
	var instance, receipt, baselineDir string
	fs.StringVar(&instance, "instance", "", "instance id, recorded on the receipt (required)")
	fs.StringVar(&receipt, "receipt", "", "path to write the receipt verify will read (required)")
	fs.StringVar(&baselineDir, "baseline-schemas", "",
		"a `backend/services` tree from the release being seeded, so entities that release cannot express are skipped instead of refused")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(instance) == "" || strings.TrimSpace(receipt) == "" {
		return failWith(exitSetup, "--instance and --receipt are both required")
	}

	var base *baseline
	if strings.TrimSpace(baselineDir) != "" {
		var err error
		if base, err = loadBaseline(baselineDir); err != nil {
			return err
		}
	}

	session, cred, err := c.provision(ctx)
	if err != nil {
		return err
	}

	st := newState(c.tenant)
	var skipped []string
	out := Receipt{
		Instance: instance,
		Tenant:   c.tenant,
		Identity: cred,
		Written:  time.Now().UTC(),
	}

	for _, e := range entities {
		write, why, err := plan(e, base)
		if err != nil {
			return err
		}
		if !write {
			skipped = append(skipped, e.Name)
			fmt.Printf("  skipped %-26s %s\n", e.Name, why)
			continue
		}

		var envelope map[string]json.RawMessage
		if err := session.Query(ctx, c.areaURL(e.Area), e.createDoc(), e.Vars(st), &envelope); err != nil {
			// exitRefused, not exitSetup: the API answered and said no. That is a
			// verdict about the platform — a validation rule that tightened, a
			// field that was renamed — and it must not be filed under "the
			// environment was not ready".
			return failWith(exitRefused, "create %s via %s: %w", e.Name, c.areaURL(e.Area), err)
		}

		created, ok := envelope[e.Mutation]
		if !ok {
			return failWith(exitShape, "create %s returned no %q key; the mutation's shape has changed", e.Name, e.Mutation)
		}

		objects, err := createdObjects(e, created)
		if err != nil {
			return err
		}
		for i, object := range objects {
			var fields map[string]any
			if err := json.Unmarshal(object, &fields); err != nil {
				return failWith(exitShape, "create %s returned a non-object: %w", e.Name, err)
			}
			token := str(fields["token"])
			if token == "" {
				// Every entity in the table is token-addressed, because that is
				// how verify finds it again. One that came back without a token
				// cannot be read back at all, and recording it would produce a
				// receipt that verify reports as missing forever.
				return failWith(exitShape, "create %s returned no token; verify would have nothing to look it up by", e.Name)
			}
			// Only the first of a bulk create feeds the state. Later entries
			// reference ONE token, and silently taking the last of N would make
			// which one they got depend on the batch size.
			if e.Record != nil && i == 0 {
				e.Record(st, fields)
			}

			out.Entities = append(out.Entities, Recorded{
				Name: e.Name, Area: e.Area, Token: token, Object: object, Fields: e.Fields,
			})
			fmt.Printf("  seeded  %-26s %s\n", e.Name, token)
		}
	}

	// A receipt with nothing on it makes verify pass instantly, having checked
	// nothing. That is the vacuous green this whole rig exists to refuse, and a
	// baseline filter is the most plausible way to produce one by accident.
	if len(out.Entities) == 0 {
		return failWith(exitSetup, "nothing was seeded: all %d entities were skipped against the baseline at %s",
			len(entities), baselineDir)
	}

	out.Skipped = skipped
	if err := writeReceipt(receipt, out); err != nil {
		return err
	}
	fmt.Printf("\n%d rows from %d of %d entities seeded into tenant %q; receipt at %s\n",
		len(out.Entities), len(entities)-len(skipped), len(entities), c.tenant, receipt)
	if len(skipped) > 0 {
		// Named, not just counted. "24 of 26" tells a reader how much was
		// covered; only the names tell them what a green verify says nothing
		// about.
		fmt.Printf("skipped, because the baseline at %s cannot express them: %s\n",
			baselineDir, strings.Join(skipped, ", "))
	}
	return nil
}

// createdObjects unwraps a create response into the objects it actually made:
// one for most entities, N for a bulk create, and — for a create that answers
// with a result envelope — the object inside it, or a refusal.
//
// 🔑 A REFUSAL IS NOT AN ABSENT OBJECT. createCommand and createCommandBatch
// answer {command|batch, rejection}: the request reached the platform, was
// understood, and was declined with a reason. Reporting that as a shape problem
// would send a reader looking for a schema change; it is exitRefused, and the
// whole envelope is quoted so the code and reason travel with it.
func createdObjects(e entity, created json.RawMessage) ([]json.RawMessage, error) {
	if e.Wrap != "" {
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(created, &wrapper); err != nil {
			return nil, failWith(exitShape, "create %s returned a non-object envelope: %w", e.Name, err)
		}
		inner, ok := wrapper[e.Wrap]
		if !ok {
			return nil, failWith(exitShape, "create %s returned no %q key inside its envelope; the shape has changed", e.Name, e.Wrap)
		}
		if isJSONNull(inner) {
			return nil, failWith(exitRefused, "create %s was REFUSED: %s", e.Name, string(created))
		}
		created = inner
	}

	if !e.Bulk {
		return []json.RawMessage{created}, nil
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(created, &objects); err != nil {
		return nil, failWith(exitShape, "create %s returned a non-list: %w", e.Name, err)
	}
	if len(objects) == 0 {
		// A bulk create that made nothing and said so is not a refusal — it is a
		// success that recorded no rows, which would leave the receipt shorter
		// than the table and verify passing over the gap without comment.
		return nil, failWith(exitShape, "create %s returned an empty list; it was asked for rows and made none", e.Name)
	}
	return objects, nil
}

// isJSONNull reports whether raw is a JSON null, tolerating surrounding space.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
