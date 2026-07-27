// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
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
		opts.Namespace = natsInfraNamespace
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
		switch {
		case !hasSync:
			add("B3", opts.ClusterName, "the Cluster spec carries NO synchronous "+
				"replication block, so this store is replicating asynchronously. If the "+
				"chart appears to set one, note that Helm accepts an unknown field and "+
				"the API server prunes it silently — check the field NAMES")
		case durability != "required":
			add("B3", opts.ClusterName, "dataDurability is %q, not \"required\"; writes "+
				"are not held for a standby", durability)
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
	skip := func(why string) {
		if opts.RequireSynchronous {
			add("B6", opts.ClusterName, "synchronous replication was REQUIRED but could "+
				"not be verified against the running server: %s. This is a finding rather "+
				"than a skipped check because a report that examined nothing must not be "+
				"green", why)
			return
		}
		rep.Skipped = append(rep.Skipped, "the PostgreSQL state checks ("+why+")")
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
	return nil
}
