// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/replication"
	pgx "github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DbVerifyOptions selects the database store to check.
type DbVerifyOptions struct {
	// KubeContext is the kubeconfig context; empty uses the current one.
	KubeContext string
	// Namespace holds the CloudNativePG Cluster (the infrastructure namespace).
	Namespace string
	// ClusterName is the Cluster object, e.g. "dc-rdb".
	ClusterName string
	// AliasService is the DNS contract clients use, e.g. "dc-postgresql".
	AliasService string
	// Instances overrides the expected instance count. Zero reads it from the
	// live Cluster's spec.
	//
	// Passing it explicitly is what the negative control does: it STATES the
	// topology the instance is expected not to hold, which is the only way to
	// make a single-instance store fail a check. Read from the spec, a
	// single-instance cluster expects one instance and duly has one.
	Instances int
	// RequireSynchronous demands synchronous replication.
	//
	// 🔴 NOT derived from the spec, deliberately, and this is the check that
	// catches the failure mode measured while building the chart: Helm silently
	// PRUNES an unknown field, so a one-character slip in the template yields a
	// Cluster whose spec has no `synchronous` block, three healthy pods, and
	// fully asynchronous replication. Derived from the spec, this check would
	// read that spec, conclude synchronous was never wanted, and pass.
	RequireSynchronous bool
	// Durability is the dataDurability the store is expected to declare, checked
	// only when RequireSynchronous is set. Empty means "required".
	//
	// 🔴 This is a per-store expectation and must not be defaulted away. The
	// relational store runs "required" because it holds the audit journal and a
	// stall there is a loud recoverable failure. The event store runs
	// "preferred", so a lost standby degrades it to asynchronous replication
	// instead of applying backpressure to ingest. Hardcoding "required" — which
	// this did while the relational store was the only Cluster — reports a
	// correctly-configured event store as broken, and an assertion that is red on
	// a working system is one that gets switched off.
	Durability string
	// TimescaleJobs turns on the background-job health checks (ADR-020 A2.4).
	//
	// Only meaningful against the event store: continuous aggregates, retention
	// and compression are all TimescaleDB background jobs, and a database whose
	// scheduler has stopped looks completely healthy from every other angle in
	// this report — right instance count, right replication, right pod spread,
	// and silently no longer aggregating.
	TimescaleJobs bool
	// Settle is how long to keep re-checking while assertions fail — the window
	// in which a failover is completing or a standby is rejoining.
	Settle time.Duration
	// Timeout bounds the whole check.
	Timeout time.Duration
}

// clusterGVR is the CloudNativePG Cluster resource.
var clusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

// DbReport is the verdict. It reuses replication.Finding so the database and
// broker halves of the HA claim print identically and the rig's negative-control
// wrapper reads one shape.
type DbReport struct {
	// Cluster echoes what was judged, so a report cannot be read out of context.
	Cluster string
	// Instances is the expected instance count everything was judged against.
	Instances int
	// Findings is every failed assertion. Empty means the claim holds.
	Findings []replication.Finding
	// Skipped names each axis that could not be exercised, and why. An assertion
	// that did not run is not one that passed.
	Skipped []string
	// TimescaleJobsChecked records that the background-job axis was requested, so
	// the report prints its counts. Without it a run with the checks OFF and a run
	// that found zero jobs print the same thing.
	TimescaleJobsChecked bool
	// Checked counts what was actually examined. Printed whether or not the run
	// passed: a green run over zero objects is the exact failure this workstream
	// exists to remove, and it is indistinguishable from a real pass unless the
	// counts are on screen.
	Checked DbCounts
}

// DbCounts is what the run actually looked at.
type DbCounts struct {
	ReadyInstances int
	Pods           int
	Nodes          int
	Standbys       int
	SyncStandbys   int
	// TimescaleDatabases counts the databases found carrying the extension, and
	// TimescaleJobs the background jobs examined across them. Both are printed
	// because "no job is stuck" over zero jobs and over forty are the same word.
	TimescaleDatabases int
	TimescaleJobs      int
}

// OK reports whether every assertion held.
func (r DbReport) OK() bool { return len(r.Findings) == 0 }

