// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/fatih/color"
)

// defaultProfile is used when the user does not specify one.
const defaultProfile = "dev"

// doing prints a white "doing…" progress line (house style).
func doing(msg string) {
	fmt.Print(color.WhiteString("%s... ", msg))
}

// done prints a green "done." after a doing() line.
func done() {
	fmt.Println(color.GreenString("done."))
}

// wouldDo prints what a step WOULD do under --dry-run.
func wouldDo(msg string) {
	fmt.Println(color.YellowString("[dry-run] would %s", msg))
}

// stepRenderConfig generates per-instance values into State. This step actually
// populates state (namespace, generated DB password, resolved profile) so later
// steps and the report have something concrete to consume.
func stepRenderConfig(ctx context.Context, st *State) error {
	if st.Values == nil {
		st.Values = map[string]string{}
	}

	// Default the profile when empty so downstream config is deterministic.
	if st.Profile == "" {
		st.Profile = defaultProfile
	}

	// Map instance id to a namespace. Kept simple/derived for the skeleton.
	namespace := "dc-" + st.Instance

	doing(fmt.Sprintf("rendering config for instance %q (profile %q)", st.Instance, st.Profile))

	// Generate a DB password regardless of dry-run so the value is reproducible
	// within a run; we just never persist/apply it under dry-run.
	password, err := randomSecret(24)
	if err != nil {
		fmt.Println(color.RedString("failed."))
		return fmt.Errorf("generating db password: %w", err)
	}

	st.Values["instance"] = st.Instance
	st.Values["namespace"] = namespace
	st.Values["profile"] = st.Profile
	st.Values["dbPassword"] = password
	done()
	return nil
}

// stepLocalRegistry ensures images are pullable by reference rather than
// side-loaded into the node.
func stepLocalRegistry(ctx context.Context, st *State) error {
	// TODO(ADR-032 §image-model): ensure a local registry (kind/minikube/k3d native) so images are pulled by reference (localhost:5000), not side-loaded.
	doing("ensuring local image registry")
	if st.DryRun {
		fmt.Println()
		wouldDo("provision a local registry reachable at localhost:5000")
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepInfraApply deploys the shared data/infra stack via OpenTofu.
func stepInfraApply(ctx context.Context, st *State) error {
	// TODO(ADR-032 phase: infra): tofu init+apply deploy/opentofu via terraform-exec (NATS/Postgres/Timescale/ingress/cert-manager).
	doing("applying infrastructure stack")
	if st.DryRun {
		fmt.Println()
		wouldDo("tofu init+apply deploy/opentofu (NATS, Postgres, Timescale, ingress, cert-manager)")
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepInstallCore installs CRDs and the operator.
func stepInstallCore(ctx context.Context, st *State) error {
	// TODO(ADR-032 phase: core): install CRDs+operator (reuse the install_core.go client-go apply pattern with a lazy config).
	doing("installing core components (CRDs + operator)")
	if st.DryRun {
		fmt.Println()
		wouldDo("apply CRDs, RBAC and the operator to context " + st.KubeContext)
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepHelmInstall installs the per-instance chart.
func stepHelmInstall(ctx context.Context, st *State) error {
	// TODO(ADR-032 phase: instance): helm install deploy/helm/devicechain via the Helm Go SDK (instance.id, image.registry/tag).
	doing("installing instance chart (Helm)")
	if st.DryRun {
		fmt.Println()
		wouldDo("helm install deploy/helm/devicechain into namespace " + st.Values["namespace"])
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepSeedAdmin captures the seeded bootstrap admin credential.
func stepSeedAdmin(ctx context.Context, st *State) error {
	// TODO(ADR-032 phase: seed): capture the seeded bootstrap admin credential.
	doing("seeding bootstrap admin credential")
	if st.DryRun {
		fmt.Println()
		wouldDo("capture the seeded bootstrap admin username/password")
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepWaitReady polls each area's readiness endpoint.
func stepWaitReady(ctx context.Context, st *State) error {
	// TODO(ADR-032 phase: wait): poll each area's /readyz.
	doing("waiting for areas to become ready")
	if st.DryRun {
		fmt.Println()
		wouldDo("poll each area's /readyz until ready")
		return nil
	}
	fmt.Println(color.YellowString("skipped (not yet wired)."))
	return nil
}

// stepReport prints an access-info summary from State. This step is real.
func stepReport(ctx context.Context, st *State) error {
	fmt.Println(color.HiGreenString("\nDeviceChain bootstrap summary"))
	fmt.Printf("  %s %s\n", color.WhiteString("Instance:"), color.GreenString(st.Values["instance"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Namespace:"), color.GreenString(st.Values["namespace"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Profile:"), color.GreenString(st.Values["profile"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Kube context:"), color.GreenString(st.KubeContext))
	fmt.Println(color.YellowString(
		"\nNote: infra/core/instance/seed steps are not yet wired (ADR-032 skeleton); no workloads were deployed."))
	return nil
}

// randomSecret returns a hex-encoded random string with the given byte length
// of entropy. Uses crypto/rand so generated credentials are not predictable.
func randomSecret(nbytes int) (string, error) {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
