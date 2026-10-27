// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package functionalarea

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The release image-ref contract, extended past the services.
//
// TestCatalogMatchesPublishedServiceImages (release_contract_test.go) pins the service half:
// backend/services/* == the functional-area catalog. That test says outright that the operator
// and the console "are excluded from both sides by construction" -- which is exactly the gap:
// dcctl pulls `<registry>/operator:<tag>` and the chart pulls `<registry>/frontend:<tag>`, and
// nothing tied either name to what release.yml publishes, or to the list the signature
// verifier checks (hack/verify-release-signatures.sh).
//
// This file closes that. The sets compared are:
//
//	PUBLISHED   release.yml, parsed: the `guard` job's image modules (services via
//	            hack/list-services.sh's rule, plus every module it echoes by hand), the
//	            backend/k8s -> "operator" rename, the console's tag line, the chart's OCI path.
//	EXPECTED    what the deploy side pulls: the catalog's areas, dcctl's operator.ImageName,
//	            the chart's default console image, the edge agent RUNBOOK's image, the
//	            chart's own name.
//	VERIFIED    the RELEASE_IMAGES array embedded in hack/verify-release-signatures.sh -- the
//	            list a user's signature check walks. An image released but missing there ships
//	            unverified while the verifier reports success.
//
// A rename on one side only fails here, at PR time, instead of as an ImagePullBackOff or a
// signature that was never checked on a tag.

const repoRoot = "../../.."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

