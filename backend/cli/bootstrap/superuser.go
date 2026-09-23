// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/devicechain-io/dc-microservice/auth"
)

// The instance's global superuser, and the one secret it is seeded from.
//
// 🔴 THERE USED TO BE NO SECRET HERE AT ALL. user-management filled a blank seed
// password with a literal, this package restated the same literal in its report, and
// the quickstart published it — so every instance anyone had built held an authority-`*`
// superuser whose password was on the documentation site. dcctl now generates the
// password per instance, writes it to a Secret of the instance's own, and the chart
// projects that Secret into user-management alone (DC_SUPERUSER_PASSWORD). No default
// exists anywhere: without the value, user-management refuses to seed.
//
// 🔴 THE VALUE IS A SEED, NOT A LIVE CREDENTIAL, AND THAT DECIDES EVERYTHING ELSE HERE.
// It is read exactly once — when user-management first starts against an EMPTY
// identity table — and never again. After that the superuser changes its password
// through the API, and the Secret goes on holding the value it was seeded with. So
// the Secret is only as true as the table it seeded:
//
//   - A bootstrap that generated it, on a fresh instance, is the one case where it is
//     known to be the superuser's password — and the report says so, once.
//   - A bootstrap that RECOVERS an instance restores an identity table that was seeded
//     long ago; the Secret it writes seeded nothing, and the report says that instead.
//   - An upgrade never writes it. An instance built before this existed has no such
//     Secret, and generating one there would be worse than having none: its superuser
//     was seeded with the old literal, and every tool that trusted the new Secret would
//     fail to sign in while the literal went on working. So the upgrade leaves it absent
//     and says what that means (see warnPreGeneratedSuperuser).

// superuserSecretKey is the key the chart's secretKeyRef reads. Restated in
// deploy/helm/devicechain/templates/deployment.yaml, and held against it by
// TestTheChartReadsTheSuperuserSecretDcctlWrites.
const superuserSecretKey = secretKeyPassword

// superuserSecretName is the Secret the chart projects into user-management by default
// (devicechain.superuserSecret in templates/_helpers.tpl).
func superuserSecretName(instance string) string {
	return "dci-" + instance + "-superuser"
}

func superuserSecretRef(instance string) mintedCredentialRef {
	return mintedCredentialRef{InstanceNamespace(instance), superuserSecretName(instance), superuserSecretKey}
}

// superuserSecret is the instance's seed-password Secret, in the instance's namespace
// because the pod that reads it runs there.
func superuserSecret(st *State, set *credentialSet) ownedSecret {
	ref := superuserSecretRef(st.Instance)
	return ownedSecret{
		Name:      ref.Name,
		Namespace: ref.Namespace,
		Type:      corev1.SecretTypeOpaque,
		Labels: map[string]string{
			"app.kubernetes.io/component": "user-management",
		},
		Data: map[string]string{
			ref.Key: set.SuperuserPassword,
		},
	}
}

// superuserSeedState is what one run knows about the seed password it holds — which is
// what the report may claim about it.
type superuserSeedState int

const (
	// superuserSeedUnsettled: nothing was settled — a dry run, or a run that plans no
	// instance. The report may say where the value will be, and nothing about it.
	superuserSeedUnsettled superuserSeedState = iota
	// superuserSeedMinted: this run generated it. The only state in which the report
	// shows the value.
	superuserSeedMinted
	// superuserSeedRecovered: an earlier run of this bootstrap generated it and this one
	// read it back. It may already have been shown, so it is not shown again.
	superuserSeedRecovered
	// superuserSeedAbsent: an upgrade found no Secret — the instance's superuser was
	// seeded before dcctl generated one. See warnPreGeneratedSuperuser.
	superuserSeedAbsent
)

