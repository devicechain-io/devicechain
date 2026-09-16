// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

// ClusterRdb is the relational store's contract, read back from the cluster root: where
// the shared store runs, and which Secret holds the identity allowed to give an instance
// a login and a database of its own.
type ClusterRdb struct {
	Namespace         string `json:"namespace"`
	ClusterName       string `json:"clusterName"`
	ProvisionerSecret string `json:"provisionerSecret"`
}

// rdbFromOutputs decodes the relational store's contract out of the cluster root's
// outputs. Every field is required: unlike the archive, the relational store is not
// optional, so an empty value is never a meaningful answer — it is a root that stopped
// exporting it.
func rdbFromOutputs(outputs map[string]tfexec.OutputMeta) (ClusterRdb, error) {
	var rdb ClusterRdb
	for _, field := range []struct {
		name string
		into *string
	}{
		{"namespace", &rdb.Namespace},
		{"postgres_cluster_name", &rdb.ClusterName},
		{"postgres_provisioner_credentials_secret", &rdb.ProvisionerSecret},
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
	// 🔴 THE SECRET THE STORE READS MUST BE THE ONE dcctl WROTE. The role reconciles its
	// password from whatever the chart names; if that is not the Secret written before the
	// apply, the provisioner has a password nobody holds and every bootstrap on the cluster
	// fails authenticating as it — long after the rename that caused it.
	if rdb.ProvisionerSecret != rdbProvisionerSecretName {
		return rdb, fmt.Errorf("the relational store reads its provisioner password from Secret %q, "+
			"but dcctl writes it to %q; the provisioner would hold a password nothing else does",
			rdb.ProvisionerSecret, rdbProvisionerSecretName)
	}
	return rdb, nil
}

// instanceDatabaseTimeout bounds waiting for the shared store to accept the provisioner.
// On a fresh cluster the apply returns once the Cluster object exists, not once its
// primary is serving, and the operator reconciles the provisioner role after that.
const instanceDatabaseTimeout = 10 * time.Minute

// provisionInstanceDatabase gives this instance its own login and database on the shared
// relational store, signed in as the provisioner.
//
// Indirected so the wiring around it can be exercised without a cluster; the SQL it runs
// is ensureInstanceDatabase, which is tested against a real PostgreSQL.
var provisionInstanceDatabase = func(ctx context.Context, st *State, rdb ClusterRdb) error {
	if st.Credentials == nil || st.Credentials.RDBInstancePassword == "" {
		return errors.New("no password was settled for this instance's own database login")
	}
	return withProvisionerSession(ctx, st.KubeContext, rdb, func(q instanceDBQuerier) error {
		return ensureInstanceDatabase(ctx, q, st.Instance, st.Credentials.RDBInstancePassword)
	})
}

// removeInstanceDatabase drops this instance's database and login, verifying they are
// gone. Indirected for the same reason as provisionInstanceDatabase.
var removeInstanceDatabase = func(ctx context.Context, kubeContext, instance string, rdb ClusterRdb) error {
	return withProvisionerSession(ctx, kubeContext, rdb, func(q instanceDBQuerier) error {
		return dropInstanceDatabase(ctx, q, instance)
	})
}

// withProvisionerSession opens one session on the shared store's primary as the
// provisioner and runs fn in it, retrying what a store that is still coming up answers
// with — no primary yet, a pod not accepting connections, a role the operator has not
// reconciled — until instanceDatabaseTimeout.
//
// 🔴 A REFUSAL IS NEVER RETRIED. errInstanceDatabaseNotOurs means something by this name
// exists and is not dcctl's; waiting does not change that, and ten minutes of retries
// would bury the one message the operator needs.
func withProvisionerSession(ctx context.Context, kubeContext string, rdb ClusterRdb, fn func(instanceDBQuerier) error) error {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube config to reach the relational store: %w", err)
	}
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to reach the relational store: %w", err)
	}

	attempt := func() (retry bool, err error) {
		cl, err := dyn.Resource(clusterGVR).Namespace(rdb.Namespace).Get(ctx, rdb.ClusterName, metav1.GetOptions{})
		if err != nil {
			return true, fmt.Errorf("reading Cluster %s/%s: %w", rdb.Namespace, rdb.ClusterName, err)
		}
		primary, _, _ := unstructured.NestedString(cl.Object, "status", "currentPrimary")
		if primary == "" {
			return true, fmt.Errorf("Cluster %s/%s has no primary yet", rdb.Namespace, rdb.ClusterName)
		}
		sec, err := typed.CoreV1().Secrets(rdb.Namespace).Get(ctx, rdb.ProvisionerSecret, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("reading the provisioner's credentials from Secret %s/%s: %w",
				rdb.Namespace, rdb.ProvisionerSecret, err)
		}
		user, pass := string(sec.Data[secretKeyUsername]), string(sec.Data[secretKeyPassword])
		if user == "" || pass == "" {
			return false, fmt.Errorf("Secret %s/%s does not carry the provisioner's username and password",
				rdb.Namespace, rdb.ProvisionerSecret)
		}
		local, stop, err := forwardPort(restCfg, rdb.Namespace, primary, 5432)
		if err != nil {
			return true, fmt.Errorf("opening a port-forward to the primary %s: %w", primary, err)
		}
		defer stop()
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(connectCtx, localPostgresURL(user, pass, local, "postgres"))
		if err != nil {
			// Authentication failing is retried: the operator reconciles the role, and its
			// password, some time after the Cluster starts accepting connections.
			return true, fmt.Errorf("connecting to the relational store as %q: %w", user, err)
		}
		defer conn.Close(context.Background())
		if err := fn(pgxSession{conn}); err != nil {
			var pe *pgconn.PgError
			// A statement the server rejected is an answer, not a store still starting.
			return !errors.Is(err, errInstanceDatabaseNotOurs) && !errors.As(err, &pe), err
		}
		return false, nil
	}

	deadline := time.Now().Add(instanceDatabaseTimeout)
	for {
		retry, err := attempt()
		if err == nil {
			return nil
		}
		if !retry || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last attempt: %v)", ctx.Err(), err)
		case <-time.After(5 * time.Second):
		}
	}
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
		"in place. Recreate the cluster (delete it, then bootstrap again) to build a store that can "+
		"isolate instances from each other", infraNamespace, rdbClusterName, owner, rdbOwnerUsername)
}

