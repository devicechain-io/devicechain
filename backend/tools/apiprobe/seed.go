// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	var sso ssoOptions
	fs.StringVar(&sso.issuer, "idp-issuer", "",
		"https issuer of an identity provider the INSTANCE can reach; seeds a single sign-on connection "+
			"(admin plane). Without it the connection is skipped by name, and the table sweep then reports "+
			"the tables it feeds as uncovered")
	fs.StringVar(&sso.clientID, "idp-client-id", ssoDefaultClientID, "client identifier registered at the --idp-issuer provider")
	fs.StringVar(&sso.domain, "idp-domain", "",
		"email domain asserted for the seeded connection (default: one derived from the tenant, under "+ssoDomainSuffix+
			"; a domain is unique across the instance, so two tenants' seeds must not share one)")
	deployedFn := bindDeployedAreas(fs)
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	deployed, err := deployedFn()
	if err != nil {
		return err
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

	for _, e := range allEntities() {
		skip, why, err := skipForUndeployedArea(e, deployed)
		if err != nil {
			return err
		}
		if skip {
			skipped = append(skipped, e.Name)
			fmt.Printf("  skipped %-26s %s\n", e.Name, why)
			continue
		}
		write, why, err := plan(e, base)
		if err != nil {
			return err
		}
		if !write {
			skipped = append(skipped, e.Name)
			fmt.Printf("  skipped %-26s %s\n", e.Name, why)
			continue
		}

		// 🔑 REBOUND, not shadowed in a narrower scope, because everything below
		// has to see the SAME entity: createDoc renders the envelope, and
		// createdObjects unwraps it. Adapting for one and not the other would
		// send a document the response is then decoded against the wrong rule.
		if adapted := base.adapt(e); adapted.Wrap != e.Wrap {
			fmt.Printf("  adapted %-26s the baseline's %s returns the object directly; seeding without the result envelope\n",
				e.Name, e.Mutation)
			e = adapted
		}

		// Bound rather than called inline: a publish records the token it SENT, and
		// building the variables twice would leave the receipt free to disagree with
		// what the mutation acted on.
		vars := e.Vars(st)
		if base != nil && e.BaselineInput != nil {
			e.BaselineInput(base, e, vars)
		}

		var after func(context.Context) error
		if e.After != nil {
			after = func(ctx context.Context) error { return e.After(ctx, &c, session, st) }
		}
		rows, err := seedOne(ctx, session, c.areaURL(e.Area), e, vars, st, after, os.Stdout)
		if err != nil {
			return err
		}
		out.Entities = append(out.Entities, rows...)
	}

	// The admin-plane row. Skipped by name, like an entity, when the caller named no
	// provider or the baseline cannot express it; never silently.
	entitySkips := len(skipped)
	adminRows, adminSkipped, err := seedAdminRows(ctx, sso, base, c.tenant, c.adminURL(),
		func(ctx context.Context) (querier, error) { return c.adminSession(ctx) }, os.Stdout)
	if err != nil {
		return err
	}
	out.Admin = adminRows
	skipped = append(skipped, adminSkipped...)

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
	// The entity count is of ENTITIES: the admin-plane row is not one of them, so its
	// skip must not be subtracted from them, and it is stated on a line of its own.
	fmt.Printf("\n%d rows from %d of %d entities seeded into tenant %q; receipt at %s\n",
		len(out.Entities), len(entities)-entitySkips, len(entities), c.tenant, receipt)
	fmt.Printf("%d admin-plane row(s) seeded\n", len(out.Admin))
	if entitySkips > 0 {
		// Named, not just counted. "24 of 26" tells a reader how much was
		// covered; only the names tell them what a green verify says nothing
		// about.
		fmt.Printf("skipped, because the baseline at %s cannot express them or their area is not deployed: %s\n",
			baselineDir, strings.Join(skipped[:entitySkips], ", "))
	}
	if len(adminSkipped) > 0 {
		fmt.Printf("skipped admin-plane row(s), for the reason printed beside each above: %s\n",
			strings.Join(adminSkipped, ", "))
	}
	return nil
}