// printSuperuserReport is the bootstrap summary's superuser block.
//
// 🔴 WHERE, ALWAYS; THE VALUE, ONCE. The location is printed on every path because it
// is the only statement that stays true after the password is changed or the report
// is lost. The value is printed only when THIS run generated it on a fresh instance,
// so it appears in exactly one report — and never under a recovery, where the restored
// identity table was seeded long before this Secret existed and the value it holds
// signs in as nobody.
func printSuperuserReport(st *State) {
	ref := superuserSecretRef(st.Instance)
	fmt.Printf("  %s %s\n", color.WhiteString("Superuser:"), color.GreenString(auth.DefaultSuperuserEmail))

	// A recovery is the root key restored from escrow: that is how a bootstrap rebuilds
	// an instance whose identities the install brought back with the relational store.
	// NOT st.Restore.Active() — the event-store restore a bootstrap can carry holds no
	// identities, so an instance restoring only its telemetry is seeded fresh and the
	// value it is seeded with is the one to show.
	recovering := st.Escrow.RestoredFrom != "" || st.Restore.RestoresRelationalStore()
	switch {
	case recovering:
		fmt.Printf("           %s\n", color.YellowString(
			"this instance was RECOVERED, so its superuser keeps the password it had: the restored identities are "+
				"not reseeded. Secret %s/%s holds a seed password generated by this run, used only if the identity "+
				"table is empty — it is not the restored superuser's password", ref.Namespace, ref.Name))
		return
	case st.SuperuserSeed == superuserSeedMinted && !st.DryRun && st.Credentials != nil:
		fmt.Printf("           %s %s\n", color.WhiteString("password:"), color.GreenString(st.Credentials.SuperuserPassword))
		fmt.Printf("           %s\n", color.YellowString(
			"generated for this instance and shown only this once — sign in to the admin console to create your "+
				"first tenant, and change it there"))
	case st.DryRun:
		fmt.Printf("           %s\n", color.WhiteString(
			"a password is generated for this instance and shown once, at the end of the real run"))
	}
	fmt.Printf("           %s\n", color.WhiteString(fmt.Sprintf(
		"the password the superuser was first seeded with is kept in Secret %s/%s, key %s "+
			"(changing the password in the console does not update it):", ref.Namespace, ref.Name, ref.Key)))
	fmt.Printf("           %s\n", color.GreenString(
		"kubectl -n %s get secret %s -o jsonpath='{.data.%s}' | base64 -d", ref.Namespace, ref.Name, ref.Key))
}

// warnPreGeneratedSuperuser is said at the end of an upgrade whose instance has no
// seed-password Secret: its superuser was seeded by a release that used the literal
// every release before it published.
//
// 🔴 SAID LOUDLY BECAUSE SILENCE READS AS REMEDIATED. Upgrading does not change the
// superuser's password and cannot: the table is already seeded, and nothing here knows
// whether the operator has changed it since. What the upgrade can do is say so.
func warnPreGeneratedSuperuser(st *State) {
	if st.SuperuserSeed != superuserSeedAbsent {
		return
	}
	fmt.Println(color.RedString(
		"\nThis instance's superuser (%s) was seeded before dcctl generated a password for it,\n"+
			"with the password earlier releases published. Upgrading does not change it. If it has\n"+
			"not been changed since, change it now in the admin console — or recreate the instance\n"+
			"(`dcctl destroy` then `dcctl bootstrap`) to have one generated.", auth.DefaultSuperuserEmail))
}

// ReadSuperuserPassword reads an instance's generated superuser seed password from its
// Secret, for the tools that sign in as the superuser (`dcctl sim`).
//
// It returns the SEED: the password the superuser was created with, which is its
// password until somebody changes it. It fails, naming the Secret and the command that
// reads it, when the Secret is missing or empty — never an empty string, which a caller
// would send as a password.
func ReadSuperuserPassword(ctx context.Context, kubeContext, instance string) (string, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return "", fmt.Errorf("connecting to the cluster to read instance %q's superuser password: %w", instance, err)
	}
	return readSuperuserPassword(ctx, typed, instance)
}

func readSuperuserPassword(ctx context.Context, typed kubernetes.Interface, instance string) (string, error) {
	ref := superuserSecretRef(instance)
	how := fmt.Sprintf("kubectl -n %s get secret %s -o jsonpath='{.data.%s}' | base64 -d",
		ref.Namespace, ref.Name, ref.Key)
	s, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", fmt.Errorf("instance %q has no Secret %s/%s, so its superuser password cannot be read "+
			"from the cluster. An instance built before dcctl generated one was seeded with the password earlier "+
			"releases published; pass the password explicitly", instance, ref.Namespace, ref.Name)
	case err != nil:
		return "", fmt.Errorf("reading Secret %s/%s for instance %q's superuser password: %w (by hand: %s)",
			ref.Namespace, ref.Name, instance, err, how)
	}
	v := string(s.Data[ref.Key])
	if v == "" {
		return "", fmt.Errorf("Secret %s/%s has no %q key, so instance %q's superuser password cannot be read "+
			"from it (by hand: %s)", ref.Namespace, ref.Name, ref.Key, instance, how)
	}
	return v, nil
}
