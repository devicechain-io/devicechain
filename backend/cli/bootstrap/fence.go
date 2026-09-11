// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"strings"
)

// retiredStateAddresses are the resources this build no longer declares, and which
// an instance built before it still has in its state.
//
// 🔴 WHAT MAKES THIS LIST DANGEROUS RATHER THAN MERELY STALE. OpenTofu destroys what
// leaves the configuration. Every address here held a CREDENTIAL or the material a
// credential is made of, so an apply against an older instance's state would not fail
// — it would succeed, having deleted the Secret the database authenticates its own
// services with, or the certificate authority the broker's clients trust. Nothing
// announces it, and `prevent_destroy` cannot help: a resource removed from the
// configuration is not a resource the configuration can guard.
//
// 🔑 THE ADDRESSES ARE LITERALS, AND THEY HAVE TO BE. They name resources that no
// longer exist in this tree, so there is nothing to derive them from and nothing that
// will fail to compile if one is wrong. A missing entry is a silent hole in the fence
// — which is why the list is checked against the state of a REAL pre-cutover instance
// in the migration note rather than against this file.
var retiredStateAddresses = []string{
	// The database credentials. CloudNativePG reads these when it creates a Cluster;
	// dcctl writes them now, before the apply, so the role and the services are built
	// from one value.
	"module.cnpg_rdb.kubernetes_secret_v1.app",
	"module.cnpg_tsdb.kubernetes_secret_v1.app",
	// The object store's root credentials, which are also what the backup plugin
	// presents.
	"module.object_store[0].kubernetes_secret_v1.credentials",
	// The broker's TLS material. The CA private key lived in state in cleartext,
	// which is the one confirmed disclosure this slice exists to close.
	"module.nats.tls_private_key.ca[0]",
	"module.nats.tls_self_signed_cert.ca[0]",
	"module.nats.tls_private_key.server[0]",
	"module.nats.tls_cert_request.server[0]",
	"module.nats.tls_locally_signed_cert.server[0]",
	"module.nats.kubernetes_secret_v1.nats_tls[0]",
}

// checkNoRetiredInfrastructure refuses to act on an instance built before the
// credentials moved.
//
// 🔴 IT IS KEYED ON EVIDENCE THE INSTANCE WAS BUILT, NOT ON A VERSION NUMBER. A
// version recorded in the declaration says what built the instance; the STATE says
// what that build left behind, and it is the state the apply is about to act on. The
// two come apart exactly where it matters — an instance rebuilt halfway, a declaration
// restored from elsewhere, a state file older than the binary that wrote it.
//
// 🔑 AND IT FAILS CLOSED ON "CANNOT TELL". stateHasAddress returns an error rather
// than false when the state cannot be read, and that error stops the run: reading an
// unreadable state as "nothing retired here" is precisely the reading that lets the
// apply through to delete the credentials.
//
// 🔴 WHAT THIS FENCE DOES TO adoptChartWrittenInstanceConfig — ANSWERED, NOT LEFT
// TO BE NOTICED.
//
// The takeover exists so an operator whose CHART wrote the instance configuration
// Secret is not forced to rebuild. This fence refuses instances built before the
// credentials moved. If those were one population, the takeover would be code
// nothing can run, and the right change would be to DELETE it rather than explain
// it — the shape this project has shipped before and gone looking for since.
//
// They are two populations, and the difference is what this fence keys on:
//
//   - AN INSTANCE dcctl BUILT before the cutover has the retired resources in its
//     OpenTofu state, so it is refused here and never reaches the takeover. The
//     takeover could not help it anyway — its credentials are still OpenTofu's.
//   - AN INSTANCE INSTALLED WITH PLAIN HELM has no OpenTofu state at all, so
//     nothing here fires, and it arrives at the Helm step with a chart-written
//     Secret that is exactly what the takeover claims. That is not a hypothetical
//     population: `helm install dc deploy/helm/devicechain` is what the published
//     deployment documentation tells an operator to run, and `dc` in namespace
//     `default` is precisely what the takeover matches on.
//
// So the takeover stays until plain Helm is withdrawn, and goes in that change.
// TestTheChartWrittenTakeoverIsStillReachablePastTheFence holds the first half;
// TestTheTakeoverGoesWhenPlainHelmDoes fails in front of whoever removes the
// documented install path, which is the person who needs to be told.
//
// Recreating is the remedy because there is no in-place one that is honest. The
// database passwords, the object-store credentials and the broker's certificate
// authority would each have to be taken over from resources OpenTofu still believes
// it owns, and any run that then failed partway would leave half the instance
// authenticating with values the other half no longer has.
func checkNoRetiredInfrastructure(ctx context.Context, tf stateLister, instance string) error {
	var found []string
	for _, address := range retiredStateAddresses {
		has, err := stateHasAddress(ctx, tf, address)
		if err != nil {
			return err
		}
		if has {
			found = append(found, address)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"instance %q was built by a dcctl that kept its credentials in the infrastructure "+
			"state, and this build keeps them in Secrets it owns. Applying over it would DELETE "+
			"the credentials the databases and the broker are running on, because OpenTofu "+
			"destroys what leaves the configuration — %d such resource(s) are still in this "+
			"instance's state:\n  %s\n"+
			"There is no in-place upgrade for this: destroy the instance and bootstrap it again "+
			"(`dcctl destroy %s` then `dcctl bootstrap %s`). Back up anything you need first — "+
			"a destroy takes the databases with it",
		instance, len(found), strings.Join(found, "\n  "), instance, instance)
}