// Format renders the report for a human.
func (r DbReport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nDatabase replication — cluster %s, expecting %d instance(s)\n",
		r.Cluster, r.Instances)
	fmt.Fprintf(&b, "  examined: %d ready instance(s), %d pod(s) on %d node(s), "+
		"%d standby(s) of which %d synchronous\n",
		r.Checked.ReadyInstances, r.Checked.Pods, r.Checked.Nodes,
		r.Checked.Standbys, r.Checked.SyncStandbys)
	if r.TimescaleJobsChecked {
		fmt.Fprintf(&b, "            %d background job(s) across %d TimescaleDB database(s)\n",
			r.Checked.TimescaleJobs, r.Checked.TimescaleDatabases)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "  SKIPPED: %s\n", s)
	}
	if len(r.Findings) == 0 {
		b.WriteString("  every assertion held\n")
		return b.String()
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  %s\n", f.String())
	}
	return b.String()
}

// VerifyDatabaseReplication asserts a database store's live state against the
// topology it is expected to hold (ADR-020 A2.3, check B).
//
// Like the broker check, every input is observed rather than rendered:
//
//	the Cluster spec/status  <- the Kubernetes API
//	the pod placement        <- the Kubernetes API
//	the replication state    <- PostgreSQL itself, over the alias Service
//
// The last one is the point. The chart can be correct and the cluster still not
// replicate: Helm prunes an unknown field silently, so a misspelling in the
// template produces a Cluster with no synchronous block, a green apply, and
// three healthy asynchronous pods. Nothing short of asking PostgreSQL detects
// that, which is why this reads pg_settings and pg_stat_replication rather than
// trusting the spec it just applied.
func VerifyDatabaseReplication(ctx context.Context, opts DbVerifyOptions) (DbReport, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if opts.ClusterName == "" {
		return DbReport{}, fmt.Errorf("a cluster name is required")
	}
	if opts.Namespace == "" {
		opts.Namespace = infraNamespace
	}

	restCfg, err := RestConfig(opts.KubeContext)
	if err != nil {
		return DbReport{}, fmt.Errorf("building kube config: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return DbReport{}, err
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return DbReport{}, err
	}

	attempt := func() (DbReport, error) {
		return collectAndVerifyDatabase(ctx, restCfg, typed, dyn, opts)
	}
	return settleDbUntilOK(ctx, attempt, opts.Settle)
}

// settleDbUntilOK mirrors settleUntilOK, with the same three exits — and in
// particular the same refusal to return a stale report when collection was
// failing as the window expired. "This store is not replicated" and "we could
// not find out" are different results, and a drill that records the first when
// the second is true has recorded a fiction.
func settleDbUntilOK(ctx context.Context, attempt func() (DbReport, error), settle time.Duration) (DbReport, error) {
	rep, err := attempt()
	if err != nil || rep.OK() || settle <= 0 {
		return rep, err
	}
	deadline := time.Now().Add(settle)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(settleInterval):
		}
		next, err := attempt()
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		rep = next
		if rep.OK() {
			return rep, nil
		}
	}
	if lastErr != nil {
		return DbReport{}, fmt.Errorf("the check could not complete: the settle window "+
			"expired while collection was failing, so the last report is stale and is NOT "+
			"being reported as a verdict: %w", lastErr)
	}
	return rep, nil
}

