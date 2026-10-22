// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The login for the broker's IDENTITY account, in a Secret of its own.
//
// 🔴 WHY A SECRET OF ITS OWN, AND WHY AN ACCOUNT AT ALL. user-management's sign-in buckets hold
// bearer secrets (refresh tokens, OAuth and single sign-on codes). In the shared account any
// service login that can create a JetStream stream can source, mirror or capture a bucket into a
// stream it reads, so no permission on the shared login protects them. They live in an account
// only this login can enter, and exactly one Deployment (user-management) mounts the Secret that
// holds its password (templates/deployment.yaml). The instance document, which every pod mounts,
// carries only the FLAG that the account exists.
//
// 🔑 THE LOGIN IS AN INTRODUCED CREDENTIAL, the same class as the callout's curve key
// (calloutsecret.go): absent from an existing instance's starting state, nothing authenticates
// with it until the broker is told, so minting it replaces nothing. It is minted once, by
// bootstrap or by the first upgrade that finds none, recorded with the other broker credentials
// before the broker is configured, and reused from then on.

const (
	// identitySecretKey is the key the chart's secretKeyRef reads. Restated in
	// deploy/helm/devicechain/templates/deployment.yaml and held against it by
	// TestTheChartReadsTheIdentitySecretDcctlWrites.
	identitySecretKey = "password"

	// identityEnabledValue is the State value that says the instance document will tell
	// user-management the account exists. identityDeployedValue records that the RUNNING
	// document already says so.
	identityEnabledValue  = "natsIdentityEnabled"
	identityDeployedValue = "natsIdentityDeployed"
)

// identitySecretName is the Secret the chart projects into user-management by default
// (devicechain.identitySecret in templates/_helpers.tpl).
func identitySecretName(instance string) string {
	return "dci-" + instance + "-identity"
}

// identitySecret is the instance's identity-login Secret, or false when there is nothing to put in
// it (no password was minted: a broker with no authentication).
func identitySecret(st *State) (ownedSecret, bool) {
	pw := st.Values["natsIdentityPassword"]
	if pw == "" {
		return ownedSecret{}, false
	}
	return ownedSecret{
		Name:      identitySecretName(st.Instance),
		Namespace: InstanceNamespace(st.Instance),
		Type:      corev1.SecretTypeOpaque,
		Labels: map[string]string{
			"app.kubernetes.io/component": "user-management",
		},
		Data: map[string]string{identitySecretKey: pw},
	}, true
}

// writeIdentitySecret writes the password the services of this run will find. It runs before the
// release so the pods can project the Secret at container start, like the instance document.
func writeIdentitySecret(ctx context.Context, typed kubernetes.Interface, st *State) error {
	spec, ok := identitySecret(st)
	if !ok {
		return nil
	}
	return writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID), spec, time.Now)
}

// readIdentityPassword reads the password a running instance holds, from the Secret in namespace.
// An absent Secret yields "" and no error: an instance built before the account has none.
//
// 🔴 IT READS BY NAME AND NOT BY OWNER, like readCalloutSeeds, because the question is "what is
// this instance running on". A Secret that is present but unreadable, or present with no password,
// is an ERROR: a caller that took it for "none" would mint one over a broker that already trusts
// the real one.
func readIdentityPassword(ctx context.Context, typed kubernetes.Interface, namespace, instance string) (string, error) {
	name := identitySecretName(instance)
	s, err := typed.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the identity-account login from Secret %s/%s: %w. Refusing to continue: "+
			"treating an unreadable Secret as absent would mint a new login over a broker that trusts the old one",
			namespace, name, err)
	}
	pw := string(s.Data[identitySecretKey])
	if pw == "" {
		return "", fmt.Errorf("Secret %s/%s exists but holds no %q, so the login the broker trusts for the "+
			"identity account cannot be recovered from it. Minting a replacement would lock user-management out",
			namespace, name, identitySecretKey)
	}
	return pw, nil
}

// identityEnabledFor says whether the instance document of THIS run tells user-management the
// identity account exists.
//
// It must be true only where the broker has the account or this run gives it one. An upgrade that
// skips the infrastructure applies nothing to the broker, so a document that claimed the account
// would send user-management to a login the broker has never heard of, and (failing closed) it
// would refuse to start. So with --skip-infrastructure the flag keeps whatever the running
// document says, and the next upgrade without the flag turns it on.
func identityEnabledFor(st *State) bool {
	if st.Values["natsIdentityPassword"] == "" {
		return false
	}
	return !st.SkipInfrastructure || st.Values[identityDeployedValue] == "true"
}

// setIdentityValues records the identity credential and the decision above in the values the
// broker apply and the instance document are rendered from.
func setIdentityValues(st *State, creds natsauth.Credentials) {
	st.Values["natsIdentityPassword"] = creds.IdentityPassword
	st.Values["natsIdentityPasswordBcrypt"] = creds.IdentityPasswordBcrypt
	if identityEnabledFor(st) {
		st.Values[identityEnabledValue] = "true"
	} else {
		delete(st.Values, identityEnabledValue)
	}
}

// introduceIdentity settles the identity login for an upgrade, from the password the running
// instance holds (read from its identity Secret) or, when it has none, a freshly minted one.
//
// 🔑 THE SECOND PLACE AN UPGRADE MINTS (after the callout's curve key), and for the same reason:
// the credential is INTRODUCED, so nothing yet authenticates with it and minting replaces nothing.
// A password that is present is reused as it is. It runs whether or not the infrastructure is
// applied, so a `--skip-infrastructure` run still writes the Secret its next, full run will use.
func introduceIdentity(st *State) error {
	if st.Values["natsCalloutIssuerSeed"] == "" {
		return nil // a broker with no authentication has no accounts to separate
	}
	creds, err := withIdentity(natsauth.Credentials{}, st.Values["natsIdentityPassword"], "")
	if err != nil {
		return err
	}
	setIdentityValues(st, creds)
	return nil
}

// identityIntroduced reports whether this run turns the identity account on for an instance whose
// running document did not have it: the run after which every sign-in session is gone.
func identityIntroduced(st *State) bool {
	return st.Values[identityEnabledValue] == "true" && st.Values[identityDeployedValue] != "true"
}

// sayIdentityIntroduced closes an upgrade that moved the sign-in buckets into the identity account
// with what that did to people, because it is the one visible consequence of the change.
func sayIdentityIntroduced(st *State) {
	if !identityIntroduced(st) {
		return
	}
	fmt.Println(color.YellowString(
		"\nThe sign-in buckets moved into the broker's own IDENTITY account, reachable only by user-management.\n" +
			"Sessions from before the upgrade were NOT carried over, deliberately: their tokens were readable by\n" +
			"every service login, so they were deleted. Everyone signs in once more (console, dashboards, MCP and\n" +
			"OAuth clients); access tokens already issued keep working until they expire (15 minutes by default)."))
}
