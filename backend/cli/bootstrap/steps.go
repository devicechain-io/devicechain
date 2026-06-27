// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	apply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultProfile is used when the user does not specify one. "full" enables all
// functional areas — the right default for a complete local instance (the chart
// schema accepts: full, telemetry, ingest-only).
const defaultProfile = "full"

// Local developer build-path conventions (mirrors deploy/local). The registry
// is reachable from the host as ImageRegistry (localhost:<port>) and from the
// cluster nodes via the same name on the kind docker network.
const (
	registryContainerName = "kind-registry"
	kindNetwork           = "kind"
	// operatorImageName must match the name the release pipeline publishes the
	// operator under — ghcr.io/devicechain-io/operator (see .github/workflows/
	// release.yml, which special-cases backend/k8s to ".../operator"). A
	// mismatch makes the published-image bootstrap pull a nonexistent image.
	operatorImageName = "operator"
)

// doing prints a white "doing…" progress line (house style).
func doing(msg string) {
	fmt.Print(color.WhiteString("%s... ", msg))
}

// done prints a green "done." after a doing() line.
func done() {
	fmt.Println(color.GreenString("done."))
}

// fail prints a red "failed." after a doing() line and wraps the error.
func fail(what string, err error) error {
	fmt.Println(color.RedString("failed."))
	return fmt.Errorf("%s: %w", what, err)
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

	// The chart deploys every per-instance workload into a namespace named after
	// the instance id (templates/namespace.yaml uses .Values.instance.id), so the
	// readiness gate and report must target exactly that.
	namespace := st.Instance

	doing(fmt.Sprintf("rendering config for instance %q (profile %q)", st.Instance, st.Profile))

	// Generate a DB password regardless of dry-run so the value is reproducible
	// within a run; we just never persist/apply it under dry-run.
	password, err := randomSecret(24)
	if err != nil {
		return fail("generating db password", err)
	}

	// Resolve the image source. Default to published images at a pinned version;
	// the developer path builds from source into a local registry instead.
	if st.ImageRegistry == "" {
		if st.BuildImages {
			st.ImageRegistry = LocalRegistry
		} else {
			st.ImageRegistry = DefaultImageRegistry
		}
	}
	if st.ImageVersion == "" {
		if st.BuildImages {
			st.ImageVersion = "dev"
		} else {
			st.ImageVersion = DefaultImageVersion
		}
	}
	// A binary built without version stamping (plain `go build`) falls back to
	// "dev", which only ever exists in a local registry — the published registry
	// has no :dev tag. Fail clearly here instead of an ImagePullBackOff on every
	// workload several minutes into the run.
	if !st.BuildImages && st.ImageVersion == "dev" {
		return fail("resolving image source", fmt.Errorf(
			"this dcctl build has no pinned image version; deploy a tagged release with --version <tag>, or build from source with --build"))
	}

	imageSource := fmt.Sprintf("%s/<area>:%s (published)", st.ImageRegistry, st.ImageVersion)
	if st.BuildImages {
		imageSource = fmt.Sprintf("built from source → %s/<area>:%s", st.ImageRegistry, st.ImageVersion)
	}

	st.Values["instance"] = st.Instance
	st.Values["namespace"] = namespace
	st.Values["profile"] = st.Profile
	st.Values["dbPassword"] = password
	st.Values["imageRegistry"] = st.ImageRegistry
	st.Values["imageVersion"] = st.ImageVersion
	st.Values["imageSource"] = imageSource
	done()
	return nil
}

// stepLocalRegistry ensures a local registry exists and builds+pushes images
// from source for the developer build path. End users pull published images, so
// this is a no-op unless BuildImages is set.
func stepLocalRegistry(ctx context.Context, st *State) error {
	if !st.BuildImages {
		doing("local image registry")
		fmt.Println(color.GreenString("not needed (using published images)."))
		return nil
	}

	if st.DryRun {
		doing("ensuring local registry + building images from source")
		fmt.Println()
		wouldDo(fmt.Sprintf("provision a registry at %s, wire it to the kind network, and ko-build+push images at tag %q",
			st.ImageRegistry, st.ImageVersion))
		return nil
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}

	doing("ensuring local image registry")
	if err := ensureLocalRegistry(ctx, st); err != nil {
		return fail("provisioning local registry", err)
	}
	done()

	doing("building + pushing images from source (ko)")
	if err := buildImages(ctx, root, st); err != nil {
		return fail("building images", err)
	}
	done()
	return nil
}

