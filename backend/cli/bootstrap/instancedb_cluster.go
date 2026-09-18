// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	pgx "github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// ClusterRdb is the relational store's contract, read back from the cluster root: where
// the shared store runs, and which Secret holds the identity allowed to give an instance
// a login and a database of its own.
type ClusterRdb struct {
	Namespace         string `json:"namespace"`
	ClusterName       string `json:"clusterName"`
	ProvisionerSecret string `json:"provisionerSecret"`
	// MaxConnections is what the store runs with: the budget every instance's
	// connection limit is admitted against.
	MaxConnections int `json:"maxConnections"`
}

// rdbFromOutputs decodes the relational store's contract out of the cluster root's
// outputs. Every field is required: unlike the archive, the relational store is not
// optional, so an empty value is never a meaningful answer — it is a root that stopped
// exporting it.
func rdbFromOutputs(outputs map[string]tfexec.OutputMeta) (ClusterRdb, error) {
	rdb := ClusterRdb{ProvisionerSecret: rdbProvisionerSecretName}
	for _, field := range []struct {
		name string
		into *string
	}{
		{"namespace", &rdb.Namespace},
		{"postgres_cluster_name", &rdb.ClusterName},
	} {
		out, ok := outputs[field.name]
		if !ok {
			return rdb, fmt.Errorf("the cluster prerequisite root did not export %q, so dcctl "+
				"cannot find the relational store to give this instance its own login", field.name)
		}
		if err := json.Unmarshal(out.Value, field.into); err != nil || *field.into == "" {
			return rdb, fmt.Errorf("the cluster prerequisite root exported %q as %s, which names "+
				"nothing; dcctl cannot give this instance its own login without it", field.name, out.Value)
		}
	}
	out, ok := outputs["postgres_max_connections"]
	if !ok {
		return rdb, fmt.Errorf("the cluster prerequisite root did not export %q, so dcctl cannot "+
			"tell how many connections the relational store has to give out", "postgres_max_connections")
	}
	if err := json.Unmarshal(out.Value, &rdb.MaxConnections); err != nil || rdb.MaxConnections <= 0 {
		return rdb, fmt.Errorf("the cluster prerequisite root exported postgres_max_connections as %s, "+
			"which is not a connection budget", out.Value)
	}
	return rdb, nil
}

// instanceDatabaseTimeout bounds waiting for the shared store to accept the provisioner.
// On a fresh cluster the apply returns once the Cluster object exists, not once its
// primary is serving.
const instanceDatabaseTimeout = 10 * time.Minute

// errStoreNotReady marks what a store still coming up answers with. withProvisionerSession
// retries these, and only these: anything else is an answer, and ten minutes of retries
// would bury it.
var errStoreNotReady = errors.New("the relational store is not ready")

func notReady(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errStoreNotReady, fmt.Sprintf(format, args...))
}

// provisionInstanceDatabase gives this instance its own login and database on the shared
// relational store, signed in as the provisioner.
//
// Indirected so the wiring around it can be exercised without a cluster; the SQL it runs
// is ensureInstanceDatabase, which is tested against a real PostgreSQL.
var provisionInstanceDatabase = func(ctx context.Context, st *State, rdb ClusterRdb) error {
	if st.Credentials == nil || st.Credentials.RDBInstancePassword == "" {
		return errors.New("no password was settled for this instance's own database login")
	}
	limit, err := instanceConnectionLimit(st)
	if err != nil {
		return fmt.Errorf("sizing this instance's connection limit: %w", err)
	}
	admit := connectionAdmission{Limit: limit, Budget: rdb.MaxConnections}
	return withProvisionerSession(ctx, st.KubeContext, rdb, func(q instanceDBQuerier) error {
		return ensureInstanceDatabase(ctx, q, st.Instance, st.Credentials.RDBInstancePassword, admit)
	})
}

// removeInstanceDatabase drops this instance's database and login, verifying they are
// gone. Indirected for the same reason as provisionInstanceDatabase.
var removeInstanceDatabase = func(ctx context.Context, kubeContext, instance string, rdb ClusterRdb) error {
	return withProvisionerSession(ctx, kubeContext, rdb, func(q instanceDBQuerier) error {
		return dropInstanceDatabase(ctx, q, instance)
	})
}

