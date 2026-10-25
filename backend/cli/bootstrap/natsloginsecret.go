// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// A broker login of its own for every area, each in a Secret of its own.
//
// 🔴 WHY A SECRET PER AREA. The instance document is mounted by every pod, and it carries the shared
// `dc_service` password. A per-area login read out of that document would be per-area in name only:
// any pod could present any area's login. Each login is therefore in a Secret named for its area,
// and the chart projects into each Deployment only the Secret of that Deployment's area
// (templates/deployment.yaml). The document carries only the FLAG that the logins exist.
//
// ...and mcp and ai-inference mount none, because they never open a broker connection
// (config.NatsLoginAreas is the list of those that do).
//
// 🔴 EACH LOGIN GETS WHAT THE SHARED ONE HAS, AND NOTHING IS NARROWED HERE. The broker places every
// one in the same account with the same permissions as `dc_service`, so what an area can do is
// unchanged by this. What changes is that the credential an area presents is one only that area's
// pod can read. The shared login stays in place beside them for this one upgrade, so that pods that
// have not rolled yet still connect; the next upgrade retires it (sharedlogin.go). Rolling back is
// not what keeps it: a helm rollback does not restore the instance document, so an upgrade rolls
// forward.
//
// 🔑 THE LOGINS ARE INTRODUCED CREDENTIALS, the same class as the callout's curve key
// (calloutsecret.go) and the identity login (identitysecret.go): absent from an existing instance's
// starting state, so nothing authenticates with one until the broker is told, and minting it
// replaces nothing. Each is minted once, by bootstrap or by the first upgrade that finds it absent,
// recorded with the other broker credentials before the broker is configured, and reused from then
// on; a login that is present is NEVER replaced, one principal at a time.
//
// The broker administrator (config.NatsAdminPrincipal) is the odd one out: it belongs to no area, and
// no Deployment mounts its Secret. Only the chart's pre-install/pre-upgrade hook Job does
// (templates/broker-admin-job.yaml), which is what makes the right to create and change streams
// one credential's.

const (
	// natsLoginUserKey and natsLoginPasswordKey are the keys the chart's secretKeyRefs read.
	// Restated in deploy/helm/devicechain/templates/deployment.yaml and held against it by
	// TestTheChartMountsEachAreaItsOwnBrokerLogin.
	natsLoginUserKey     = "user"
	natsLoginPasswordKey = "password"

	// State values. A login's plaintext and hash are kept per principal under these prefixes.
	natsLoginPasswordPrefix = "natsLoginPassword."
	natsLoginBcryptPrefix   = "natsLoginBcrypt."

	// areaLoginsEnabledValue is the State value that says the instance document of this run will
	// tell the areas the logins exist. areaLoginsDeployedValue records that the RUNNING document
	// already says so.
	areaLoginsEnabledValue  = "natsAreaLoginsEnabled"
	areaLoginsDeployedValue = "natsAreaLoginsDeployed"
)

// natsLoginSecretName is the Secret the chart projects into a principal's Deployment by default
// (devicechain.natsLoginSecret in templates/_helpers.tpl).
func natsLoginSecretName(instance, principal string) string {
	return "dci-" + instance + "-nats-" + principal
}

// loginPassword and loginBcrypt read a principal's login out of the run's values.
func loginPassword(st *State, principal string) string {
	return st.Values[natsLoginPasswordPrefix+principal]
}

func loginBcrypt(st *State, principal string) string {
	return st.Values[natsLoginBcryptPrefix+principal]
}

// loginsInValues is every principal's recorded password, keyed by principal.
func loginsInValues(st *State) map[string]string {
	out := map[string]string{}
	for _, p := range config.NatsLoginPrincipals() {
		if pw := loginPassword(st, p); pw != "" {
			out[p] = pw
		}
	}
	return out
}

// natsLoginSecrets are the Secrets of this run: one per principal that has a login. Nothing is
// written for a principal with none (a broker with no authentication).
func natsLoginSecrets(st *State) []ownedSecret {
	var out []ownedSecret
	for _, p := range config.NatsLoginPrincipals() {
		pw := loginPassword(st, p)
		if pw == "" {
			continue
		}
		out = append(out, ownedSecret{
			Name:      natsLoginSecretName(st.Instance, p),
			Namespace: InstanceNamespace(st.Instance),
			Type:      corev1.SecretTypeOpaque,
			Labels:    map[string]string{"app.kubernetes.io/component": p},
			Data: map[string]string{
				natsLoginUserKey:     natsauth.LoginUser(p),
				natsLoginPasswordKey: pw,
			},
		})
	}
	return out
}

// writeNatsLoginSecrets writes the logins the services of this run will find. It runs before the
// release so the pods can project the Secrets at container start, like the instance document.
func writeNatsLoginSecrets(ctx context.Context, typed kubernetes.Interface, st *State) error {
	for _, spec := range natsLoginSecrets(st) {
		if err := writeOwnedSecret(ctx, typed, instanceOwner(st.Instance, st.InstanceUID), spec, time.Now); err != nil {
			return err
		}
	}
	return nil
}