// seedAdminRows writes the admin-plane rows (today the single sign-on connection) or
// names each one skipped, with its reason. The superuser's session is opened only when
// a row will be written, so a seed with nothing to write there needs no password.
//
// Its own function so the decision and the receipt bookkeeping are tested through the
// code runSeed calls, not a copy of it: a row seeded and then left off the receipt, or a
// skip left off the skipped list, would both make verify's report claim more than ran.
func seedAdminRows(ctx context.Context, o ssoOptions, base *baseline, tenant, adminURL string,
	session func(context.Context) (querier, error), w io.Writer,
) ([]Recorded, []string, error) {
	ok, why := o.willSeed(base)
	if !ok {
		fmt.Fprintf(w, "  skipped %-26s %s\n", ssoRowName, why)
		return nil, []string{ssoRowName}, nil
	}
	admin, err := session(ctx)
	if err != nil {
		return nil, nil, err
	}
	row, err := seedConnection(ctx, admin, adminURL, tenant, o)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(w, "  seeded  %-26s %s\n", row.Name, row.Token)
	return []Recorded{row}, nil, nil
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
// How long the seed waits for a row its After step changed to read back settled, and how
// often it asks. Variables so a test can make the wait instant; nothing else sets them.
var (
	settleTimeout  = 30 * time.Second
	settleInterval = time.Second
)

// seedOne creates one entity, runs its After step, and returns the rows to record.
//
// 🔑 WHAT IS RECORDED IS WHAT VERIFY WILL READ, ON A HEALTHY INSTANCE. For a plain create
// that is the create response. For an entity with an After step it is NOT: the step
// changes the row (an upload makes an artifact AVAILABLE), so the response describes a row
// that no longer exists. The row is then read back with readRow, the very query verify
// sends, until the entity's Settled accepts it, and THAT is recorded. Nothing is dropped
// from the comparison: a state an upgrade changes still fails verify.
func seedOne(ctx context.Context, q querier, url string, e entity, vars map[string]any, st *state,
	after func(context.Context) error, w io.Writer) ([]Recorded, error) {
	if e.After != nil && e.Settled == nil {
		return nil, failWith(exitSetup, "%s has an After step but no Settled check, so the row it changes "+
			"cannot be recorded as it will be read back", e.Name)
	}

	var envelope map[string]json.RawMessage
	if err := q.Query(ctx, url, e.createDoc(), vars, &envelope); err != nil {
		// exitRefused, not exitSetup: the API answered and said no. That is a
		// verdict about the platform — a validation rule that tightened, a
		// field that was renamed — and it must not be filed under "the
		// environment was not ready".
		return nil, failWith(exitRefused, "create %s via %s: %w", e.Name, url, err)
	}

	created, ok := envelope[e.Mutation]
	if !ok {
		return nil, failWith(exitShape, "create %s returned no %q key; the mutation's shape has changed", e.Name, e.Mutation)
	}

	objects, err := createdObjects(e, created)
	if err != nil {
		return nil, err
	}
	var rows []Recorded
	for i, object := range objects {
		var fields map[string]any
		if err := json.Unmarshal(object, &fields); err != nil {
			return nil, failWith(exitShape, "create %s returned a non-object: %w", e.Name, err)
		}
		token := str(fields["token"])
		if e.TokenFrom != nil {
			// The created object carries no token of its own — a natural-keyed row,
			// addressed by (entityType, entity, scope, attrKey) or (memberType, key).
			// The receipt still needs a KEY, and it is only that: the read-back is
			// addressed by the table's criteria, never by this string.
			token = e.TokenFrom(st)
		}
		if e.Publish {
			// A published version has no token of its own — it is addressed by
			// (parent token, version number) — so the receipt records the PARENT's,
			// which is also exactly what its read-back query is keyed by. Taken from
			// the variables that were SENT rather than invented here, so the value
			// recorded is the one the mutation actually acted on.
			token = str(vars["token"])
		}
		if token == "" {
			// Every entity in the table is token-addressed, because that is
			// how verify finds it again. One that came back without a token
			// cannot be read back at all, and recording it would produce a
			// receipt that verify reports as missing forever.
			return nil, failWith(exitShape, "create %s returned no token; verify would have nothing to look it up by", e.Name)
		}
		// Only the first of a bulk create feeds the state. Later entries
		// reference ONE token, and silently taking the last of N would make
		// which one they got depend on the batch size.
		if e.Record != nil && i == 0 {
			e.Record(st, fields)
		}
		rows = append(rows, Recorded{Name: e.Name, Area: e.Area, Token: token, Object: object, Fields: e.Fields})
	}

	if e.After != nil {
		if len(rows) != 1 {
			return nil, failWith(exitSetup, "%s has an After step but created %d rows; it can settle exactly one", e.Name, len(rows))
		}
		if err := after(ctx); err != nil {
			return nil, failWith(exitRefused, "after creating %s: %w", e.Name, err)
		}
		settled, err := readSettled(ctx, q, url, e, rows[0])
		if err != nil {
			return nil, err
		}
		rows[0].Object = settled
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  seeded  %-26s %s\n", r.Name, r.Token)
	}
	return rows, nil
}

// readSettled reads r back with verify's own query until e.Settled accepts it, for at
// most settleTimeout. A row that never settles is a refusal of the platform, named with
// the last reason Settled gave: recording it unsettled would turn into a false CHANGED
// after the upgrade, and skipping it would be coverage that quietly stops existing.
func readSettled(ctx context.Context, q querier, url string, e entity, r Recorded) (json.RawMessage, error) {
	deadline := time.Now().Add(settleTimeout)
	for {
		found, err := readRow(ctx, q, url, e, r.Token, r.Object)
		if err != nil {
			return nil, err
		}
		var fields map[string]any
		if err := json.Unmarshal(found, &fields); err != nil {
			return nil, failWith(exitShape, "read %s back: not an object: %w", e.Name, err)
		}
		why := e.Settled(fields)
		if why == nil {
			return found, nil
		}
		if !time.Now().Before(deadline) {
			return nil, failWith(exitRefused, "%s %q never settled after its After step (waited %s): %v",
				e.Name, r.Token, settleTimeout, why)
		}
		select {
		case <-ctx.Done():
			return nil, failWith(exitSetup, "waiting for %s to settle: %w", e.Name, ctx.Err())
		case <-time.After(settleInterval):
		}
	}
}

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