func collectAndVerifyDatabase(
	ctx context.Context,
	restCfg *rest.Config,
	typed *kubernetes.Clientset,
	dyn dynamic.Interface,
	opts DbVerifyOptions,
) (DbReport, error) {
	rep := DbReport{Cluster: opts.ClusterName}
	add := func(check, object, format string, args ...any) {
		rep.Findings = append(rep.Findings, replication.Finding{
			Check: check, Object: object, Message: fmt.Sprintf(format, args...),
		})
	}

	cl, err := dyn.Resource(clusterGVR).Namespace(opts.Namespace).
		Get(ctx, opts.ClusterName, metav1.GetOptions{})
	if err != nil {
		return rep, fmt.Errorf("reading Cluster %s/%s: %w (is the CloudNativePG "+
			"operator installed, and has this slice been applied?)",
			opts.Namespace, opts.ClusterName, err)
	}

	specInstances, _, _ := unstructured.NestedInt64(cl.Object, "spec", "instances")
	rep.Instances = opts.Instances
	if rep.Instances == 0 {
		rep.Instances = int(specInstances)
	}
	if rep.Instances < 1 {
		return rep, fmt.Errorf("the cluster declares %d instances, which is not a "+
			"topology; nothing can be checked against it", rep.Instances)
	}

	readyInstances, _, _ := unstructured.NestedInt64(cl.Object, "status", "readyInstances")
	rep.Checked.ReadyInstances = int(readyInstances)
	if int(readyInstances) < rep.Instances {
		add("B1", opts.ClusterName, "only %d of %d instances are ready",
			readyInstances, rep.Instances)
	}

	// --- Placement -----------------------------------------------------------
	pods, err := typed.CoreV1().Pods(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + opts.ClusterName,
	})
	if err != nil {
		return rep, fmt.Errorf("listing database pods: %w", err)
	}
	nodes := map[string]struct{}{}
	for _, p := range pods.Items {
		if p.Spec.NodeName != "" {
			nodes[p.Spec.NodeName] = struct{}{}
		}
	}
	rep.Checked.Pods = len(pods.Items)
	rep.Checked.Nodes = len(nodes)
	// Vacuity guard FIRST, and as a finding rather than an early return: the
	// question a reader has when a suite goes green is "did it look at anything".
	if len(pods.Items) == 0 {
		add("B2", opts.ClusterName, "no pods carry cnpg.io/cluster=%s, so nothing was "+
			"examined; a pass here would mean nothing", opts.ClusterName)
	} else if len(nodes) < rep.Instances {
		// The false-HA shape, in its database form. Three pods on one node survive
		// exactly zero node failures while looking identical to three pods on three
		// nodes from any check that counts pods.
		add("B2", opts.ClusterName, "%d pod(s) are spread over only %d node(s); %d "+
			"instances on fewer nodes survive no node failure",
			len(pods.Items), len(nodes), rep.Instances)
	}

	// --- The DNS contract ----------------------------------------------------
	if opts.AliasService != "" {
		verifyAliasService(ctx, typed, opts, cl, add)
	} else {
		rep.Skipped = append(rep.Skipped, "the alias Service check (no alias name given)")
	}

	// --- The declared synchronous configuration ------------------------------
	//
	// Read from the live object, NOT from the values that produced it. Helm
	// accepts a misspelled field and the API server prunes it, so this is where a
	// template typo becomes visible.
	durability, hasSync, _ := unstructured.NestedString(cl.Object,
		"spec", "postgresql", "synchronous", "dataDurability")
	syncNumber, _, _ := unstructured.NestedInt64(cl.Object,
		"spec", "postgresql", "synchronous", "number")
	if opts.RequireSynchronous {
		wantDurability := opts.Durability
		if wantDurability == "" {
			wantDurability = "required"
		}
		switch {
		case !hasSync:
			add("B3", opts.ClusterName, "the Cluster spec carries NO synchronous "+
				"replication block, so this store is replicating asynchronously. If the "+
				"chart appears to set one, note that Helm accepts an unknown field and "+
				"the API server prunes it silently — check the field NAMES")
		case durability != wantDurability:
			add("B3", opts.ClusterName, "dataDurability is %q, expected %q for this "+
				"store", durability, wantDurability)
		}
	}

	// --- PostgreSQL ground truth ---------------------------------------------
	// 🔴 opts.RequireSynchronous alone, NOT `hasSync && opts.RequireSynchronous`.
	//
	// The first draft passed the conjunction, and it was wrong in the way this
	// whole workstream is about: it disabled the LIVE synchronous checks whenever
	// the spec had no synchronous block — which is precisely the state a pruned
	// field produces, and precisely when asking PostgreSQL directly matters most.
	// The B3 finding above happened to cover it, so the suite still went red and
	// the defect was invisible in the result. A check that switches itself off
	// when its subject is broken is not a check.
	//
	// syncNumber is 0 in that case; verifyPostgresState floors it at 1.
	if err := verifyPostgresState(ctx, restCfg, typed, opts, cl, &rep, add,
		opts.RequireSynchronous, int(syncNumber)); err != nil {
		return rep, err
	}

	return rep, nil
}

