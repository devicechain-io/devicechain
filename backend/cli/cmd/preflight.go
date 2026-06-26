// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// doctor accumulates and renders preflight results. The goal is to diagnose
// every local-system gotcha BEFORE a bootstrap run, with an actionable fix for
// each problem found (color/emoji output auto-disables on non-TTY / NO_COLOR via
// fatih/color).
type doctor struct {
	fails int
	warns int
}

func (d *doctor) pass(msg string) { fmt.Printf("  %s %s\n", color.GreenString("✅"), msg) }
func (d *doctor) info(msg string) { fmt.Printf("  %s %s\n", color.HiBlackString("•"), msg) }
func (d *doctor) warn(msg, fix string) {
	fmt.Printf("  %s %s\n     %s\n", color.YellowString("⚠️"), msg, color.YellowString("↳ "+fix))
	d.warns++
}
func (d *doctor) fail(msg, fix string) {
	fmt.Printf("  %s %s\n     %s\n", color.RedString("❌"), msg, color.RedString("↳ "+fix))
	d.fails++
}

func section(title, emoji string) { fmt.Printf("\n%s\n", GreenUnderline(emoji+" "+title)) }

// run executes a command and returns trimmed stdout (empty on error).
func run(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func haveTool(name string) bool { _, err := exec.LookPath(name); return err == nil }

// ---- tooling ---------------------------------------------------------------

type toolCheck struct {
	names    []string // alternatives; first found satisfies the check
	required bool     // a missing required tool is a FAIL, otherwise a WARN
	guidance string
}

func (d *doctor) checkTools(provider string) {
	section("Tooling", "🧰")
	checks := []toolCheck{
		{names: []string{"docker"}, required: true, guidance: "install a native Docker engine (not Docker Desktop)"},
		{names: []string{"kubectl"}, required: true, guidance: "https://kubernetes.io/docs/tasks/tools/"},
		{names: []string{"helm"}, required: true, guidance: "https://helm.sh/docs/intro/install/"},
		{names: []string{"tofu", "terraform"}, required: true, guidance: "install OpenTofu (https://opentofu.org) or Terraform"},
	}
	// The local provider targets a kind cluster specifically, so require kind
	// (not just any cluster tool) and cloud-provider-kind (LoadBalancer support).
	// Other/unset providers accept any local cluster tool and treat the LB
	// helper as optional.
	if provider == "local" {
		checks = append(checks,
			toolCheck{names: []string{"kind"}, required: true, guidance: "go install sigs.k8s.io/kind@latest — the local provider deploys to a kind cluster"},
			toolCheck{names: []string{"cloud-provider-kind"}, required: true, guidance: "go install sigs.k8s.io/cloud-provider-kind@latest — required for type=LoadBalancer (ingress-nginx, NATS MQTT)"},
		)
	} else {
		checks = append(checks,
			toolCheck{names: []string{"kind", "minikube", "k3d"}, required: true, guidance: "install a local cluster tool, e.g. go install sigs.k8s.io/kind@latest"},
			toolCheck{names: []string{"cloud-provider-kind"}, required: false, guidance: "go install sigs.k8s.io/cloud-provider-kind@latest — type=LoadBalancer services stay <pending> without it"},
		)
	}
	checks = append(checks,
		toolCheck{names: []string{"ko"}, required: false, guidance: "go install github.com/google/ko@latest — only needed to build local images (--build)"},
	)
	for _, c := range checks {
		found := ""
		for _, n := range c.names {
			if path, err := exec.LookPath(n); err == nil {
				found = fmt.Sprintf("%s (%s)", n, path)
				break
			}
		}
		label := strings.Join(c.names, "/")
		switch {
		case found != "":
			d.pass(found)
		case c.required:
			d.fail(label+" not found", c.guidance)
		default:
			d.warn(label+" not found", c.guidance)
		}
	}
}

// ---- docker engine ---------------------------------------------------------
// Returns the docker data-root directory ("" if unknown) for the disk check.
func (d *doctor) checkDockerEngine() string {
	section("Docker engine", "🐳")
	if !haveTool("docker") {
		d.fail("docker not installed", "install a native Docker engine")
		return ""
	}
	if run("docker", "info", "--format", "{{.ServerVersion}}") == "" {
		d.fail("docker daemon not reachable", "start dockerd (e.g. 'sudo service docker start') and ensure your user is in the 'docker' group")
		return ""
	}
	d.pass("docker daemon reachable")

	if ctx := run("docker", "context", "show"); ctx != "" {
		if ctx == "default" {
			d.pass("docker context = default (native engine)")
		} else {
			d.warn("docker context = "+ctx, "for the clean WSL2/Linux-native path run: docker context use default")
		}
	}

	if n, _ := strconv.Atoi(run("docker", "info", "--format", "{{.NCPU}}")); n > 0 {
		if n >= 4 {
			d.pass(fmt.Sprintf("engine CPUs: %d", n))
		} else {
			d.warn(fmt.Sprintf("engine CPUs: %d", n), "recommend >= 4 for the full stack")
		}
	}
	if b, _ := strconv.ParseInt(run("docker", "info", "--format", "{{.MemTotal}}"), 10, 64); b > 0 {
		gi := b / (1 << 30)
		if gi >= 8 {
			d.pass(fmt.Sprintf("engine memory: %dGi", gi))
		} else {
			d.warn(fmt.Sprintf("engine memory: %dGi", gi), "recommend >= 8Gi")
		}
	}
	if drv := run("docker", "info", "--format", "{{.Driver}}"); drv != "" {
		if drv == "overlay2" {
			d.pass("storage driver: overlay2")
		} else {
			d.warn("storage driver: "+drv, "overlay2 is recommended for kind")
		}
	}
	return run("docker", "info", "--format", "{{.DockerRootDir}}")
}

// ---- ports -----------------------------------------------------------------

func (d *doctor) checkPorts() {
	section("Ports & registry", "🔌")
	registryUp := strings.Contains(run("docker", "ps", "--format", "{{.Names}}"), "kind-registry")
	for _, p := range []int{80, 443, 5000} {
		inUse := portInUse(p)
		switch {
		case p == 5000 && inUse && registryUp:
			d.pass("port 5000 in use by the kind-registry container (expected)")
		case inUse:
			d.warn(fmt.Sprintf("port %d already in use", p), "free it or it may conflict with the cluster/registry")
		default:
			d.pass(fmt.Sprintf("port %d free", p))
		}
	}
}

func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---- kube contexts ---------------------------------------------------------

func (d *doctor) checkKubeContexts() {
	section("Kubernetes contexts", "☸️")
	names, current, err := bootstrap.KubeContexts()
	if err != nil {
		d.warn("unable to load kube contexts", err.Error())
		return
	}
	if current != "" {
		d.info("current-context: " + color.GreenString(current))
	} else {
		d.warn("no current-context set", "select one with: kubectl config use-context <name>")
	}
	if len(names) == 0 {
		d.warn("no contexts found in kubeconfig", "create a local cluster (e.g. deploy/local/up.sh) or add a context")
		return
	}
	for _, name := range names {
		d.info("- " + name)
	}
}

// ---- summary ---------------------------------------------------------------

func (d *doctor) summary() {
	fmt.Println()
	switch {
	case d.fails > 0:
		fmt.Println(color.New(color.FgRed, color.Bold).Sprintf("Preflight: %d failure(s), %d warning(s) — fix failures before bootstrap.", d.fails, d.warns))
	case d.warns > 0:
		fmt.Println(color.New(color.FgYellow, color.Bold).Sprintf("Preflight: ready, with %d warning(s) — review above.", d.warns))
	default:
		fmt.Println(color.New(color.FgGreen, color.Bold).Sprint("Preflight: all checks passed. ✨"))
	}
}

// runDoctor performs the full preflight and returns the accumulated result so
// callers (the preflight command and bootstrap) can decide whether to proceed.
// provider tightens provider-specific checks ("local" requires kind +
// cloud-provider-kind); an empty provider uses the generic checks.
func runDoctor(provider string) *doctor {
	d := &doctor{}
	d.checkTools(provider)
	dockerRoot := d.checkDockerEngine()
	checkSystem(d, dockerRoot) // platform-specific (cgroup/inotify/disk/WSL)
	d.checkPorts()
	d.checkKubeContexts()
	d.summary()
	return d
}

var preflightCmd = &cobra.Command{
	Use:   "preflight [provider]",
	Short: "Check prerequisites for bootstrap",
	Long:  `Proactively diagnoses local-system prerequisites (tools, docker, kernel limits, disk, ports, kube contexts) before a bootstrap run, with an actionable fix for each problem found.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider := ""
		if len(args) > 0 {
			provider = args[0]
		}
		d := runDoctor(provider)
		if d.fails > 0 {
			return fmt.Errorf("%d preflight check(s) failed", d.fails)
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(preflightCmd)
}
