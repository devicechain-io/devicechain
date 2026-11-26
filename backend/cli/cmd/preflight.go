// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/spf13/cobra"
)

// doctor accumulates and renders preflight results. The goal is to diagnose
// every local-system gotcha BEFORE a bootstrap run, with an actionable fix for
// each problem found (color/emoji output auto-disables on non-TTY / NO_COLOR via
// fatih/color).
type doctor struct {
	fails int
	warns int
	// cluster is the kind cluster the run would create or reuse; the port check
	// needs it to tell its own published ports from someone else's. Empty when the
	// run targets an existing cluster.
	cluster string
	// observe receives each observation as the checks make it. Nil prints it on
	// stdout as dcctl always has, which is what `dcctl preflight` and bootstrap do;
	// an install hands the observations to its engine, which reports them through
	// the run's event stream to whichever presenter is drawing it.
	observe func(installflow.PreflightObservation)
}

const dockerInstallHint = "install Docker (Docker Desktop on macOS and Windows, Docker Engine on Linux): https://docs.docker.com/get-docker/"

// record hands one observation on. The verdicts and their thresholds are the
// doctor's; how an observation is drawn is the presenter's.
func (d *doctor) record(o installflow.PreflightObservation) {
	if d.observe != nil {
		d.observe(o)
		return
	}
	installplain.RenderObservation(os.Stdout, o)
}

func (d *doctor) check(v installflow.CheckVerdict, msg, fix string) {
	d.record(installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: v,
		Text: installflow.Text(msg), Fix: installflow.Text(fix)})
}

func (d *doctor) pass(msg string) { d.check(installflow.CheckPass, msg, "") }
func (d *doctor) info(msg string) { d.check(installflow.CheckInfo, msg, "") }
func (d *doctor) warn(msg, fix string) {
	d.check(installflow.CheckWarn, msg, fix)
	d.warns++
}
func (d *doctor) fail(msg, fix string) {
	d.check(installflow.CheckFail, msg, fix)
	d.fails++
}

func (d *doctor) section(code installflow.MessageCode) {
	d.record(installflow.PreflightObservation{Observation: installflow.ObserveSection, Section: code})
}

// run executes a command and returns trimmed stdout (empty on error).
func run(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func haveTool(name string) bool { _, err := lookPath(name); return err == nil }

// lookPath and runCmd are the doctor's only windows onto the host. They are
// variables so a test can present a host with (or without) a Docker daemon, or a
// kubectl that answers per --context, without needing either on the machine.
var (
	lookPath = exec.LookPath
	runCmd   = run
)

// doctorOpts says what the run about to follow will actually need from this host.
// The zero value is the full local-cluster check, which is what `dcctl preflight`
// with no flags diagnoses.
type doctorOpts struct {
	// KubeContext is the --kube-context the run was given. Empty means dcctl brings
	// its own kind cluster.
	KubeContext string
	// Target is the context the run will actually act on: KubeContext, or the kind
	// cluster's context when none was given. Build it with doctorOptsFor.
	Target string
	// Build is --build: images are built from source into a local registry.
	Build bool
	// FullSizeInstall is set only by install after resolving its compact preset.
	// Other callers retain advisory capacity checks with the zero value.
	FullSizeInstall bool
}

// doctorOptsFor is the ONE mapping from a run's flags to what the doctor checks, shared
// by install, bootstrap and preflight so they cannot disagree about the target.
func doctorOptsFor(kubeContext, cluster string, build bool) doctorOpts {
	target := kubeContext
	if target == "" {
		target = bootstrap.KindContext(cluster)
	}
	return doctorOpts{KubeContext: kubeContext, Target: target, Build: build}
}

// hostsKind reports whether dcctl creates or reuses a kind cluster on this host, which
// is what the kind-node kernel limits (inotify) exist for.
func (o doctorOpts) hostsKind() bool { return o.KubeContext == "" }

// needsDocker reports whether the run touches a local Docker daemon: a kind
// cluster is containers on it, and --build builds and pushes through it. An
// install into an existing cluster from published images pulls everything on the
// cluster's own nodes and never talks to a daemon here, so demanding one would
// turn away an operator on a laptop (or a CI runner) with nothing but kubectl.
func (o doctorOpts) needsDocker() bool { return o.Build || o.KubeContext == "" }

// ---- tooling ---------------------------------------------------------------

type toolCheck struct {
	names    []string // alternatives; first found satisfies the check
	required bool     // a missing required tool is a FAIL, otherwise a WARN
	guidance string
}

func (d *doctor) checkTools(provider string, opts doctorOpts) {
	d.section(installplain.SectionTooling)
	var checks []toolCheck
	if opts.needsDocker() {
		checks = append(checks, toolCheck{names: []string{"docker"}, required: true, guidance: dockerInstallHint})
	}
	// There is no helm entry on purpose: dcctl installs the chart through Helm's Go
	// library, and nothing in this module or in the OpenTofu roots it applies runs the
	// helm command, so requiring the binary turned away a host that installs fine.
	// tofu stays: the cluster-prerequisite and instance applies drive it as a binary.
	checks = append(checks,
		toolCheck{names: []string{"kubectl"}, required: true, guidance: "https://kubernetes.io/docs/tasks/tools/"},
		toolCheck{names: []string{"tofu", "terraform"}, required: true, guidance: "install OpenTofu (https://opentofu.org) or Terraform"},
	)
	// The local provider targets a kind cluster specifically, so require kind
	// (not just any cluster tool). cloud-provider-kind is optional: bootstrap
	// reaches ingress and MQTT through host-port/NodePort mappings, so a
	// LoadBalancer (and thus the LB helper) is not needed for the default path.
	switch {
	case opts.KubeContext != "":
		// An existing cluster: dcctl never creates it, so no cluster tool is needed.
	case provider == "local":
		checks = append(checks,
			toolCheck{names: []string{"kind"}, required: true, guidance: "go install sigs.k8s.io/kind@latest — the local provider deploys to a kind cluster"},
			toolCheck{names: []string{"cloud-provider-kind"}, required: false, guidance: "go install sigs.k8s.io/cloud-provider-kind@latest — only needed for type=LoadBalancer services; the default bootstrap uses host-port/NodePort"},
		)
	default:
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
			if path, err := lookPath(n); err == nil {
				found = fmt.Sprintf("%s (%s)", n, path)
				break
			}
		}
		label := strings.Join(c.names, "/")
		switch {
		case found != "":
			if c.names[0] == "tofu" {
				d.checkTofuVersion()
			} else {
				d.pass(found)
			}
		case c.required:
			d.fail(label+" not found", c.guidance)
		default:
			d.warn(label+" not found", c.guidance)
		}
	}
}