// ensureLocalRegistry starts the registry:2 container (if not running), connects
// it to the kind network so cluster nodes can pull by reference, and advertises
// it to the cluster via the KEP-1755 ConfigMap.
func ensureLocalRegistry(ctx context.Context, st *State) error {
	port := registryPort(st.ImageRegistry)

	running, _ := outputOf(ctx, "docker", "inspect", "-f", "{{.State.Running}}", registryContainerName)
	if strings.TrimSpace(running) != "true" {
		if err := run(ctx, "docker", "run", "-d", "--restart=always",
			"-p", fmt.Sprintf("127.0.0.1:%s:5000", port), "--name", registryContainerName, "registry:2"); err != nil {
			return err
		}
	}
	// Idempotent: ignore "already connected" / "endpoint already exists".
	_ = run(ctx, "docker", "network", "connect", kindNetwork, registryContainerName)

	// KEP-1755 ConfigMap so tooling in the cluster can discover the registry.
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "local-registry-hosting", Namespace: "kube-public"},
		Data: map[string]string{
			"localRegistryHosting.v1": fmt.Sprintf("host: \"localhost:%s\"\nhelp: \"https://kind.sigs.k8s.io/docs/user/local-registry/\"\n", port),
		},
	}
	_, err = typed.CoreV1().ConfigMaps("kube-public").Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = typed.CoreV1().ConfigMaps("kube-public").Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

// buildImages ko-builds every service (backend/services/*/main.go) and the
// operator, pushing to ImageRegistry at ImageVersion. --bare names each image
// exactly REGISTRY/<area>:TAG, matching what the chart and operator deploy pull.
func buildImages(ctx context.Context, root string, st *State) error {
	koConfig := filepath.Join(root, ".ko.yaml")
	build := func(moduleDir, imageName string) error {
		cmd := exec.CommandContext(ctx, "ko", "build", "--bare",
			"--tags", st.ImageVersion, "--platform", "linux/amd64", "./")
		cmd.Dir = moduleDir
		cmd.Env = append(os.Environ(),
			"KO_DOCKER_REPO="+imageName,
			"KO_CONFIG_PATH="+koConfig)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ko build %s: %w\n%s", imageName, err, out)
		}
		return nil
	}

	servicesDir := filepath.Join(root, "backend", "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		moduleDir := filepath.Join(servicesDir, e.Name())
		if _, err := os.Stat(filepath.Join(moduleDir, "main.go")); err != nil {
			continue
		}
		if err := build(moduleDir, fmt.Sprintf("%s/%s", st.ImageRegistry, e.Name())); err != nil {
			return err
		}
	}
	if err := build(filepath.Join(root, "backend", "k8s"),
		fmt.Sprintf("%s/%s", st.ImageRegistry, operatorImageName)); err != nil {
		return err
	}

	// The web console is a static nginx SPA, not a Go service, so ko can't build
	// it — docker build from frontend/Dockerfile and push to the same registry
	// the chart resolves ({registry}/frontend:{tag}).
	return buildFrontend(ctx, root, st)
}

// buildFrontend builds the web console image from frontend/Dockerfile and pushes
// it to the local registry at the same registry/tag the services use, so the
// chart's default frontend image (registry/frontend:tag) resolves.
func buildFrontend(ctx context.Context, root string, st *State) error {
	image := fmt.Sprintf("%s/frontend:%s", st.ImageRegistry, st.ImageVersion)
	frontendDir := filepath.Join(root, "frontend")
	if err := run(ctx, "docker", "build", "-t", image, frontendDir); err != nil {
		return err
	}
	return run(ctx, "docker", "push", image)
}

// stepInfraApply deploys the shared data/infra stack via OpenTofu, driving the
// tofu/terraform binary through terraform-exec.
func stepInfraApply(ctx context.Context, st *State) error {
	if st.DryRun {
		doing("applying infrastructure stack (OpenTofu)")
		fmt.Println()
		wouldDo("tofu init+apply deploy/opentofu (NATS, Postgres, Timescale, ingress, cert-manager)")
		return nil
	}
	return runStreamed("applying infrastructure stack (OpenTofu)", "infrastructure stack",
		func() error { return applyInfra(ctx, st) })
}

// stepInstallCore renders the operator overlay (CRDs + RBAC + controller) the
// same way `make deploy` does and applies it via client-go server-side apply.
// The manifests are rendered in-process from manifests embedded in the binary —
// no source checkout or kubectl/kustomize binary required.
func stepInstallCore(ctx context.Context, st *State) error {
	operatorImage := fmt.Sprintf("%s/%s:%s", st.ImageRegistry, operatorImageName, st.ImageVersion)
	doing("installing core components (CRDs + operator)")
	if st.DryRun {
		fmt.Println()
		wouldDo("render the operator overlay and apply CRDs/RBAC/operator (" + operatorImage + ") to " + st.KubeContext)
		return nil
	}

	manifests, err := dck8s.RenderOperator(operatorImage)
	if err != nil {
		return fail("rendering operator manifests", err)
	}
	dyn, disco, _, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	if err := apply.NewApplyOptions(dyn, disco).WithServerSide(true).Apply(ctx, manifests); err != nil {
		return fail("applying operator manifests", err)
	}
	done()
	return nil
}