// removeInstanceRelationalLogin drops the instance's database and login from the shared
// store and deletes the Secret holding the login's password — in that order, so a failed
// drop leaves the credential a re-run needs to find the login it is removing.
//
// An instance with no login Secret was never given a login: it was built before
// instances had their own, or its bootstrap stopped before the step that writes one. There
// is nothing of that shape to remove, and that is said rather than skipped silently.
func removeInstanceRelationalLogin(ctx context.Context, typed kubernetes.Interface, kubeContext, instance string) error {
	name := instanceRdbSecretName(instance)
	_, err := typed.CoreV1().Secrets(infraNamespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		fmt.Println(color.YellowString("  instance %q has no database login of its own to remove "+
			"(no Secret %s/%s); any database it created before instances had logins stays on the "+
			"shared store until the cluster is recreated", instance, infraNamespace, name))
		return nil
	case err != nil:
		return fmt.Errorf("reading Secret %s/%s: %w", infraNamespace, name, err)
	}

	// Where the store is, from the record the install wrote — not from constants, which
	// would agree with this dcctl rather than with the cluster.
	clusterUID, err := ClusterUID(ctx, typed)
	if err != nil {
		return err
	}
	rec, err := readInstallRecord(ctx, typed, clusterUID)
	if err != nil {
		return fmt.Errorf("finding the relational store through the install record: %w", err)
	}
	if err := removeInstanceDatabase(ctx, kubeContext, instance, rec.Outputs.Rdb); err != nil {
		return err
	}
	if err := typed.CoreV1().Secrets(infraNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil &&
		!apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Secret %s/%s after dropping the login it held: %w", infraNamespace, name, err)
	}
	return nil
}