// verifyAliasService asserts the DNS contract: the alias exists, is OWNED by the
// Cluster, carries the operator's own selector, and resolves to the CURRENT
// primary.
//
// The ownership check is the one that is easy to leave out and hard to replace.
// An earlier version compared only selectors, on the theory that a hand-rolled
// Service "would stop following the primary on the first failover" — that is
// FALSE, and worth recording because it sounds right. The selector is
// label-based (`cnpg.io/instanceRole: primary`) and CNPG relabels the POD on
// promotion, so any Service carrying that selector follows the primary perfectly
// well, operator-managed or not.
//
// What is genuinely lost with an unmanaged Service is the SERVER CERTIFICATE:
// only names CNPG knows about are added to the cert's SANs, so an impostor alias
// works today over `sslmode=disable` and breaks the moment anyone asks for
// `verify-full`. That failure is invisible until the day TLS is tightened, which
// is exactly the kind of latent break worth an assertion now.
//
// So both are checked: the ownerReference (is this the operator's Service?) and
// the selector (does it point where the operator's points?).
func verifyAliasService(
	ctx context.Context,
	typed *kubernetes.Clientset,
	opts DbVerifyOptions,
	cl *unstructured.Unstructured,
	add func(check, object, format string, args ...any),
) {
	alias, err := typed.CoreV1().Services(opts.Namespace).
		Get(ctx, opts.AliasService, metav1.GetOptions{})
	if err != nil {
		add("B4", opts.AliasService, "the alias Service does not exist, so every client "+
			"using the configured hostname cannot resolve its database: %v", err)
		return
	}
	rw, err := typed.CoreV1().Services(opts.Namespace).
		Get(ctx, opts.ClusterName+"-rw", metav1.GetOptions{})
	if err != nil {
		add("B4", opts.AliasService, "the operator's %s-rw Service is missing, so the "+
			"alias cannot be compared against it: %v", opts.ClusterName, err)
		return
	}
	if fmt.Sprint(alias.Spec.Selector) != fmt.Sprint(rw.Spec.Selector) {
		add("B4", opts.AliasService, "its selector %v does not match %s-rw's %v, so it "+
			"does not point at the primary", alias.Spec.Selector, opts.ClusterName,
			rw.Spec.Selector)
	}

	// Owned by this Cluster, i.e. actually a managed service. A hand-rolled
	// Service with the right selector DOES follow the primary — so this is not
	// about failover. It is about the server certificate: only names the operator
	// knows about reach the cert's SANs, so an unmanaged alias works over
	// sslmode=disable and fails the day anyone asks for verify-full.
	managed := false
	for _, o := range alias.OwnerReferences {
		if o.Kind == "Cluster" && o.Name == opts.ClusterName {
			managed = true
		}
	}
	if !managed {
		add("B4", opts.AliasService, "it is not owned by Cluster %s, so it is not an "+
			"operator-managed service. It may still route correctly today, but its name "+
			"is absent from the server certificate's SANs, so TLS verification against "+
			"this hostname cannot succeed", opts.ClusterName)
	}

	// And that it actually points at the primary right now.
	primary, _, _ := unstructured.NestedString(cl.Object, "status", "currentPrimary")
	slices, err := typed.DiscoveryV1().EndpointSlices(opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + opts.AliasService,
	})
	if err != nil {
		add("B4", opts.AliasService, "listing endpointslices: %v", err)
		return
	}
	found := false
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.TargetRef != nil && e.TargetRef.Name == primary {
				found = true
			}
		}
	}
	if primary != "" && !found {
		add("B4", opts.AliasService, "it does not resolve to the current primary (%s), "+
			"so clients are not reaching the writable instance", primary)
	}
}