// ---- docker engine ---------------------------------------------------------

// engineMemoryRefuseBelow is the engine memory, in bytes, below which a full-size local
// install is refused. It sits under the 8Gi recommendation on purpose: a Docker Desktop
// VM given "8 GB" reports a MemTotal a little under 8GiB (the kernel keeps some), and an
// operator who set exactly the recommended size must get the warning, not a refusal.
// Compared on the exact byte value: a floored GiB would read 7.7GiB as 7 and refuse it.
const engineMemoryRefuseBelow int64 = 15 << 29 // 7.5GiB

// engineMemoryRefused reports whether an engine with b bytes of memory is too small to
// host a full-size install at all.
func engineMemoryRefused(b int64) bool { return b < engineMemoryRefuseBelow }

// Returns the docker data-root directory ("" if unknown) for the disk check.
func (d *doctor) checkDockerEngine(opts doctorOpts) string {
	d.section(installplain.SectionDockerEngine)
	if !haveTool("docker") {
		d.fail("docker not installed", dockerInstallHint)
		return ""
	}
	if runCmd("docker", "info", "--format", "{{.ServerVersion}}") == "" {
		d.fail("docker daemon not reachable", "start Docker Desktop, or on Linux start dockerd (e.g. 'sudo service docker start') and make sure your user is in the 'docker' group")
		return ""
	}
	d.pass("docker daemon reachable")

	if ctx := runCmd("docker", "context", "show"); ctx != "" {
		switch {
		case ctx == "default":
			d.pass("docker context = default (native engine)")
		case runtime.GOOS != "linux":
			// Docker Desktop's own context is the normal, working setup off Linux.
			d.pass("docker context = " + ctx)
		default:
			d.warn("docker context = "+ctx, "for the clean WSL2/Linux-native path run: docker context use default")
		}
	}

	refuseSmall := opts.FullSizeInstall && opts.hostsKind()
	capacityIssue := d.warn
	if refuseSmall {
		capacityIssue = d.fail
	}
	const capacityFix = "give Docker >= 4 CPUs and >= 8Gi, or re-run install with --compact for the small-footprint preset"
	if n, _ := strconv.Atoi(runCmd("docker", "info", "--format", "{{.NCPU}}")); n > 0 {
		if n >= 4 {
			d.pass(fmt.Sprintf("engine CPUs: %d", n))
		} else {
			capacityIssue(fmt.Sprintf("engine CPUs: %d", n), capacityFix)
		}
	}
	if b, _ := strconv.ParseInt(runCmd("docker", "info", "--format", "{{.MemTotal}}"), 10, 64); b > 0 {
		gi := b / (1 << 30)
		switch {
		case gi >= 8:
			d.pass(fmt.Sprintf("engine memory: %dGi", gi))
		case refuseSmall && engineMemoryRefused(b):
			d.fail(fmt.Sprintf("engine memory: %.1fGi", float64(b)/(1<<30)), capacityFix)
		default:
			d.warn(fmt.Sprintf("engine memory: %.1fGi", float64(b)/(1<<30)), capacityFix)
		}
	}
	if drv := runCmd("docker", "info", "--format", "{{.Driver}}"); drv != "" {
		// "overlayfs" is the containerd image store's snapshotter, the default engine
		// since Docker 29; kind runs on it as it does on overlay2.
		if drv == "overlay2" || drv == "overlayfs" {
			d.pass("storage driver: " + drv)
		} else {
			d.warn("storage driver: "+drv, "overlay2 is recommended for kind")
		}
	}
	return runCmd("docker", "info", "--format", "{{.DockerRootDir}}")
}