// withProvisionerSession makes sure the provisioner exists with the password in its
// Secret, opens one session on the shared store's primary as it, and runs fn there.
//
// 🔴 THE PROVISIONER IS dcctl'S, NOT THE DATABASE OPERATOR'S, AND IT CANNOT BE OTHERWISE.
// It was first declared under the Cluster's `managed.roles`, and the operator reconciles
// the memberships of every role declared there — revoking any the declaration does not
// list. Since PostgreSQL 16, creating a role makes its creator a member of it with ADMIN,
// and that membership IS the provisioner's authority over the login. So the operator
// tried, on every reconcile, to revoke exactly what lets the provisioner manage the
// logins it made; measured on a live cluster, it reported the provisioner as impossible
// to reconcile, and only a dependent grant stood between it and a login nobody could
// drop. Nothing about the membership is declarable ahead of time — it names instances
// that do not exist yet — so the role is created here instead, as the database
// superuser over the primary's local socket, and the operator never hears of it.
func withProvisionerSession(ctx context.Context, kubeContext string, rdb ClusterRdb, fn func(instanceDBQuerier) error) error {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube config to reach the relational store: %w", err)
	}
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to reach the relational store: %w", err)
	}

	attempt := func() error {
		cl, err := dyn.Resource(clusterGVR).Namespace(rdb.Namespace).Get(ctx, rdb.ClusterName, metav1.GetOptions{})
		if err != nil {
			return notReady("reading Cluster %s/%s: %v", rdb.Namespace, rdb.ClusterName, err)
		}
		primary, _, _ := unstructured.NestedString(cl.Object, "status", "currentPrimary")
		if primary == "" {
			return notReady("Cluster %s/%s has no primary yet", rdb.Namespace, rdb.ClusterName)
		}
		sec, err := typed.CoreV1().Secrets(rdb.Namespace).Get(ctx, rdb.ProvisionerSecret, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return fmt.Errorf("the provisioner's credentials are missing: Secret %s/%s does not exist",
				rdb.Namespace, rdb.ProvisionerSecret)
		case err != nil:
			return notReady("reading Secret %s/%s: %v", rdb.Namespace, rdb.ProvisionerSecret, err)
		}
		user, pass := string(sec.Data[secretKeyUsername]), string(sec.Data[secretKeyPassword])
		if user != rdbProvisionerUsername || pass == "" {
			return fmt.Errorf("Secret %s/%s does not carry the provisioner %q and a password",
				rdb.Namespace, rdb.ProvisionerSecret, rdbProvisionerUsername)
		}
		if err := ensureProvisioner(ctx, restCfg, typed, rdb.Namespace, primary, pass); err != nil {
			return err
		}

		local, stop, err := forwardPort(restCfg, rdb.Namespace, primary, 5432)
		if err != nil {
			return notReady("opening a port-forward to the primary %s: %v", primary, err)
		}
		defer stop()
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(connectCtx, localPostgresURL(user, pass, local, "postgres"))
		if err != nil {
			return notReady("connecting to the relational store as %q: %v", user, err)
		}
		defer conn.Close(context.Background())
		return fn(pgxSession{conn})
	}

	deadline := time.Now().Add(instanceDatabaseTimeout)
	lastSaid := time.Now()
	for {
		err := attempt()
		if err == nil || !errors.Is(err, errStoreNotReady) || time.Now().After(deadline) {
			return err
		}
		if time.Since(lastSaid) > 30*time.Second {
			fmt.Printf("\n  still waiting for the relational store: %v", err)
			lastSaid = time.Now()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last attempt: %v)", ctx.Err(), err)
		case <-time.After(5 * time.Second):
		}
	}
}

// ensureProvisioner creates the provisioner, or brings it back to exactly what it should
// be, as the database superuser on the primary's local socket.
//
// 🔴 THE SUPERUSER IS USED FOR THIS AND NOTHING ELSE, AND NOT OVER THE NETWORK. The
// Cluster keeps superuser access disabled, which closes the network path; the local
// socket inside the primary is the database operator's own route in, reachable only to
// something already allowed to exec into that pod. Everything after this runs as the
// provisioner.
func ensureProvisioner(ctx context.Context, restCfg *rest.Config, typed kubernetes.Interface, namespace, primary, password string) error {
	sql, err := provisionerSQL(password)
	if err != nil {
		return err
	}
	_, stderr, err := execInPod(ctx, restCfg, typed, namespace, primary, "postgres",
		[]string{"psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-q", "-f", "-"},
		strings.NewReader(sql))
	if err != nil {
		// 🔴 ONLY psql's OWN VERDICT LINES, never the rest of its stderr: on an error psql
		// echoes the failing statement, and that statement carries the password verifier.
		return notReady("creating the provisioner on %s: %v %s", primary, err, psqlVerdict(stderr))
	}
	return nil
}

// provisionerSQL is the statement that makes the provisioner exist with exactly these
// attributes and this password. One DO block, so it is one round trip and idempotent.
func provisionerSQL(password string) (string, error) {
	verifier, err := scramSHA256Verifier(password)
	if err != nil {
		return "", err
	}
	role := pgx.Identifier{rdbProvisionerUsername}.Sanitize()
	attrs := fmt.Sprintf("LOGIN NOSUPERUSER CREATEROLE CREATEDB NOREPLICATION NOBYPASSRLS "+
		"CONNECTION LIMIT %d PASSWORD '%s'", provisionerConnectionLimit, verifier)
	return fmt.Sprintf(`DO $dcctl$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s %s;
  ELSE
    ALTER ROLE %s %s;
  END IF;
END
$dcctl$;
`, "'"+rdbProvisionerUsername+"'", role, attrs, role, attrs), nil
}

// provisionerConnectionLimit bounds the provisioner's sessions. dcctl holds one at a time;
// the headroom is for two runs overlapping on one cluster.
const provisionerConnectionLimit = 3