// verifyPostgresState asks PostgreSQL what it is actually doing, with the app
// credentials every service uses.
//
// It port-forwards to the current primary POD by name, not through the alias
// Service — client-go's port-forwarding is pod-based. So this does not exercise
// the DNS contract; verifyAliasService does that, against the Kubernetes API.
func verifyPostgresState(
	ctx context.Context,
	restCfg *rest.Config,
	typed *kubernetes.Clientset,
	opts DbVerifyOptions,
	cl *unstructured.Unstructured,
	rep *DbReport,
	add func(check, object, format string, args ...any),
	requireSync bool,
	syncNumber int,
) error {
	// 🔴 A SKIPPED AXIS IS NOT A PASS, and OK() cannot see Skipped — it counts
	// findings only. So when the caller DEMANDED synchronous replication, being
	// unable to ask PostgreSQL has to be a finding, or the report goes green
	// having verified nothing about the property it was asked to verify.
	//
	// This is not hypothetical. A Cluster created by `bootstrap.recovery` — which
	// is exactly what A2.5's restore drill produces — has no `initdb` block at
	// all, so `database` and `secretName` are both empty. Without this, B5/B6/B7
	// and B8 would all silently vanish on a restored cluster and CHECK B would
	// pass with `--require-synchronous` set, because only B3 reads the spec.
	// Set BEFORE any skip path, so the counts line prints even when the axis could
	// not run. Without it a requested-but-skipped run and a not-requested run look
	// identical, which is the one distinction the counts line exists to make.
	if opts.TimescaleJobs {
		rep.TimescaleJobsChecked = true
	}
	skip := func(why string) {
		demanded := false
		if opts.RequireSynchronous {
			add("B6", opts.ClusterName, "synchronous replication was REQUIRED but could "+
				"not be verified against the running server: %s. This is a finding rather "+
				"than a skipped check because a report that examined nothing must not be "+
				"green", why)
			demanded = true
		}
		if opts.TimescaleJobs {
			// Same rule, same reason. The background-job checks reach the server
			// through this identical path, so every way of skipping out of it takes
			// them with it — including the restored-cluster case, which is exactly
			// where "have the aggregates resumed?" is the question being asked.
			add("B9", opts.ClusterName, "background-job health was REQUESTED but could "+
				"not be verified against the running server: %s. Reported as a finding "+
				"because a restored cluster is precisely where this axis matters and "+
				"precisely where it would otherwise vanish", why)
			demanded = true
		}
		if !demanded {
			rep.Skipped = append(rep.Skipped, "the PostgreSQL state checks ("+why+")")
		}
	}

	primary, _, _ := unstructured.NestedString(cl.Object, "status", "currentPrimary")
	if primary == "" {
		skip("the Cluster reports no current primary")
		return nil
	}
	database, _, _ := unstructured.NestedString(cl.Object,
		"spec", "bootstrap", "initdb", "database")
	secretName, _, _ := unstructured.NestedString(cl.Object,
		"spec", "bootstrap", "initdb", "secret", "name")
	if database == "" || secretName == "" {
		skip("the Cluster does not name a bootstrap database and credentials Secret, " +
			"which is normal for a cluster restored via bootstrap.recovery")
		return nil
	}

	sec, err := typed.CoreV1().Secrets(opts.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading database credentials from Secret %s/%s: %w",
			opts.Namespace, secretName, err)
	}
	user, pass := string(sec.Data["username"]), string(sec.Data["password"])

	local, stop, err := forwardPort(restCfg, opts.Namespace, primary, 5432)
	if err != nil {
		return fmt.Errorf("opening a port-forward to the primary %s: %w", primary, err)
	}
	defer stop()

	dsn := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable",
		user, pass, local, database)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to the database as %q: %w", user, err)
	}
	defer conn.Close(ctx)

	// Coupling (a). Every DeviceChain service issues CREATE DATABASE at startup,
	// and CNPG's app role does not have CREATEDB by default — so without the
	// grant, every service crashloops on a cluster that otherwise looks perfect.
	// Asserted here because the symptom (permission denied to create database)
	// appears eleven times in service logs and nowhere in the database's own
	// health.
	var createdb bool
	if err := conn.QueryRow(ctx,
		`select rolcreatedb from pg_roles where rolname = current_user`).Scan(&createdb); err != nil {
		return fmt.Errorf("reading the app role's attributes: %w", err)
	}
	if !createdb {
		add("B5", user, "the application role lacks CREATEDB, so every DeviceChain "+
			"service will fail at startup on `CREATE DATABASE` — the database itself "+
			"will look entirely healthy while nothing can start")
	}

	// The live synchronous configuration. Distinct from the spec check above:
	// this is what the running server has, which is the thing that decides
	// whether a commit waits.
	var standbyNames string
	if err := conn.QueryRow(ctx,
		`select current_setting('synchronous_standby_names')`).Scan(&standbyNames); err != nil {
		return fmt.Errorf("reading synchronous_standby_names: %w", err)
	}
	if requireSync && strings.TrimSpace(standbyNames) == "" {
		add("B6", opts.ClusterName, "synchronous_standby_names is EMPTY on the running "+
			"primary, so commits are not waiting for any standby regardless of what the "+
			"Cluster spec says")
	}

	// And the standbys themselves.
	//
	// 🔴 sync_state is counted as ('sync','quorum'). With `method: any` — which is
	// what `ANY N (...)` renders — PostgreSQL reports quorum, NOT sync. A check
	// written on 'sync' alone counts zero against a perfectly healthy cluster.
	//
	// 🔴 These columns are NULL unless the querying role is a member of
	// pg_monitor, which the chart grants for exactly this reason. Without it the
	// row COUNT is right and every column is NULL, so this would report zero
	// synchronous standbys on a healthy cluster — and, far worse, would let a
	// zero-standby precondition appear satisfied instantly.
	var total, sync int
	if err := conn.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where sync_state in ('sync','quorum') and state = 'streaming')
		  from pg_stat_replication`).Scan(&total, &sync); err != nil {
		return fmt.Errorf("reading pg_stat_replication: %w", err)
	}
	rep.Checked.Standbys = total
	rep.Checked.SyncStandbys = sync

	if want := rep.Instances - 1; total < want {
		add("B7", opts.ClusterName, "%d standby(s) are connected, expected %d for a "+
			"%d-instance cluster", total, want, rep.Instances)
	}
	if requireSync {
		need := syncNumber
		if need < 1 {
			need = 1
		}
		if sync < need {
			add("B8", opts.ClusterName, "%d standby(s) are streaming synchronously, and "+
				"%d must be for a commit to complete; writes are either stalling or not "+
				"being held at all", sync, need)
		}
	}

	if opts.TimescaleJobs {
		if err := verifyTimescaleJobs(ctx, local, user, pass, opts, rep, add); err != nil {
			return err
		}
	}
	return nil
}

// timescaleTelemetryJob is excluded from the job-health checks below, and the
// reason is measured rather than defensive.
//
// The Cluster sets `timescaledb.telemetry_level = off`. That stops the phone-home
// but does NOT remove the job: measured on 2.28.3, `policy_telemetry` remains
// present with `scheduled = true` while never being assigned a `next_start` and
// never running. So on a correctly configured cluster it sits permanently in the
// exact state B11 exists to catch.
//
// The Cluster also deletes it at initdb for this reason, which handles every
// cluster built by the current configuration. This exclusion covers the ones
// that are not: databases created before that change, and clusters arriving via
// `bootstrap.recovery`, where postInitTemplateSQL does not run and template1
// comes back from the backup as it was.
const timescaleTelemetryJob = "policy_telemetry"

// verifyTimescaleJobs asserts that TimescaleDB's background scheduler is alive
// (ADR-020 A2.4).
//
// This is the highest-value check in the slice, because the failure it catches
// is invisible to every other one. Continuous aggregates, retention and
// compression are all background jobs. If the scheduler stops, the database
// stays up, accepts reads and writes, replicates, fails over, passes B1 through
// B8 — and quietly stops aggregating. The symptom surfaces days later as
// dashboards that have gone flat, and by then the window it should have
// aggregated is a manual backfill.
//
// Upstream timescale#9360 is the named instance: after a failover, jobs can
// stick at `next_start = -infinity`, which is a value the scheduler will never
// reach. Our operand image ships 2.28.3, above the 2.26.4 fix, so this asserts a
// property we believe holds rather than working around a known break — which is
// the right time to add an assertion, not after it breaks.
//
// 🔴 Jobs are PER-DATABASE, so this cannot just look at the one it connected to.
// Every DeviceChain service creates its own database at startup, so the real
// hypertables and the aggregate refresh job live in a database named after the
// instance, and the bootstrap database is very likely empty of them. Checking
// only the connection database would examine the one place the answer is
// guaranteed to be boring.
func verifyTimescaleJobs(
	ctx context.Context,
	localPort int,
	user, pass string,
	opts DbVerifyOptions,
	rep *DbReport,
	add func(check, object, format string, args ...any),
) error {
	// Built with url.URL rather than by interpolating into a format string: a
	// password containing @ / # or ? silently produces a DIFFERENT DSN, and the
	// symptom is an authentication failure that looks like a wrong password.
	connect := func(db string) (*pgx.Conn, error) {
		u := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(user, pass),
			Host:     fmt.Sprintf("127.0.0.1:%d", localPort),
			Path:     "/" + db,
			RawQuery: "sslmode=disable",
		}
		return pgx.Connect(ctx, u.String())
	}

	admin, err := connect("postgres")
	if err != nil {
		return fmt.Errorf("connecting to enumerate databases: %w", err)
	}
	rows, err := admin.Query(ctx,
		`select datname from pg_database
		  where datallowconn and not datistemplate and datname <> 'postgres'
		  order by datname`)
	if err != nil {
		admin.Close(ctx)
		return fmt.Errorf("listing databases: %w", err)
	}
	var databases []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			admin.Close(ctx)
			return fmt.Errorf("scanning database list: %w", err)
		}
		databases = append(databases, d)
	}
	rows.Close()
	admin.Close(ctx)
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	for _, db := range databases {
		conn, err := connect(db)
		if err != nil {
			// 🔴 A FINDING, not a skip, and the distinction is the same one the
			// whole file turns on: OK() counts findings and cannot see Skipped.
			//
			// The tempting reading is "a database we cannot open is not our
			// business". But the database this check most needs is the one holding
			// the hypertables and the aggregate-refresh policy — named after the
			// instance, created by the app at startup — and the bootstrap database
			// beside it will happily satisfy the extension guard above. So a
			// connect failure on exactly the interesting database would leave a
			// green report that never looked at it.
			add("B9", fmt.Sprintf("%s/database %q", opts.ClusterName, db),
				"could not be opened to check its background jobs (%v), so its jobs were "+
					"not examined. Reported rather than skipped because a report that "+
					"could not reach the database holding the hypertables must not be "+
					"green", err)
			continue
		}
		err = verifyTimescaleJobsIn(ctx, conn, db, opts, rep, add)
		conn.Close(ctx)
		if err != nil {
			return err
		}
	}

	// 🔴 VACUITY. Everything above passes trivially when nothing carries the
	// extension, and "no databases have TimescaleDB" is not a healthy event
	// store — it is coupling (b) broken. The extension reaches each database by
	// being present in template1 when the app creates it, and nothing in the
	// codebase ever issues CREATE EXTENSION, so if template1 lost it every new
	// database silently comes up without hypertable support.
	if rep.Checked.TimescaleDatabases == 0 {
		add("B9", opts.ClusterName, "no database on this server carries the timescaledb "+
			"extension, so the background-job checks examined nothing. Every DeviceChain "+
			"database inherits the extension from template1 at CREATE DATABASE time and "+
			"nothing in the platform ever issues CREATE EXTENSION, so this means new "+
			"databases are coming up without hypertable support")
		return nil
	}

	// 🔴 THE SECOND HALF OF THE VACUITY GUARD, and the half that was missing.
	//
	// The check above asks "did we find any TimescaleDB databases?". It does NOT
	// ask "did we examine any JOBS?" — and every assertion in this axis lives
	// inside a `for rows.Next()` loop, so a query returning zero rows produces no
	// findings and a green report over nothing examined.
	//
	// That is not hypothetical. It is exactly what the NATURAL JOIN did: zero rows
	// against a perfectly healthy cluster. That was fixed by changing the join,
	// which fixes the instance and not the shape — any future cause of zero rows
	// (a renamed view, an upstream proc_name change, a `where` clause that stops
	// matching) reproduces the identical silent pass.
	//
	// A server-level assertion is the right level. Per DATABASE, zero jobs is a
	// legitimate state — retention and compression policies are opt-in, so a
	// database with the extension and no policies genuinely has none. But a server
	// carrying TimescaleDB databases and not one background job anywhere is not a
	// healthy event store: the extension creates its own job-history retention
	// policy in every database that has it, so the floor is one per database.
	if rep.Checked.TimescaleJobs == 0 {
		add("B9", opts.ClusterName, "%d database(s) carry the timescaledb extension but NOT "+
			"ONE background job was examined across them, so every job assertion below "+
			"passed over an empty set. The extension installs a job-history retention "+
			"policy into each database that has it, so zero is not a state a healthy "+
			"server reaches — treat this as the query being wrong rather than the jobs "+
			"being fine", rep.Checked.TimescaleDatabases)
	}
	return nil
}

// verifyTimescaleJobsIn runs the job assertions against one database.
func verifyTimescaleJobsIn(
	ctx context.Context,
	conn *pgx.Conn,
	db string,
	opts DbVerifyOptions,
	rep *DbReport,
	add func(check, object, format string, args ...any),
) error {
	var hasExtension bool
	if err := conn.QueryRow(ctx,
		`select exists(select 1 from pg_extension where extname = 'timescaledb')`).
		Scan(&hasExtension); err != nil {
		return fmt.Errorf("checking for the timescaledb extension in %q: %w", db, err)
	}
	if !hasExtension {
		return nil
	}
	rep.Checked.TimescaleDatabases++

	// 🔴 `join ... using (job_id)`, never NATURAL JOIN — measured, and the reason
	// is NOT the one an earlier version of this comment gave.
	//
	// The two views also share `next_start`, `hypertable_schema` and
	// `hypertable_name`. The `next_start` values are in fact EQUAL (both derive
	// from the same stats row), so that column is a red herring. What empties the
	// join is the hypertable pair, in two independent ways at once:
	//
	//   - for a continuous-aggregate job the two views report DIFFERENT
	//     hypertables — `jobs` names the user-facing view
	//     (event-management/measurement_rollups) while `job_stats` names the
	//     internal materialized hypertable
	//     (_timescaledb_internal/_materialized_hypertable_6), so the equality
	//     fails outright;
	//   - for a job with no hypertable at all both sides are NULL, and NULL never
	//     equals NULL, so that row drops too.
	//
	// Measured on a healthy cluster: NATURAL JOIN returns 0 rows where
	// `using (job_id)` returns 2. That is the worst possible shape here, because
	// an empty result is indistinguishable from "no jobs are broken" — it cost a
	// wasted failover measurement to find, which is why B9's job-count guard
	// exists rather than only this comment.
	rows, err := conn.Query(ctx, `
		select j.job_id,
		       j.proc_name,
		       j.scheduled,
		       j.next_start is null                        as next_start_unset,
		       coalesce(j.next_start = '-infinity', false)  as next_start_past,
		       coalesce(j.next_start = 'infinity', false)   as next_start_never,
		       coalesce(s.last_run_status, '')              as last_run_status
		  from timescaledb_information.jobs j
		  left join timescaledb_information.job_stats s using (job_id)
		 where not (j.proc_name = $1 and j.proc_schema like '\_timescaledb%')
		 order by j.job_id`, timescaleTelemetryJob)
	if err != nil {
		return fmt.Errorf("reading background jobs in %q: %w", db, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			st   jobState
			id   int64
			proc string
		)
		if err := rows.Scan(&id, &proc, &st.Scheduled, &st.NextStartUnset,
			&st.NextStartPast, &st.NextStartNever, &st.LastRunStatus); err != nil {
			return fmt.Errorf("scanning background jobs in %q: %w", db, err)
		}
		rep.Checked.TimescaleJobs++

		// `opts.Settle > 0` is the right proxy for "a settle window elapsed": the
		// caller discards every non-OK report until the window expires, so a
		// finding that survives to be PRINTED is one that outlived it.
		if check, reason := classifyJob(st, opts.Settle > 0); check != "" {
			add(check, fmt.Sprintf("%s/job %d (%s)", db, id, proc), "%s", reason)
		}
	}
	return rows.Err()
}

// jobState is one background job as OBSERVED, with no judgement applied.
type jobState struct {
	Scheduled      bool
	NextStartUnset bool
	NextStartPast  bool // next_start = -infinity
	NextStartNever bool // next_start = +infinity
	LastRunStatus  string
}

// classifyJob is the verdict, as a pure function of an observed job.
//
// Split out from the query loop so the precedence between these conditions is
// testable without a database. The I/O gathers, this judges — the same split
// drdrill's classify() uses, and for the same reason: a switch buried in a scan
// loop is reachable only by standing up the thing it is judging.
//
// `settled` says whether a settle window actually elapsed, and exists so the
// B11 message cannot claim one did when --settle was 0.
func classifyJob(s jobState, settled bool) (check, reason string) {
	switch {
	case s.NextStartPast:
		// The timescale#9360 shape. `-infinity` is not "later", it is never: the
		// scheduler compares against it and the job is dead while the database
		// reports itself entirely healthy.
		return "B10", "next_start is -infinity, which the scheduler will never reach — this " +
			"job is permanently stuck and the work it does (continuous aggregate refresh, " +
			"retention, compression) has silently stopped"
	case s.NextStartNever:
		// `alter_job(id, next_start => 'infinity')` is the documented way to park
		// a job indefinitely. Indistinguishable, from the outside, from one that
		// stopped on its own — and equally not aggregating.
		return "B10", "next_start is +infinity, so the job is parked and will never run " +
			"again. This is what alter_job(next_start => 'infinity') does, so it may be " +
			"deliberate — but nothing it was doing is still happening"
	case !s.Scheduled:
		// `alter_job(id, scheduled => false)` — a paused refresh job has stopped
		// aggregating exactly as thoroughly as one stuck at -infinity, and every
		// other signal about it looks healthy.
		return "B11", "is NOT scheduled, so it will not run again until something schedules " +
			"it. A paused job stops aggregating just as completely as a broken one, and " +
			"reports no error while doing it"
	case s.NextStartUnset:
		// 🔴 A finding, but a SETTLE-ELIGIBLE one, and that distinction is the
		// whole reason this check is usable.
		//
		// The A2 spike's first oracle called a NULL next_start BROKEN and
		// false-alarmed during the seconds after a promotion — in the exact
		// window the check exists for, which is how an oracle gets switched off.
		// It is reported as an ordinary finding precisely because the caller
		// re-runs the whole verification for --settle before returning a verdict,
		// so a value that has simply not been recomputed yet clears itself.
		//
		// NULL here means there is no stats row at all — the column is NOT NULL in
		// the underlying table — i.e. the job has never run or its statistics were
		// reset, which is exactly the post-promotion state.
		if settled {
			return "B11", "is scheduled but has no next_start, and still had none after the " +
				"settle window — the scheduler is not assigning it a next run"
		}
		return "B11", "is scheduled but has no next_start, so the scheduler has not assigned " +
			"it a next run. NOTE: no settle window was allowed (--settle 0), and this clears " +
			"on its own within seconds of a promotion — re-run with a settle window before " +
			"treating it as a fault"
	case s.LastRunStatus == "Failed":
		return "B12", "last run FAILED. The job is still scheduled, so this is one that runs " +
			"and errors rather than one that has stopped"
	}
	return "", ""
}
