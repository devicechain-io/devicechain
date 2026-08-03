// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var simDestroyCmd = &cobra.Command{
	Use:   "destroy <name>",
	Short: "Delete a sim's scoped identity and its tenant",
	Long: `Tear a sim back down: delete its scoped identity (with its membership), then its
tenant, then the local sim record.

The tenant goes with the sim, unconditionally. A sim tenant exists only to hold that
sim's entities, and the identity 'sim create' minted is the only member it is ever
given — so a kept tenant is one that appears in nobody's tenant menu, reachable only
by a superuser breaking glass into it through the API. (This used to be opt-in behind
--purge, whose default claimed to keep the tenant "for inspection" while deleting the
membership that made inspection routine.)

Teardown order is enforced: the identity goes first, because the server refuses a
tenant delete while any membership still references it.

🔴 THE NAME STAYS TAKEN UNTIL THE PURGE FINISHES. 'dcctl sim create <name>' refuses a
name whose tenant is still being deleted, and that refusal is the point: the tenant
token is the key every functional area stores its rows under, so a sim recreated at a
name whose data is still there would attach to the previous run's devices, dashboards
and telemetry instead of starting clean. Once every area reports its rows reclaimed the
name is free again.

What this does NOT delete, yet: those rows in the other functional areas. Nothing
cascades a tenant delete to them today, so they are kept (telemetry too, unless the
instance opted in to a retention policy) until the tenant purge reclaims them — which
means, until that sweep exists, the name does not come back.

Human access ends at once: no membership remains and no token can be minted. The DEVICE
plane is cut too, but it drains rather than stopping dead, and the difference matters if
you are watching a dashboard. New connects, ingest and command dispatch are refused
within about a minute (the refusal is a cached read, refreshed on that interval). A
session already established keeps its broker-issued credential until it expires — up to
12 hours by default — though its ingest is refused at admission regardless. And a device
still holding a lifetime or a subscription drops off on its own schedule, so presence can
read connected for a while after the data stops.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runSimDestroy,
	SilenceUsage: true,
}

func init() {
	simCmd.AddCommand(simDestroyCmd)
}

// simTeardown is the slice of the admin surface destroy uses. An interface so the
// teardown below can be driven over a fake in a test — the network is what gets
// replaced, not the ordering logic under test.
type simTeardown interface {
	DeleteIdentity(ctx context.Context, email string) (bool, error)
	DeleteTenant(ctx context.Context, token string) (bool, error)
}

// destroySim removes the sim's identity, then its tenant, then — via deleteRecord —
// the local sim record, stopping at the first failure.
//
// 🔴 deleteRecord is a parameter rather than a call in the caller, and that is the
// whole point: "a teardown that could not finish stays retryable" is a claim about
// ORDER, and order asserted only by where two statements sit in the caller is a claim
// no test can hold. Behind the seam, a record deleted too early is a failing test
// rather than an operator left with a live tenant and nothing naming it.
func destroySim(ctx context.Context, admin simTeardown, rec *sim.Record, deleteRecord func() error, out io.Writer) error {
	// DeleteIdentity removes the identity AND its memberships, which is what makes
	// the tenant delete below legal.
	removed, err := admin.DeleteIdentity(ctx, rec.SimEmail)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "identity %q removed: %t\n", rec.SimEmail, removed)

	tenantRemoved, err := admin.DeleteTenant(ctx, rec.Tenant)
	if err != nil {
		// The advice is CONDITIONAL because this path catches every failure, most of
		// which are not the memberships case — a refused connection told to go hunt
		// for a membership that does not exist is a confident diagnosis nothing made.
		return fmt.Errorf("%w (the sim record was kept, so destroy can be retried; if the server "+
			"refused because memberships remain, remove the one added outside the sim flow first)", err)
	}
	// false here means the tenant was already gone — the server's delete is
	// idempotent, so an out-of-band removal is reported rather than failed on.
	fmt.Fprintf(out, "tenant %q removed: %t\n", rec.Tenant, tenantRemoved)

	if err := deleteRecord(); err != nil {
		return err
	}
	fmt.Fprintf(out, "✅ sim %q destroyed\n", rec.Name)
	return nil
}

// simActorRunning reports whether a dc-simulator process for THIS sim is answering on
// its control address.
//
// It compares the tenant the process reports against the record's, which is what makes
// the answer trustworthy: --control-addr defaults to localhost:8090 for EVERY sim, so
// "something answered" is not evidence that THIS sim is running — a second sim's actor
// on the same default port would otherwise be reported as this one.
//
// Every uncertainty resolves to false. This gates a warning, and a warning that fires
// on an unreachable host would train an operator to ignore it.
func simActorRunning(ctx context.Context, controlAddr, tenant string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(controlAddr, "/")+"/status", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var status struct {
		Tenant string `json:"tenant"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&status); err != nil {
		return false
	}
	return status.Tenant == tenant
}

func runSimDestroy(cmd *cobra.Command, args []string) error {
	name := args[0]
	rec, err := sim.Load(name)
	if err != nil {
		return err
	}

	adminEmail, _ := cmd.Flags().GetString("admin-email")
	adminPassword, _ := cmd.Flags().GetString("admin-password")

	// Tear down against the SAME host the sim was created on: both the login base
	// and the admin URL come from the record, so destroy is correct even when
	// --server is not repeated. Fall back to a flag-derived admin URL for records
	// written before adminURL was persisted.
	adminURL := rec.AdminURL
	if adminURL == "" {
		server, _ := cmd.Flags().GetString("server")
		tls, _ := cmd.Flags().GetBool("tls")
		adminURL = sim.AdminURL(server, tls)
	}
	admin := sim.NewAdmin(rec.Endpoints.UserGraphQL, adminURL, adminEmail, adminPassword)
	ctx := cmd.Context()
	if err := admin.EnsureSuperuser(ctx); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	// Warn rather than refuse: there is no override flag to offer, and an actor on a
	// host dcctl cannot reach must not be able to block a teardown.
	if simActorRunning(ctx, rec.ControlAddr, rec.Tenant) {
		fmt.Fprintf(out, "⚠️  the dc-simulator process for %q is still running at %s — stop it "+
			"first (Ctrl-C). Destroy removes the record `dcctl sim stop` resolves that address "+
			"from, so this is the last moment dcctl can reach it.\n", name, rec.ControlAddr)
	}

	return destroySim(ctx, admin, rec, func() error { return sim.Delete(name) }, out)
}