// checkDocker runs the engine check only when the run needs a daemon, and says so
// when it does not, so a skipped check is visible rather than silently absent.
func (d *doctor) checkDocker(opts doctorOpts) string {
	if !opts.needsDocker() {
		d.section(installplain.SectionDockerEngine)
		d.info("not needed — installing into " + opts.KubeContext + " from published images")
		return ""
	}
	return d.checkDockerEngine(opts)
}

// ---- ports -----------------------------------------------------------------

func (d *doctor) checkPorts() {
	d.section(installplain.SectionPorts)
	registryUp := strings.Contains(runCmd("docker", "ps", "--format", "{{.Names}}"), "kind-registry")
	// A cluster dcctl is about to CREATE publishes 80, 443 and 1883-1885 on the host (ingress
	// HTTP, ingress HTTPS, and one MQTT port for each of up to three local instances). If
	// something else holds one, the create dies on Docker's bind error, so say so here; a
	// cluster that already exists holds them itself.
	clusterExists := d.cluster != "" && containsLine(runCmd("kind", "get", "clusters"), d.cluster)
	// A cluster created before the extra MQTT maps does not publish 1884 or 1885, so a holder of
	// either is not that cluster: it is noted, and it blocks nothing, since no cluster is created.
	publishes := map[int]bool{}
	if clusterExists {
		if out := runCmd("docker", "port", d.cluster+"-control-plane"); out != "" {
			for _, p := range []int{1884, 1885} {
				publishes[p] = strings.Contains(out, fmt.Sprintf(":%d\n", p)) || strings.HasSuffix(strings.TrimSpace(out), fmt.Sprintf(":%d", p))
			}
		}
	}
	for _, p := range []int{80, 443, 1883, 1884, 1885, 5000} {
		inUse := portInUse(p)
		extraMQTT := p == 1884 || p == 1885
		switch {
		case p == 5000 && inUse && registryUp:
			d.pass("port 5000 in use by the kind-registry container (expected)")
		case inUse && p == 5000:
			d.warn("port 5000 already in use", "free it or it may conflict with the local image registry")
		case inUse && clusterExists && extraMQTT && !publishes[p]:
			d.warn(portHolderMessage(p)+fmt.Sprintf(", and not by the existing kind cluster %q, which does not publish it", d.cluster),
				"nothing needs it on this cluster; a new local cluster would, for a second or third instance's MQTT port")
		case inUse && clusterExists:
			d.pass(fmt.Sprintf("port %d in use (by the existing kind cluster %q, expected)", p, d.cluster))
		case inUse:
			d.fail(portHolderMessage(p), fmt.Sprintf("stop what holds port %d and re-run: creating the kind cluster publishes it on the host and fails while it is taken", p))
		default:
			d.pass(fmt.Sprintf("port %d free", p))
		}
	}
}

// portHolderMessage names the Docker container publishing a port when there is one,
// which is the usual holder on a machine that already runs another kind cluster.
func portHolderMessage(p int) string {
	holder := strings.TrimSpace(runCmd("docker", "ps", "--filter", fmt.Sprintf("publish=%d", p), "--format", "{{.Names}}"))
	if holder != "" {
		return fmt.Sprintf("port %d already in use by container %s", p, strings.ReplaceAll(holder, "\n", ", "))
	}
	return fmt.Sprintf("port %d already in use", p)
}