// psqlVerdict keeps the ERROR/FATAL lines of psql's stderr and drops everything else.
func psqlVerdict(stderr string) string {
	var keep []string
	for _, line := range strings.Split(stderr, "\n") {
		if i := strings.Index(line, "ERROR:"); i >= 0 {
			keep = append(keep, strings.TrimSpace(line[i:]))
		} else if i := strings.Index(line, "FATAL:"); i >= 0 {
			keep = append(keep, strings.TrimSpace(line[i:]))
		}
	}
	return strings.Join(keep, "; ")
}

// execInPod runs command in a container and returns what it wrote. Indirected so what
// is sent through it can be exercised without a cluster.
var execInPod = func(ctx context.Context, restCfg *rest.Config, typed kubernetes.Interface,
	namespace, pod, container string, command []string, stdin io.Reader) (string, string, error) {
	req := typed.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).
		SubResource("exec").VersionedParams(&corev1.PodExecOptions{
		Container: container, Command: command,
		Stdin: stdin != nil, Stdout: true, Stderr: true,
	}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdout, stderr bytes.Buffer
	err = ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	return stdout.String(), stderr.String(), err
}

// checkRelationalStoreOwner refuses a shared relational store built before instances had
// logins of their own.
//
// 🔴 IT HAS TO RUN BEFORE ANYTHING IS WRITTEN. Such a store was initialised with
// `devicechain` as its owner — a name that is also a valid instance name, and the
// default one — and every instance database on it belongs to that owner. The owner is
// fixed when the Cluster is created and cannot be changed by an apply, so the only way
// out is a store created by this dcctl; writing credentials first would change the owner
// Secret's username under a store that will never use it.
//
// A cluster with no relational store yet, or no database operator, has nothing to check.
func checkRelationalStoreOwner(ctx context.Context, kubeContext string) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to check the relational store: %w", err)
	}
	cl, err := dyn.Resource(clusterGVR).Namespace(infraNamespace).Get(ctx, rdbClusterName, metav1.GetOptions{})
	switch {
	case err != nil && (apierrors.IsNotFound(err) || meta.IsNoMatchError(err)):
		return nil
	case err != nil:
		return fmt.Errorf("reading Cluster %s/%s to check who owns it: %w", infraNamespace, rdbClusterName, err)
	}
	return refuseAPreIsolationOwner(cl)
}

// refuseAPreIsolationOwner is checkRelationalStoreOwner's decision, with no cluster in it.
func refuseAPreIsolationOwner(cl *unstructured.Unstructured) error {
	var owner string
	for _, path := range [][]string{
		{"spec", "bootstrap", "initdb", "owner"},
		{"spec", "bootstrap", "recovery", "owner"},
	} {
		v, found, err := unstructured.NestedString(cl.Object, path...)
		if err != nil {
			return fmt.Errorf("reading %v of Cluster %s/%s: %w", path, infraNamespace, rdbClusterName, err)
		}
		if found && v != "" {
			owner = v
			break
		}
	}
	if owner == rdbOwnerUsername {
		return nil
	}
	return fmt.Errorf("the shared relational store (Cluster %s/%s) was created with owner %q, not %q: it "+
		"was built before each instance had a database login of its own, and its owner cannot be changed "+
		"in place. Recreate the cluster (delete it, then `dcctl install` and `dcctl bootstrap` again) to build a store that can "+
		"isolate instances from each other", infraNamespace, rdbClusterName, owner, rdbOwnerUsername)
}

// removeInstanceRelationalLogin drops the instance's database and login from the shared
// store, verifies both are gone, and deletes the Secret that held the login's password.
//
// 🔴 IT ASKS THE STORE, NOT THE SECRET, WHETHER THERE IS ANYTHING TO DROP. The Secret
// lives in the instance's namespace, which the uninstall before this takes with it — so
// its absence says nothing, and a re-run after a failed drop would read it as "never had
// a login" and leave the database behind. The drop itself is idempotent and refuses
// anything dcctl's provisioner did not create, so it is always asked.
//
// A cluster whose install record is missing or from another dcctl has no store this
// dcctl can find; that is said, and nothing is dropped.
//
// It returns why the database was left behind, or "" when it was dropped: a destroy that
// skipped the drop must not close by saying the instance is gone.
func removeInstanceRelationalLogin(ctx context.Context, typed kubernetes.Interface, kubeContext, instance string) (string, error) {
	clusterUID, err := ClusterUID(ctx, typed)
	if err != nil {
		return "", err
	}
	rec, err := readInstallRecord(ctx, typed, clusterUID)
	switch {
	case errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrInstallRecordSchema):
		return err.Error(), nil
	case err != nil:
		return "", fmt.Errorf("finding the relational store through the install record: %w", err)
	}
	if err := removeInstanceDatabase(ctx, kubeContext, instance, rec.Outputs.Rdb); err != nil {
		return "", err
	}
	name := instanceRdbSecretName(instance)
	ns := InstanceNamespace(instance)
	if err := typed.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		return "", fmt.Errorf("deleting Secret %s/%s after dropping the login it held: %w", ns, name, err)
	}
	return "", nil
}