func serviceDirsWithMain(t *testing.T) []string {
	t.Helper()
	servicesDir := filepath.Join("..", "..", "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("reading %s: %v", servicesDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(servicesDir, e.Name(), "main.go")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// moduleName maps a ko image module path to the image name the release publishes it under:
// backend/k8s is the operator; every other module is its directory basename.
func moduleName(module string) string {
	if module == "backend/k8s" {
		return "operator"
	}
	return module[strings.LastIndex(module, "/")+1:]
}

var guardExtraModule = regexp.MustCompile(`(?m)^\s*echo "(backend/[^"]+)"\s*$`)

// releaseWorkflowModules returns the by-hand modules the release `guard` job appends to the
// discovered services (the operator and the edge agent today).
func releaseWorkflowModules(wf string) []string {
	var out []string
	for _, m := range guardExtraModule.FindAllStringSubmatch(wf, -1) {
		out = append(out, m[1])
	}
	return out
}

// verifierImages parses the RELEASE_IMAGES=( ... ) array and RELEASE_CHART="..." out of the
// verifier script.
func verifierImages(t *testing.T, script string) (images []string, chart string) {
	t.Helper()
	start := strings.Index(script, "RELEASE_IMAGES=(")
	if start < 0 {
		t.Fatal("hack/verify-release-signatures.sh has no RELEASE_IMAGES=( array")
	}
	rest := script[start+len("RELEASE_IMAGES=("):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("RELEASE_IMAGES array is not closed")
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		images = append(images, strings.Fields(line)...)
	}
	m := regexp.MustCompile(`(?m)^RELEASE_CHART="([^"]+)"`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("hack/verify-release-signatures.sh has no RELEASE_CHART=\"...\"")
	}
	return images, m[1]
}

// diffSets reports, in both directions, what one side has that the other lacks.
func diffSets(aName string, a []string, bName string, b []string) []string {
	as, bs := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		as[x] = true
	}
	for _, x := range b {
		bs[x] = true
	}
	var msgs []string
	for x := range as {
		if !bs[x] {
			msgs = append(msgs, "in "+aName+" but not in "+bName+": "+x)
		}
	}
	for x := range bs {
		if !as[x] {
			msgs = append(msgs, "in "+bName+" but not in "+aName+": "+x)
		}
	}
	sort.Strings(msgs)
	return msgs
}

func TestReleaseImageSetMatchesDeployAndVerifier(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/release.yml")

	// PUBLISHED: services + by-hand modules (mapped) + the console.
	published := serviceDirsWithMain(t)
	modules := releaseWorkflowModules(wf)
	if len(modules) == 0 {
		t.Fatal("found no by-hand image modules in release.yml's guard job -- the pattern or the file layout changed")
	}
	sawOperator := false
	for _, m := range modules {
		published = append(published, moduleName(m))
		if m == "backend/k8s" {
			sawOperator = true
		}
	}
	if !sawOperator {
		t.Error("release.yml's guard job no longer builds backend/k8s (the operator image)")
	}
	published = append(published, "frontend")

	// The release publishes the operator at ".../operator" and the console at ".../frontend";
	// assert the workflow text still says so, rather than trusting the constants above.
	for _, want := range []string{
		`repo=${REGISTRY}/${name}`,               // ko repo is derived from the name below
		`name=operator`,                          // backend/k8s -> operator
		`tags="${REGISTRY}/frontend:${VERSION}"`, // the console image name
		`oci://${REGISTRY}/charts`,               // the chart's OCI namespace
		`then "operator" else`,                   // the sign matrix applies the same rename
		`{artifact: "frontend", path: "frontend"`,
		`{artifact: "chart", path: "charts/devicechain"`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml no longer contains %q -- the image-name contract moved; update this test, the verifier and dcctl together", want)
		}
	}

	// EXPECTED: what the deploy side pulls.
	var expected []string
	for area := range catalog {
		expected = append(expected, string(area))
	}
	opSrc := readRepoFile(t, "backend/cli/operator/operator.go")
	m := regexp.MustCompile(`(?m)^const ImageName = "([^"]+)"`).FindStringSubmatch(opSrc)
	if m == nil {
		t.Fatal("backend/cli/operator/operator.go has no `const ImageName = \"...\"`")
	}
	expected = append(expected, m[1]) // what dcctl pulls for the controller
	helpers := readRepoFile(t, "deploy/helm/devicechain/templates/_helpers.tpl")
	if !strings.Contains(helpers, `(printf "%s/frontend" .Values.image.registry)`) {
		t.Error("the chart's default console image is no longer {registry}/frontend -- update the release and this test together")
	}
	expected = append(expected, "frontend")
	// The edge agent is a standalone image no chart area renders. What an operator pulls is
	// the image its RUNBOOK tells them to run, so each by-hand module other than the operator
	// joins the expected side from its RUNBOOK's `ghcr.io/devicechain-io/<name>:<version>`
	// line -- not from the release's own module name, which would compare the release to itself.
	runbookImage := regexp.MustCompile(`ghcr\.io/devicechain-io/([a-z0-9-]+):<version>`)
	for _, mod := range modules {
		if mod == "backend/k8s" {
			continue
		}
		if mod == "backend/admin/dc-broker-admin" {
			// Not an image anyone runs by hand: the chart's pre-install/pre-upgrade hook Job
			// pulls it, so what is expected is the name the chart's helper builds.
			if !strings.Contains(helpers, `"%s/dc-broker-admin:%s" .Values.image.registry`) {
				t.Error("the chart's broker administrator image is no longer {registry}/dc-broker-admin:{tag} -- update the release, the verifier and the chart together")
			}
			// ...and the local image build, which the upgrade drill and every registry bring-up
			// populate the registry with, builds it too: a hook whose image is missing fails the
			// install or upgrade on ImagePullBackOff before any service rolls.
			if build := readRepoFile(t, "deploy/local/build-images.sh"); !strings.Contains(build, `build_one "$ROOT/backend/admin/dc-broker-admin" "$REGISTRY/dc-broker-admin"`) {
				t.Error("deploy/local/build-images.sh does not build the broker administrator's image ({registry}/dc-broker-admin), which the chart's hook pulls")
			}
			expected = append(expected, "dc-broker-admin")
			continue
		}
		if mod == "backend/broker/dc-nats-server" {
			// Not a chart image either: the OpenTofu nats module pulls it, at the repository dcctl
			// passes as nats_server_image_repository ({registry}/dc-nats-server) and the module
			// defaults to. Expected is that name, read from both, not the release's own.
			tofu := readRepoFile(t, "deploy/opentofu/modules/nats/main.tf")
			if !strings.Contains(tofu, `default     = "ghcr.io/devicechain-io/dc-nats-server"`) {
				t.Error("the nats module's server_image_repository no longer defaults to ghcr.io/devicechain-io/dc-nats-server -- update the release, the verifier and the module together")
			}
			if cli := readRepoFile(t, "backend/cli/bootstrap/brokerimage.go"); !strings.Contains(cli, `brokerImageName = "dc-nats-server"`) {
				t.Error("dcctl no longer passes the broker image as {registry}/dc-nats-server")
			}
			if build := readRepoFile(t, "deploy/local/build-images.sh"); !strings.Contains(build, `build_one "$ROOT/backend/broker/dc-nats-server" "$REGISTRY/dc-nats-server"`) {
				t.Error("deploy/local/build-images.sh does not build the broker image ({registry}/dc-nats-server), which the infrastructure apply pulls")
			}
			expected = append(expected, "dc-nats-server")
			continue
		}
		names := map[string]bool{}
		for _, m := range runbookImage.FindAllStringSubmatch(readRepoFile(t, mod+"/RUNBOOK.md"), -1) {
			names[m[1]] = true
		}
		if len(names) != 1 {
			t.Errorf("%s/RUNBOOK.md should name exactly one ghcr.io/devicechain-io/<name>:<version> image, found %v", mod, names)
		}
		for n := range names {
			expected = append(expected, n)
		}
	}

	if msgs := diffSets("release.yml", published, "dcctl/chart", expected); len(msgs) > 0 {
		t.Errorf("release.yml's image set and what dcctl / the chart pull disagree:\n  %s", strings.Join(msgs, "\n  "))
	}

	// VERIFIED: the signature verifier walks the same set, and the chart under its own path.
	vscript := readRepoFile(t, "hack/verify-release-signatures.sh")
	vimages, vchart := verifierImages(t, vscript)
	if msgs := diffSets("release.yml", published, "hack/verify-release-signatures.sh", vimages); len(msgs) > 0 {
		t.Errorf("an image is released without being verified (or verified without being released):\n  %s", strings.Join(msgs, "\n  "))
	}
	chartYAML := readRepoFile(t, "deploy/helm/devicechain/Chart.yaml")
	cm := regexp.MustCompile(`(?m)^name:\s*(\S+)`).FindStringSubmatch(chartYAML)
	if cm == nil {
		t.Fatal("Chart.yaml has no name")
	}
	if want := "charts/" + cm[1]; vchart != want {
		t.Errorf("verifier checks the chart at %q but release.yml pushes it to oci://<registry>/%s", vchart, want)
	}
}