func containsLine(haystack, line string) bool {
	for _, l := range strings.Split(haystack, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// portInUse is a variable so a test can present a host whose ports are taken.
var portInUse = func(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---- kube contexts ---------------------------------------------------------

func (d *doctor) checkKubeContexts(selected string) {
	d.section(installplain.SectionKubeContexts)
	names, current, err := bootstrap.KubeContexts()
	if err != nil {
		d.warn("unable to load kube contexts", err.Error())
		if selected != "" {
			d.fail("--kube-context "+selected+" could not be checked", "fix the kubeconfig error above")
		}
		return
	}
	if current != "" {
		d.record(installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: installflow.CheckInfo,
			Text: installflow.Text("current-context: "), Highlight: installflow.Text(current)})
	} else if selected == "" {
		// dcctl creates its own kind cluster and context, so none yet is the normal start.
		d.info("no current-context set yet (the kind cluster dcctl creates provides one)")
	} else {
		d.warn("no current-context set", "select one with: kubectl config use-context <name>")
	}
	if len(names) == 0 && selected == "" {
		d.info("no contexts in the kubeconfig yet (expected before the first install)")
	} else if len(names) == 0 {
		d.warn("no contexts found in kubeconfig", "create a local cluster (kind create cluster --config deploy/local/kind-cluster.yaml) or add a context")
	}
	for _, name := range names {
		d.info("- " + name)
	}
	if selected != "" {
		// The run targets this context, not the current one, so it is the one that
		// has to exist; a typo here would otherwise surface as a kubectl error deep
		// in the install.
		found := false
		for _, name := range names {
			found = found || name == selected
		}
		if found {
			d.pass("--kube-context " + selected + " exists")
		} else {
			d.fail("--kube-context "+selected+" is not in the kubeconfig", "pick one of the contexts listed above")
		}
	}
}

// ---- kube server version ----------------------------------------------------

// minKubeMajor/minKubeMinor is the API-server floor the infrastructure apply
// requires. It comes from the CloudNativePG charts, which both declare
// `kubeVersion: '>=1.29.0-0'` — helm refuses the release outright below that.
//
// This is worth a preflight check specifically because of WHEN it would otherwise
// bite: the infra apply runs after the render step, so the failure lands once the
// instance credentials and the root-key escrow file have already been written.
// Finding it here costs a second.
const (
	minKubeMajor = 1
	minKubeMinor = 29
)

func (d *doctor) checkKubeServerVersion(kubeContext string) {
	d.section(installplain.SectionKubeServerVersion)

	out := runCmd("kubectl", kubectlVersionArgs(kubeContext)...)
	if out == "" {
		// Not a failure. The local provider is allowed to run before a cluster
		// exists, and an unreachable server is already reported by the context
		// check above; claiming a version problem here would be a false verdict.
		d.info("no reachable cluster — skipping (requires Kubernetes " +
			fmt.Sprintf("%d.%d", minKubeMajor, minKubeMinor) + " or newer)")
		return
	}

	var v struct {
		ServerVersion struct {
			Major      string `json:"major"`
			Minor      string `json:"minor"`
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.ServerVersion.Major == "" {
		d.warn("could not read the Kubernetes server version",
			fmt.Sprintf("DeviceChain requires %d.%d or newer (the CloudNativePG charts refuse older)", minKubeMajor, minKubeMinor))
		return
	}

	ok, err := kubeVersionMeetsFloor(v.ServerVersion.Major, v.ServerVersion.Minor)
	if err != nil {
		d.warn("unparseable Kubernetes server version: "+v.ServerVersion.GitVersion,
			fmt.Sprintf("DeviceChain requires %d.%d or newer", minKubeMajor, minKubeMinor))
		return
	}

	shown := v.ServerVersion.GitVersion
	if shown == "" {
		shown = v.ServerVersion.Major + "." + v.ServerVersion.Minor
	}
	if ok {
		d.pass("kubernetes server " + shown)
		return
	}
	d.fail(fmt.Sprintf("kubernetes server %s is below the %d.%d floor", shown, minKubeMajor, minKubeMinor),
		"the CloudNativePG charts declare kubeVersion >=1.29.0-0 and helm refuses the release, "+
			"which would fail the infra apply AFTER the root-key escrow has been written — upgrade the cluster")
}

// kubectlVersionArgs builds the version query for the cluster the run targets.
//
// 🔴 --context IS THE WHOLE POINT. Without it kubectl answers for the kubeconfig's
// CURRENT context, so `dcctl install --kube-context prod` was gated on whatever
// cluster the shell happened to be pointed at: a new cluster passed on an old
// one's version, and an old one on a new one's.
func kubectlVersionArgs(kubeContext string) []string {
	args := []string{"version", "-o", "json"}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	return args
}

// kubeVersionMeetsFloor compares a reported server version against the floor.
//
// Split out of the check so the REJECTING path can be tested: the only cluster to
// hand is far above the floor, so exercising checkKubeServerVersion against it
// proves the parse works and says nothing about the comparison. A version gate
// that never rejects is indistinguishable from no gate.
func kubeVersionMeetsFloor(rawMajor, rawMinor string) (bool, error) {
	// Managed distributions report minor as "29+" / "30+", so keep the digits.
	major, err := strconv.Atoi(digitsOnly(rawMajor))
	if err != nil {
		return false, fmt.Errorf("major %q: %w", rawMajor, err)
	}
	minor, err := strconv.Atoi(digitsOnly(rawMinor))
	if err != nil {
		return false, fmt.Errorf("minor %q: %w", rawMinor, err)
	}
	return major > minKubeMajor || (major == minKubeMajor && minor >= minKubeMinor), nil
}

// digitsOnly keeps the leading digits of a version component, so "29+" reads as 29.
func digitsOnly(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

// ---- summary ---------------------------------------------------------------

func (d *doctor) summary() {
	d.record(installflow.PreflightObservation{Observation: installflow.ObserveSummary, Fails: d.fails, Warns: d.warns})
}

// runDoctor performs the full preflight and returns the accumulated result so
// callers (the preflight command and bootstrap) can decide whether to proceed.
// provider tightens provider-specific checks ("local" requires kind +
// cloud-provider-kind); an empty provider uses the generic checks.
// Seams for the host-dependent checks, so a test can see whether they ran.
var (
	systemCheck = checkSystem
	portsCheck  = (*doctor).checkPorts
)

func runDoctor(provider string, opts doctorOpts) *doctor {
	return runDoctorTo(provider, opts, nil)
}

// doctorRunner is the preflight an install's engine runs, as its install.preflight
// stage: the same checks as runDoctor, observed through the run instead of printed.
func doctorRunner(provider string, opts doctorOpts) installflow.PreflightRunner {
	return installflow.PreflightFunc(func(_ context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		d := runDoctorTo(provider, opts, observe)
		return installflow.PreflightResult{Fails: d.fails, Warns: d.warns}, nil
	})
}

// runDoctorTo is runDoctor with its observations sent to observe (nil: stdout).
func runDoctorTo(provider string, opts doctorOpts, observe func(installflow.PreflightObservation)) *doctor {
	d := &doctor{observe: observe}
	if opts.hostsKind() {
		d.cluster = strings.TrimPrefix(opts.Target, "kind-")
	}
	d.checkTools(provider, opts)
	dockerRoot := d.checkDocker(opts)
	if opts.needsDocker() {
		// The kernel limits, docker data-root headroom and local ports are all
		// about hosting a kind cluster; none applies to a remote one.
		systemCheck(d, dockerRoot, opts.hostsKind()) // platform-specific (cgroup/inotify/disk/WSL)
		if opts.hostsKind() {
			portsCheck(d)
		}
	}
	d.checkKubeContexts(opts.KubeContext)
	d.checkKubeServerVersion(opts.Target)
	d.summary()
	return d
}

var preflightCmd = &cobra.Command{
	Use:   "preflight [provider]",
	Short: "Check local prerequisites for installing a cluster",
	Long: `Proactively diagnoses local-system prerequisites (tools, docker, kernel limits, disk, ports, kube contexts) before 'dcctl install' prepares a cluster, with an actionable fix for each problem found.

These are the prerequisites for standing a cluster up, not for building an instance on one: a bootstrap runs against a cluster 'dcctl install' has already prepared, so run this before the install.`,
	Example: `  dcctl preflight local
  dcctl preflight local --kube-context prod
  dcctl preflight local --cluster scratch --build`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider := ""
		if len(args) > 0 {
			provider = args[0]
		}
		d := runDoctor(provider, doctorOptsFor(preflightKubeContext, preflightCluster, preflightBuild))
		if d.fails > 0 {
			return fmt.Errorf("%d preflight check(s) failed", d.fails)
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	preflightKubeContext string
	preflightCluster     string
	preflightBuild       bool
)

func init() {
	preflightCmd.Flags().StringVar(&preflightKubeContext, "kube-context", "", "check for installing into this existing cluster instead of a local kind cluster")
	preflightCmd.Flags().StringVar(&preflightCluster, "cluster", bootstrap.DefaultClusterName, "local provider: the kind cluster the run would create or use")
	preflightCmd.Flags().BoolVar(&preflightBuild, "build", false, "check for building images from source (needs Docker even on an existing cluster)")
	rootCmd.AddCommand(preflightCmd)
}