// stepHelmInstall installs (or upgrades) the per-instance chart via the Helm Go
// SDK, blocking until the rendered workloads are ready.
func stepHelmInstall(ctx context.Context, st *State) error {
	if st.DryRun {
		doing("installing instance chart (Helm)")
		fmt.Println()
		wouldDo("helm upgrade --install the devicechain chart into namespace " + st.Values["namespace"])
		return nil
	}
	return runStreamed("installing instance chart (Helm)", "instance chart",
		func() error { return helmInstall(ctx, st) })
}

// runStreamed frames a step whose underlying tool (tofu, helm) streams its own
// progress to stdout: it prints a heading, runs the work, then prints a final
// indented status line so the noisy sub-tool output is bracketed cleanly.
func runStreamed(heading, label string, work func() error) error {
	fmt.Println(color.WhiteString("%s:", heading))
	if err := work(); err != nil {
		return fail(label, err)
	}
	fmt.Print("  ")
	doing(label)
	done()
	return nil
}

// stepSeedAdmin surfaces the bootstrap admin credential. user-management seeds
// the admin automatically on first start (ADR-008); this step records the
// coordinates so the final report can show them.
func stepSeedAdmin(ctx context.Context, st *State) error {
	doing("recording bootstrap admin credential")
	// These mirror user-management's config defaults (config.Defaults): the admin
	// is seeded into tenant "default" as user "admin" with a well-known initial
	// password that MUST be changed. A future enhancement can read overrides from
	// the chart values / a generated secret.
	st.Values["adminTenant"] = "default"
	st.Values["adminUsername"] = "admin"
	st.Values["adminPassword"] = "devicechain"
	done()
	return nil
}

// stepWaitReady waits for every per-instance workload to report ready. The Helm
// step already blocks on readiness; this is an explicit confirmation gate.
func stepWaitReady(ctx context.Context, st *State) error {
	doing("waiting for areas to become ready")
	if st.DryRun {
		fmt.Println()
		wouldDo("poll each area's deployment until available")
		return nil
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fail("building kube clients", err)
	}
	ns := st.Values["namespace"]
	deadline := time.Now().Add(5 * time.Minute)
	for {
		deps, err := typed.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fail("listing deployments", err)
		}
		ready, total := 0, len(deps.Items)
		for _, d := range deps.Items {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.AvailableReplicas >= want {
				ready++
			}
		}
		if total > 0 && ready == total {
			fmt.Println(color.GreenString("done (%d/%d ready).", ready, total))
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Println(color.RedString("timed out (%d/%d ready).", ready, total))
			return fmt.Errorf("not all areas became ready in namespace %q (%d/%d)", ns, ready, total)
		}
		time.Sleep(3 * time.Second)
	}
}

// stepReport prints an access-info summary from State. This step is real.
func stepReport(ctx context.Context, st *State) error {
	fmt.Println(color.HiGreenString("\nDeviceChain bootstrap summary"))
	fmt.Printf("  %s %s\n", color.WhiteString("Instance:"), color.GreenString(st.Values["instance"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Namespace:"), color.GreenString(st.Values["namespace"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Profile:"), color.GreenString(st.Values["profile"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Images:"), color.GreenString(st.Values["imageSource"]))
	fmt.Printf("  %s %s\n", color.WhiteString("Kube context:"), color.GreenString(st.KubeContext))
	if st.Values["adminUsername"] != "" {
		fmt.Printf("  %s %s / %s  %s\n",
			color.WhiteString("Admin:"),
			color.GreenString(st.Values["adminUsername"]),
			color.GreenString(st.Values["adminPassword"]),
			color.YellowString("(tenant %q — change this password immediately)", st.Values["adminTenant"]))
	}
	if !st.DryRun {
		fmt.Println(color.HiGreenString("\nDeviceChain is up. Open the web console at the cluster ingress root (https://<host>/); the GraphQL APIs are served under /api/<area>."))
	}
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

// registryPort extracts the port from an ImageRegistry like "localhost:5000".
// Defaults to 5000 when no port is present.
func registryPort(registry string) string {
	host := registry
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i+1:]
	}
	return "5000"
}

// run executes a command, returning a combined-output error on failure.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// outputOf runs a command and returns its stdout (trimmed of nothing), ignoring
// the combined error for callers that only care about the value.
func outputOf(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