// TestImageSetDiffCanFail is the contract test's negative control: a comparison that cannot
// report a difference passes forever. Each mutation must produce at least one message.
func TestImageSetDiffCanFail(t *testing.T) {
	base := []string{"a", "b", "operator", "frontend"}
	if msgs := diffSets("x", base, "y", append([]string(nil), base...)); len(msgs) != 0 {
		t.Fatalf("identical sets reported differences: %v", msgs)
	}
	for name, mutated := range map[string][]string{
		"missing":  {"a", "b", "operator"},
		"extra":    {"a", "b", "operator", "frontend", "ghost"},
		"renamed":  {"a", "b", "k8s", "frontend"},
		"emptied":  nil,
		"misspelt": {"a", "b", "operator", "frontned"},
	} {
		if msgs := diffSets("x", base, "y", mutated); len(msgs) == 0 {
			t.Errorf("%s: a changed set reported no difference", name)
		}
	}
	// The parsers must find what they claim to, or the comparison above compares nothing.
	if got := releaseWorkflowModules("      echo \"backend/k8s\"\n      echo \"backend/edge/dc-edge-agent\"\n"); len(got) != 2 {
		t.Errorf("guard-module parser found %v", got)
	}
	if got := moduleName("backend/edge/dc-edge-agent"); got != "dc-edge-agent" {
		t.Errorf("moduleName = %q", got)
	}
}
