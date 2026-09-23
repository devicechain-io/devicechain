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
//   - A bootstrap that RECOVERS an instance may restore an identity table that was
//     seeded long ago; the Secret it writes then seeded nothing, and the report says
//     that instead.
//   - A bootstrap re-run over a LIVE instance (stepRefuseRebuild's carve-outs) treats a
//     missing Secret exactly as the upgrade does, below.
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
	// superuserSeedMinted: this run generated it.
	superuserSeedMinted
	// superuserSeedRecovered: a Secret this instance already had was read back. On an
	// instance that is not yet running, that is an earlier run of this same bootstrap
	// which died before its report — a re-run is refused once the configuration
	// document exists, and the report runs after it — so the value was never shown.
	// Over a LIVE instance (a carve-out re-run) it may have been shown and changed
	// since, and is not shown again.
	superuserSeedRecovered
	// superuserSeedAbsent: a live instance has no Secret — its superuser was seeded
	// before dcctl generated one. Settled by an upgrade, or by a bootstrap re-run over
	// a live instance; either way nothing is minted or written.
	// See warnPreGeneratedSuperuser.
	superuserSeedAbsent
)

// printSuperuserReport is the bootstrap summary's superuser block.
//
// 🔴 WHERE, ALWAYS; THE VALUE, ONCE. The location is printed whenever there is a Secret,
// because it is the only statement that stays true after the password is changed or
// the report is lost. The value is printed only when it is known to be what the
// superuser is about to be seeded with and has not been shown before: generated by
// this run, or by an earlier run of this bootstrap that died before reaching here, on
// an instance that is not yet running. Never under a recovery or over a live instance,
// where the identity table may have been seeded long before this Secret existed and
// the value it holds would sign in as nobody.
func printSuperuserReport(st *State) {
	ref := superuserSecretRef(st.Instance)
	fmt.Printf("  %s %s\n", color.WhiteString("Superuser:"), color.GreenString(auth.DefaultSuperuserEmail))

	if st.SuperuserSeed == superuserSeedAbsent {
		fmt.Printf("           %s\n", color.YellowString(
			"this instance was already running and has no Secret %s/%s, so this run generated no password",
			ref.Namespace, ref.Name))
		warnPreGeneratedSuperuser(st)
		return
	}

	switch {
	case st.Restore.RestoresRelationalStore():
		// The identities came back with the relational store, and are not reseeded.
		fmt.Printf("           %s\n", color.YellowString(
			"this instance's identities were RESTORED, so its superuser keeps the password it had. "+
				"The Secret below holds a seed used only to seed an EMPTY identity table — "+
				"it is not the restored superuser's password"))
	case st.Escrow.RestoredFrom != "":
		// The root key came back from escrow, but whether the identities did depends on
		// what the relational store holds, which this run does not decide.
		fmt.Printf("           %s\n", color.YellowString(
			"this instance was RECOVERED from escrow. If its identity table came back with it, its superuser "+
				"keeps the password it had and the Secret below does not sign in; if the table was empty, "+
				"the superuser was seeded from the Secret below"))
	case st.DryRun && st.OverLiveInstance:
		fmt.Printf("           %s\n", color.WhiteString(
			"this instance is already running, so a real run generates no password: its superuser keeps the one it has"))
	case st.DryRun:
		fmt.Printf("           %s\n", color.WhiteString(
			"a password is generated for this instance and shown once, at the end of the real run"))
	case st.Credentials != nil && !st.OverLiveInstance &&
		(st.SuperuserSeed == superuserSeedMinted || st.SuperuserSeed == superuserSeedRecovered):
		fmt.Printf("           %s %s\n", color.WhiteString("password:"), color.GreenString(st.Credentials.SuperuserPassword))
		fmt.Printf("           %s\n", color.YellowString(
			"generated for this instance and shown only this once — sign in to the admin console to create your "+
				"first tenant, and change it there"))
	}
	fmt.Printf("           %s\n", color.WhiteString(fmt.Sprintf(
		"the superuser's seed password is kept in Secret %s/%s, key %s "+
			"(changing the password in the console does not update it):", ref.Namespace, ref.Name, ref.Key)))
	fmt.Printf("           %s\n", color.GreenString(
		"kubectl -n %s get secret %s -o jsonpath='{.data.%s}' | base64 -d", ref.Namespace, ref.Name, ref.Key))
}

// warnPreGeneratedSuperuser is said at the end of an upgrade — or a bootstrap re-run
// over a live instance — whose instance has no seed-password Secret: its superuser was
// seeded by a release that used the literal every release before it published.
//
// 🔴 SAID LOUDLY BECAUSE SILENCE READS AS REMEDIATED. Neither verb changes the
// superuser's password, and neither can: the table is already seeded, and nothing here
// knows whether the operator has changed it since. What they can do is say so.
func warnPreGeneratedSuperuser(st *State) {
	if st.SuperuserSeed != superuserSeedAbsent {
		return
	}
	fmt.Println(color.RedString(
		"\nThis instance's superuser (%s) was seeded before dcctl generated a password for it,\n"+
			"with the password earlier releases published. Neither upgrading nor re-running bootstrap\n"+
			"changes it. If it has not been changed since, change it now in the admin console — or\n"+
			"recreate the instance (`dcctl destroy` then `dcctl bootstrap`) to have one generated.",
		auth.DefaultSuperuserEmail))
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