// readNatsLoginPasswords reads the logins a running instance holds, from the Secrets in namespace,
// keyed by principal. A principal with no Secret is simply absent from the result: an instance built
// before the logins has none, and one that was interrupted partway has some.
//
// 🔴 IT READS BY NAME AND NOT BY OWNER, like readIdentityPassword, because the question is "what is
// this instance running on". A Secret that is present but unreadable, or present with no password,
// is an ERROR: a caller that took it for "none" would mint a login over a broker that already
// trusts the real one.
func readNatsLoginPasswords(ctx context.Context, typed kubernetes.Interface, namespace, instance string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range config.NatsLoginPrincipals() {
		name := natsLoginSecretName(instance, p)
		s, err := typed.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("reading the broker login for %s from Secret %s/%s: %w. Refusing to continue: "+
				"treating an unreadable Secret as absent would mint a new login over a broker that trusts the old one",
				p, namespace, name, err)
		}
		pw := string(s.Data[natsLoginPasswordKey])
		if pw == "" {
			return nil, fmt.Errorf("Secret %s/%s exists but holds no %q, so the login the broker trusts for %s "+
				"cannot be recovered from it. Minting a replacement would lock that area out",
				namespace, name, natsLoginPasswordKey, p)
		}
		out[p] = pw
	}
	return out, nil
}

// areaLoginsEnabledFor says whether the instance document of THIS run tells the areas the logins
// exist.
//
// It must be true only where the broker has them or this run gives them. An upgrade that skips the
// infrastructure applies nothing to the broker, so a document that claimed the logins would send
// every area to a login the broker has never heard of and (failing closed) none would start. So with
// --skip-infrastructure the flag keeps whatever the running document says, and the next upgrade
// without the flag turns it on.
func areaLoginsEnabledFor(st *State) bool {
	for _, p := range config.NatsLoginPrincipals() {
		if loginPassword(st, p) == "" || loginBcrypt(st, p) == "" {
			return false
		}
	}
	return !st.SkipInfrastructure || st.Values[areaLoginsDeployedValue] == "true"
}

// setLoginValues records the logins and the decision above in the values the broker apply and the
// instance document are rendered from.
func setLoginValues(st *State, creds natsauth.Credentials) {
	for p, l := range creds.Logins {
		st.Values[natsLoginPasswordPrefix+p] = l.Password
		st.Values[natsLoginBcryptPrefix+p] = l.PasswordBcrypt
	}
	if areaLoginsEnabledFor(st) {
		st.Values[areaLoginsEnabledValue] = "true"
	} else {
		delete(st.Values, areaLoginsEnabledValue)
	}
}

// introduceLogins settles every principal's login for an upgrade, from the passwords the running
// instance holds (read from their Secrets) or, for each that has none, a freshly minted one.
//
// 🔑 THE THIRD PLACE AN UPGRADE MINTS (after the callout's curve key and the identity login), and
// for the same reason: each login is INTRODUCED, so nothing yet authenticates with it and minting
// replaces nothing. A password that is present is reused as it is, per principal. It runs whether
// or not the infrastructure is applied, so a `--skip-infrastructure` run still writes the Secrets
// its next, full run will use.
func introduceLogins(st *State) error {
	if st.Values["natsCalloutIssuerSeed"] == "" {
		return nil // a broker with no authentication has no logins to separate
	}
	creds, err := withLogins(natsauth.Credentials{}, loginsInValues(st), nil)
	if err != nil {
		return err
	}
	setLoginValues(st, creds)
	return nil
}

// loginsBcryptByUser is the hash of every login of this run, keyed by username, as the broker apply
// takes them.
func loginsBcryptByUser(st *State) map[string]string {
	out := map[string]string{}
	for _, p := range config.NatsLoginPrincipals() {
		if h := loginBcrypt(st, p); h != "" {
			out[natsauth.LoginUser(p)] = h
		}
	}
	return out
}

// loginsVar renders the hashes as the HCL map the instance root's nats_login_password_bcrypts takes.
// Sorted, so two plans of the same inputs are the same plan.
func loginsVar(hashes map[string]string) string {
	users := make([]string, 0, len(hashes))
	for u := range hashes {
		users = append(users, u)
	}
	sort.Strings(users)
	parts := make([]string, 0, len(users))
	for _, u := range users {
		parts = append(parts, fmt.Sprintf("%q=%q", u, hashes[u]))
	}
	return "nats_login_password_bcrypts={" + strings.Join(parts, ",") + "}"
}

// areaLoginsIntroduced reports whether this run turns the per-area logins on for an instance whose
// running document did not have them: the run after which every area connects as itself.
func areaLoginsIntroduced(st *State) bool {
	return st.Values[areaLoginsEnabledValue] == "true" && st.Values[areaLoginsDeployedValue] != "true"
}

// sayAreaLoginsIntroduced closes an upgrade that gave every area a broker login of its own with what
// it did and did not change, because the shared login staying is the part a reader would otherwise
// assume away.
func sayAreaLoginsIntroduced(st *State) {
	if !areaLoginsIntroduced(st) {
		return
	}
	fmt.Println(color.WhiteString(
		"\nEvery area that uses the message broker now has a login of its own, in a Secret only that area mounts,\n" +
			"and so does the broker administrator, which only the pre-upgrade job mounts and which alone creates\n" +
			"and changes streams and buckets. Each area's login is held to what that area uses: its own streams,\n" +
			"consumers, buckets and reply subjects, and it cannot create or change a stream. The shared login is\n" +
			"still in place, with the permissions it always had, so pods that had not rolled yet kept their\n" +
			"connection. This was phase 1 of 2: run `dcctl upgrade` once more to retire it."))
}

// loginPlaintexts is every principal's password in creds, keyed by principal, as the broker record
// stores them.
func loginPlaintexts(creds natsauth.Credentials) map[string]string {
	if len(creds.Logins) == 0 {
		return nil
	}
	out := make(map[string]string, len(creds.Logins))
	for p, l := range creds.Logins {
		out[p] = l.Password
	}
	return out
}

// isNatsLoginUser reports whether user is the username of one of the logins this file manages, at
// any generation.
func isNatsLoginUser(user string) bool {
	for _, p := range config.NatsLoginPrincipals() {
		if config.IsNatsLoginOf(user, p) {
			return true
		}
	}
	return false
}
